import AppKit
import RexdKit
import SwiftUI

private struct RexThemeKey: EnvironmentKey {
    static let defaultValue = RexTheme.load(systemIsDark: true, settings: nil, themes: nil)
}

extension EnvironmentValues {
    var rex: RexTheme {
        get { self[RexThemeKey.self] }
        set { self[RexThemeKey.self] = newValue }
    }
}

extension RexTheme {
    /// The person's Rex, read again each time the panel opens, so it follows a new theme.
    @MainActor public static func current() -> RexTheme {
        let app = NSWorkspace.shared.urlForApplication(withBundleIdentifier: "com.superlogical.rex")
        let themes = app?.appending(path: "Contents/Resources/SuperUI_SuperDesignTokens.bundle/Contents/Resources/themes.json")
        let isDark = NSApp.effectiveAppearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
        return load(systemIsDark: isDark, settings: UserDefaults(suiteName: "com.superlogical.rex"), themes: themes)
    }

    func color(_ token: String) -> Color {
        let value = self[token]
        return Color(.sRGB, red: value.red, green: value.green, blue: value.blue, opacity: value.alpha)
    }

    /// Rex's terminal font, for anything a terminal would print.
    func mono(_ size: CGFloat) -> Font {
        if let fontName, NSFont(name: fontName, size: size) != nil || NSFontManager.shared.availableFontFamilies.contains(fontName) {
            return .custom(fontName, fixedSize: size)
        }
        return .system(size: size, design: .monospaced)
    }

    var colorScheme: ColorScheme { appearance == .dark ? .dark : .light }
}

extension Health {
    var token: String {
        switch self {
        case .loaded: "text-tertiary"
        case .running: "success"
        case .waiting, .restarting: "warning"
        case .failing: "danger"
        }
    }
}

struct Dot: View {
    @Environment(\.rex) private var rex
    var token: String
    var body: some View {
        Circle().fill(rex.color(token)).frame(width: 7, height: 7)
    }
}

/// A row that lights up under the pointer, as Rex's lists do.
struct HoverRow: ViewModifier {
    @Environment(\.rex) private var rex
    @State private var hovering = false
    func body(content: Content) -> some View {
        content
            .contentShape(RoundedRectangle(cornerRadius: 6))
            .background(RoundedRectangle(cornerRadius: 6).fill(hovering ? rex.color("hover") : .clear))
            .onHover { hovering = $0 }
    }
}

enum RexButtonKind { case primary, plain, danger }

struct RexButtonStyle: ButtonStyle {
    @Environment(\.rex) private var rex
    @Environment(\.isEnabled) private var isEnabled
    var kind = RexButtonKind.plain
    @State private var hovering = false

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 12, weight: .medium))
            .foregroundStyle(foreground)
            .padding(.horizontal, 10)
            .frame(height: 24)
            .background(RoundedRectangle(cornerRadius: 6).fill(background(hovering: hovering)))
            .overlay {
                if kind == .plain {
                    RoundedRectangle(cornerRadius: 6).strokeBorder(rex.color("border"))
                }
            }
            .opacity(isEnabled ? 1 : 0.45)
            .scaleEffect(configuration.isPressed ? 0.97 : 1)
            .animation(.easeOut(duration: 0.1), value: configuration.isPressed)
            .contentShape(RoundedRectangle(cornerRadius: 6))
            .onHover { hovering = $0 }
    }

    private var foreground: Color {
        switch kind {
        case .primary: rex.color("accent-on-solid")
        case .plain: rex.color("text")
        case .danger: rex.color("danger")
        }
    }

    private func background(hovering: Bool) -> Color {
        switch kind {
        case .primary: rex.color("accent-solid")
        case .plain: hovering ? rex.color("hover") : .clear
        case .danger: hovering ? rex.color("danger-container") : .clear
        }
    }
}

/// A small glyph button, like the ones in the corner of a Rex pane.
struct IconButton: View {
    @Environment(\.rex) private var rex
    @State private var hovering = false
    var symbol: String
    var label: String
    var action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: symbol)
                .font(.system(size: 12, weight: .medium))
                .frame(width: 24, height: 24)
                .foregroundStyle(hovering ? rex.color("text") : rex.color("text-secondary"))
                .background(RoundedRectangle(cornerRadius: 6).fill(hovering ? rex.color("hover") : .clear))
                .contentShape(RoundedRectangle(cornerRadius: 6))
        }
        .buttonStyle(.plain)
        .onHover { hovering = $0 }
        .accessibilityLabel(label)
        .help(label)
    }
}

/// Rex's pane title: a mark, a name, and something dim beside it.
struct TitleBar<Trailing: View>: View {
    @Environment(\.rex) private var rex
    var title: String
    var detail: String = ""
    var back: (() -> Void)?
    @ViewBuilder var trailing: () -> Trailing

    var body: some View {
        HStack(spacing: 6) {
            if let back {
                IconButton(symbol: "chevron.left", label: "Back", action: back)
                    .padding(.leading, -6)
            } else {
                Text(">_")
                    .font(rex.mono(12).weight(.semibold))
                    .foregroundStyle(rex.color("accent"))
            }
            Text(title)
                .font(.system(size: 13, weight: .semibold))
                .foregroundStyle(rex.color("text"))
                .lineLimit(1)
            Text(detail)
                .font(.system(size: 13))
                .foregroundStyle(rex.color("text-secondary"))
                .lineLimit(1)
            Spacer(minLength: 8)
            trailing()
        }
        .padding(.horizontal, 12)
        .frame(height: 40)
        .overlay(alignment: .bottom) { Rule() }
    }
}

struct Rule: View {
    @Environment(\.rex) private var rex
    var body: some View {
        rex.color("border").frame(height: 1)
    }
}

/// A label and its value, as in the details of a pane in Rex Launcher.
struct DetailRow: View {
    @Environment(\.rex) private var rex
    var label: String
    var value: String
    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Text(label)
                .font(.system(size: 12))
                .foregroundStyle(rex.color("text-secondary"))
                .frame(width: 58, alignment: .leading)
            Text(verbatim: value)
                .font(rex.mono(11.5))
                .foregroundStyle(rex.color("text"))
                .textSelection(.enabled)
                .lineLimit(2)
                .truncationMode(.middle)
            Spacer(minLength: 0)
        }
    }
}

struct SectionLabel: View {
    @Environment(\.rex) private var rex
    var text: String
    var body: some View {
        Text(text)
            .font(.system(size: 11, weight: .medium))
            .foregroundStyle(rex.color("text-tertiary"))
    }
}

/// What a terminal would show, on a terminal's background.
struct TerminalBlock: View {
    @Environment(\.rex) private var rex
    var text: String
    var height: CGFloat
    var body: some View {
        ScrollView {
            Text(verbatim: text.isEmpty ? " " : text)
                .font(rex.mono(11))
                .foregroundStyle(rex.color("text"))
                .textSelection(.enabled)
                .padding(8)
                // A short log starts at the top, and a long one shows its end.
                .frame(maxWidth: .infinity, minHeight: height, alignment: .topLeading)
        }
        .defaultScrollAnchor(.bottom)
        .frame(height: height)
        .background(RoundedRectangle(cornerRadius: 6).fill(rex.color("term-bg")))
        .overlay(RoundedRectangle(cornerRadius: 6).strokeBorder(rex.color("border")))
    }
}
