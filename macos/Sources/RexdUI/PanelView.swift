import RexdKit
import ServiceManagement
import SwiftUI

private let panelWidth: CGFloat = 340

/// The whole panel. Each screen replaces the last at once: this opens many times a day,
/// and motion would only make it slower.
public struct PanelView: View {
    @Bindable var model: PanelModel
    private let fixedTheme: RexTheme?
    @State private var theme: RexTheme

    /// Without a theme, it follows the person's Rex.
    @MainActor public init(model: PanelModel, theme: RexTheme? = nil) {
        self.model = model
        fixedTheme = theme
        _theme = State(initialValue: theme ?? .current())
    }

    public var body: some View {
        VStack(spacing: 0) {
            switch model.phase {
            case .loading: Color.clear.frame(height: 120)
            case .missing: MissingScreen()
            case .notConnected: ConnectScreen(model: model)
            case .ready: screen
            }
            CommandStrip(model: model)
        }
        .frame(width: panelWidth)
        .background(theme.color("bg"))
        .environment(\.rex, theme)
        .preferredColorScheme(theme.colorScheme)
        .onExitCommand { model.screen = .list }
        .onAppear {
            // A menu bar app is not active just because its panel is open, and only an
            // active app gets what is typed.
            NSApp.activate(ignoringOtherApps: true)
            theme = fixedTheme ?? .current()
            model.isOpen = true
            Task { await model.refresh() }
        }
        .onDisappear {
            model.isOpen = false
            model.screen = .list
        }
    }

    @ViewBuilder private var screen: some View {
        switch model.screen {
        case .list:
            ListScreen(model: model)
        case .plugin(let id):
            if let plugin = model.plugin(id) {
                PluginScreen(model: model, plugin: plugin)
            }
        case .install:
            InstallScreen(model: model)
        case .daemonLog:
            DaemonLogScreen(model: model)
        case .disconnect:
            DisconnectScreen(model: model)
        }
    }
}

// MARK: - Plugins

private struct ListScreen: View {
    @Environment(\.rex) private var rex
    var model: PanelModel

    var body: some View {
        TitleBar(title: "rexd", detail: model.version) {
            IconButton(symbol: "plus", label: "Install a plugin") { model.screen = .install }
            MoreMenu(model: model)
        }
        if model.plugins.isEmpty {
            VStack(spacing: 10) {
                Text("No plugins yet")
                    .font(.system(size: 13, weight: .medium))
                    .foregroundStyle(rex.color("text"))
                Text("Rex Launcher finds any pane by what was said in it.")
                    .font(.system(size: 12))
                    .foregroundStyle(rex.color("text-secondary"))
                Button("Install Rex Launcher") {
                    Task { await model.install(source: "tornikegomareli/rexlauncher", link: false) }
                }
                .buttonStyle(RexButtonStyle(kind: .primary))
                .disabled(model.isBusy)
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 28)
        } else {
            ScrollView {
                VStack(spacing: 2) {
                    ForEach(model.plugins) { plugin in
                        PluginRow(plugin: plugin) { model.screen = .plugin(plugin.id) }
                    }
                }
                .padding(6)
            }
            .frame(height: min(CGFloat(model.plugins.count) * 46 + 12, 332))
        }
        Rule()
        HStack(spacing: 7) {
            Dot(token: daemonToken)
            Text(daemonText)
                .font(.system(size: 12))
                .foregroundStyle(rex.color("text-secondary"))
                .lineLimit(1)
            Spacer(minLength: 8)
            if !model.plugins.isEmpty {
                Button("Update All") { Task { await model.update() } }
                    .buttonStyle(RexButtonStyle())
                    .disabled(model.isBusy)
            }
        }
        .padding(.horizontal, 12)
        .frame(height: 40)
    }

    private var daemonToken: String {
        if !model.daemon.running { return "danger" }
        if model.daemon.problem != nil || model.rexState == "down" { return "warning" }
        return "success"
    }

    private var daemonText: String {
        if !model.daemon.running { return "Daemon stopped" }
        if let problem = model.daemon.problem { return problem }
        switch model.rexState {
        case "down": return "Daemon waiting for Rex"
        case "no-sessions": return "Daemon waiting for a session"
        default: return "Daemon running"
        }
    }
}

