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
        case .none: return "\(sender) has no setup.sh. Make one in Settings ▸ Machines ▸ Machine scripts."
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


// MARK: Settings ▸ Machines ▸ Machine scripts (#7512, gate 2 boards *files two*, *files none*, *files offline*)

public extension MachineScriptWords {
    /// The page's line under its title.
    static let filesLead = "They are files on the machine that makes your machines. Your dotfiles can keep them."

    /// Where each script goes in clear (gate 1 of #7151, section 8).
    static let filesClear = "root.sh goes to the provider in clear, in the machine’s first boot, and setup.sh goes through the control plane. Scripts are not secret: do not put a token in one."

    /// The words when neither script is there.
    static let filesNone = "No scripts yet. Make root.sh for packages, or setup.sh for your tools and sign-ins."

    /// The guide's example, with gh.
    static let exampleGuide = URL(string: "https://docs.agentloops.dev/how-to/set-up-machines-with-scripts/#an-example-gh")!

    /// What a script does and when it runs.
    static func filesRuns(_ name: String) -> String {
        name == "root" ? "Runs once, as root, when a machine is made." : "Runs as your user, after the machine joins."
    }

    /// The note after a file's name: *changed, not approved*, *new, not approved*, or none.
    static func filesNote(_ script: DaemonAPI.SetupScript) -> String? {
        if script.problem != nil || !script.needsApprovalForMade { return nil }
        return script.approvedText == nil ? "new, not approved" : "changed, not approved"
    }

    /// The line under a file's name: what it does and when it runs, whether it is approved,
    /// and the secrets setup.sh reads. A file that can't be read says why (with its size and
    /// limit when it is too large), what new machines get now, and why there is no Review (#7548).
    static func filesLine(_ script: DaemonAPI.SetupScript) -> String {
        if let problem = script.problem { return "\(problem) \(filesProblemResult(script.name))" }
        guard script.present else {
            return script.name == "root" ? "None. Make one for the packages of each new machine."
                : "None. Make one for your tools and sign-ins on each new machine."
        }
        var line = filesRuns(script.name)
        if !script.needsApprovalForMade { line += " Approved for new machines." }
        let secrets = declaredSecrets(script.text ?? "")
        if !secrets.isEmpty { line += " Reads \(ListFormatter.localizedString(byJoining: secrets))." }
        return line
    }

    /// What a script that can't be read does to new machines, as `scriptsBeforeMake` and
    /// `setupWanted` in `Go/internal/agentsd` do: a root.sh stops each make, and a setup.sh is not
    /// sent, also when an older text was approved. Review comes back when the file can be read.
    static func filesProblemResult(_ name: String) -> String {
        name == "root" ? "New machines can’t be made until you fix it. Then you can review it."
            : "New machines get no setup.sh until you fix it. Then you can review it."
    }

    /// The names in the `# secrets:` lines of the comment lines at the top of a setup.sh (#7156),
    /// as `declaredSecrets` in `Go/internal/agentsd/setupsecrets.go` reads them: after a `#!`
    /// line, with blank lines between, split at white space, each once.
    static func declaredSecrets(_ text: String) -> [String] {
        var names: [String] = []
        for (i, raw) in lines(text).enumerated() {
            let line = raw.trimmingCharacters(in: .whitespaces)
            if i == 0, line.hasPrefix("#!") { continue }
            if line.isEmpty { continue }
            guard line.hasPrefix("#") else { break }
            let body = line.dropFirst().trimmingCharacters(in: .whitespaces)
            guard body.hasPrefix("secrets:") else { continue }
            for name in body.dropFirst("secrets:".count).split(whereSeparator: \.isWhitespace)
            where !names.contains(String(name)) {
                names.append(String(name))
            }
        }
        return names
    }

    /// The example New root.sh and New setup.sh write: gh, from GitHub's own package source,
    /// then signed in with a token from Secrets. It is the host's, from the pinned dotagents
    /// (`readyMade`, #7461), when it has one; else the copy below.
    static func example(_ name: String, readyMade: String? = nil) -> String {
        if let readyMade, !readyMade.isEmpty { return readyMade }
        return name == "root" ? exampleRoot : exampleSetup
    }

    static let exampleRoot = """
        #!/bin/bash
        # root.sh: runs once, as root, on each new machine's first boot, with bash -e.
        # It goes to the provider in clear: do not put a token here.
        # An example: the GitHub CLI, from GitHub's own package source.
        set -eu
        if command -v gh >/dev/null 2>&1; then
          gh --version
          exit 0
        fi
        if ! command -v apt-get >/dev/null 2>&1; then
          echo "root.sh: no apt-get here; install gh with this machine's package manager" >&2
          exit 0
        fi
        export DEBIAN_FRONTEND=noninteractive
        install -d -m 755 /etc/apt/keyrings
        curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \\
          -o /etc/apt/keyrings/githubcli-archive-keyring.gpg
        chmod 644 /etc/apt/keyrings/githubcli-archive-keyring.gpg
        echo "deb [signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \\
          > /etc/apt/sources.list.d/github-cli.list
        apt-get -o DPkg::Lock::Timeout=300 update
        apt-get -o DPkg::Lock::Timeout=300 install -y gh
        gh --version

        """

    static let exampleSetup = """
        #!/bin/bash
        # secrets: GH_TOKEN
        # setup.sh: runs as your user on each new machine, after it joins, with bash -e.
        # It reads only the secrets named above, with agents-secret. Keep it safe to run again.
        # An example: sign gh in with the token GH_TOKEN from your Secrets.
        set -eu
        if ! command -v gh >/dev/null 2>&1; then
          echo "setup.sh: gh is not installed; root.sh installs it" >&2
          exit 1
        fi
        # The token goes from agents-secret to gh on stdin: it is never in a line or a log.
        agents-secret GH_TOKEN | gh auth login --with-token
        gh auth setup-git
        gh auth status

        """
}

// MARK: Send Set-up's tries (#7547)

public extension MachineScriptWords {
    /// How long a Send Set-up that waits is tried again for, from the first try.
    static let sendTriesFor: Duration = .seconds(2 * 60 * 60)

    /// The pause before try `n` again (1 is the first one after the first try) of a Send Set-up
    /// that waits: 15 s, twice as long each time up to 5 min, for `sendTriesFor` in all. Nil
    /// after that: the tries stop.
    static func sendPause(before n: Int) -> Duration? {
        guard n >= 1 else { return nil }
        let first = 15, most = 5 * 60
        var waited = 0, pause = first
        for _ in 1..<n {
            waited += pause
            pause = min(2 * pause, most)
        }
        return waited + pause > Int(sendTriesFor.components.seconds) ? nil : .seconds(pause)
    }

    /// The row's note while a send waits: *· waiting for dev-3 to be online*.
    static func sendWaitNote(_ waitsFor: String) -> String { "waiting for \(waitsFor) to be online" }

    /// The row's note once the tries stopped with nothing sent.
    static let sendEndedNote = "not sent"

    /// The row's line once the tries stopped because `waitsFor` was not online.
    static func sendGaveUpLine(_ waitsFor: String) -> String {
        "\(waitsFor) was not online for 2 hours. Send Set-up again when it is."
    }
}
