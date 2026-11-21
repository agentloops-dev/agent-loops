import Foundation

// MARK: The wire (#5259)

/// Where a variable comes from (#5259), the lowest first: the login environment, the folder's `.agents/env`, then
/// `~/.agents/env`. The later wins. Agents' own variables come after all of them and are not listed.
public enum EnvLayer: String, Codable, Sendable, Hashable {
    case login, repo, yours
}

/// What a session gets of a variable (#5259).
public enum EnvVariableState: String, Codable, Sendable, Hashable {
    /// Given.
    case given
    /// A name Agents or the system own, or that a repo's file may not set: not given.
    case refused
    /// Given, but its value looks like a secret: the page puts it in Needs you.
    case secretLooking
    /// The organisation's environment rule does not allow it: not given.
    case policyBlocked
}

/// One variable of a file (#5259).
public struct EnvVariable: Codable, Sendable, Hashable {
    public var name: String
    public var value: String
    /// The `#` comment line just above it, without the `#`.
    public var description: String?
    /// Its line in the file, from 1.
    public var line: Int
    public var state: EnvVariableState
    /// Why it is refused or blocked, or why it looks like a secret.
    public var reason: String?

    public init(name: String, value: String, description: String? = nil, line: Int, state: EnvVariableState = .given,
                reason: String? = nil) {
        self.name = name
        self.value = value
        self.description = description
        self.line = line
        self.state = state
        self.reason = reason
    }
}

/// One file of variables (#5259): `~/.agents/env` (yours) or a folder's `.agents/env` (repo), as a machine reads it.
public struct EnvFile: Codable, Sendable, Hashable {
    public var layer: EnvLayer
    public var path: String
    /// For repo: the folder, as a path.
    public var folder: String?
    public var exists: Bool
    public var variables: [EnvVariable]
    /// Why the file is not read: it is too big or has too many variables.
    public var problem: String?
    /// For repo: the file has changes that git has not committed.
    public var uncommitted: Bool?
    /// For yours: the file differs from what the person's settings last wrote to it.
    public var changedByHand: Bool?

    public init(layer: EnvLayer, path: String, folder: String? = nil, exists: Bool = true, variables: [EnvVariable] = [],
                problem: String? = nil, uncommitted: Bool? = nil, changedByHand: Bool? = nil) {
        self.layer = layer
        self.path = path
        self.folder = folder
        self.exists = exists
        self.variables = variables
        self.problem = problem
        self.uncommitted = uncommitted
        self.changedByHand = changedByHand
    }
}

/// `env/list`'s answer: `~/.agents/env` first, then each known folder's `.agents/env`.
public struct EnvList: Codable, Sendable, Hashable {
    public var files: [EnvFile]
}

/// `env/list`: with `folder`, only `~/.agents/env` and that folder's file.
public struct EnvListRequest: Codable, Sendable, Hashable {
    public var folder: String?
    public init(folder: String? = nil) { self.folder = folder }
}

/// `env/write`: add, change (`value`) or remove (no `value`) one variable of a folder's `.agents/env`. `description`
/// nil keeps the one there; empty removes it.
public struct EnvWriteRequest: Codable, Sendable, Hashable {
    public var folder: String
    public var name: String
    public var value: String?
    public var description: String?
    public init(folder: String, name: String, value: String?, description: String?) {
        self.folder = folder
        self.name = name
        self.value = value
        self.description = description
    }
}

/// `env/resolve`: a folder, or a running session's folder.
public struct EnvResolveRequest: Codable, Sendable, Hashable {
    public var folder: String?
    public var agentID: UUID?
    public init(folder: String? = nil, agentID: UUID? = nil) {
        self.folder = folder
        self.agentID = agentID
    }
}

/// One name as a session sees it.
public struct EnvResolvedVariable: Codable, Sendable, Hashable, Identifiable {
    public var name: String
    /// Absent when no layer gives it.
    public var value: String?
    /// The layer that wins; absent when none gives it.
    public var layer: EnvLayer?
    /// The lower layers that also set it.
    public var over: [EnvLayer]?
    public var state: EnvVariableState
    public var reason: String?

    public var id: String { name }

    public init(name: String, value: String? = nil, layer: EnvLayer? = nil, over: [EnvLayer]? = nil,
                state: EnvVariableState = .given, reason: String? = nil) {
        self.name = name
        self.value = value
        self.layer = layer
        self.over = over
        self.state = state
        self.reason = reason
    }
}