private struct PluginRow: View {
    @Environment(\.rex) private var rex
    var plugin: Plugin
    var open: () -> Void

    var body: some View {
        Button(action: open) {
            HStack(spacing: 10) {
                Dot(token: plugin.health.token)
                VStack(alignment: .leading, spacing: 1) {
                    Text(plugin.name)
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(rex.color("text"))
                    Text(plugin.summary)
                        .font(.system(size: 11.5))
                        .foregroundStyle(rex.color(plugin.health == .failing ? "danger" : "text-secondary"))
                }
                .lineLimit(1)
                Spacer(minLength: 8)
                Text(verbatim: plugin.version)
                    .font(rex.mono(11))
                    .foregroundStyle(rex.color("text-tertiary"))
                    .lineLimit(1)
            }
            .padding(.horizontal, 8)
            .frame(height: 44)
            .modifier(HoverRow())
        }
        .buttonStyle(.plain)
    }
}

private struct PluginScreen: View {
    @Environment(\.rex) private var rex
    var model: PanelModel
    var plugin: Plugin
    @State private var confirmingRemove = false

    var body: some View {
        TitleBar(title: plugin.name, back: { model.screen = .list }) { EmptyView() }
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if let problem = plugin.problem {
                    Text(verbatim: problem)
                        .font(rex.mono(11))
                        .foregroundStyle(rex.color("danger"))
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(8)
                        .background(RoundedRectangle(cornerRadius: 6).fill(rex.color("danger-container")))
                }
                VStack(alignment: .leading, spacing: 5) {
                    DetailRow(label: "Plugin", value: plugin.id)
                    DetailRow(label: "Version", value: plugin.version)
                    if !plugin.source.isEmpty { DetailRow(label: "Source", value: plugin.source) }
                    if let ref = plugin.ref { DetailRow(label: "Follows", value: ref) }
                    if let commit = plugin.commit, !commit.isEmpty { DetailRow(label: "Commit", value: String(commit.prefix(7))) }
                }
                if !plugin.scripts.isEmpty {
                    VStack(alignment: .leading, spacing: 5) {
                        SectionLabel(text: "Event scripts")
                        ForEach(plugin.scripts, id: \.script) { script in
                            HStack(spacing: 8) {
                                Dot(token: scriptToken(script.state))
                                Text(verbatim: script.script).font(rex.mono(11.5)).foregroundStyle(rex.color("text"))
                                Spacer()
                                Text(verbatim: script.restarts > 0 ? "\(script.state), \(script.restarts) restarts" : script.state)
                                    .font(.system(size: 12))
                                    .foregroundStyle(rex.color("text-secondary"))
                            }
                        }
                    }
                }
                OptionsEditor(model: model, plugin: plugin)
                if !plugin.scripts.isEmpty {
                    VStack(alignment: .leading, spacing: 5) {
                        SectionLabel(text: "Log")
                        LogView(model: model, id: plugin.id, height: 130)
                    }
                }
            }
            .padding(12)
        }
        .frame(maxHeight: 500)
        .fixedSize(horizontal: false, vertical: true)
        Rule()
        HStack(spacing: 8) {
            if confirmingRemove {
                Text("Remove \(plugin.name)?")
                    .font(.system(size: 12))
                    .foregroundStyle(rex.color("text"))
                Spacer()
                Button("Cancel") { confirmingRemove = false }
                    .buttonStyle(RexButtonStyle())
                Button("Remove") { Task { await model.remove(id: plugin.id) } }
                    .buttonStyle(RexButtonStyle(kind: .danger))
                    .disabled(model.isBusy)
            } else {
                Button("Remove") { confirmingRemove = true }
                    .buttonStyle(RexButtonStyle(kind: .danger))
                Spacer()
                Button("Update") { Task { await model.update(ids: [plugin.id]) } }
                    .buttonStyle(RexButtonStyle())
                    .disabled(model.isBusy)
            }
        }
        .padding(.horizontal, 12)
        .frame(height: 40)
    }

    private func scriptToken(_ state: String) -> String {
        switch state {
        case "running": "success"
        case "crashing": "danger"
        default: "warning"
        }
    }
}

