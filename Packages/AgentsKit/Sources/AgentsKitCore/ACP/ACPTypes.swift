import Foundation

/// The wire shapes we use, and the names of everything either side can call.
///
/// Deliberately thin. Everything a runtime sends that is not listed here stays a
/// `JSONValue` and is either kept whole or ignored.
///
/// `_meta` is read in a few named places and nowhere else: `authMethods[]._meta.terminal-auth`
/// (the command that signs in), `PromptResponse._meta.quota` (a runtime's own usage figure),
/// `initialize`'s `_meta.steering.supported` (the marker for `_session/steering`) and
/// `agentCapabilities._meta.authStatus` (the marker for `_auth/status_update`). It is
/// written on `session/new`, `load`, `resume` and `fork` for tool scoping and plugins,
/// by runtime through the catalogs (`ToolPolicyCatalog`, `DotAgents`), and in our own
/// capabilities for one opt-in, JetBrains "AIR" (057): a capability two runtimes share,
/// whose results arrive as update kinds of their own, never by asking which runtime this
/// is. What is not read: vendor `_meta` on updates, and the rest of the handshake's —
/// Grok's `x.ai/hooks`, Claude's `goal`. Reading those is how one code path becomes three.
public enum ACP {
    // MARK: What we tell the agent we can do

    /// Advertising a capability is a promise, not a hint. Grok stops doing its own file
    /// reads and writes the moment `fs` is true and has no fallback if we then refuse,
    /// which is why each flag is turned on in the same change that makes it true.
    public struct ClientCapabilities: Hashable, Sendable {
        public var readTextFile: Bool
        public var writeTextFile: Bool
        public var terminal: Bool
        public var booleanConfigOptions: Bool
        public var compaction: Bool
        public var plan: Bool
        public var terminalAuth: Bool
        public var elicitationForm: Bool
        public var elicitationURL: Bool
        public var notices: Bool
        /// JetBrains "AIR" `asyncTasks` (057): the runtime sends `async_task_*` updates for
        /// what it runs in the background, and takes `_session/async_task/stop`. It stops
        /// saying so in the Bash result's prose the moment this is on.
        public var backgroundTasks: Bool
        /// JetBrains "AIR" `nativeSubagentSessions` (057): a subagent is announced with
        /// `subagent_*`, and everything it says and does arrives under its own session id.
        /// The Agent tool card stops coming, so this is only on where those are routed.
        public var subagentSessions: Bool
        /// JetBrains' AIR session-failure extension (052, R1). Asked for only where every
        /// kind of typed failure is read: once asked, Claude and Codex end a refused turn
        /// with `end_turn` and the failure under `_meta`, and a client that does not read
        /// it would call the turn done.
        public var sessionFailures: Bool
        /// `_meta["terminal-auth"]`, the older spelling of `auth.terminal`. Only for a runtime
        /// that needs it to name its sign-in command (049: OpenCode, research R6); never in
        /// `.app`, because Claude's adapter then adds a terminal command to every method and
        /// the app would stop signing Claude in itself (measured 2026-09-29).
        public var terminalAuthMeta: Bool = false

        public init(readTextFile: Bool = false,
                    writeTextFile: Bool = false,
                    terminal: Bool = false,
                    booleanConfigOptions: Bool = false,
                    compaction: Bool = false,
                    plan: Bool = false,
                    terminalAuth: Bool = false,
                    elicitationForm: Bool = false,
                    elicitationURL: Bool = false,
                    notices: Bool = false,
                    backgroundTasks: Bool = false,
                    subagentSessions: Bool = false,
                    sessionFailures: Bool = false) {
            self.readTextFile = readTextFile
            self.writeTextFile = writeTextFile
            self.terminal = terminal
            self.booleanConfigOptions = booleanConfigOptions
            self.compaction = compaction
            self.plan = plan
            self.terminalAuth = terminalAuth
            self.elicitationForm = elicitationForm
            self.elicitationURL = elicitationURL
            self.notices = notices
            self.backgroundTasks = backgroundTasks
            self.subagentSessions = subagentSessions
            self.sessionFailures = sessionFailures
        }