/// `env/resolve`'s answer, "As a session sees it".
public struct EnvResolved: Codable, Sendable, Hashable {
    public var folder: String
    public var variables: [EnvResolvedVariable]
}

/// `env/changed`: `folder`'s `.agents/env` changed, or `~/.agents/env` (`yours`).
public struct EnvChanged: Codable, Sendable, Hashable {
    public var folder: String?
    public var yours: Bool?
    public init(folder: String? = nil, yours: Bool? = nil) {
        self.folder = folder
        self.yours = yours
    }
}

/// The environment part of user settings (#5259): `~/.agents/env`, whole, as text.
public struct UserEnvironment: Codable, Sendable, Hashable {
    public var text: String
    public init(text: String) { self.text = text }
}

// MARK: The file's text

/// `~/.agents/env` as text (#5259): the same rules as `Go/internal/envfile`, so a client can change one variable of
/// the person's file and send it whole (`user/setSettings`, part `environment`), keeping every other line. The web
/// page's are in `Web/src/model/environmentPage.ts`.
public enum EnvFileText {
    /// A name a shell takes.
    public static func isName(_ name: String) -> Bool {
        name.range(of: "^[A-Za-z_][A-Za-z0-9_]*$", options: .regularExpression) != nil
    }

    /// A name that says it holds a secret, as the daemon's `SecretLookingName`.
    public static func nameLooksSecret(_ name: String) -> Bool {
        name.range(of: "token|secret|key|password|passwd|auth|bearer|cookie|credential",
                   options: [.regularExpression, .caseInsensitive]) != nil
    }

    /// Why a variable looks like a secret, or nil: a name that says so, with a value. The daemon also refuses a value
    /// with a token's shape, and says why.
    public static func secretWords(name: String, value: String) -> String? {
        guard !value.isEmpty, nameLooksSecret(name) else { return nil }
        return "\(name) looks like the name of a secret."
    }

    /// NAME=value from one trimmed line, with `export ` and one pair of quotes taken off.
    static func split(_ line: String) -> (name: String, value: String)? {
        var line = line.trimmingCharacters(in: .whitespaces)
        if line.hasPrefix("export ") { line = String(line.dropFirst(7)).trimmingCharacters(in: .whitespaces) }
        guard let eq = line.firstIndex(of: "=") else { return nil }
        let name = line[..<eq].trimmingCharacters(in: .whitespaces)
        guard isName(name) else { return nil }
        var value = line[line.index(after: eq)...].trimmingCharacters(in: .whitespaces)
        if value.count >= 2, let first = value.first, first == "\"" || first == "'", value.last == first {
            value = String(value.dropFirst().dropLast())
        }
        return (name, value)
    }

    /// The line a file keeps: quoted when the value has a space, a `#` or a quote, so it reads back the same.
    static func line(_ name: String, _ value: String) -> String {
        if value != value.trimmingCharacters(in: .whitespaces) || value.contains(where: { "#\"' ".contains($0) }) {
            return value.contains("'") ? "\(name)=\"\(value)\"" : "\(name)='\(value)'"
        }
        return "\(name)=\(value)"
    }

    static func oneLine(_ text: String) -> String {
        text.replacingOccurrences(of: "\r", with: " ").replacingOccurrences(of: "\n", with: " ")
            .trimmingCharacters(in: .whitespaces)
    }

    /// The text of a file with these variables, each with its description above it, a blank line between: for a file
    /// the settings have no text of (one from before the part came, or one changed by hand).
    public static func text(of variables: [EnvVariable]) -> String {
        variables.sorted { $0.line < $1.line }.map { variable in
            let about = oneLine(variable.description ?? "")
            return (about.isEmpty ? "" : "# \(about)\n") + line(variable.name, variable.value)
        }.joined(separator: "\n\n").appending(variables.isEmpty ? "" : "\n")
    }

