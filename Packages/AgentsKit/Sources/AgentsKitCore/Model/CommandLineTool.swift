#if os(macOS)
import Foundation

/// `loops` on the `PATH` (#6875): a link `~/.local/bin/loops` to the `loops` that this app
/// carries in `Contents/Helpers`, so the command updates when the app does. No administrator
/// password (hosts never escalate), and the person's shell files are never edited: when the
/// folder is not on the login shell's `PATH`, Settings shows the one line to add.
///
/// Set-up links it with no question; Settings ▸ Machines ▸ This Mac ▸ Command line shows
/// its state, with Install and Remove. A file there that is not a link into an Agent Loops
/// app (a release download, a Homebrew `loops`) is left as it is.
public struct CommandLineTool: Sendable, Equatable {
    /// What is at the link's place now.
    public enum State: Sendable, Equatable {
        case notInstalled
        /// A link to this app's `loops`.
        case installed
        /// A link into another copy of Agent Loops, or one whose program is gone: Install
        /// points it at this app.
        case otherCopy(String)
        /// Another program, which Agent Loops leaves alone.
        case otherFile
    }

    /// The folder the link goes in: `~/.local/bin`.
    public let folder: URL
    /// This app's own `loops`.
    public let program: URL
    /// `XDG_CONFIG_HOME/loops`, else `~/.config/loops`: where `loops` keeps its pairings.
    public let config: URL

    public init(home: URL, program: URL, environment: [String: String] = ProcessInfo.processInfo.environment) {
        folder = home.appendingPathComponent(".local/bin", isDirectory: true)
        self.program = program
        if let xdg = environment["XDG_CONFIG_HOME"], !xdg.isEmpty {
            config = URL(fileURLWithPath: xdg, isDirectory: true).appendingPathComponent("loops", isDirectory: true)
        } else {
            config = home.appendingPathComponent(".config/loops", isDirectory: true)
        }
    }

    /// `~/.local/bin/loops`.
    public var link: URL { folder.appendingPathComponent("loops") }

    /// The link as a person reads it: `~/.local/bin/loops`.
    public static let linkWords = "~/.local/bin/loops"

    public var state: State {
        let manager = FileManager.default
        guard let target = try? manager.destinationOfSymbolicLink(atPath: link.path) else {
            return manager.fileExists(atPath: link.path) ? .otherFile : .notInstalled
        }
        let resolved = URL(fileURLWithPath: target, relativeTo: folder).standardizedFileURL.path
        if resolved == program.standardizedFileURL.path { return .installed }
        return Self.isAppHelper(resolved) || !manager.fileExists(atPath: resolved) ? .otherCopy(resolved) : .otherFile
    }

    /// A path to `loops` inside an app's Helpers.
    static func isAppHelper(_ path: String) -> Bool { path.hasSuffix(".app/Contents/Helpers/loops") }

    /// Whether `loops` keeps a pairing: its hosts.json names one. Only its count is read.
    public var paired: Bool {
        guard let data = try? Data(contentsOf: config.appendingPathComponent("hosts.json")),
              let file = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let hosts = file["hosts"] as? [Any] else { return false }
        return !hosts.isEmpty
    }

    public enum Failure: Error, Equatable, LocalizedError {
        /// Another program is there: it stays.
        case otherFile
        case noProgram
        case cannot(String)

        public var errorDescription: String? {
            switch self {
            case .otherFile: "\(CommandLineTool.linkWords) is another program, so Agent Loops leaves it as it is."
            case .noProgram: "This copy of Agent Loops has no loops in it."
            case .cannot(let why): why
            }
        }
    }

    /// Install: the link, made or pointed at this app. Never over another program.
    public func install() throws {
        let manager = FileManager.default
        guard manager.isExecutableFile(atPath: program.path) else { throw Failure.noProgram }
        switch state {
        case .installed: return
        case .otherFile: throw Failure.otherFile
        case .otherCopy:
            do { try manager.removeItem(at: link) } catch { throw Failure.cannot(error.localizedDescription) }
        case .notInstalled: break
        }
        do {
            try manager.createDirectory(at: folder, withIntermediateDirectories: true)
            try manager.createSymbolicLink(atPath: link.path, withDestinationPath: program.path)
        } catch {
            throw Failure.cannot(error.localizedDescription)
        }
    }

    /// Remove: the link, when it is one into an Agent Loops app. Another program stays.
    public func remove() throws {
        switch state {
        case .notInstalled: return
        case .otherFile: throw Failure.otherFile
        case .installed, .otherCopy:
            do { try FileManager.default.removeItem(at: link) } catch { throw Failure.cannot(error.localizedDescription) }
        }
    }

    /// Whether `folder` is on `path`, a `PATH` as the login shell sets it.
    public func onPath(_ path: String) -> Bool {
        let want = folder.standardizedFileURL.path
        return path.split(separator: ":").contains { URL(fileURLWithPath: String($0)).standardizedFileURL.path == want }
    }

    /// The line the person adds to their shell's start-up file, and that file, by the shell's
    /// program: fish's own command, else the line zsh and bash read.
    public static func pathLine(shell: String) -> (line: String, file: String) {
        switch (shell as NSString).lastPathComponent {
        case "fish": ("fish_add_path ~/.local/bin", "~/.config/fish/config.fish")
        case "bash": ("export PATH=\"$HOME/.local/bin:$PATH\"", "~/.bash_profile")
        default: ("export PATH=\"$HOME/.local/bin:$PATH\"", "~/.zshrc")
        }
    }
}
#endif
