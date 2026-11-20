import Foundation

// A policy is which of a runtime's own tools an agent started by this app may keep.
//
// Every runtime arrives holding its own version of nearly everything this app owns: a
// way to make something happen on a schedule, a way to raise a question to somebody, a
// way to start another agent, somewhere to put what it wrote. The app has been arguing
// with those in words, and words lose — an instruction sits in the first prompt of a
// conversation, and a tool sits in front of the model on every single turn. So the tool
// goes instead, for the sessions this app starts and only for those. Nothing here reads
// or writes anything of the person's, and nothing is left behind when the app is not
// running.
//
// It is a table rather than a run of conditionals because of the rule the README sets
// for the whole app: no code in the app asks which runtime it is talking to. `Lever` is
// five ways of *asking* — a deny list in the session, flags at launch, JSON in a variable, a file the runtime is pointed at, or nothing at
// all — and the runtime
// id appears exactly once, as the
// key this table is looked up by.
//
// Nothing in here was read out of documentation. Every name, every key path and every
// flag was sent to the real runtime and the answer checked; the measurements, with their
// dates and versions, are in `specs/015-runtime-tool-scoping/research.md`. That is also
// why `residue` exists: three of the four runtimes will not let go of everything, and a
// policy that pretended otherwise would be a policy nobody could trust.

/// What job of this app's a tool duplicates.
public enum RemitCategory: String, Codable, Hashable, Sendable, CaseIterable {
    /// Schedules, cron entries, timers, wake-ups, monitors and standing goals.
    case standingArrangements
    /// Raising a question, a form or a notification to a person.
    case escalation
    /// Creating, starting, messaging, inspecting or stopping other agents.
    case agents
    /// Storing work outside the folder: document stores, drives, session databases.
    case artefacts
    /// Offering the person a follow-up prompt.
    case suggestions
    /// Changing the folder the session works in, or making a worktree to move into (053).
    case workingFolder

    /// What to do instead, said in the one sentence an agent is given.
    ///
    /// Every removal and every piece of residue names exactly one category, which is
    /// what makes this table arguable rather than a list of names somebody disliked —
    /// and it is why the briefing line and the permission refusal cannot disagree about
    /// a tool: they both read this.
    ///
    /// Exhaustive on purpose, with no `default`: a category added without a sentence is
    /// a category an agent would be refused with silence.
    public var instead: String {
        switch self {
        case .standingArrangements:
            "Use `\(AppTool.manageWorkflows)` for anything that has to happen on its own."
        case .escalation:
            "Ask me with your question or form tool; it reaches me wherever I am."
        case .agents:
            "This app starts and stops agents; ask me rather than starting one."
        case .artefacts:
            "Put it in the conversation or in a file in this folder."
        case .suggestions:
            "If you suggest a next step, say it in your reply."
        case .workingFolder:
            "Use `\(AppTool.moveWorktree)` to change where you work."
        }
    }
}

/// A tool taken away, and the job of ours it was duplicating.
public struct RemovedTool: Codable, Hashable, Sendable {
    /// The runtime's own id for the tool, spelled the way its lever spells it: bare
    /// (`task`), whole-server (`mcp__claude_ai_Google_Drive`) or exact (`ScheduleWakeup`).
    public var name: String
    public var category: RemitCategory

    public init(name: String, category: RemitCategory) {
        self.name = name
        self.category = category
    }
}

/// A runtime's sign-in relayed from the Mac to a server (047, research R12; Claude's, 056).
///
/// The sign-in stays on the Mac. On the server the runtime starts with a stand-in that holds
/// no secret, pointed at the relay gate on loopback over TLS it is told to trust; the Mac's
/// relay puts its own current token on each request and sends it on to `upstreamHost`.
public struct SignInRelay: Codable, Hashable, Sendable {
    /// Where the Mac's own sign-in is kept.
    public enum MacSignInLocation: Codable, Hashable, Sendable {
        /// A file, relative to the home folder (Codex's `~/.codex/auth.json`).
        case file(String)
        /// A generic password in the login Keychain, as `/usr/bin/security` reads it
        /// (Claude's `Claude Code-credentials`, 056 research R5).
        case keychain(service: String)
    }