private struct OptionsEditor: View {
    @Environment(\.rex) private var rex
    var model: PanelModel
    var plugin: Plugin
    @State private var addingKey = ""
    @State private var addingValue = ""
    @State private var adding = false
    @FocusState private var field: AddingField?

    private enum AddingField { case key, value }

    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            HStack {
                SectionLabel(text: "Options")
                Spacer()
                IconButton(symbol: "plus", label: "Add an option") { adding = true }
                    .scaleEffect(0.85)
            }
            .frame(height: 18)
            ForEach(plugin.options.keys.sorted(), id: \.self) { key in
                OptionRow(key: key, stored: plugin.options[key] ?? "") { value in
                    Task { await model.setOption(id: plugin.id, key: key, value: value) }
                }
            }
            if adding {
                HStack(spacing: 6) {
                    TextField("key", text: $addingKey)
                        .frame(width: 96)
                        .focused($field, equals: .key)
                        .onSubmit { field = .value }
                    TextField("value", text: $addingValue)
                        .focused($field, equals: .value)
                        .onSubmit(add)
                }
                .textFieldStyle(.plain)
                .font(rex.mono(11.5))
                .foregroundStyle(rex.color("text"))
                .padding(.horizontal, 7)
                .frame(height: 24)
                .background(RoundedRectangle(cornerRadius: 5).fill(rex.color("term-bg")))
                .overlay(RoundedRectangle(cornerRadius: 5).strokeBorder(rex.color("accent")))
                .task {
                    try? await Task.sleep(for: .milliseconds(50))
                    field = .key
                }
            } else if plugin.options.isEmpty {
                Text("None set. The plugin's README says which it has.")
                    .font(.system(size: 12))
                    .foregroundStyle(rex.color("text-tertiary"))
            }
        }
    }

    private func add() {
        let key = addingKey, value = addingValue
        addingKey = ""
        addingValue = ""
        adding = false
        Task { await model.setOption(id: plugin.id, key: key, value: value) }
    }
}

private struct OptionRow: View {
    @Environment(\.rex) private var rex
    var key: String
    var stored: String
    var save: (String) -> Void
    @State private var value = ""
    @State private var hovering = false
    @FocusState private var focused: Bool

    var body: some View {
        HStack(spacing: 6) {
            Text(verbatim: key)
                .font(rex.mono(11.5))
                .foregroundStyle(rex.color("text-secondary"))
                .frame(width: 96, alignment: .leading)
                .lineLimit(1)
            TextField("", text: $value)
                .textFieldStyle(.plain)
                .font(rex.mono(11.5))
                .foregroundStyle(rex.color("text"))
                .focused($focused)
                .onSubmit { save(value) }
            if hovering {
                IconButton(symbol: "xmark", label: "Remove \(key)") { save("") }
                    .scaleEffect(0.75)
            }
        }
        .padding(.leading, 7)
        .frame(height: 22)
        .background(RoundedRectangle(cornerRadius: 5).fill(focused ? rex.color("term-bg") : hovering ? rex.color("hover") : .clear))
        .overlay(RoundedRectangle(cornerRadius: 5).strokeBorder(focused ? rex.color("accent") : .clear))
        .onHover { hovering = $0 }
        .onAppear { value = stored }
        .onChange(of: stored) { value = stored }
        .padding(.horizontal, -7)
    }
}

/// A log that keeps up while it is on the screen.
private struct LogView: View {
    var model: PanelModel
    var id: String?
    var height: CGFloat
    @State private var text = ""

    var body: some View {
        TerminalBlock(text: text, height: height)
            .task(id: id) {
                while !Task.isCancelled {
                    text = await model.log(id: id)
                    try? await Task.sleep(for: .seconds(2))
                }
            }
    }
}

// MARK: - Install

private struct InstallScreen: View {
    @Environment(\.rex) private var rex
    @Bindable var model: PanelModel
    @State private var link = false
    @FocusState private var focused: Bool

