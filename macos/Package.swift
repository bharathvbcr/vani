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
        .executable(name: "vani-apply-agent", targets: ["vani-apply-agent"]),
        .executable(name: "vani-schedule-agent", targets: ["vani-schedule-agent"]),
        .executable(name: "vani-fm", targets: ["vani-fm"]),
        .executable(name: "VaniApplyExtension", targets: ["VaniApplyExtension"]),
    ],
    targets: [
        .target(name: "VaniApplyKit"),
        .executableTarget(name: "VaniApply", dependencies: ["VaniApplyKit"]),
        .executableTarget(name: "vani-apply-host", dependencies: ["VaniApplyKit"]),
        // The login-item agent both browsers reach over XPC for `vani` jobs.
        .executableTarget(name: "vani-apply-agent", dependencies: ["VaniApplyKit"]),
        // The opt-in login item that keeps `vani daemon` running, so the daily schedule is kept.
        .executableTarget(name: "vani-schedule-agent", dependencies: ["VaniApplyKit"]),
        // The on-device (Apple Intelligence) decision helper, one JSON request in and one reply out. It
        // imports FoundationModels (macOS 26+), which the macOS 14 deployment target links weakly (checked with
        // otool -L), so the helper still starts on macOS 14 and answers "unavailable".
        .executableTarget(
            name: "vani-fm",
            dependencies: ["VaniApplyKit"]
        ),
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
        // Drives the Jobs tab's own controls in a second process (this bundle,
        // re-run as a host) through the accessibility API: SwiftPM builds no UI
        // test bundles, and XCUIApplication refuses to run in a unit test bundle.
        // The runner must be trusted for accessibility, or these tests skip.
        .testTarget(name: "VaniApplyUITests", dependencies: ["VaniApply", "VaniApplyKit"]),
    ]
)
