import Foundation

public struct RexdError: LocalizedError, Sendable, Equatable {
    public var message: String
    public init(_ message: String) { self.message = message }
    public var errorDescription: String? { message }
}

/// Something rexd can change. The app only ever asks the `rexd` command, so it does
/// what the command does, and it cannot drift from it.
public enum RexdCommand: Sendable, Equatable {
    case connect
    case disconnect(purge: Bool)
    case install(source: String, link: Bool)
    case remove(id: String)
    case update(ids: [String])
    case set(id: String, changes: [String: String])
    case sync

    public var arguments: [String] {
        switch self {
        case .connect: ["setup"]
        case .disconnect(let purge): purge ? ["teardown", "--purge"] : ["teardown"]
        case .install(let source, let link): ["install", source] + (link ? ["--link"] : [])
        case .remove(let id): ["remove", id]
        case .update(let ids): ["update"] + ids
        case .set(let id, let changes): ["set", id] + changes.keys.sorted().map { "\($0)=\(changes[$0] ?? "")" }
        case .sync: ["sync"]
        }
    }
}

/// Turns chunks of output into lines. A chunk can end inside a line or inside a character.
struct LineSplitter {
    private var pending = Data()

    mutating func add(_ chunk: Data) -> [String] {
        pending.append(chunk)
        var lines: [String] = []
        while let end = pending.firstIndex(of: UInt8(ascii: "\n")) {
            lines.append(String(decoding: pending[pending.startIndex..<end], as: UTF8.self))
            pending.removeSubrange(pending.startIndex...end)
        }
        return lines
    }

    mutating func finish() -> [String] {
        defer { pending = Data() }
        return pending.isEmpty ? [] : [String(decoding: pending, as: UTF8.self)]
    }
}

/// Runs the `rexd` command.
public struct RexdClient: Sendable {
    public var path: String
    public var environment: [String: String]
    /// How long a command that only reads can take. It asks Rex's checker, which could hang.
    public var readTimeout: TimeInterval = 15

    public init(path: String, environment: [String: String] = ProcessInfo.processInfo.environment) {
        self.path = path
        self.environment = environment
    }

    public func status() async throws -> Status {
        try JSONDecoder().decode(Status.self, from: try await read(["status", "--json"]))
    }

    public func installed() async throws -> [InstalledPlugin] {
        try JSONDecoder().decode([InstalledPlugin].self, from: try await read(["list", "--json"]))
    }

    public func version() async throws -> String {
        let text = String(decoding: try await read(["version"]), as: UTF8.self)
        return text.trimmingCharacters(in: .whitespacesAndNewlines).replacingOccurrences(of: "rexd ", with: "")
    }

    /// A plugin's log, or the daemon's without an id.
    public func logs(id: String?, lines: Int = 200) async throws -> String {
        let target = id.map { [$0] } ?? ["--daemon"]
        return String(decoding: try await read(["logs"] + target + ["-n", String(lines)]), as: UTF8.self)
    }

    /// Runs a command, and gives each line it prints as it prints it. A failure ends the
    /// stream with what the command said.
    public func perform(_ command: RexdCommand) -> AsyncThrowingStream<String, Error> {
        let path = path
        let environment = environment
        return AsyncThrowingStream { continuation in
            // Its own thread: this waits on a pipe for as long as a build takes.
            DispatchQueue.global(qos: .userInitiated).async {
                let process = Process()
                process.executableURL = URL(fileURLWithPath: path)
                process.arguments = command.arguments
                process.environment = environment
                process.standardInput = FileHandle.nullDevice
                let pipe = Pipe()
                process.standardOutput = pipe
                process.standardError = pipe
                do {
                    try process.run()
                } catch {
                    continuation.finish(throwing: error)
                    return
                }
                var splitter = LineSplitter()
                var printed: [String] = []
                let emit = { (lines: [String]) in
                    printed += lines
                    lines.forEach { continuation.yield($0) }
                }
                while case let chunk = pipe.fileHandleForReading.availableData, !chunk.isEmpty {
                    emit(splitter.add(chunk))
                }
                emit(splitter.finish())
                process.waitUntilExit()
                if process.terminationStatus == 0 {
                    continuation.finish()
                } else {
                    continuation.finish(throwing: RexdError(Self.failure(printed, status: process.terminationStatus)))
                }
            }
        }
    }

    /// What a failed command said: its error, which starts with `rexd:` or `rexd install:`,
    /// and the lines after it, without the usage that a mistake in the arguments prints.
    static func failure(_ printed: [String], status: Int32) -> String {
        guard !printed.isEmpty else { return "rexd exited with status \(status)." }
        let prefix = #/^rexd( [a-z]+)?: /#
        let start = printed.lastIndex { $0.contains(prefix) } ?? max(printed.count - 8, 0)
        var lines = Array(printed[start...])
        if let usage = lines.firstIndex(where: { $0.hasPrefix("Usage: ") }) {
            lines.removeSubrange(usage...)
        }
        return lines.joined(separator: "\n").replacing(prefix, with: "", maxReplacements: 1)
    }

