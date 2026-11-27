import AgentsKitCore
import SwiftUI

/// The diff of a set-up script against the text approved last (#7155, #7157): each line marked
/// `+`, `-` or a space, added lines and removed lines tinted.
struct ScriptDiffView: View {
    let script: DaemonAPI.SetupScript

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                let lines = MachineScriptWords.diff(script.changed ? script.approvedText : nil, script.text ?? "")
                ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                    Text("\(String(line.mark)) \(line.text)")
                        .appText(.code)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .foregroundStyle(line.mark == " " ? AnyShapeStyle(.secondary) : AnyShapeStyle(.primary))
                        .background(Self.tint(line.mark))
                }
            }
            .textSelection(.enabled)
            .padding(8)
        }
        .frame(minHeight: 80, maxHeight: 280)
        .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 6))
    }

    static func tint(_ mark: Character) -> Color {
        switch mark {
        case "+": StateTint.vouched.color?.opacity(0.15) ?? .clear
        case "-": StateTint.failure.color?.opacity(0.15) ?? .clear
        default: .clear
        }
    }
}

/// The approval of the person's set-up scripts for new machines (#7157, gate 1 of #7151,
/// section 2; gate 2 boards *approve first*, *approve changed* and *approve token*): each
/// script that is new or changed since it was approved, with its diff, and one press that signs
/// an approval of each with this device's lock key. New Machine shows it before a make (Approve
/// and Make), and a warm pool that waits for root.sh (Approve). A text that looks like a token
/// warns, and the press says Approve Anyway. Behind the flag `machine-scripts`; the Mac only.
struct SetupScriptReviewSheet: View {
    @Environment(\.dismiss) private var dismiss
    let scripts: [DaemonAPI.SetupScript]
    /// The machine that has the scripts and sends them.
    let sender: String
    /// The press: *Approve and Make*, or *Approve*.
    let action: String
    /// Signs and keeps an approval of each: nil once all are kept, else why not.
    let approve: @MainActor ([DaemonAPI.SetupScript]) async -> String?
    /// After the approvals are kept: the presenter closes the sheet, and goes on once it has gone.
    let approved: @MainActor () -> Void

    @State private var working = false
    @State private var problem: String?

    private var tokenAt: (script: DaemonAPI.SetupScript, line: Int)? {
        for script in scripts {
            if let line = MachineScriptWords.tokenLine(script.text ?? "") { return (script, line) }
        }
        return nil
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(MachineScriptWords.reviewTitle(scripts)).appText(.reading)
            if let tokenAt {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Line \(tokenAt.line) of \(tokenAt.script.file) looks like a token.")
                        .foregroundStyle(StateTint.failure.style(or: .primary))
                    Text("Keep it in Secrets, and read it with agents-secret.").appText(.fine).foregroundStyle(.secondary)
                }
                .padding(10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .overlay(RoundedRectangle(cornerRadius: 8).stroke(StateTint.failure.color?.opacity(0.4) ?? .secondary))
            }
            ForEach(scripts) { script in
                VStack(alignment: .leading, spacing: 6) {
                    Text(MachineScriptWords.reviewLine(script, sender: sender)).appText(.fine).foregroundStyle(.secondary)
                    ScriptDiffView(script: script)
                }
            }
            Text(MachineScriptWords.reviewLimit(sender: sender)).appText(.fine).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let problem {
                Text(problem).foregroundStyle(StateTint.failure.style(or: .primary))
                    .fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                Spacer()
                if working { ProgressView().controlSize(.small) }
                Button("Cancel", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction)
                if tokenAt == nil {
                    Button(action) { press() }.keyboardShortcut(.defaultAction).disabled(working)
                } else {
                    // A token is not refused, but Return does not approve it.
                    Button("\(action) Anyway") { press() }.disabled(working)
                }
            }
        }
        .padding(20)
        .frame(width: 600)
    }

    private func press() {
        working = true
        problem = nil
        Task {
            let refused = await approve(scripts)
            working = false
            if let refused { problem = refused } else { approved() }
        }
    }
}
