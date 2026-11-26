import AgentsKitCore
import AppKit
import SwiftUI

/// The `loops` command on this Mac (#6875): set-up links it, and Settings ▸ Machines ▸ This
/// Mac ▸ Command line shows it. One for the app.
@MainActor
@Observable
final class LoopsCommand {
    static let shared = LoopsCommand()
    /// The app menu's item: Agent Loops ▸ Install loops Command….
    static let menuItem = "Install loops Command…"

    private(set) var state: CommandLineTool.State = .notInstalled
    private(set) var paired = false
    /// Whether `~/.local/bin` is on the login shell's `PATH`; nil until read.
    private(set) var onPath: Bool?
    /// What the last Install or Remove said, when it failed.
    private(set) var problem: String?

    let tool: CommandLineTool
    /// The login shell, for the line to add.
    let shell: String

    init(environment: [String: String] = ProcessInfo.processInfo.environment) {
        // A walk's scratch home (#6875): the real ~/.local/bin is never written by a scratch app.
        let home = environment["AGENTS_TEST_HOME"].map { URL(fileURLWithPath: $0, isDirectory: true) }
            ?? FileManager.default.homeDirectoryForCurrentUser
        tool = CommandLineTool(home: home, program: HostPaths.current.helpers.appendingPathComponent("loops"), environment: environment)
        shell = environment["SHELL"] ?? "/bin/zsh"
    }

    var pathLine: (line: String, file: String) { CommandLineTool.pathLine(shell: shell) }

    /// Reads the link, the pairing and the `PATH` again.
    func refresh() async {
        state = tool.state
        paired = tool.paired
        let shell = shell
        let path = await Task.detached { Self.loginPath(shell: shell) }.value
        onPath = path.map(tool.onPath)
    }

    func install() async {
        do {
            try tool.install()
            problem = nil
        } catch {
            problem = error.localizedDescription
        }
        await refresh()
    }

    func remove() async {
        do {
            try tool.remove()
            problem = nil
        } catch {
            problem = error.localizedDescription
        }
        await refresh()
    }

    /// The set-up step (#6875): the link, with no question. The words when it failed, for
    /// the line under the box; nil when it is there, or another program is (which stays).
    func installAtSetUp() async -> String? {
        if case .otherFile = tool.state { return nil }
        await install()
        return problem.map { "loops is not on your PATH: \($0) Install it in Settings ▸ This Mac ▸ Command line." }
    }

    /// The login shell's `PATH`, as a terminal gets it: `$SHELL -l -c`, in 5 s at most.
    nonisolated static func loginPath(shell: String) -> String? {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: shell)
        process.arguments = ["-l", "-c", "printf %s \"$PATH\""]
        let out = Pipe()
        process.standardOutput = out
        process.standardError = FileHandle.nullDevice
        process.standardInput = FileHandle.nullDevice
        let ended = DispatchSemaphore(value: 0)
        process.terminationHandler = { _ in ended.signal() }
        do { try process.run() } catch { return nil }
        if ended.wait(timeout: .now() + 5) == .timedOut {
            process.terminate()
            return nil
        }
        let data = out.fileHandleForReading.readDataToEndOfFile()
        return String(data: data, encoding: .utf8)
    }
}

/// Settings ▸ Machines ▸ This Mac ▸ Command line (#6875): whether `loops` is installed and
/// paired, Install or Remove, and the `PATH` line to copy when its folder is not on the
/// login shell's `PATH`. The app never edits a shell file.
struct CommandLineSection: View {
    @State private var cli = LoopsCommand.shared
    @State private var copied = false

    var body: some View {
        Section {
            LabeledContent("loops") {
                HStack(spacing: 8) {
                    // Whole, never cut: the path is the state (#6875).
                    Text(stateWords).foregroundStyle(.secondary).fixedSize()
                    switch cli.state {
                    case .installed:
                        Button("Remove") { Task { await cli.remove() } }
                    case .notInstalled, .otherCopy:
                        Button("Install") { Task { await cli.install() } }
                    case .otherFile:
                        EmptyView()
                    }
                }
            }
            if cli.state == .installed {
                LabeledContent("Pairing", value: cli.paired ? "Paired" : "Not paired")
            }
            if cli.state == .installed, cli.onPath == false {
                VStack(alignment: .leading, spacing: 6) {
                    Text("\(CommandLineTool.linkWords.replacingOccurrences(of: "/loops", with: "")) is not on your PATH. Add this line to \(cli.pathLine.file):")
                        .appText(.fine).foregroundStyle(.secondary)
                    HStack(spacing: 8) {
                        Text(cli.pathLine.line)
                            .appText(.code)
                            .textSelection(.enabled)
                        Spacer(minLength: 8)
                        Button(copied ? "Copied" : "Copy") { copy() }
                    }
                }
            }
            if let problem = cli.problem {
                Text(problem).appText(.fine).tinted(.failure)
            }
        } header: {
            Text("Command line")
        } footer: {
            Text(footer).appText(.fine).foregroundStyle(.secondary)
        }
        .paperListRow()
        .task { await cli.refresh() }
    }

    private var stateWords: String {
        switch cli.state {
        case .installed: "Installed at \(CommandLineTool.linkWords)"
        case .notInstalled: "Not installed"
        case .otherCopy: "Installed from another copy of Agent Loops"
        case .otherFile: "\(CommandLineTool.linkWords) is another program"
        }
    }

    private var footer: String {
        switch cli.state {
        case .otherFile:
            "Agent Loops leaves that program as it is. Remove it yourself to use the loops that comes with Agent Loops."
        default:
            "loops starts and continues sessions from a terminal. The link goes into Agent Loops, so loops updates when the app does. The first time it runs, Agent Loops asks you to approve it in Needs you."
        }
    }

    private func copy() {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(cli.pathLine.line, forType: .string)
        copied = true
    }
}
