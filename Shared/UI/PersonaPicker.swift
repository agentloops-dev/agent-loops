import AgentsKitCore
import SwiftUI

/// The persona picker of a start form (#760): segments, or a menu of the picked one's name and
/// a chevron where they do not fit (#2795). No Auto segment (#4721): the prompt's words pick
/// one live until one is picked by hand. New Session and New Session in Background on the Mac
/// and the Remote's start page draw it (#5844).
struct PersonaPicker: View {
    static let noPersona = "No persona"

    let usable: [Persona]
    let picked: String?
    let menu: Bool
    let choose: (Persona?) -> Void

    init(_ usable: [Persona], picked: String?, menu: Bool = false, choose: @escaping (Persona?) -> Void) {
        self.usable = usable
        self.picked = picked
        self.menu = menu
        self.choose = choose
    }

    var body: some View {
        let picker = Picker("Persona", selection: Binding<Persona.Segment>(
            get: { Persona.Segment(picked: picked) },
            set: { segment in
                switch segment {
                case .none: choose(nil)
                case .persona(let id): choose(usable.first { $0.id == id })
                }
            }
        )) {
            Text(Self.noPersona).tag(Persona.Segment.none)
            ForEach(usable) { persona in
                Text(persona.name).tag(Persona.Segment.persona(persona.id))
            }
        }
        let shown = usable.first { $0.id == picked }?.name ?? Self.noPersona
        if menu {
            picker
                .pickerStyle(.menu)
                .labelsHidden()
                .fixedSize()
                .accessibilityLabel("Persona: \(shown)")
        } else {
            picker
                .pickerStyle(.segmented)
                .labelsHidden()
                .fixedSize(horizontal: true, vertical: false)
                .accessibilityLabel("Persona: \(shown)")
        }
    }
}
