import Foundation

public struct ThemeColor: Sendable, Equatable {
    public var red, green, blue, alpha: Double

    /// Reads Rex's forms: `12 14 17`, `rgb(52 133 250)`, `rgb(230 237 243 / 0.042)` and `#0d1117`.
    public init?(css: String) {
        let text = css.trimmingCharacters(in: .whitespaces)
        if text.hasPrefix("#"), text.count == 7, let value = Int(text.dropFirst(), radix: 16) {
            self.init(byte: (value >> 16) & 255, (value >> 8) & 255, value & 255)
            return
        }
        let inner = text.hasPrefix("rgb(") ? String(text.dropFirst(4).dropLast()) : text
        let sides = inner.components(separatedBy: "/")
        let numbers = sides[0].split(separator: " ").compactMap { Int($0) }
        guard numbers.count == 3 else { return nil }
        self.init(byte: numbers[0], numbers[1], numbers[2])
        if sides.count == 2 {
            guard let alpha = Double(sides[1].trimmingCharacters(in: .whitespaces)) else { return nil }
            self.alpha = alpha
        }
    }

    private init(byte red: Int, _ green: Int, _ blue: Int) {
        self.red = Double(red) / 255
        self.green = Double(green) / 255
        self.blue = Double(blue) / 255
        alpha = 1
    }
}

/// The colors and the font of the person's Rex, so the panel looks like the rest of it.
/// Rex keeps these in its own files, which can change, so anything missing falls back to
/// Rex's default theme.
public struct RexTheme: Sendable, Equatable {
    public enum Appearance: Sendable { case light, dark }

    public var id: String
    public var appearance: Appearance
    public var fontName: String?
    private var tokens: [String: ThemeColor]

    public subscript(token: String) -> ThemeColor {
        tokens[token] ?? RexTheme.fallback(appearance)[token] ?? ThemeColor(css: "128 128 128")!
    }

    public static let tokenNames = [
        "bg", "sidebar", "raised", "term-bg", "border", "border-strong", "hover", "selected", "text",
        "text-secondary", "text-tertiary", "accent", "accent-solid", "accent-on-solid", "danger",
        "danger-container", "success", "warning",
    ]

    /// The theme for the appearance Rex shows: its own setting, or else the system's.
    public static func load(systemIsDark: Bool, settings: UserDefaults?, themes: URL?) -> RexTheme {
        let appearance: Appearance
        switch settings?.string(forKey: "Workspace.appearance") {
        case "light": appearance = .light
        case "dark": appearance = .dark
        default: appearance = systemIsDark ? .dark : .light
        }
        let isDark = appearance == .dark
        let key = isDark ? "Workspace.darkThemeID" : "Workspace.lightThemeID"
        let id = settings?.string(forKey: key) ?? (isDark ? "merino-dark" : "merino-light")
        var theme = RexTheme(id: id, appearance: appearance, fontName: settings?.string(forKey: "Workspace.terminalFontName"), tokens: [:])
        if let themes, let found = read(id: id, from: themes) {
            theme.tokens = found
        } else {
            theme.tokens = fallback(appearance)
        }
        return theme
    }

    private static func read(id: String, from url: URL) -> [String: ThemeColor]? {
        struct Entry: Decodable {
            var id: String
            var cssVariables: [String: String]
        }
        guard let data = try? Data(contentsOf: url), let entries = try? JSONDecoder().decode([Entry].self, from: data),
              let entry = entries.first(where: { $0.id == id })
        else { return nil }
        var tokens: [String: ThemeColor] = [:]
        for name in tokenNames {
            if let css = entry.cssVariables["--super-\(name)"], let color = ThemeColor(css: css) {
                tokens[name] = color
            }
        }
        return tokens.isEmpty ? nil : tokens
    }

    private static func fallback(_ appearance: Appearance) -> [String: ThemeColor] {
        (appearance == .dark ? merinoDark : merinoLight).compactMapValues(ThemeColor.init(css:))
    }

    private static let merinoDark = [
        "bg": "12 14 17", "sidebar": "19 21 25", "raised": "30 32 35", "term-bg": "13 17 23",
        "border": "46 49 53", "border-strong": "66 69 73", "hover": "rgb(230 237 243 / 0.042)",
        "selected": "rgb(230 237 243 / 0.073)", "text": "230 237 243", "text-secondary": "130 135 140",
        "text-tertiary": "102 106 111", "accent": "rgb(52 133 250)", "accent-solid": "47 129 247",
        "accent-on-solid": "13 17 23", "danger": "255 123 114", "danger-container": "rgb(46 33 37)",
        "success": "63 185 80", "warning": "210 153 34",
    ]

    private static let merinoLight = [
        "bg": "246 247 249", "sidebar": "236 238 242", "raised": "250 251 252", "term-bg": "255 255 255",
        "border": "231 233 235", "border-strong": "209 210 213", "hover": "rgb(31 35 40 / 0.041)",
        "selected": "rgb(31 35 40 / 0.079)", "text": "31 35 40", "text-secondary": "105 109 113",
        "text-tertiary": "135 138 142", "accent": "rgb(6 103 216)", "accent-solid": "9 105 218",
        "accent-on-solid": "255 255 255", "danger": "207 34 46", "danger-container": "rgb(254 233 231)",
        "success": "17 99 41", "warning": "157 92 0",
    ]
}