    /// Sets (`value` non-nil) or removes (nil) one variable, keeping every other line, as `envfile.Edit`. A new one goes
    /// at the end, after a blank line, with its description above it. `description` nil keeps the one there; "" removes it.
    public static func edit(_ text: String, name: String, value: String?, description: String?) -> String {
        var body = text.replacingOccurrences(of: "\r\n", with: "\n")
        if body.hasSuffix("\n") { body.removeLast() }
        let lines = body.isEmpty ? [] : body.components(separatedBy: "\n")
        let at = lines.indices.filter { split(lines[$0].trimmingCharacters(in: .whitespaces))?.name == name }
        func commentAbove(_ i: Int) -> Bool { i > 0 && lines[i - 1].trimmingCharacters(in: .whitespaces).hasPrefix("#") }
        func blank(_ i: Int) -> Bool { i < 0 || i >= lines.count || lines[i].trimmingCharacters(in: .whitespaces).isEmpty }
        let replace = at.last
        var drop = Set<Int>()
        for i in at where i != replace || value == nil {
            drop.insert(i)
            var first = i
            if commentAbove(i) {
                drop.insert(i - 1)
                first = i - 1
            }
            if blank(first - 1) && i + 1 < lines.count && blank(i + 1) { drop.insert(i + 1) }
        }
        var out: [String] = []
        for (i, raw) in lines.enumerated() where !drop.contains(i) {
            if i == replace, let value {
                if let description {
                    if commentAbove(i) { out.removeLast() }
                    let about = oneLine(description)
                    if !about.isEmpty { out.append("# \(about)") }
                }
                out.append(line(name, value))
            } else {
                out.append(raw)
            }
        }
        if replace == nil, let value {
            if let last = out.last, !last.trimmingCharacters(in: .whitespaces).isEmpty { out.append("") }
            if let description {
                let about = oneLine(description)
                if !about.isEmpty { out.append("# \(about)") }
            }
            out.append(line(name, value))
        }
        while let last = out.last, last.trimmingCharacters(in: .whitespaces).isEmpty { out.removeLast() }
        return out.isEmpty ? "" : out.joined(separator: "\n") + "\n"
    }
}

// MARK: The page

/// Status ▸ Environment (#5260, `specs/5254-secrets-accounts-environment/wireframes.md` section 4): the rules and words
/// of the page that finds, adds and edits the plain variables sessions get when their agent starts. Yours
/// (`~/.agents/env`, on every machine) and each repo's (`.agents/env`), grouped, with Needs you on top. The window and the
/// Remote draw it in `EnvironmentPageView`; the web page's rules are the same, in `Web/src/model/environmentPage.ts`.
public enum EnvironmentPage {
    public static let title = "Environment"
    /// The header line: where the values live, so nobody takes the page for a safe place.
    public static let whereLine = "Plain text in ~/.agents/env and each repo’s .agents/env. Secrets go in Secrets."
    public static let searchPrompt = "Search names, values, descriptions…"
    public static let clearSearch = "Clear Search"
    public static let add = "Add Variable"
    public static let asSession = "As a Session Sees It…"
    public static let selectOne = "Select a variable to see it."
    public static let none = "No variables yet. Add one, and sessions get it the next time their agent starts."
    public static let nextStart = "Sessions get it the next time their agent starts."
    public static let edit = "Edit"
    public static let remove = "Remove…"
    public static let copy = "Copy"
    public static let saveAsSecret = "Save as a Secret Instead…"
    public static let looksSecret = "This looks like a secret. Values here are not sealed."
    public static let repoPublic = "Committed with the repo: anyone who can read the repo can read it."
    public static let notCommitted = "Not committed"
    public static let changedByHand = "~/.agents/env was changed by hand on this machine, so this machine uses the edited file."
    public static let keepForAll = "Keep for All Machines"
    public static let discard = "Discard"
    public static let yoursWhere = "~/.agents/env, every machine"
    public static let machineLabel = "Machine"
    /// Settings ▸ You's line, beside the Secrets one.
    public static let open = "Open Environment"
    public static func noMatch(_ search: String) -> String { "Nothing matches “\(search)”." }
    /// No machine of the person's has a host that knows `env/1`.
    public static let noMachine = "No machine of yours can show its variables yet. Update Agent Loops on it."
    public static func cannotReach(_ machine: String) -> String { "\(machine.isEmpty ? "The machine" : machine) can’t be reached." }

    public enum Chip: String, CaseIterable, Sendable, Hashable {
        case all, yours, repos, needsYou
        public var title: String {
            switch self {
            case .all: "All"
            case .yours: "Yours"
            case .repos: "Repos"
            case .needsYou: "Needs you"
            }
        }
    }