    /// How the runtime on the server is pointed at the gate.
    public enum Pointing: Codable, Hashable, Sendable {
        /// A home of the app's own holding a config (with `{port}` for the gate's port) and
        /// the stand-in sign-in file, named by `homeVariable` (Codex).
        case home(homeVariable: String, configFile: String, signInFile: String, configTemplate: String)
        /// Variables only; their values may hold `{port}` and `{standIn}` (Claude, 056 R7).
        case environment([String: String])
    }

    public var upstreamHost: String
    public var macSignIn: MacSignInLocation
    public var pointing: Pointing
    /// The variable naming the CA certificate the runtime is told to trust.
    public var certificateVariable: String
    /// Taken out of the environment of a relayed run, so no other sign-in wins over it.
    public var clearedVariables: [String]
    /// Where a server's own sign-in for this runtime may be, to tell whether it has one:
    /// variables in the login environment, and a file relative to the home folder.
    public var ownSignInVariables: [String]
    public var ownSignInFile: String?

    public init(upstreamHost: String, macSignIn: MacSignInLocation, pointing: Pointing,
                certificateVariable: String, clearedVariables: [String] = [],
                ownSignInVariables: [String] = [], ownSignInFile: String? = nil) {
        self.upstreamHost = upstreamHost
        self.macSignIn = macSignIn
        self.pointing = pointing
        self.certificateVariable = certificateVariable
        self.clearedVariables = clearedVariables
        self.ownSignInVariables = ownSignInVariables
        self.ownSignInFile = ownSignInFile
    }
}

/// How a removal is asked for.
///
/// Five ways of asking, not five runtimes. Nothing downstream switches on a runtime id:
/// the daemon asks the policy for a `_meta` object, a list of launch arguments and a set
/// of environment files, and sends whatever comes back.
///
/// There is no allow list. Grok's was the last, and Grok 1.0.52 stopped giving the
/// sub-agent tools it named (#1447): a deny list is the one claim that stays true when a
/// runtime adds a tool.
public enum Lever: Hashable, Sendable {
    /// The removed names go into an array at this key path inside `session/new` `_meta`;
    /// `extra` is whatever else that object needs to be accepted.
    case sessionMetaDenyList(path: [String], extra: [String: JSONValue] = [:])
    /// The removed names follow `flag` on the command line, and `extra` is the rest of
    /// the flags.
    case launchArguments(flag: String, repeatsFlag: Bool, extra: [String])
    /// A JSON object in an environment variable, read by the runtime at start and merged
    /// into its own config for that process only (047: Codex's `CODEX_CONFIG`, whose
    /// feature switches take the removed tools away). The removed names are for the
    /// briefing; the switches that remove them are in `value`.
    case environmentJSON(variable: String, value: JSONValue)
    /// No mechanism at all. Everything conflicting is residue.
    case words
}

/// One runtime's policy: what goes, what deliberately stays, and what we could not move.
public struct ToolPolicy: Hashable, Sendable {
    public var runtimeID: String
    public var removed: [RemovedTool] = []
    public var lever: Lever
    /// The order to offer this runtime's own sign-in methods in, by id, first to last (047:
    /// Codex puts ChatGPT before an API key, D1). Methods not named keep their order after
    /// these. Empty keeps the rule for everyone else: the first one without a terminal.
    public var preferredAuthMethods: [String]
    /// How this runtime's sign-in is relayed from the Mac to a server (047), or nil when it
    /// is not.
    public var relay: SignInRelay?
    public init(runtimeID: String,
                removed: [RemovedTool] = [],
                lever: Lever,
                preferredAuthMethods: [String] = [],
                relay: SignInRelay? = nil) {
        self.runtimeID = runtimeID
        self.removed = removed
        self.lever = lever
        self.preferredAuthMethods = preferredAuthMethods
        self.relay = relay
    }
}
