import AgentsKitCore
import AppKit
import SwiftUI

/// The cards under step 7 of a machine's set-up (#5361, board 2 note 4): for each runtime that no
/// key signed in, its own sign-in. A code and **Open sign-in page**; Claude's page also gives a code to
/// paste back. The step itself says **Waiting for you**.
struct MachineSignInCards: View {
    @Environment(AppModel.self) private var model
    let host: MachineRef

    var body: some View {
        ForEach(model.machineSignIn.cards[host] ?? []) { card in
            MachineSignInCard(host: host, card: card)
        }
    }
}

private struct MachineSignInCard: View {
    @Environment(AppModel.self) private var model
    let host: MachineRef
    let card: MachineSignInDriver.Card

    @State private var code = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Sign in to \(card.runtimeName)").fontWeight(.medium)
            if let note = card.note {
                Text(note).appText(.fine).foregroundStyle(.secondary)
            }
            if let step = card.step {
                if let userCode = step.userCode {
                    Text(userCode)
                        .font(.system(.title2, design: .monospaced))
                        .textSelection(.enabled)
                }
                HStack {
                    Button("Open sign-in page") { NSWorkspace.shared.open(step.url) }
                    Button("Copy Link") {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(step.url.absoluteString, forType: .string)
                    }
                }
                if step.wantsCode {
                    TextField("Code from the page", text: $code)
                        .autocorrectionDisabled()
                }
                HStack(spacing: 8) {
                    Button("Done") {
                        Task { await model.machineSignIn.finish(card.runtimeID, on: host, code: code, model: model) }
                    }
                    .disabled(card.busy || (step.wantsCode && code.trimmingCharacters(in: .whitespaces).isEmpty))
                    if card.busy { ProgressView().controlSize(.small) }
                }
            } else if card.problem != nil, MachineSignInDriver.clientSignIn.contains(card.runtimeID) {
                Button("Try Again") { Task { await model.machineSignIn.restart(card.runtimeID, on: host, model: model) } }
            }
            if let problem = card.problem {
                Text(problem).foregroundStyle(StateTint.failure.style(or: .primary))
            }
        }
        .padding(.vertical, 4)
    }
}
