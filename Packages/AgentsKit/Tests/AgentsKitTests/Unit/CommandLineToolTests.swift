#if os(macOS)
import AgentsKitCore
import Foundation
import Testing

/// `loops` on the PATH (#6875): a link into the app, made with no password, and never over
/// another program.
@Suite("The loops command's link")
struct CommandLineToolTests {
    /// A scratch home with an app that carries loops.
    private func scratch() throws -> (home: URL, program: URL) {
        let home = FileManager.default.temporaryDirectory.appendingPathComponent("cli-\(UUID().uuidString)", isDirectory: true)
        let helpers = home.appendingPathComponent("Applications/Agent Loops.app/Contents/Helpers", isDirectory: true)
        try FileManager.default.createDirectory(at: helpers, withIntermediateDirectories: true)
        let program = helpers.appendingPathComponent("loops")
        try Data("#!/bin/sh\n".utf8).write(to: program)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: program.path)
        return (home, program)
    }

    @Test func installLinksIntoTheAppAndRemoveTakesItAway() throws {
        let (home, program) = try scratch()
        defer { try? FileManager.default.removeItem(at: home) }
        let tool = CommandLineTool(home: home, program: program, environment: [:])
        #expect(tool.state == .notInstalled)
        try tool.install()
        #expect(tool.state == .installed)
        #expect(try FileManager.default.destinationOfSymbolicLink(atPath: tool.link.path) == program.path)
        try tool.install() // again: nothing changes
        #expect(tool.state == .installed)
        try tool.remove()
        #expect(tool.state == .notInstalled)
    }

    @Test func anotherCopysLinkIsPointedAtThisApp() throws {
        let (home, program) = try scratch()
        defer { try? FileManager.default.removeItem(at: home) }
        let tool = CommandLineTool(home: home, program: program, environment: [:])
        try FileManager.default.createDirectory(at: tool.folder, withIntermediateDirectories: true)
        try FileManager.default.createSymbolicLink(atPath: tool.link.path, withDestinationPath: "/Applications/Old Agent Loops.app/Contents/Helpers/loops")
        guard case .otherCopy = tool.state else { Issue.record("state: \(tool.state)"); return }
        try tool.install()
        #expect(tool.state == .installed)
    }

    @Test func anotherProgramStays() throws {
        let (home, program) = try scratch()
        defer { try? FileManager.default.removeItem(at: home) }
        let tool = CommandLineTool(home: home, program: program, environment: [:])
        try FileManager.default.createDirectory(at: tool.folder, withIntermediateDirectories: true)
        try Data("a release download".utf8).write(to: tool.link)
        #expect(tool.state == .otherFile)
        #expect(throws: CommandLineTool.Failure.otherFile) { try tool.install() }
        #expect(throws: CommandLineTool.Failure.otherFile) { try tool.remove() }
        #expect(try String(contentsOf: tool.link, encoding: .utf8) == "a release download")
        // A link to a program outside any app is another program too.
        try FileManager.default.removeItem(at: tool.link)
        try FileManager.default.createSymbolicLink(atPath: tool.link.path, withDestinationPath: program.path.replacingOccurrences(of: "Agent Loops.app/Contents/Helpers/", with: ""))
        try Data().write(to: URL(fileURLWithPath: program.path.replacingOccurrences(of: "Agent Loops.app/Contents/Helpers/", with: "")))
        #expect(tool.state == .otherFile)
    }

    @Test func pairedIsAPairingInHostsJSON() throws {
        let (home, program) = try scratch()
        defer { try? FileManager.default.removeItem(at: home) }
        let config = home.appendingPathComponent("xdg", isDirectory: true)
        let tool = CommandLineTool(home: home, program: program, environment: ["XDG_CONFIG_HOME": config.path])
        #expect(!tool.paired)
        try FileManager.default.createDirectory(at: tool.config, withIntermediateDirectories: true)
        try Data(#"{"hosts":[]}"#.utf8).write(to: tool.config.appendingPathComponent("hosts.json"))
        #expect(!tool.paired)
        try Data(#"{"default":"https://c","hosts":[{"client":"C1"}]}"#.utf8).write(to: tool.config.appendingPathComponent("hosts.json"))
        #expect(tool.paired)
    }

    @Test func thePathLineIsTheShellsOwn() throws {
        let tool = CommandLineTool(home: URL(fileURLWithPath: "/Users/someone"), program: URL(fileURLWithPath: "/x/loops"), environment: [:])
        #expect(tool.onPath("/usr/bin:/Users/someone/.local/bin"))
        #expect(tool.onPath("/Users/someone/.local/bin/:/bin"))
        #expect(!tool.onPath("/usr/bin:/bin"))
        #expect(CommandLineTool.pathLine(shell: "/bin/zsh").file == "~/.zshrc")
        #expect(CommandLineTool.pathLine(shell: "/opt/homebrew/bin/fish").line == "fish_add_path ~/.local/bin")
        #expect(CommandLineTool.pathLine(shell: "/bin/bash").line == "export PATH=\"$HOME/.local/bin:$PATH\"")
    }
}
#endif
