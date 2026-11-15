// swift-tools-version: 6.2
import PackageDescription

// Two targets, split by platform, because the phone needs the model and the protocol
// and cannot have `Process`, `posix_spawn` or a PTY.
//
// `AgentsKitCore` is everything both platforms can hold: the record, the JSON-RPC
// line protocol, the daemon's method names, and the client that speaks them over any
// transport. `AgentsKit` is what Agents Host needs beyond it (the daemon
// itself is Go since #1138, and its Swift sources went in #1314), and re-exports Core.
//
// Written as a string rather than `.macOS(.v27)`: this tools version has no `.v27`
// case and the enum form does not compile.
let package = Package(
    name: "AgentsKit",
    platforms: [.macOS("27.0"), .iOS("27.0")],
    products: [
        .library(name: "AgentsKit", targets: ["AgentsKit"]),
        // The phone links this one. It is the only product that builds for iOS.
        .library(name: "AgentsKitCore", targets: ["AgentsKitCore"]),
        // The control plane's WebSocket (058): the tests dial and serve with it. Never
        // linked by the apps.
        .library(name: "ControlDial", targets: ["ControlDial"]),
    ],
    dependencies: [
        // The control plane's WebSocket (058): `ControlDial`, for the tests.
        // `swift-crypto` is not a dependency: its BoringSSL would be a second copy.
        .package(url: "https://github.com/apple/swift-nio.git", from: "2.80.0"),
        .package(url: "https://github.com/apple/swift-nio-ssl.git", from: "2.29.0"),
    ],
    targets: [
        .target(name: "AgentsKitCore", swiftSettings: [.treatAllWarnings(as: .error)]),
        .target(
            name: "ControlDial",
            dependencies: [
                "AgentsKitCore",
                .product(name: "NIOCore", package: "swift-nio"),
                .product(name: "NIOPosix", package: "swift-nio"),
                .product(name: "NIOHTTP1", package: "swift-nio"),
                .product(name: "NIOWebSocket", package: "swift-nio"),
                .product(name: "NIOSSL", package: "swift-nio-ssl"),
            ]),
        // What Agents Host still links (#1314): the move of a set-up, ssh, and the local
        // socket's server.
        // The daemon is Go (#1138). SwiftTerm belongs to the app, declared in `project.yml`.
        .target(
            name: "AgentsKit",
            dependencies: ["AgentsKitCore"],
            swiftSettings: [.treatAllWarnings(as: .error)]),
        // Spike #5613: the tests are split in four targets, so a build tool can run and cache
        // each one alone. Support/ and Fixtures/ are in AgentsKitTests; the others link them.
        .testTarget(
            name: "AgentsKitTests",
            dependencies: ["AgentsKit", "AgentsKitCore"],
            exclude: ["Fixtures"],
            swiftSettings: [.treatAllWarnings(as: .error)]),
        // The tests that read files outside the package (App/, Shared/UI, Web/, Go/, design/,
        // docs/, specs/, Packages/Wire): their inputs are the repo's, not the package's.
        .testTarget(
            name: "AgentsKitRepoTests",
            dependencies: ["AgentsKit", "AgentsKitCore"],
            exclude: ["Fixtures"],
            swiftSettings: [.treatAllWarnings(as: .error)]),
        .testTarget(
            name: "AgentsKitControlTests",
            dependencies: ["ControlDial", "AgentsKit", "AgentsKitCore",
                           .product(name: "NIOCore", package: "swift-nio"),
                           .product(name: "NIOPosix", package: "swift-nio")],
            exclude: ["Fixtures"],
            swiftSettings: [.treatAllWarnings(as: .error)]),
        .testTarget(
            name: "AgentsKitIntegrationTests",
            dependencies: ["ControlDial", "AgentsKit", "AgentsKitCore", "accept-probe",
                           // A proxy and a server of the tests' own, for the dial (#2177).
                           .product(name: "NIOCore", package: "swift-nio"),
                           .product(name: "NIOPosix", package: "swift-nio")],
            exclude: ["Fixtures"],
            swiftSettings: [.treatAllWarnings(as: .error)]),
        // Tests only, and no product: a daemon socket in a process of its own with few
        // descriptors (#201). Out of descriptors is the whole process's state, so it
        // cannot be done to a server inside the test process beside every other test.
        .executableTarget(
            name: "accept-probe",
            dependencies: ["AgentsKit"],
            path: "Tests/AcceptProbe",
            swiftSettings: [.treatAllWarnings(as: .error)]),
    ]
)
