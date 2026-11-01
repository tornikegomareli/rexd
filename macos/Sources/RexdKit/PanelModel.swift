import Foundation
import Observation

/// What the panel shows, and everything it can do.
@MainActor @Observable
public final class PanelModel {
    public enum Phase: Equatable {
        case loading
        /// `rexd` is not installed.
        case missing
        /// `rexd` runs, and `rexd setup` has not run.
        case notConnected
        case ready
    }

    public enum Screen: Equatable {
        case list
        case plugin(String)
        case install
        case daemonLog
        case disconnect
    }

    public private(set) var phase: Phase = .loading
    public var screen: Screen = .list
    public private(set) var plugins: [Plugin] = []
    public private(set) var daemon = Status.Daemon(running: false)
    public private(set) var rexState = ""
    public private(set) var rexConfig: String?
    public private(set) var version = ""
    /// `rexd sync` needs `rexd.lock`, which sits beside `init.lua`.
    public private(set) var canSync = false
    /// What a command in progress is called.
    public private(set) var task: String?
    /// What the command in progress or the failed command printed.
    public private(set) var output: [String] = []
    /// Why the last command failed. It stays until the person dismisses it.
    public private(set) var failure: String?
    /// What the last command did.
    public private(set) var notice: String?
    /// Why rexd's state could not be read.
    public private(set) var readError: String?

    /// While the panel is open, the state refreshes more often.
    public var isOpen = false
    /// What the install screen holds.
    public var draftSource = ""

    private let locate: @Sendable () async -> RexdClient?
    private var client: RexdClient?
    private var noticeGeneration = 0

    public init(locate: @escaping @Sendable () async -> RexdClient? = { await RexdLocator.find() }) {
        self.locate = locate
    }

    public var isBusy: Bool { task != nil }

    /// Something is wrong that the person should look at, and the menu bar icon says so.
    public var needsAttention: Bool {
        guard phase == .ready else { return phase == .notConnected }
        return !daemon.running || daemon.problem != nil || plugins.contains { $0.health == .failing }
    }

    public func plugin(_ id: String) -> Plugin? { plugins.first { $0.id == id } }

    public func refresh() async {
        if client == nil { client = await locate() }
        guard let client else {
            phase = .missing
            return
        }
        do {
            async let status = client.status()
            async let installed = client.installed()
            let (current, list) = try await (status, installed)
            let byID = Dictionary(list.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
            let found = current.plugins.map { Plugin(status: $0, installed: byID[$0.id]) }
            // What needs a look comes first. The sort keeps rexd's order within each.
            plugins = found.enumerated().sorted {
                ($0.element.health.urgency, $0.offset) < ($1.element.health.urgency, $1.offset)
            }.map(\.element)
            daemon = current.daemon
            rexState = current.rex
            rexConfig = current.rexConfig
            canSync = current.rexConfig.map {
                let folder = URL(fileURLWithPath: $0).deletingLastPathComponent()
                return !$0.isEmpty && FileManager.default.fileExists(atPath: folder.appendingPathComponent("rexd.lock").path)
            } ?? false
            phase = current.isConnected ? .ready : .notConnected
            readError = nil
            if version.isEmpty { version = (try? await client.version()) ?? "" }
            if case .plugin(let id) = screen, plugin(id) == nil { screen = .list }
        } catch {
            // The binary may have moved, as after an upgrade.
            self.client = nil
            readError = error.localizedDescription
            if phase == .loading { phase = .missing }
        }
    }

    /// Reads a log. A failure reads as the log's text, because that is where the person looks.
    public func log(id: String?) async -> String {
        guard let client else { return "" }
        do {
            return try await client.logs(id: id)
        } catch {
            return error.localizedDescription
        }
    }

    public func connect() async { await run(.connect, title: "Connecting to Rex") }
    public func restartDaemon() async { await run(.connect, title: "Restarting the daemon", done: "Daemon restarted") }

    public func disconnect(purge: Bool) async {
        if await run(.disconnect(purge: purge), title: "Disconnecting from Rex", done: "Disconnected from Rex") {
            screen = .list
        }
    }

    public func install(source: String, link: Bool) async {
        let source = source.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !source.isEmpty else { return }
        if await run(.install(source: source, link: link), title: "Installing \(source)") {
            draftSource = ""
            screen = .list
        }
    }

    public func remove(id: String) async {
        if await run(.remove(id: id), title: "Removing \(id)") {
            screen = .list
        }
    }

    public func update(ids: [String] = []) async {
        await run(.update(ids: ids), title: ids.isEmpty ? "Updating plugins" : "Updating \(ids[0])")
    }

    public func sync() async { await run(.sync, title: "Syncing with rexd.lock") }

    /// An empty value removes the option.
    public func setOption(id: String, key: String, value: String) async {
        let key = key.trimmingCharacters(in: .whitespaces)
        guard !key.isEmpty, plugin(id)?.options[key] != value else { return }
        await run(.set(id: id, changes: [key: value]), title: "Setting \(key)", done: value.isEmpty ? "Removed \(key)" : "Set \(key)")
    }

    /// Reports a failure from outside rexd, such as a login item that macOS refused.
    public func report(failure text: String) {
        failure = text
    }

    public func dismissFailure() {
        failure = nil
        output = []
    }

    @discardableResult
    private func run(_ command: RexdCommand, title: String, done: String? = nil) async -> Bool {
        guard !isBusy else { return false }
        if client == nil { client = await locate() }
        guard let client else {
            phase = .missing
            return false
        }
        task = title
        output = []
        failure = nil
        defer { task = nil }
        do {
            for try await line in client.perform(command) {
                output.append(line)
            }
            show(notice: done ?? output.last ?? "Done")
            output = []
            await refresh()
            return true
        } catch {
            failure = error.localizedDescription
            await refresh()
            return false
        }
    }

    private func show(notice text: String) {
        notice = text
        noticeGeneration += 1
        let generation = noticeGeneration
        Task { [weak self] in
            try? await Task.sleep(for: .seconds(5))
            if self?.noticeGeneration == generation { self?.notice = nil }
        }
    }
}
