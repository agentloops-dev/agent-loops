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
        // The tests are in four targets (#5630), so a build tool can select, run and cache
        // each one alone, and a change to one runs only it. `Support/` and `Fixtures/` are
        // in AgentsKitTests; each other target has links to them. A suite in `Support/`
        // runs in every target, so suites go in the targets' own folders.
        //
        // Unit: the model, the protocol and the client, with nothing outside this package.
        .testTarget(
            name: "AgentsKitTests",
            dependencies: ["AgentsKit", "AgentsKitCore"],
            exclude: ["Fixtures"],
            swiftSettings: [.treatAllWarnings(as: .error)]),
        // Repo: the suites that read files outside this package, so their inputs are the
        // repo's. What each reads (keep it in step with this target's inputs in
        // Tests/AgentsKitRepoTests/project.json, #5631):
        //   ConsistencyTests       App/Sources, Remote/Sources, Remote/Info.plist, Shared/UI,
        //                          Host/Sources, Host/Info.plist, project.yml
        //   ControlAgreementVectorTests  Web/test/vectors.json
        //   ControlPushTests       Go/internal/controlplane/apns/testdata/control-push.json
        //   DesignTokensTests      design/tokens.json, Shared/UI/{Paper,StateTint,TypeScale}.swift,
        //                          Packages/CodeText/Sources/CodeText/InkValues.swift, App, Remote
        //   EventCatalogueTests    Go/ (the hosts' sources, less tests and Go/wire)
        //   FrontMatterEditTests   .agents/workflows, project.yml
        //   LucideIconsTests       design/icons.json, Shared/UI (Icons.xcassets, LucideIcons.swift,
        //                          PaperIcon.swift, WorkingMarks.swift), App/Sources, Remote/Sources
        //   MarkdownBlockTests, MarkdownStreamTests  specs/
        //   OneGroupingTests       App/Sources, Remote/Sources
        //   PassageTests           README.md
        //   ToolbarLayoutLoopTests App/Sources, Shared/UI
        //   ToolsetTests           App/Resources/toolsets
        //   WebDistManifestTests   Web/ (its build's inputs and Web/dist)
        //   WireSchemaTests        Packages/Wire (schema, daemon, docs)
        //   GovernanceWordsTests, MachineBrowserTests, MessagesListTests, MultiplierTests,
        //   WebFixturesTests       Packages/Wire/schema, by WireSchemaTests' schema
        .testTarget(
            name: "AgentsKitRepoTests",
            dependencies: ["AgentsKit", "AgentsKitCore"],
            // project.json is Nx's: this target is a project of its own (#5631).
            exclude: ["Fixtures", "project.json"],
            swiftSettings: [.treatAllWarnings(as: .error)]),
        // Control: the control plane's link, sign-in and store, and replies that fail on it.
        .testTarget(
            name: "AgentsKitControlTests",
            dependencies: ["ControlDial", "AgentsKit", "AgentsKitCore",
                           // A proxy and a server of the tests' own, for the dial (#2177).
                           .product(name: "NIOCore", package: "swift-nio"),
                           .product(name: "NIOPosix", package: "swift-nio")],
            exclude: ["Fixtures"],
            swiftSettings: [.treatAllWarnings(as: .error)]),
        // Integration: real sockets and processes, and the live tests.
        .testTarget(
            name: "AgentsKitIntegrationTests",
            dependencies: ["ControlDial", "AgentsKit", "AgentsKitCore", "accept-probe",
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