    /// One variable of one file.
    public struct Item: Identifiable, Sendable, Hashable {
        /// `yours/<name>`, or `repo/<folder>/<name>`.
        public var id: String
        public var name: String
        public var value: String
        public var description: String?
        public var layer: EnvLayer
        /// The file's path, as the machine has it.
        public var path: String
        /// For a repo: its folder, and that folder's name.
        public var folder: String?
        public var repo: String?
        public var state: EnvVariableState
        /// The file has changes git has not committed.
        public var uncommitted: Bool
        /// Why it is in Needs you, or nil.
        public var needs: String?
    }

    /// A group of the list: Needs you, Yours, then one for each repo.
    public struct Group: Identifiable, Sendable, Hashable {
        public var id: String
        public var title: String
        /// The file, as the header says it.
        public var file: String
        public var uncommitted: Bool
        /// Why the file is not read.
        public var problem: String?
        public var items: [Item]
    }

    public static func id(layer: EnvLayer, folder: String?, name: String) -> String {
        layer == .yours ? "yours/\(name)" : "repo/\(folder ?? "")/\(name)"
    }

    /// The folder's name, as a repo group says it.
    public static func repoName(_ folder: String) -> String {
        let trimmed = folder.hasSuffix("/") ? String(folder.dropLast()) : folder
        return trimmed.split(separator: "/").last.map(String.init) ?? trimmed
    }

    /// Why a variable is in Needs you, or nil when it is given and looks like nothing.
    public static func needs(_ variable: EnvVariable, layer: EnvLayer) -> String? {
        switch variable.state {
        case .given: nil
        case .secretLooking:
            layer == .repo ? "This is in the repo, so it is public. Revoke it, then add it to Secrets."
                : "This looks like a secret. Move it to Secrets."
        case .refused: variable.reason ?? (layer == .repo ? "A repo cannot set this. Not given." : "Agents set this. Not given.")
        case .policyBlocked: variable.reason ?? "Blocked by your organisation’s policy."
        }
    }

    /// Every variable of the files, Yours first, in the order of the files.
    public static func items(_ files: [EnvFile]) -> [Item] {
        files.flatMap { file in
            file.variables.map { variable in
                Item(id: id(layer: file.layer, folder: file.folder, name: variable.name), name: variable.name,
                     value: variable.value, description: variable.description, layer: file.layer, path: file.path,
                     folder: file.folder, repo: file.folder.map(repoName), state: variable.state,
                     uncommitted: file.uncommitted == true, needs: needs(variable, layer: file.layer))
            }
        }
    }

    /// The search matches the name, the value and the description, in any case.
    public static func matches(_ item: Item, search: String) -> Bool {
        let words = search.trimmingCharacters(in: .whitespaces)
        guard !words.isEmpty else { return true }
        return [item.name, item.value, item.description ?? ""].contains { $0.localizedCaseInsensitiveContains(words) }
    }

    public static func inChip(_ item: Item, _ chip: Chip) -> Bool {
        switch chip {
        case .all: true
        case .yours: item.layer == .yours
        case .repos: item.layer == .repo
        case .needsYou: item.needs != nil
        }
    }

    /// Each chip's count, after the search.
    public static func counts(_ items: [Item], search: String) -> [Chip: Int] {
        let found = items.filter { matches($0, search: search) }
        return Dictionary(uniqueKeysWithValues: Chip.allCases.map { chip in (chip, found.filter { inChip($0, chip) }.count) })
    }

    /// The list: Needs you on top, then Yours, then a group per repo, each by name. A file with a problem keeps its group,
    /// to say why, when no chip or search hides it.
    public static func groups(_ files: [EnvFile], search: String, chip: Chip) -> [Group] {
        let all = items(files).filter { matches($0, search: search) && inChip($0, chip) }
        var out: [Group] = []
        let needing = all.filter { $0.needs != nil }
        if !needing.isEmpty {
            out.append(Group(id: "needs", title: "Needs you", file: "", uncommitted: false, problem: nil, items: needing))
        }
        guard chip != .needsYou else { return out }
        let rest = all.filter { $0.needs == nil }
        let quiet = search.trimmingCharacters(in: .whitespaces).isEmpty
        for file in files {
            if chip == .yours && file.layer != .yours { continue }
            if chip == .repos && file.layer != .repo { continue }
            let mine = rest.filter { $0.layer == file.layer && $0.folder == file.folder }.sorted { $0.name < $1.name }
            guard !mine.isEmpty || (quiet && file.problem != nil) else { continue }
            if file.layer == .yours {
                out.append(Group(id: "yours", title: "Yours", file: yoursWhere, uncommitted: false, problem: file.problem,
                                 items: mine))
            } else {
                let folder = file.folder ?? file.path
                out.append(Group(id: "repo/\(folder)", title: "Repo · \(repoName(folder))", file: ".agents/env",
                                 uncommitted: file.uncommitted == true, problem: file.problem, items: mine))
            }
        }
        return out
    }

