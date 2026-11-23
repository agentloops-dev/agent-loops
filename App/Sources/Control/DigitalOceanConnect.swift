import AgentsKitCore
import AuthenticationServices
import SwiftUI

/// The guided DigitalOcean Connect (#6074; W2 and W2b in epic #6070), in Add a Provider and on
/// Reconnect…: Connect DigitalOcean… asks the machine that will keep the connection for the
/// consent page (providers/oauthStart), opens it in an authentication session, which catches
/// agents://oauth/callback from the hand-back page, and gives the code to that machine
/// (providers/oauthFinish). The token never reaches this Mac's window. Use an API token instead
/// is the fallback. Once connected, the provider's card offers New Droplet (#6577).
struct DigitalOceanConnect: View {
    typealias Words = DigitalOceanConnectWords
    @Environment(\.webAuthenticationSession) private var session
    let control: ControlSettingsModel
    /// Reconnect…: the provider to connect again, on the machine that has it.
    var reconnecting: DaemonAPI.ProviderSummary?
    /// Back to the list, in Add a Provider; none on Reconnect….
    var back: (() -> Void)?
    var cancel: () -> Void
    /// Connected: the provider added or connected again.
    var done: (ProviderID?) -> Void

    enum Step: Equatable {
        case ready
        case starting
        case waiting(DaemonAPI.ProviderOAuthStarted)
        case finishing
        case refused(text: String, fix: DigitalOceanConnectWords.Fix)
    }

