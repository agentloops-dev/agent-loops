import AgentsKitCore
import SwiftUI

/// The kind of session to start (#760), the same control as the folder's name, a size
/// smaller, on its own line under it (#913). What the picked one is for, and any persona file
/// that cannot be read, are under it. Not drawn where there are none. New Session draws it,
/// and New Session in Background too (#5844), so the two do not drift.
struct PersonaChoice: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let usable = model.draftPersonas.filter { $0.problem == nil }
        let broken = model.draftPersonas.filter { $0.problem != nil }
        let picked = usable.first { $0.id == model.draftPersona }
        VStack(spacing: 4) {
            if !model.draftPersonas.isEmpty {
                if !usable.isEmpty {
                    // Centred under the folder when it fits; a menu of them when it does not (#2795).
                    ViewThatFits(in: .horizontal) {
                        PersonaPicker(usable, picked: model.draftPersona) { model.choosePersona($0) }
                        PersonaPicker(usable, picked: model.draftPersona, menu: true) { model.choosePersona($0) }
                    }
                    .disabled(model.isStarting)
                    .help("Start a kind of session: a persona sets what it names")
                    // What the live pick picked and the word, so the person sees what to change (#4559).
                    if model.draftPersonaAuto {
                        Text(Persona.autoLine(model.draftAutoPick))
                            .appText(.fine)
                            .foregroundStyle(.secondary)
                    }
                }
                if let description = picked?.description {
                    Text(description)
                        .appText(.fine)
                        .foregroundStyle(.secondary)
                }
                ForEach(broken) { persona in
                    Text("\(persona.id).md: \(persona.problem ?? "")")
                        .appText(.fine)
                        .foregroundStyle(.secondary)
                }
            }
        }
    }
}