    /// The layer, as the detail says it: "Yours (~/.agents/env)" or "Repo · Agents (.agents/env)".
    public static func layerLine(_ item: Item) -> String {
        item.layer == .yours ? "Yours (~/.agents/env, every machine)" : "Repo · \(item.repo ?? "") (.agents/env)"
    }

    /// The layer in short, as a Needs you row says it: "Yours" or "Repo · Agents".
    public static func layerShort(_ item: Item) -> String {
        item.layer == .yours ? "Yours" : "Repo · \(item.repo ?? "")"
    }

    /// Also set (wireframes §4a): the same name in the other layer, and which one sessions get. Yours wins over a repo's.
    public static func alsoSet(_ item: Item, in all: [Item]) -> [String] {
        let others = all.filter { $0.name == item.name && $0.id != item.id }
        guard !others.isEmpty else { return [] }
        var lines = others.map { other in
            other.layer == .yours ? "Yours: \(other.value)" : "Repo · \(other.repo ?? ""): \(other.value)"
        }
        if item.layer == .repo, let yours = others.first(where: { $0.layer == .yours }), gives(yours.state) {
            lines.append("Yours wins: sessions get \(yours.value)")
        } else if item.layer == .yours {
            lines.append("Yours wins over a repo’s")
        }
        return lines
    }

    /// A session gets a variable in this state.
    public static func gives(_ state: EnvVariableState) -> Bool { state == .given || state == .secretLooking }

    /// The where-to sentence for a write: yours goes to every machine; a repo's is left not committed.
    public static func writeNote(_ layer: EnvLayer) -> String {
        layer == .yours ? "Written to ~/.agents/env on every machine of yours." : "Written to .agents/env in the folder, not committed: commit it as any other change."
    }

    /// The add or edit sheet's refusal before it is sent, or nil: a name a shell does not take, or one that says it is a secret.
    public static func refusal(name: String, value: String) -> String? {
        let name = name.trimmingCharacters(in: .whitespaces)
        if name.isEmpty { return nil }
        if !EnvFileText.isName(name) { return "A name is letters, digits and _, and does not start with a digit." }
        if value.utf8.count > 4096 { return "The value is over 4 KB." }
        if EnvFileText.secretWords(name: name, value: value) != nil { return looksSecret }
        return nil
    }

    /// True when a refusal from the daemon or the control plane says the value looks like a secret: the sheet then offers
    /// Save as a Secret Instead.
    public static func refusedAsSecret(_ problem: String?) -> Bool {
        guard let problem else { return false }
        return problem == looksSecret || problem.contains("looks like a token") || problem.contains("looks like the name of a secret")
            || problem.contains("looks like a secret")
    }

    // MARK: As a session sees it

    /// The layer that wins, as the row says it.
    public static func resolvedSource(_ variable: EnvResolvedVariable, repo: String) -> String {
        guard variable.value != nil, let layer = variable.layer else {
            return variable.reason ?? (variable.state == .policyBlocked ? "Blocked by policy" : "Not given")
        }
        let name = switch layer {
        case .yours: "Yours"
        case .repo: "Repo \(repo)"
        case .login: "Login"
        }
        if let over = variable.over, !over.isEmpty {
            return "\(name) (wins over \(over.map { $0 == .repo ? "the repo" : $0 == .yours ? "yours" : "the login" }.joined(separator: ", ")))"
        }
        return name
    }

    public static func asSessionTitle(_ folder: String, machine: String?) -> String {
        "As a session sees it: \(repoName(folder))" + (machine.map { " on \($0)" } ?? "")
    }
}