        /// What 001 sent. Kept as a named thing so the change that turns a flag on is
        /// visible in one place rather than spread through the handshake.
        public static let none = ClientCapabilities()

        /// What this app actually serves today.
        ///
        /// One place, so that turning a capability on is one line and a review can see
        /// it. A flag here is a promise: a runtime that takes us up on it has no
        /// fallback, so nothing is turned on before the thing behind it works.
        /// Every flag here is a promise that the thing behind it works. Grok stops
        /// doing its own file reads and writes the moment `fs` is true and has no
        /// fallback if we then refuse, so each one was turned on in the same change
        /// that made it true.
        public static let app = ClientCapabilities(
            readTextFile: true,
            writeTextFile: true,
            terminal: true,
            booleanConfigOptions: true,
            compaction: true,
            plan: true,
            terminalAuth: true,
            elicitationForm: true,
            elicitationURL: true,
            notices: true,
            backgroundTasks: true,
            subagentSessions: true,
            sessionFailures: true)

        public var wire: JSONValue {
            var caps: [String: JSONValue] = [
                "fs": ["readTextFile": .bool(readTextFile), "writeTextFile": .bool(writeTextFile)],
                "terminal": .bool(terminal),
            ]
            var session: [String: JSONValue] = [:]
            if booleanConfigOptions { session["configOptions"] = ["boolean": .object([:])] }
            if compaction { session["compaction"] = .object([:]) }
            if notices { session["notices"] = .object([:]) }
            if !session.isEmpty { caps["session"] = .object(session) }
            if plan { caps["plan"] = .object([:]) }
            if terminalAuth { caps["auth"] = ["terminal": .bool(true)] }
            var elicitation: [String: JSONValue] = [:]
            if elicitationForm { elicitation["form"] = .object([:]) }
            if elicitationURL { elicitation["url"] = .object([:]) }
            if !elicitation.isEmpty { caps["elicitation"] = .object(elicitation) }
            // All of JetBrains' AIR extensions asked for go in one list: 057's background
            // tasks and subagents, and 052's typed session failures.
            var air: [JSONValue] = []
            if backgroundTasks { air.append("asyncTasks") }
            if subagentSessions { air.append("nativeSubagentSessions") }
            if sessionFailures { air.append("sessionFailure") }
            var meta: [String: JSONValue] = [:]
            if !air.isEmpty {
                // Version 1 is the only one either adapter reads, and each checks for
                // `>= 1`, so this is the floor rather than a guess.
                meta["jetbrains"] = ["air": ["version": 1, "capabilities": .array(air)]]
            }
            if terminalAuthMeta { meta["terminal-auth"] = .bool(true) }
            if !meta.isEmpty { caps["_meta"] = .object(meta) }
            return .object(caps)
        }
    }

    // MARK: initialize

    /// The protocol's own rule: text and resource links are baseline, everything else
    /// is opted in to.
    public struct PromptCapabilities: Codable, Hashable, Sendable {
        public var image: Bool?
        public var audio: Bool?
        public var embeddedContext: Bool?

        public init(image: Bool? = nil, audio: Bool? = nil, embeddedContext: Bool? = nil) {
            self.image = image
            self.audio = audio
            self.embeddedContext = embeddedContext
        }

        /// Everything: what a client assumes of Auto before it knows the pick (#3162), as the
        /// host then picks a runtime that takes what is attached, or says why none can.
        public static let anything = PromptCapabilities(image: true, audio: true, embeddedContext: true)

        public func allows(_ requirement: ContentBlock.Requirement?) -> Bool {
            switch requirement {
            case nil: return true
            case .image: return image ?? false
            case .audio: return audio ?? false
            case .embeddedContext: return embeddedContext ?? false
            }
        }
    }

