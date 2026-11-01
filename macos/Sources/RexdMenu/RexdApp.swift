import RexdKit
import RexdUI
import SwiftUI

@main
struct RexdApp: App {
    @State private var model = PanelModel()

    var body: some Scene {
        MenuBarExtra {
            PanelView(model: model)
        } label: {
            Image(nsImage: MenuBarIcon.image(attention: model.needsAttention))
                .accessibilityLabel("rexd")
                .task { await poll() }
        }
        .menuBarExtraStyle(.window)
    }

    /// Keeps the icon true while the panel is closed, and the panel live while it is open.
    private func poll() async {
        while !Task.isCancelled {
            if !model.isBusy { await model.refresh() }
            try? await Task.sleep(for: .seconds(model.isOpen ? 2 : 15))
        }
    }
}

/// Rex's terminal mark, in the menu bar's color. A dot says something needs a look.
enum MenuBarIcon {
    static func image(attention: Bool) -> NSImage {
        let image = NSImage(size: NSSize(width: 18, height: 18), flipped: false) { _ in
            NSColor.black.setStroke()
            let mark = NSBezierPath()
            mark.lineWidth = 1.8
            mark.lineCapStyle = .round
            mark.lineJoinStyle = .round
            mark.move(to: NSPoint(x: 3.5, y: 12.75))
            mark.line(to: NSPoint(x: 8, y: 9))
            mark.line(to: NSPoint(x: 3.5, y: 5.25))
            mark.move(to: NSPoint(x: 9.5, y: 4.75))
            mark.line(to: NSPoint(x: 15, y: 4.75))
            mark.stroke()
            if attention {
                NSColor.black.setFill()
                NSBezierPath(ovalIn: NSRect(x: 11.5, y: 10.5, width: 5.5, height: 5.5)).fill()
            }
            return true
        }
        image.isTemplate = true
        return image
    }
}
