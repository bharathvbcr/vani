// swift-tools-version: 6.0
// Vani Apply for macOS: a control app, a Safari web extension and a Chrome
// native messaging host sharing one answer bank in an App Group container.
// Assembled into "Vani Apply.app" by scripts/bundle.sh (no Xcode project).
import PackageDescription

let package = Package(
    name: "VaniApply",
    platforms: [.macOS(.v14)],
    products: [
        .library(name: "VaniApplyKit", targets: ["VaniApplyKit"]),
        .executable(name: "VaniApply", targets: ["VaniApply"]),
        .executable(name: "vani-apply-host", targets: ["vani-apply-host"]),
        .executable(name: "VaniApplyExtension", targets: ["VaniApplyExtension"]),
    ],
    targets: [
        .target(name: "VaniApplyKit"),
        .executableTarget(name: "VaniApply", dependencies: ["VaniApplyKit"]),
        .executableTarget(name: "vani-apply-host", dependencies: ["VaniApplyKit"]),
        // The Safari appex: its entry point is Foundation's NSExtensionMain,
        // which loads the principal class named in the bundle's Info.plist.
        .executableTarget(
            name: "VaniApplyExtension",
            dependencies: ["VaniApplyKit"],
            swiftSettings: [.unsafeFlags(["-application-extension"])],
            linkerSettings: [
                .unsafeFlags(["-Xlinker", "-e", "-Xlinker", "_NSExtensionMain", "-Xlinker", "-application_extension"]),
                .linkedFramework("SafariServices"),
            ]
        ),
        // Reads extension/test/vectors/classify.json in place, so the Swift and
        // TypeScript classifiers are held to one set of vectors.
        .testTarget(name: "VaniApplyKitTests", dependencies: ["VaniApplyKit"]),
    ]
)
