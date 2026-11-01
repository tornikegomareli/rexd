// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "Rexd",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "Rexd", targets: ["RexdMenu"]),
        .library(name: "RexdUI", targets: ["RexdUI"]),
        .library(name: "RexdKit", targets: ["RexdKit"]),
    ],
    targets: [
        .target(name: "RexdKit"),
        .target(name: "RexdUI", dependencies: ["RexdKit"]),
        .executableTarget(name: "RexdMenu", dependencies: ["RexdKit", "RexdUI"]),
        .testTarget(name: "RexdKitTests", dependencies: ["RexdKit"]),
    ]
)
