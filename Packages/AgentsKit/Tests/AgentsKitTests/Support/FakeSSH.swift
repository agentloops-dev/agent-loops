import Foundation

/// The fake `ssh` in `Fixtures/ssh/`, and a server home for it to run commands in (037).
///
/// See `Fixtures/ssh/README.md`. Each test gets its own home and control folder, so
/// several can run at once, and removes them when it is done.
struct FakeSSH {
    static let fixtures = URL(filePath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent()
        .appendingPathComponent("Fixtures/ssh", isDirectory: true)

    /// Everything this fake server has, under one temporary folder.
    let folder: URL
    /// The server's `$HOME`.
    let home: URL
    /// Where the window's control and forward sockets go. Short, because a Unix socket
    /// path has 104 bytes and `NSTemporaryDirectory()` spends half of them.
    let hosts: URL

    init() throws {
        let id = UUID().uuidString.prefix(8).lowercased()
        folder = URL(filePath: "/tmp/fs-\(id)", directoryHint: .isDirectory)
        home = folder.appendingPathComponent("home", isDirectory: true)
        hosts = folder.appendingPathComponent("h", isDirectory: true)
        try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: hosts, withIntermediateDirectories: true)
    }
}