    var body: some View {
        TitleBar(title: "Install a plugin", back: { model.screen = .list }) { EmptyView() }
        VStack(alignment: .leading, spacing: 10) {
            TextField("owner/repo", text: $model.draftSource)
                .textFieldStyle(.plain)
                .font(rex.mono(13))
                .foregroundStyle(rex.color("text"))
                .focused($focused)
                .onSubmit(install)
                .padding(.horizontal, 9)
                .frame(height: 32)
                .background(RoundedRectangle(cornerRadius: 6).fill(rex.color("term-bg")))
                .overlay(RoundedRectangle(cornerRadius: 6).strokeBorder(focused ? rex.color("accent") : rex.color("border")))
            Text("Also owner/repo@^1.2.0, a git URL, or a folder such as ~/code/plugin.")
                .font(.system(size: 12))
                .foregroundStyle(rex.color("text-secondary"))
                .fixedSize(horizontal: false, vertical: true)
            if isFolder {
                Toggle("Link it instead of copying, for development", isOn: $link)
                    .toggleStyle(.checkbox)
                    .font(.system(size: 12))
                    .foregroundStyle(rex.color("text"))
            }
            Text("A plugin's build steps and scripts run as you. Install plugins you trust.")
                .font(.system(size: 12))
                .foregroundStyle(rex.color("text-tertiary"))
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button("Install", action: install)
                    .buttonStyle(RexButtonStyle(kind: .primary))
                    .disabled(model.isBusy || model.draftSource.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(12)
        .task {
            // The window only takes focus just after it appears.
            try? await Task.sleep(for: .milliseconds(120))
            focused = true
        }
    }

    private var isFolder: Bool {
        let source = model.draftSource
        return ["/", "./", "../", "~/"].contains { source.hasPrefix($0) }
    }

    private func install() {
        let source = model.draftSource
        let link = isFolder && link
        Task { await model.install(source: source, link: link) }
    }
}

// MARK: - More

private struct MoreMenu: View {
    @Environment(\.rex) private var rex
    var model: PanelModel
    @State private var startsAtLogin = SMAppService.mainApp.status == .enabled

    var body: some View {
        Menu {
            if model.canSync {
                Button("Sync with rexd.lock") { Task { await model.sync() } }
            }
            Button("Restart Daemon") { Task { await model.restartDaemon() } }
            Button("Daemon Log") { model.screen = .daemonLog }
            if let config = model.rexConfig, !config.isEmpty {
                Button("Open Rex Config Folder") {
                    NSWorkspace.shared.open(URL(fileURLWithPath: config).deletingLastPathComponent())
                }
            }
            Divider()
            Toggle("Open at Login", isOn: Binding(get: { startsAtLogin }, set: { setLogin($0) }))
            Button("Disconnect from Rex…") { model.screen = .disconnect }
            Divider()
            Button("Quit") { NSApplication.shared.terminate(nil) }
        } label: {
            Image(systemName: "ellipsis")
                .font(.system(size: 12, weight: .medium))
                .foregroundStyle(rex.color("text-secondary"))
                .frame(width: 24, height: 24)
                .contentShape(Rectangle())
        }
        .menuStyle(.borderlessButton)
        .menuIndicator(.hidden)
        .fixedSize()
        .accessibilityLabel("More")
    }

    private func setLogin(_ on: Bool) {
        do {
            if on {
                try SMAppService.mainApp.register()
            } else {
                try SMAppService.mainApp.unregister()
            }
            startsAtLogin = SMAppService.mainApp.status == .enabled
        } catch {
            model.report(failure: "macOS refused to change the login item: \(error.localizedDescription)")
        }
    }
}

private struct DaemonLogScreen: View {
    var model: PanelModel
    var body: some View {
        TitleBar(title: "Daemon log", back: { model.screen = .list }) { EmptyView() }
        LogView(model: model, id: nil, height: 300)
            .padding(12)
    }
}

private struct DisconnectScreen: View {
    @Environment(\.rex) private var rex
    var model: PanelModel
    @State private var purge = false

    var body: some View {
        TitleBar(title: "Disconnect from Rex", back: { model.screen = .list }) { EmptyView() }
        VStack(alignment: .leading, spacing: 10) {
            Text("This stops the daemon, and removes rexd's line from init.lua and its generated files. Plugins stay installed, and connecting again brings them back.")
                .font(.system(size: 12))
                .foregroundStyle(rex.color("text-secondary"))
                .fixedSize(horizontal: false, vertical: true)
            Toggle("Also delete plugins, state and logs", isOn: $purge)
                .toggleStyle(.checkbox)
                .font(.system(size: 12))
                .foregroundStyle(rex.color("text"))
            HStack {
                Spacer()
                Button("Cancel") { model.screen = .list }
                    .buttonStyle(RexButtonStyle())
                Button(purge ? "Disconnect and Delete" : "Disconnect") {
                    Task { await model.disconnect(purge: purge) }
                }
                .buttonStyle(RexButtonStyle(kind: .danger))
                .disabled(model.isBusy)
            }
        }
        .padding(12)
    }
}

// MARK: - Before rexd works

private struct MissingScreen: View {
    @Environment(\.rex) private var rex
    private let command = "brew install tornikegomareli/tap/rexd"

    var body: some View {
        TitleBar(title: "rexd") { QuitButton() }
        VStack(alignment: .leading, spacing: 10) {
            Text("The rexd command is not installed")
                .font(.system(size: 13, weight: .medium))
                .foregroundStyle(rex.color("text"))
            Text("This app controls it. Install it, and this panel continues by itself.")
                .font(.system(size: 12))
                .foregroundStyle(rex.color("text-secondary"))
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Text(verbatim: command)
                    .font(rex.mono(11))
                    .foregroundStyle(rex.color("text"))
                    .textSelection(.enabled)
                    .lineLimit(1)
                    .minimumScaleFactor(0.8)
                Spacer(minLength: 4)
                IconButton(symbol: "doc.on.doc", label: "Copy") {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(command, forType: .string)
                }
            }
            .padding(.leading, 8)
            .padding(.vertical, 2)
            .background(RoundedRectangle(cornerRadius: 6).fill(rex.color("term-bg")))
            .overlay(RoundedRectangle(cornerRadius: 6).strokeBorder(rex.color("border")))
        }
        .padding(12)
    }
}

private struct ConnectScreen: View {
    @Environment(\.rex) private var rex
    var model: PanelModel