    public struct AuthMethod: Decodable, Hashable, Sendable {
        public var id: String
        public var name: String?
        public var description: String?
        public var _meta: JSONValue?

        /// Copilot puts the command that fixes it in `_meta.terminal-auth`, which is
        /// worth showing verbatim rather than inventing advice of our own.
        public var terminalCommand: String? {
            guard let auth = _meta?["terminal-auth"] else { return nil }
            let command = auth["command"]?.stringValue
            let args = auth["args"]?.arrayValue?.compactMap(\.stringValue) ?? []
            guard let command else { return nil }
            return ([command] + args).map(Self.shellWord).joined(separator: " ")
        }

        /// A word as a shell reads it back: as it is when it is plain, single-quoted when it
        /// is not, so a path under "Application Support" pastes into Terminal whole.
        static func shellWord(_ word: String) -> String {
            let plain = CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_./=:@%+,")
            guard !word.isEmpty, word.unicodeScalars.contains(where: { !plain.contains($0) }) else { return word.isEmpty ? "''" : word }
            return "'" + word.replacingOccurrences(of: "'", with: #"'\''"#) + "'"
        }

        /// What the runtime says about this method, minus any sentence telling the user
        /// to run something.
        ///
        /// A runtime names itself by its own idea of its name, which is not a fact about
        /// this Mac. Cursor's `cursor_login` describes itself as "Run 'agent login' first
        /// if not logged in", and `agent` here is Grok, so a user who follows that advice
        /// signs into the wrong runtime. A command the app puts in front of someone comes
        /// from the catalog entry or from `terminalCommand`, where we know what will
        /// actually be started. Never from prose.
        public var guidance: String? {
            guard let description else { return nil }
            let kept = description
                .split(separator: ".", omittingEmptySubsequences: true)
                .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
                .filter { !$0.isEmpty && !Self.tellsYouToRunSomething($0) }
            guard !kept.isEmpty else { return nil }
            return kept.joined(separator: ". ") + "."
        }

        /// A sentence both saying "run" and carrying something shaped like a command.
        /// "This runs in your browser" keeps its place; "Run `foo login` first" does not.
        static func tellsYouToRunSomething(_ sentence: String) -> Bool {
            let lower = sentence.lowercased()
            guard lower.range(of: #"\brun\b"#, options: .regularExpression) != nil else { return false }
            let quoted = #"['"`][^'"`]+['"`]"#
            let hyphenated = #"\b[a-z0-9]+-[a-z0-9]+\b"#
            return lower.range(of: quoted, options: .regularExpression) != nil
                || lower.range(of: hyphenated, options: .regularExpression) != nil
        }
    }

    // MARK: session/new

    public struct ProviderInfo: Decodable, Hashable, Sendable {
        public var id: String
        public var name: String?
        public var `protocol`: String?
        public var configured: Bool?
        /// Mandatory: the runtime says it cannot be turned off, so `providers/disable`
        /// is never offered for it.
        public var required: Bool?

        private enum CodingKeys: String, CodingKey {
            case id, providerId, name, `protocol`, configured, required
        }

        /// `id` is the shape this was first written against; the schema now says
        /// `providerId`. Either is read, so a runtime on either side still lists.
        public init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decodeIfPresent(String.self, forKey: .id)
                ?? c.decode(String.self, forKey: .providerId)
            name = try? c.decodeIfPresent(String.self, forKey: .name)
            `protocol` = try? c.decodeIfPresent(String.self, forKey: .protocol)
            configured = try? c.decodeIfPresent(Bool.self, forKey: .configured)
            required = try? c.decodeIfPresent(Bool.self, forKey: .required)
        }

        public var canBeDisabled: Bool { required != true }
    }

    static func timestamp(from string: String) -> Date? {
        let withFraction = ISO8601DateFormatter()
        withFraction.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = withFraction.date(from: string) { return date }
        return ISO8601DateFormatter().date(from: string)
    }
}
