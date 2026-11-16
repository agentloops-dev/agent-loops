import AgentsKitCore
import SwiftUI

/// A machine's labels on its page (#5284): the provider's (`os=`, `arch=`, `provider=`) as they
/// are, and a person's, each with a button to take it off and a field to add one. Workflows'
/// Runs on and the Machines page's filters use them. Drawn by the Mac window and the Remote.
struct MachineLabelsSection: View {
    let row: DaemonAPI.ControlMachine
    /// Sets the person's labels on the machine; returns nothing, the page reads the new list.
    let onSet: ([String]) -> Void
    @State private var typed = ""
    @State private var problem: String?

    var body: some View {
        Section {
            if (row.labels ?? []).isEmpty {
                Text("No labels.").foregroundStyle(.secondary)
            }
            ForEach(row.autoLabels ?? [], id: \.self) { label in
                Text(label).foregroundStyle(.secondary)
                    .accessibilityLabel("\(label), set by the provider")
            }
            ForEach(row.ownLabels, id: \.self) { label in
                HStack {
                    Text(label)
                    Spacer(minLength: 0)
                    Button {
                        onSet(row.ownLabels.filter { $0 != label })
                    } label: {
                        Image(icon: "xmark.circle").accessibilityLabel("Remove \(label)")
                    }
                    .buttonStyle(.plain)
                }
            }
            TextField("Add a label", text: $typed)
                .onSubmit(add)
                #if os(iOS)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                #endif
            if let problem {
                Text(problem).foregroundStyle(StateTint.failure.style(or: .secondary))
            }
        } header: {
            Text("Labels")
        } footer: {
            Text(MachineLabels.hint)
        }
    }

    private func add() {
        let added = MachineLabels.parse(typed)
        guard !added.isEmpty else { return }
        if let bad = added.compactMap(MachineLabels.problem).first {
            problem = bad
            return
        }
        problem = nil
        typed = ""
        let have = Set(row.ownLabels.map { $0.lowercased() })
        onSet(row.ownLabels + added.filter { !have.contains($0.lowercased()) })
    }
}
