import Foundation
import Testing

@testable import RexdKit

private func temporaryFolder() throws -> URL {
    let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
    return url
}

/// A stand-in for `rexd` that answers from a state folder and records its calls.
private struct FakeRexd {
    let folder: URL
    var path: String { folder.appendingPathComponent("rexd").path }
    var client: RexdClient { RexdClient(path: path) }

    init(status: String = FakeRexd.connected, list: String = "[]", script: String = "") throws {
        folder = try temporaryFolder()
        try status.write(to: folder.appendingPathComponent("status.json"), atomically: true, encoding: .utf8)
        try list.write(to: folder.appendingPathComponent("list.json"), atomically: true, encoding: .utf8)
        let body = """
        #!/bin/sh
        case "$1" in
        status) cat "\(folder.path)/status.json"; exit 0 ;;
        list) cat "\(folder.path)/list.json"; exit 0 ;;
        version) echo "rexd v0.3.1"; exit 0 ;;
        esac
        echo "$*" >> "\(folder.path)/calls"
        \(script)
        """
        try body.write(toFile: path, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: path)
    }

    var calls: [String] {
        let text = (try? String(contentsOf: folder.appendingPathComponent("calls"), encoding: .utf8)) ?? ""
        return text.split(separator: "\n").map(String.init)
    }

    func setStatus(_ status: String) throws {
        try status.write(to: folder.appendingPathComponent("status.json"), atomically: true, encoding: .utf8)
    }

    @MainActor func model() -> PanelModel {
        let client = client
        return PanelModel(locate: { client })
    }

    static let connected = #"{"daemon":{"running":true,"pid":7},"rex":"up","rex_config":"/x/init.lua","plugins":[]}"#

