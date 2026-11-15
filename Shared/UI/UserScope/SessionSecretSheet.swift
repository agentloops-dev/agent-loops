import AgentsKitCore
import SwiftUI

/// Where Settings ▸ You ▸ Secrets ▸ For One Session reads and writes (#3548): the window's model
/// and the Remote's, each through the Mac's host it reaches (#4032).
@MainActor
protocol SessionSecretsSource: AnyObject {
    /// Nil from a host too old to know the method.
    func sessionSecrets() async -> SessionSecrets?
    func setSessionSecret(_ request: SessionSecretSetRequest) async -> Result<SessionSecrets, Error>
    func setSessionSecretAsksBeforeUse(_ secret: SessionSecret, _ on: Bool) async -> Result<SessionSecrets?, Error>
    func removeSessionSecret(_ secret: SessionSecret) async -> SessionSecrets?
    /// The sessions on that host that can take a secret for one session: not archived, latest first.
    var sessionsForSecrets: [Agent] { get }
    /// Whether that host keeps Ask before use (#4014): it offers the toggle (#4016).
    var sessionHostAsksBeforeUse: Bool { get }
}

/// Add for a Session…: which session, a name and a value. The value is never shown again.
struct SessionSecretSheet: View {
    let sessions: [Agent]
    /// The host keeps Ask before use (#4016); a new secret starts on (#4162).
    let offersAsk: Bool
    let save: (SessionSecretSetRequest) async -> String?

    @Environment(\.dismiss) private var dismiss
    @State private var session: UUID?
    @State private var name = ""
    @State private var value = ""
    @State private var about = ""
    @State private var askBeforeUse = true
    @State private var problem: String?
    @State private var saving = false

    private var chosen: String { name.trimmingCharacters(in: .whitespaces) }

    var body: some View {
        Form {
            Section {
                if sessions.isEmpty {
                    Text(SessionSecretWords.noSessions).foregroundStyle(.secondary)
                } else {
                    Picker(SessionSecretWords.session, selection: $session) {
                        ForEach(sessions) { agent in
                            Text(agent.title ?? "Untitled session").tag(Optional(agent.id))
                        }
                    }
                }
                TextField("Name", text: $name, prompt: Text("SHOP_PASSWORD"))
                    .autocorrectionDisabled()
                    #if os(iOS)
                    .textInputAutocapitalization(.characters)
                    #endif
                SecureField("Value", text: $value)
                SecretDescriptionField(text: $about)
                if offersAsk {
                    AskBeforeUseToggle(isOn: $askBeforeUse)
                }
            } header: {
                Text(SessionSecretWords.sheetTitle)
            } footer: {
                Text(problem ?? SessionSecretWords.footer).appText(.fine)
                    .tinted(problem == nil ? .none : .failure)
                    .fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                Button("Cancel", role: .cancel) { dismiss() }
                Spacer()
                Button("Add") {
                    guard let session else { return }
                    Task {
                        saving = true
                        problem = await save(SessionSecretSetRequest(agentID: session, name: chosen, value: value,
                                                                     needsApproval: offersAsk && askBeforeUse ? true : nil,
                                                                     description: UserScopeWords.description(about)))
                        saving = false
                        if problem == nil { dismiss() }
                    }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(saving || session == nil || value.isEmpty || !UserScopeWords.isSecretName(chosen))
            }
            .rowButtons()
        }
        .paperForm()
        #if os(macOS)
        .frame(minWidth: 400)
        #endif
        .onAppear { session = session ?? sessions.first?.id }
        .onDisappear { value = "" }
    }
}