    /// Runs a command that only reads. Its output goes to files: unlike a pipe, a file
    /// cannot fill up, and nothing it left running can hold one open. Nothing waits on a
    /// thread, because the app asks rexd all day.
    private func read(_ arguments: [String]) async throws -> Data {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent("rexd-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: folder) }
        let outputURL = folder.appendingPathComponent("out")
        let errorURL = folder.appendingPathComponent("err")
        FileManager.default.createFile(atPath: outputURL.path, contents: nil)
        FileManager.default.createFile(atPath: errorURL.path, contents: nil)

        let process = Process()
        process.executableURL = URL(fileURLWithPath: path)
        process.arguments = arguments
        process.environment = environment
        process.standardInput = FileHandle.nullDevice
        process.standardOutput = try FileHandle(forWritingTo: outputURL)
        process.standardError = try FileHandle(forWritingTo: errorURL)

        let status: Int32 = try await withCheckedThrowingContinuation { continuation in
            process.terminationHandler = { continuation.resume(returning: $0.terminationStatus) }
            do {
                try process.run()
            } catch {
                process.terminationHandler = nil
                continuation.resume(throwing: error)
                return
            }
            let limit = readTimeout
            DispatchQueue.global().asyncAfter(deadline: .now() + limit) {
                if process.isRunning { process.terminate() }
            }
        }
        guard status == 0 else {
            if process.terminationReason == .uncaughtSignal {
                throw RexdError("rexd did not answer: \(arguments.joined(separator: " "))")
            }
            let said = (try? String(contentsOf: errorURL, encoding: .utf8)) ?? ""
            throw RexdError(said.trimmingCharacters(in: .whitespacesAndNewlines).replacingOccurrences(of: "rexd: ", with: ""))
        }
        return try Data(contentsOf: outputURL)
    }
}

/// Finds `rexd`. An app that launchd starts has almost no PATH, so the login shell says
/// what the person's PATH is. `rexd install` needs it too, for git, go and rex.
public enum RexdLocator {
    private static let marker = "__REXD_PATH__"

    public static func find() async -> RexdClient? {
        var environment = ProcessInfo.processInfo.environment
        let shellPath = await loginShellPath(shell: environment["SHELL"] ?? "/bin/zsh")
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        let known = ["\(home)/.local/bin", "/opt/homebrew/bin", "/usr/local/bin", "\(home)/go/bin", "/usr/bin", "/bin"]
        let directories = locations(shellPath: shellPath ?? environment["PATH"] ?? "", known: known)
        environment["PATH"] = directories.joined(separator: ":")
        guard let path = firstExecutable(named: "rexd", in: directories) else { return nil }
        return RexdClient(path: path, environment: environment)
    }

    /// The PATH's folders first, then the usual ones, each once.
    static func locations(shellPath: String, known: [String]) -> [String] {
        var seen = Set<String>()
        return (shellPath.split(separator: ":").map(String.init) + known).filter { seen.insert($0).inserted }
    }

    static func firstExecutable(named name: String, in directories: [String]) -> String? {
        directories.map { "\($0)/\(name)" }.first { FileManager.default.isExecutableFile(atPath: $0) }
    }

    /// A shell's start-up files can print anything, so markers surround the answer.
    static func parsePath(_ output: String) -> String? {
        let parts = output.components(separatedBy: marker)
        return parts.count >= 3 ? parts[parts.count - 2] : nil
    }

    private static func loginShellPath(shell: String) async -> String? {
        await withTaskGroup(of: String?.self) { group in
            group.addTask {
                let process = Process()
                process.executableURL = URL(fileURLWithPath: shell)
                process.arguments = ["-lic", "printf '\(marker)%s\(marker)' \"$PATH\""]
                process.standardInput = FileHandle.nullDevice
                let pipe = Pipe()
                process.standardOutput = pipe
                process.standardError = FileHandle.nullDevice
                guard (try? process.run()) != nil else { return nil }
                let data = (try? pipe.fileHandleForReading.readToEnd()) ?? nil
                return data.flatMap { parsePath(String(decoding: $0, as: UTF8.self)) }
            }
            group.addTask {
                // A start-up file that waits for input must not stop the app.
                try? await Task.sleep(for: .seconds(4))
                return nil
            }
            let first = await group.next() ?? nil
            group.cancelAll()
            return first
        }
    }
}