    static func withPlugin(state: String? = nil, restarts: Int = 0, problem: String? = nil) -> String {
        let scripts = state.map { #"[{"plugin":"acme.foo","script":"watch","state":"\#($0)","restarts":\#(restarts)}]"# } ?? "[]"
        let issue = problem.map { #","problem":"\#($0)""# } ?? ""
        return #"{"daemon":{"running":true,"pid":7},"rex":"up","rex_config":"/x/init.lua","plugins":[{"id":"acme.foo","version":"1.0","kind":"git","has_config":true\#(issue),"scripts":\#(scripts)}]}"#
    }

    static let fooList = #"[{"id":"acme.foo","version":"1.0","kind":"git","source":"https://x/y.git","ref":"^1.0.0","commit":"abc","options":{"key":"cmd+k","size":3,"on":true},"generation":1}]"#
}

/// One at a time, because each test writes a stand-in program and runs it, and a program
/// that another test's new process still holds open will not start.
@Suite(.serialized) struct RexdKitTests {}

extension RexdKitTests {
@Suite struct ModelTests {
    @Test func itReadsTheFormatsOfRexd() throws {
        let status = try JSONDecoder().decode(Status.self, from: Data(FakeRexd.withPlugin(state: "running").utf8))
        let list = try JSONDecoder().decode([InstalledPlugin].self, from: Data(FakeRexd.fooList.utf8))
        let plugin = Plugin(status: status.plugins[0], installed: list[0])
        #expect(plugin.name == "foo")
        #expect(plugin.options == ["key": "cmd+k", "size": "3", "on": "true"])
        #expect(plugin.summary == "1 script running")
        #expect(plugin.ref == "^1.0.0")
    }

    @Test func aRexdBefore031CountsAsConnected() throws {
        let status = try JSONDecoder().decode(Status.self, from: Data(#"{"daemon":{"running":true},"rex":"","plugins":[]}"#.utf8))
        #expect(status.isConnected)
    }

    @Test(arguments: [
        (nil, nil, Health.loaded, "Actions and keys"),
        ("running", nil, Health.running, "1 script running"),
        ("waiting", nil, Health.waiting, "Waiting for Rex"),
        ("backoff", nil, Health.restarting, "Restarting"),
        ("crashing", nil, Health.failing, "Crashing, 4 restarts"),
        (nil, "config code: bad", Health.failing, "config code: bad"),
    ] as [(String?, String?, Health, String)])
    func itSaysHowAPluginIsDoing(state: String?, problem: String?, health: Health, summary: String) throws {
        let json = FakeRexd.withPlugin(state: state, restarts: 4, problem: problem)
        let status = try JSONDecoder().decode(Status.self, from: Data(json.utf8))
        let plugin = Plugin(status: status.plugins[0], installed: nil)
        #expect(plugin.health == health)
        #expect(plugin.summary == summary)
    }
}

@Suite struct CommandTests {
    @Test func eachCommandBecomesItsArguments() {
        #expect(RexdCommand.connect.arguments == ["setup"])
        #expect(RexdCommand.disconnect(purge: true).arguments == ["teardown", "--purge"])
        #expect(RexdCommand.disconnect(purge: false).arguments == ["teardown"])
        #expect(RexdCommand.install(source: "./a b", link: true).arguments == ["install", "./a b", "--link"])
        #expect(RexdCommand.install(source: "o/r", link: false).arguments == ["install", "o/r"])
        #expect(RexdCommand.update(ids: []).arguments == ["update"])
        #expect(RexdCommand.set(id: "a.b", changes: ["z": "1", "a": ""]).arguments == ["set", "a.b", "a=", "z=1"])
        #expect(RexdCommand.sync.arguments == ["sync"])
    }

    @Test func linesSurviveAnyChunking() {
        var splitter = LineSplitter()
        let text = Array("one\nგამარჯობა\n\nlast".utf8)
        var lines: [String] = []
        for byte in text { lines += splitter.add(Data([byte])) }
        lines += splitter.finish()
        #expect(lines == ["one", "გამარჯობა", "", "last"])
    }

    @Test func aFailureSaysWhatRexdSaid() {
        #expect(RexdClient.failure(["Cloning x", "rexd: it broke", "line 2"], status: 1) == "it broke\nline 2")
        #expect(RexdClient.failure(["only this"], status: 1) == "only this")
        #expect(RexdClient.failure(["rexd install: ./x does not exist", "Usage: rexd install SOURCE", "Flags:"], status: 2) == "./x does not exist")
        #expect(RexdClient.failure([], status: 3) == "rexd exited with status 3.")
    }
}

@Suite struct LocatorTests {
    @Test func theAnswerSurvivesWhatAStartUpFilePrints() {
        #expect(RexdLocator.parsePath("welcome!\n__REXD_PATH__/a:/b__REXD_PATH__\nbye") == "/a:/b")
        #expect(RexdLocator.parsePath("nothing") == nil)
    }

    @Test func thePathComesFirstAndEachFolderOnce() {
        #expect(RexdLocator.locations(shellPath: "/a:/b", known: ["/b", "/c"]) == ["/a", "/b", "/c"])
    }

    @Test func itFindsTheFirstThatRuns() throws {
        let fake = try FakeRexd()
        #expect(RexdLocator.firstExecutable(named: "rexd", in: ["/nowhere", fake.folder.path]) == fake.path)
        #expect(RexdLocator.firstExecutable(named: "rexd", in: ["/nowhere"]) == nil)
    }
}

@Suite struct ThemeTests {
    @Test func itReadsEachFormOfRex() throws {
        #expect(ThemeColor(css: "255 0 0")?.red == 1)
        #expect(ThemeColor(css: "rgb(0 255 0)")?.green == 1)
        let dim = try #require(ThemeColor(css: "rgb(0 0 255 / 0.5)"))
        #expect(dim.blue == 1 && dim.alpha == 0.5)
        #expect(ThemeColor(css: "#ff0000")?.red == 1)
        #expect(ThemeColor(css: "not a color") == nil)
    }

    private func settings(_ values: [String: String]) -> UserDefaults {
        let name = "test.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: name)!
        values.forEach { defaults.set($1, forKey: $0) }
        return defaults
    }

    private func themes() throws -> URL {
        let url = try temporaryFolder().appendingPathComponent("themes.json")
        let json = #"[{"id":"neon-arcade","name":"N","cssVariables":{"--super-accent":"rgb(255 0 216)","--super-bg":"13 13 21"}}]"#
        try json.write(to: url, atomically: true, encoding: .utf8)
        return url
    }

    @Test func itUsesTheThemeRexShows() throws {
        let theme = RexTheme.load(
            systemIsDark: true,
            settings: settings(["Workspace.darkThemeID": "neon-arcade", "Workspace.terminalFontName": "Mono"]),
            themes: try themes())
        #expect(theme.id == "neon-arcade")
        #expect(theme["accent"].red == 1)
        #expect(theme.fontName == "Mono")
        // Tokens the theme leaves out come from Rex's default.
        #expect(theme["text"] == ThemeColor(css: "230 237 243"))
    }

    @Test func rexsOwnAppearanceWinsOverTheSystem() throws {
        let theme = RexTheme.load(systemIsDark: true, settings: settings(["Workspace.appearance": "light"]), themes: nil)
        #expect(theme.appearance == .light)
        #expect(theme.id == "merino-light")
        #expect(theme["bg"] == ThemeColor(css: "246 247 249"))
    }

    @Test func withoutRexItStillDraws() {
        let theme = RexTheme.load(systemIsDark: true, settings: nil, themes: URL(fileURLWithPath: "/nowhere"))
        #expect(theme.id == "merino-dark")
        for name in RexTheme.tokenNames {
            #expect(theme[name] != ThemeColor(css: "128 128 128"), "\(name) has no fallback")
        }
    }
}

@MainActor @Suite struct PanelTests {
    @Test func withoutRexdItSaysSo() async {
        let model = PanelModel(locate: { nil })
        await model.refresh()
        #expect(model.phase == .missing)
    }

    @Test func beforeSetupItOffersToConnect() async throws {
        let fake = try FakeRexd(status: #"{"daemon":{"running":false},"rex":"","rex_config":"","plugins":[]}"#)
        let model = fake.model()
        await model.refresh()
        #expect(model.phase == .notConnected)
        #expect(model.needsAttention)
        try fake.setStatus(FakeRexd.connected)
        await model.connect()
        #expect(fake.calls == ["setup"])
        #expect(model.phase == .ready)
        #expect(!model.needsAttention)
    }

    @Test func itJoinsBothListsOfRexd() async throws {
        let fake = try FakeRexd(status: FakeRexd.withPlugin(state: "running"), list: FakeRexd.fooList)
        let model = fake.model()
        await model.refresh()
        #expect(model.plugins.map(\.id) == ["acme.foo"])
        #expect(model.plugins[0].source == "https://x/y.git")
        #expect(model.version == "v0.3.1")
    }

    @Test func aFailingPluginNeedsAttention() async throws {
        let fake = try FakeRexd(status: FakeRexd.withPlugin(state: "crashing", restarts: 5))
        let model = fake.model()
        await model.refresh()
        #expect(model.needsAttention)
    }

    @Test func aDeadDaemonNeedsAttention() async throws {
        let fake = try FakeRexd(status: #"{"daemon":{"running":false},"rex":"","rex_config":"/x/init.lua","plugins":[]}"#)
        let model = fake.model()
        await model.refresh()
        #expect(model.needsAttention)
    }

    @Test func installSucceedsAndGoesBackToTheList() async throws {
        let fake = try FakeRexd(script: #"echo "Cloning"; echo "Installed acme.foo 1.0""#)
        let model = fake.model()
        await model.refresh()
        model.screen = .install
        await model.install(source: "  acme/foo  ", link: false)
        #expect(fake.calls == ["install acme/foo"])
        #expect(model.screen == .list)
        #expect(model.notice == "Installed acme.foo 1.0")
        #expect(model.failure == nil)
        #expect(!model.isBusy)
    }

    @Test func aFailedInstallStaysAndSaysWhy() async throws {
        let fake = try FakeRexd(script: #"echo "Cloning"; echo "rexd: no such repository"; exit 1"#)
        let model = fake.model()
        await model.refresh()
        model.screen = .install
        await model.install(source: "acme/foo", link: false)
        #expect(model.screen == .install)
        #expect(model.failure == "no such repository")
        #expect(model.output == ["Cloning", "rexd: no such repository"])
        model.dismissFailure()
        #expect(model.failure == nil && model.output.isEmpty)
    }

    @Test func anEmptySourceDoesNothing() async throws {
        let fake = try FakeRexd()
        let model = fake.model()
        await model.refresh()
        await model.install(source: "   ", link: false)
        #expect(fake.calls.isEmpty)
    }

    @Test func removingAPluginLeavesItsScreen() async throws {
        let fake = try FakeRexd(status: FakeRexd.withPlugin(), list: FakeRexd.fooList, script: #"echo "Removed acme.foo""#)
        let model = fake.model()
        await model.refresh()
        model.screen = .plugin("acme.foo")
        await model.remove(id: "acme.foo")
        #expect(fake.calls == ["remove acme.foo"])
        #expect(model.screen == .list)
    }

    @Test func aPluginThatGoesAwayLeavesItsScreen() async throws {
        let fake = try FakeRexd(status: FakeRexd.withPlugin(), list: FakeRexd.fooList)
        let model = fake.model()
        await model.refresh()
        model.screen = .plugin("acme.foo")
        try fake.setStatus(FakeRexd.connected)
        await model.refresh()
        #expect(model.screen == .list)
    }

    @Test func anOptionOnlyChangesWhenItChanged() async throws {
        let fake = try FakeRexd(status: FakeRexd.withPlugin(), list: FakeRexd.fooList, script: "exit 0")
        let model = fake.model()
        await model.refresh()
        await model.setOption(id: "acme.foo", key: "key", value: "cmd+k")
        #expect(fake.calls.isEmpty)
        await model.setOption(id: "acme.foo", key: "key", value: "cmd+j")
        await model.setOption(id: "acme.foo", key: "size", value: "")
        #expect(fake.calls == ["set acme.foo key=cmd+j", "set acme.foo size="])
    }

    @Test func oneCommandRunsAtATime() async throws {
        let fake = try FakeRexd(script: "sleep 0.3")
        let model = fake.model()
        await model.refresh()
        async let first: Void = model.update()
        async let second: Void = model.sync()
        _ = await (first, second)
        #expect(fake.calls.count == 1)
    }

    /// The menu redraws when this changes, so it must be stored, and read on each refresh.
    @Test func syncAppearsOnceThereIsALock() async throws {
        let folder = try temporaryFolder()
        let config = folder.appendingPathComponent("init.lua").path
        let fake = try FakeRexd(status: #"{"daemon":{"running":true},"rex":"","rex_config":"\#(config)","plugins":[]}"#)
        let model = fake.model()
        await model.refresh()
        #expect(!model.canSync)
        try "{}".write(to: folder.appendingPathComponent("rexd.lock"), atomically: true, encoding: .utf8)
        await model.refresh()
        #expect(model.canSync)
    }

    @Test func whatFailsComesFirst() async throws {
        let status = #"{"daemon":{"running":true},"rex":"up","rex_config":"/x/init.lua","plugins":["# + [
            ("a.calm", "running"), ("b.loop", "backoff"), ("c.bad", "crashing"), ("d.wait", "waiting"),
        ].map { #"{"id":"\#($0.0)","version":"1","kind":"git","has_config":true,"scripts":[{"plugin":"\#($0.0)","script":"s","state":"\#($0.1)","restarts":0}]}"# }.joined(separator: ",") + "]}"
        let fake = try FakeRexd(status: status)
        let model = fake.model()
        await model.refresh()
        #expect(model.plugins.map(\.id) == ["c.bad", "b.loop", "d.wait", "a.calm"])
    }

    @Test func aLogCanFailWithoutBreakingAnything() async throws {
        let fake = try FakeRexd(script: "exit 0")
        let model = fake.model()
        await model.refresh()
        _ = await model.log(id: "acme.foo")
        #expect(model.failure == nil)
    }
}

@Suite struct ReadingTests {
    /// A command that hangs must not stop the app, which asks rexd again and again.
    @Test func aHungCommandEnds() async throws {
        let folder = try temporaryFolder()
        let path = folder.appendingPathComponent("rexd").path
        try "#!/bin/sh\nsleep 30 &\nsleep 30\n".write(toFile: path, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: path)
        var client = RexdClient(path: path)
        client.readTimeout = 0.3
        let start = Date()
        await #expect(throws: RexdError.self) { try await client.status() }
        #expect(Date().timeIntervalSince(start) < 5)
    }

    @Test func aCommandCanPrintMoreThanAPipeHolds() async throws {
        let folder = try temporaryFolder()
        let path = folder.appendingPathComponent("rexd").path
        try "#!/bin/sh\nhead -c 300000 /dev/zero | tr '\\\\0' 'x'\nhead -c 300000 /dev/zero | tr '\\\\0' 'y' >&2\n".write(toFile: path, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: path)
        let text = try await RexdClient(path: path).logs(id: "a.b")
        #expect(text.count == 300_000)
    }

    @Test func manyReadsAtOnceAllFinish() async throws {
        let fake = try FakeRexd(status: FakeRexd.withPlugin(state: "running"), list: FakeRexd.fooList)
        let client = fake.client
        try await withThrowingTaskGroup(of: Void.self) { group in
            for _ in 0..<60 {
                group.addTask {
                    _ = try await client.status()
                    _ = try await client.installed()
                }
            }
            try await group.waitForAll()
        }
    }

    @Test func whatACommandSaysOnFailureComesBack() async throws {
        let folder = try temporaryFolder()
        let path = folder.appendingPathComponent("rexd").path
        try "#!/bin/sh\necho 'rexd: it broke' >&2\nexit 1\n".write(toFile: path, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: path)
        await #expect(throws: RexdError("it broke")) { try await RexdClient(path: path).status() }
    }
}

/// The app reads what the real command prints, so a change in rexd fails here.
@Suite struct RealRexdTests {
    private func build() throws -> String? {
        let find = Process()
        find.executableURL = URL(fileURLWithPath: "/usr/bin/which")
        find.arguments = ["go"]
        find.standardOutput = FileHandle.nullDevice
        try find.run()
        find.waitUntilExit()
        guard find.terminationStatus == 0 else { return nil }
        let folder = try temporaryFolder()
        let binary = folder.appendingPathComponent("rexd").path
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("../../..").standardized
        let go = Process()
        go.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        go.arguments = ["go", "build", "-o", binary, "./cmd/rexd"]
        go.currentDirectoryURL = root
        try go.run()
        go.waitUntilExit()
        return go.terminationStatus == 0 ? binary : nil
    }

    @Test func itReadsTheStatusOfARealRexd() async throws {
        guard let binary = try build() else { return }
        let home = try temporaryFolder()
        var environment = ProcessInfo.processInfo.environment
        environment["XDG_DATA_HOME"] = home.appendingPathComponent("d").path
        environment["XDG_STATE_HOME"] = home.appendingPathComponent("s").path
        environment["XDG_CONFIG_HOME"] = home.appendingPathComponent("c").path
        let client = RexdClient(path: binary, environment: environment)
        let status = try await client.status()
        #expect(status.rexConfig == "")
        #expect(!status.isConnected)
        #expect(try await client.installed().isEmpty)
    }
}
}