    var body: some View {
        TitleBar(title: "rexd", detail: model.version) { QuitButton() }
        VStack(alignment: .leading, spacing: 10) {
            Text("Connect rexd to Rex")
                .font(.system(size: 13, weight: .medium))
                .foregroundStyle(rex.color("text"))
            Text("This adds one line to Rex's init.lua, and starts the daemon. Disconnecting takes both away again.")
                .font(.system(size: 12))
                .foregroundStyle(rex.color("text-secondary"))
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button("Connect") { Task { await model.connect() } }
                    .buttonStyle(RexButtonStyle(kind: .primary))
                    .disabled(model.isBusy)
            }
        }
        .padding(12)
    }
}

private struct QuitButton: View {
    var body: some View {
        IconButton(symbol: "power", label: "Quit") { NSApplication.shared.terminate(nil) }
    }
}

// MARK: - What a command is doing

/// A command's progress, a result, or why it failed, below whichever screen ran it.
private struct CommandStrip: View {
    @Environment(\.rex) private var rex
    var model: PanelModel

    var body: some View {
        if let task = model.task {
            Rule()
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small).scaleEffect(0.7).frame(width: 14, height: 14)
                    Text(task)
                        .font(.system(size: 12))
                        .foregroundStyle(rex.color("text"))
                        .lineLimit(1)
                }
                if !model.output.isEmpty {
                    TerminalBlock(text: model.output.suffix(40).joined(separator: "\n"), height: 84)
                }
            }
            .padding(12)
        } else if let failure = model.failure {
            Rule()
            VStack(alignment: .leading, spacing: 8) {
                Text(verbatim: failure)
                    .font(rex.mono(11))
                    .foregroundStyle(rex.color("danger"))
                    .textSelection(.enabled)
                    .lineLimit(8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                HStack {
                    Spacer()
                    Button("Dismiss") { model.dismissFailure() }
                        .buttonStyle(RexButtonStyle())
                }
            }
            .padding(12)
            .background(rex.color("danger-container"))
        } else if let notice = model.notice {
            Rule()
            Text(verbatim: notice)
                .font(.system(size: 12))
                .foregroundStyle(rex.color("text-secondary"))
                .lineLimit(1)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 12)
                .frame(height: 30)
        }
    }
}
