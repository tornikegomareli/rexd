// Draws the app's icon: Rex's terminal mark on a dark square. Usage: make_icon.swift OUTPUT.iconset
import AppKit

let output = CommandLine.arguments[1]
try FileManager.default.createDirectory(atPath: output, withIntermediateDirectories: true)

func draw(pixels: Int) -> Data {
    let rep = NSBitmapImageRep(
        bitmapDataPlanes: nil, pixelsWide: pixels, pixelsHigh: pixels, bitsPerSample: 8, samplesPerPixel: 4,
        hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
    let size = CGFloat(pixels)
    let inset = size * 0.1
    let square = NSRect(x: inset, y: inset, width: size - inset * 2, height: size - inset * 2)
    let shape = NSBezierPath(roundedRect: square, xRadius: size * 0.18, yRadius: size * 0.18)
    NSColor(srgbRed: 13 / 255, green: 17 / 255, blue: 23 / 255, alpha: 1).setFill()
    shape.fill()
    NSColor(srgbRed: 46 / 255, green: 49 / 255, blue: 53 / 255, alpha: 1).setStroke()
    shape.lineWidth = max(size * 0.006, 1)
    shape.stroke()
    let mark = NSBezierPath()
    mark.lineWidth = size * 0.07
    mark.lineCapStyle = .round
    mark.lineJoinStyle = .round
    mark.move(to: NSPoint(x: size * 0.29, y: size * 0.66))
    mark.line(to: NSPoint(x: size * 0.47, y: size * 0.5))
    mark.line(to: NSPoint(x: size * 0.29, y: size * 0.34))
    NSColor(srgbRed: 88 / 255, green: 166 / 255, blue: 1, alpha: 1).setStroke()
    mark.stroke()
    let cursor = NSBezierPath()
    cursor.lineWidth = size * 0.07
    cursor.lineCapStyle = .round
    cursor.move(to: NSPoint(x: size * 0.52, y: size * 0.32))
    cursor.line(to: NSPoint(x: size * 0.72, y: size * 0.32))
    NSColor(srgbRed: 230 / 255, green: 237 / 255, blue: 243 / 255, alpha: 1).setStroke()
    cursor.stroke()
    NSGraphicsContext.restoreGraphicsState()
    return rep.representation(using: .png, properties: [:])!
}

for (name, pixels) in [
    ("16x16", 16), ("16x16@2x", 32), ("32x32", 32), ("32x32@2x", 64), ("128x128", 128),
    ("128x128@2x", 256), ("256x256", 256), ("256x256@2x", 512), ("512x512", 512), ("512x512@2x", 1024),
] {
    try draw(pixels: pixels).write(to: URL(fileURLWithPath: "\(output)/icon_\(name).png"))
}
