import Foundation

/// What `rexd status --json` prints.
public struct Status: Decodable, Sendable, Equatable {
    public struct Daemon: Decodable, Sendable, Equatable {
        public var running: Bool
        public var pid: Int?
        public var problem: String?
    }

    public struct Plugin: Decodable, Sendable, Equatable {
        public var id: String
        public var version: String
        public var kind: String
        public var hasConfig: Bool
        public var configBroken: Bool?
        public var problem: String?
        public var scripts: [Script]

        enum CodingKeys: String, CodingKey {
            case id, version, kind, problem, scripts
            case hasConfig = "has_config"
            case configBroken = "config_broken"
        }
    }

    public struct Script: Decodable, Sendable, Equatable {
        public var script: String
        public var state: String
        public var restarts: Int
    }

    public var daemon: Daemon
    public var rex: String
    /// The `init.lua` rexd is connected to. A rexd before 0.3.1 does not report it.
    public var rexConfig: String?
    public var plugins: [Plugin]

    enum CodingKeys: String, CodingKey {
        case daemon, rex, plugins
        case rexConfig = "rex_config"
    }

    public var isConnected: Bool { rexConfig != "" }
}

/// One item of what `rexd list --json` prints.
public struct InstalledPlugin: Decodable, Sendable, Equatable {
    public var id: String
    public var source: String
    public var ref: String?
    public var commit: String?
    public var options: [String: OptionValue]?
}

/// A plugin option: a string, a number or a boolean.
public enum OptionValue: Decodable, Sendable, Equatable {
    case text(String)
    case number(Double)
    case flag(Bool)

    public init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if let flag = try? container.decode(Bool.self) {
            self = .flag(flag)
        } else if let number = try? container.decode(Double.self) {
            self = .number(number)
        } else {
            self = .text(try container.decode(String.self))
        }
    }

    /// The form `rexd set` takes.
    public var text: String {
        switch self {
        case .text(let text): text
        case .flag(let flag): String(flag)
        case .number(let number):
            number == number.rounded() && abs(number) < 1e15 ? String(Int(number)) : String(number)
        }
    }
}

/// How a plugin is doing.
public enum Health: Sendable, Equatable {
    case loaded, running, waiting, restarting, failing

    var urgency: Int {
        switch self {
        case .failing: 0
        case .restarting: 1
        case .waiting: 2
        case .running, .loaded: 3
        }
    }
}

/// A plugin, from both of rexd's lists.
public struct Plugin: Identifiable, Sendable, Equatable {
    public var id: String
    public var version: String
    public var kind: String
    public var source: String
    public var ref: String?
    public var commit: String?
    public var options: [String: String]
    public var scripts: [Status.Script]
    public var problem: String?
    public var health: Health

    /// The last part of the id, which is the name people say.
    public var name: String { id.split(separator: ".").last.map(String.init) ?? id }

    /// A plugin from a local folder cannot update from anywhere new, but it can copy again.
    public var isLocal: Bool { kind != "git" }

    public init(status: Status.Plugin, installed: InstalledPlugin?) {
        id = status.id
        version = status.version
        kind = status.kind
        source = installed?.source ?? ""
        ref = installed?.ref
        commit = installed?.commit
        options = (installed?.options ?? [:]).mapValues(\.text)
        scripts = status.scripts
        problem = status.problem
        health = Plugin.health(of: status)
    }

    static func health(of plugin: Status.Plugin) -> Health {
        let states = plugin.scripts.map(\.state)
        if plugin.problem != nil || plugin.configBroken == true || states.contains("crashing") { return .failing }
        if states.contains("backoff") { return .restarting }
        if states.contains("waiting") || states.contains("stopping") { return .waiting }
        return states.isEmpty ? .loaded : .running
    }

    /// One line on how it is doing.
    public var summary: String {
        switch health {
        case .failing:
            if let problem { return problem }
            return "Crashing, \(restarts) restarts"
        case .restarting: return "Restarting"
        case .waiting: return "Waiting for Rex"
        case .running: return scripts.count == 1 ? "1 script running" : "\(scripts.count) scripts running"
        case .loaded: return "Actions and keys"
        }
    }

    private var restarts: Int { scripts.reduce(0) { $0 + $1.restarts } }
}

/// One line on what `rexd update` did: only what changed, or that nothing had to.
public enum UpdateSummary {
    public static func text(_ printed: [String], ids: [String]) -> String {
        let updated = printed.compactMap(parse)
        switch updated.count {
        case 0:
            guard let id = ids.first else { return "All plugins are up to date" }
            let name = shortName(id)
            if let line = printed.first(where: { $0.hasPrefix("\(id): pinned to ") }) {
                let pinned = line.dropFirst("\(id): pinned to ".count)
                return "\(name) is pinned to \(pinned.prefix { $0 != ";" })"
            }
            return "\(name) is up to date"
        case 1:
            let only = updated[0]
            return only.version.map { "Updated \(only.name) to \($0)" } ?? "Updated \(only.name)"
        case 2:
            return "Updated \(updated[0].name) and \(updated[1].name)"
        default:
            return "Updated \(updated.count) plugins: \(updated.map(\.name).joined(separator: ", "))"
        }
    }

    /// Reads `Updated acme.foo: 1.0 -> 2.0`, or `Updated acme.foo: new code, still 1.0`.
    private static func parse(_ line: String) -> (name: String, version: String?)? {
        guard line.hasPrefix("Updated "), let colon = line.firstIndex(of: ":") else { return nil }
        let id = line[line.index(line.startIndex, offsetBy: "Updated ".count)..<colon]
        let change = line[line.index(after: colon)...].components(separatedBy: " -> ")
        let version = change.count == 2 ? change[1].trimmingCharacters(in: .whitespaces) : nil
        // A rexd before 0.3.2 prints this when the version stayed the same.
        let moved = change.count == 2 && change[0].trimmingCharacters(in: .whitespaces) != version
        return (shortName(String(id)), moved ? version : nil)
    }

    private static func shortName(_ id: String) -> String {
        id.split(separator: ".").last.map(String.init) ?? id
    }
}
