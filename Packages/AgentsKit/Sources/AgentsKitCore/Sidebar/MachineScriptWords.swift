import Foundation

/// The "Your scripts" section of a machine's page in words (#7155, gate 2 of #7151): what the
/// machine ran of the person's `setup.sh`, against the file on the machine that sends it.
public enum MachineScriptWords {
    /// How `setup.sh` stands on a machine.
    public enum Standing: Equatable, Sendable {
        /// The sender has no `setup.sh`.
        case none
        /// The machine was sent none yet.
        case notSent
        /// It runs there now.
        case running
        /// It ran there, and the file is the same now.
        case same
        /// It ran there, and the file changed since.
        case changed
        /// Its last run failed.
        case failed
    }

    /// The step `user` of a machine's set-up, the one that ran `setup.sh`.
    public static func step(_ setup: DaemonAPI.MachineSetup?) -> DaemonAPI.MachineSetupStep? {
        setup?.steps.first { $0.id == "user" }
    }

    public static func standing(step: DaemonAPI.MachineSetupStep?, script: DaemonAPI.SetupScript?) -> Standing {
        guard let script, script.present else { return step == nil ? .none : .same }
        guard let step, step.state != .waiting else { return .notSent }
        switch step.state {
        case .running, .needsYou: return .running
        case .failed: return .failed
        default: return step.hash != nil && step.hash == script.hash ? .same : .changed
        }
    }

    /// The row's note after the file's name: *· changed on This Mac*.
    public static func note(_ standing: Standing, sender: String) -> String? {
        standing == .changed ? "changed on \(sender)" : nil
    }

    /// The row's line under its name.
    public static func line(_ standing: Standing, step: DaemonAPI.MachineSetupStep?, machine: String, sender: String,
                            now: Date = Date()) -> String {
        let ran = (step?.endedAt ?? step?.startedAt).map { when($0, now: now) }
        switch standing {
        case .none: return "\(sender) has no setup.sh. Make one in Settings ▸ Machines."
        case .notSent: return "Not sent to \(machine) yet."
        case .running: return step?.detail ?? "Running setup.sh"
        case .same: return ran.map { "Ran \($0). The same as on \(sender)." } ?? "The same as on \(sender)."
        case .changed: return ran.map { "\(machine) ran the one of \($0)." } ?? "\(machine) ran an older one."
        case .failed: return step?.error.map { firstLine($0) } ?? "setup.sh failed."
        }
    }

    /// The first line of a failure, for one row.
    static func firstLine(_ text: String) -> String {
        text.split(separator: "\n", omittingEmptySubsequences: true).first.map(String.init) ?? text
    }

    /// *Nov 25, 09:04*, or *09:04* today.
    static func when(_ date: Date, now: Date) -> String {
        date.formatted(date: Calendar.current.isDate(date, inSameDayAs: now) ? .omitted : .abbreviated, time: .shortened)
    }

    /// Whether Send Set-up is the row's main button: the file changed, or was never sent.
    public static func sendIsMain(_ standing: Standing) -> Bool {
        standing == .changed || standing == .notSent || standing == .failed
    }

    /// Whether Run Again is there: it ran there, and runs now no more.
    public static func canRunAgain(_ standing: Standing) -> Bool {
        standing == .same || standing == .changed || standing == .failed
    }

    /// The lines of a diff of `old` to `new`, each marked `+`, `-` or ` ` (a space), by the
    /// standard library's `difference(from:)`. With no `old`, each line of `new` is added.
    public static func diff(_ old: String?, _ new: String) -> [(mark: Character, text: String)] {
        let b = lines(new)
        guard let old else { return b.map { ("+", $0) } }
        let a = lines(old)
        var removed = Set<Int>(), inserted = Set<Int>()
        for change in b.difference(from: a) {
            switch change {
            case let .remove(offset, _, _): removed.insert(offset)
            case let .insert(offset, _, _): inserted.insert(offset)
            }
        }
        var out: [(Character, String)] = []
        var i = 0, j = 0
        while i < a.count || j < b.count {
            if i < a.count, removed.contains(i) {
                out.append(("-", a[i])); i += 1
            } else if j < b.count, inserted.contains(j) {
                out.append(("+", b[j])); j += 1
            } else {
                out.append((" ", b[j])); i += 1; j += 1
            }
        }
        return out
    }

    static func lines(_ text: String) -> [String] {
        var out = text.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        if out.last == "" { out.removeLast() }
        return out
    }

    // MARK: Approving a script (#7157, gate 2 boards *approve first*, *approve changed*, *approve token*)

    /// The approval sheet's title: *Approve setup.sh?* for a first use, *root.sh changed since
    /// you approved it* for a change; for both scripts at once, *Approve your scripts?*.
    public static func reviewTitle(_ scripts: [DaemonAPI.SetupScript]) -> String {
        guard scripts.count == 1, let script = scripts.first else { return "Approve your scripts?" }
        return script.approvedText == nil ? "Approve \(script.file)?" : "\(script.file) changed since you approved it"
    }

    /// The line over a script's diff: where it is, how much changed, and how it runs.
    public static func reviewLine(_ script: DaemonAPI.SetupScript, sender: String) -> String {
        let lines = diff(script.approvedText, script.text ?? "")
        let added = lines.filter { $0.mark == "+" }.count, removed = lines.filter { $0.mark == "-" }.count
        let change = script.approvedText == nil ? "new" : "+\(added) −\(removed)"
        let runs = script.name == "root" ? "runs as root on each new machine" : "runs as your user on new machines"
        return "\(sender) · ~/.agents/machine-setup/\(script.file) · \(change) · \(runs)"
    }

    /// The words under the diffs.
    public static func reviewLimit(sender: String) -> String {
        "Approving signs this text with \(sender)’s key, for new machines made for you. Scripts are not secret: do not put a token in one."
    }

    /// A row of New Machine's *Your scripts*: its size, and whether it is approved.
    public static func makeRow(_ script: DaemonAPI.SetupScript) -> String {
        if let problem = script.problem { return problem }
        let size = ByteCountFormatter.string(fromByteCount: script.size ?? 0, countStyle: .file)
        if !script.needsApprovalForMade { return "\(size) · approved" }
        return script.approvedText == nil ? "\(size) · new, not approved" : "\(size) · changed since approved"
    }

    /// The first line of a text that looks like a token (#7157, board *approve token*), counted
    /// from one: a GitHub, GitLab, Slack, OpenAI or Anthropic token, or an AWS key id. It warns
    /// and does not refuse.
    public static func tokenLine(_ text: String) -> Int? {
        let shapes = [#"gh[pousr]_[A-Za-z0-9]{20,}"#, #"github_pat_[A-Za-z0-9_]{20,}"#, #"glpat-[A-Za-z0-9_\-]{20,}"#,
                      #"xox[abprs]-[A-Za-z0-9\-]{10,}"#, #"sk-(ant-)?[A-Za-z0-9_\-]{20,}"#, #"AKIA[0-9A-Z]{16}"#]
        for (i, line) in lines(text).enumerated() where shapes.contains(where: { line.range(of: $0, options: .regularExpression) != nil }) {
            return i + 1
        }
        return nil
    }
}