    @State private var step: Step = .ready
    @State private var carrier: MachineRef?
    @State private var usesToken = false
    @State private var token = ""
    @State private var signIn: Task<Void, Never>?

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            header
            Form {
                Section { connectSection }
                if reconnecting == nil {
                    Section { keeper }
                    Section {
                        DisclosureGroup(Words.tokenInstead, isExpanded: $usesToken) { tokenFallback }
                            .disabled(busy)
                    }
                }
            }
            .formStyle(.grouped)
            HStack {
                if let back {
                    Button("Back") { back() }.disabled(busy)
                }
                Spacer()
                Button("Cancel") { stop(); cancel() }.keyboardShortcut(.cancelAction)
            }
        }
        .task { carrier = ProviderCarrier.start(reconnecting, choices: control.carriers) }
        .onDisappear { stop() }
    }

    private var busy: Bool {
        switch step {
        case .starting, .waiting, .finishing: true
        default: false
        }
    }

    private var header: some View {
        HStack(alignment: .top, spacing: 10) {
            // Decorative: the kind's name is beside it.
            Image(icon: AddProviderSheet.Kind.digitalOcean.symbol).font(.system(size: 17)).foregroundStyle(.secondary)
                .frame(width: 24).accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(reconnecting?.name ?? AddProviderSheet.Kind.digitalOcean.title).fontWeight(.semibold)
                Text(Words.intro).appText(.supporting).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer()
            Link(Words.howItWorks, destination: AddProviderSheet.Kind.digitalOcean.docs).appText(.supporting)
        }
    }

    @ViewBuilder
    private var connectSection: some View {
        switch step {
        case .ready, .starting:
            Text(Words.connectLine).fixedSize(horizontal: false, vertical: true)
            connectButton
            if reconnecting == nil {
                HStack(spacing: 4) {
                    Text(Words.noAccount).foregroundStyle(.secondary)
                    Link(Words.signUp, destination: Words.signUpURL)
                }
                .appText(.supporting)
            }
        case .waiting(let started):
            HStack(alignment: .top, spacing: 8) {
                ProgressView().controlSize(.small)
                VStack(alignment: .leading, spacing: 2) {
                    Text(Words.waiting)
                    Text(Words.waitingHelp).appText(.supporting).foregroundStyle(.secondary)
                }
            }
            HStack {
                Button(Words.openAgain) { open(started) }
                Button("Cancel") { stop(); step = .ready }
            }
        case .finishing:
            HStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text(Words.waiting)
            }
        case .refused(let text, let fix):
            Label { Text(text).fixedSize(horizontal: false, vertical: true) } icon: {
                Image(icon: "exclamationmark.triangle").tinted(.attention)
            }
            switch fix {
            case .connect:
                connectButton
            case .payment:
                HStack {
                    Link(Words.addPayment, destination: Words.billingURL)
                    Button(Words.checkAgain) { connect() }
                }
            }
        }
    }

    private var connectButton: some View {
        HStack {
            Spacer()
            Button(Words.connect) { connect() }
                .buttonStyle(.paperProminent)
                .keyboardShortcut(.defaultAction)
                .disabled(busy || carrier == nil)
            Spacer()
        }
    }

    /// Keeps the connection: a picker, or with one machine its name as a line (W2).
    @ViewBuilder
    private var keeper: some View {
        let choices = control.carriers
        if choices.isEmpty {
            LabeledContent(Words.keeps) {
                Text(Words.noMachine).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        } else if choices.count == 1, let only = choices.first {
            LabeledContent(Words.keeps) {
                VStack(alignment: .leading, spacing: 3) {
                    Text(only.name)
                    Text(Words.keepsHelp).appText(.fine).foregroundStyle(.secondary)
                }
            }
        } else {
            LabeledContent(Words.keeps) {
                VStack(alignment: .leading, spacing: 3) {
                    CarrierPicker(control: control, carrier: $carrier).labelsHidden()
                    Text(Words.keepsHelp).appText(.fine).foregroundStyle(.secondary)
                }
            }
            .disabled(busy)
        }
    }

    @ViewBuilder
    private var tokenFallback: some View {
        VStack(alignment: .leading, spacing: 6) {
            SecureField(Words.tokenField, text: $token, prompt: Text("Required"))
            Text(Words.tokenScopes).appText(.fine).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            HStack {
                Link(Words.tokenPage, destination: Words.tokenPageURL).appText(.supporting)
                Spacer()
                Button(Words.connectWithToken) { addWithToken() }
                    .disabled(busy || carrier == nil || token.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
    }

    // MARK: Connecting

    private func stop() {
        signIn?.cancel()
        signIn = nil
    }

    private func connect() {
        stop()
        step = .starting
        signIn = Task {
            let request = DaemonAPI.ProviderOAuthStart(preset: Words.preset, machine: reconnecting?.machine ?? carrier,
                                                       id: reconnecting?.id)
            switch await control.oauthStart(request) {
            case .failure(let refusal):
                refuse(refusal)
            case .success(let started):
                step = .waiting(started)
                await authenticate(started)
            }
        }
    }

    /// Open the Page Again: the same consent page, in a new authentication session.
    private func open(_ started: DaemonAPI.ProviderOAuthStarted) {
        stop()
        signIn = Task { await authenticate(started) }
    }

    private func authenticate(_ started: DaemonAPI.ProviderOAuthStarted) async {
        guard let address = URL(string: started.url) else {
            step = .refused(text: "DigitalOcean gave no address to open. Connect again.", fix: .connect)
            return
        }
        let callback: URL
        do {
            callback = try await session.authenticate(
                using: address, callback: .customScheme(DaemonAPI.ConnectionSignInCompleteRequest.callbackScheme),
                preferredBrowserSession: nil, additionalHeaderFields: [:])
        } catch let error as ASWebAuthenticationSessionError where error.code == .canceledLogin {
            if !Task.isCancelled { step = .ready }
            return
        } catch {
            if !Task.isCancelled { step = .refused(text: error.localizedDescription, fix: .connect) }
            return
        }
        guard !Task.isCancelled else { return }
        guard let answer = DaemonAPI.ProviderOAuthFinish(callback: callback) else {
            step = .refused(text: "DigitalOcean came back with no answer. Connect again.", fix: .connect)
            return
        }
        step = .finishing
        switch await control.oauthFinish(answer) {
        case .failure(let refusal): refuse(refusal)
        case .success(let summary): done(summary.id)
        }
    }

    private func refuse(_ refusal: ControlSettingsModel.OAuthRefusal) {
        let said = Words.refusal(reason: refusal.reason, words: refusal.words)
        step = .refused(text: said.text, fix: said.fix)
    }

    /// The fallback: the token as the provider's secret, as Add did before (#1119).
    private func addWithToken() {
        stop()
        step = .finishing
        let before = Set(control.providers.map(\.id))
        var request = DaemonAPI.ProviderAdd(name: AddProviderSheet.Kind.digitalOcean.title, kind: .commands,
                                            commands: ["preset": .string(Words.preset)],
                                            secrets: ["DIGITALOCEAN_API_TOKEN": token.trimmingCharacters(in: .whitespacesAndNewlines)])
        request.machine = carrier
        Task {
            if let refused = await control.addProvider(request) {
                step = .refused(text: refused, fix: .connect)
            } else {
                done(control.providers.first { !before.contains($0.id) }?.id)
            }
        }
    }
}
