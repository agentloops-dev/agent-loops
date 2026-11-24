import AgentsKitCore
import Foundation
import Observation

/// What a client keeps of the user scope (#1760): the person's settings and the names of
/// their secrets, read again when the control plane says they changed, and the last
/// machine that went with secrets on it.
///
/// The window's model and the Remote's each own one, fed from the control plane's own
/// connection: `heard` for each notification.
@MainActor @Observable
final class UserScopeState {
    private(set) var settings: DaemonAPI.UserSettings?
    private(set) var secrets: DaemonAPI.UserSecretNames?
    /// When `settings` and `secrets` were last read (#5146), for the "Last update" line of
    /// Secrets and Accounts.
    private(set) var readAt: Date?
    /// The last control/secretsLeft, until the person reads it.
    var left: DaemonAPI.SecretsLeft?
    /// Why the user scope could not be read, or nil.
    private(set) var problem: String?
    /// The machine lock, so a secret is sealed only to the machines it names (#3491, D8).
    @ObservationIgnored weak var lock: LockState?
    /// The secrets agents saved on the person's machines that are not theirs yet (#5308).
    private(set) var offers: [DaemonAPI.SecretOffer] = []
    /// The offers answered from here and not done yet, and why one failed: by card (#6626), so
    /// a new offer of the same name keeps them.
    private(set) var offerAnswers = DaemonAPI.SecretOfferAnswers()
    /// This client's key, to take an offer (the Mac window). Nil on the Remote, which does not
    /// seal: it shows the offers only.
    @ObservationIgnored var sealing: (() -> (DeviceKey, UUID)?)?
    /// The files of variables the ⌘K menu read when it last opened (#5261): names only are shown.
    var menuFiles: [EnvFile] = []

    func load(_ control: DaemonClient?) async {
        guard let control else {
            problem = "The control plane can’t be reached."
            return
        }
        do {
            settings = try await control.call(DaemonAPI.Method.userSettings, DaemonAPI.Empty(),
                                              returning: DaemonAPI.UserSettings.self)
            secrets = try await control.call(DaemonAPI.Method.userSecretNames, DaemonAPI.Empty(),
                                             returning: DaemonAPI.UserSecretNames.self)
            readAt = Date()
            problem = nil
        } catch {
            problem = UserScopeWords.refusal(error)
        }
        await loadOffers(control)
    }

    /// The agents' secrets (#5308): read, and each with no conflict taken at once when this
    /// client seals. On `control/sealingWanted` and with each load.
    func loadOffers(_ control: DaemonClient) async {
        let keyed = sealing?()
        let lock = lock
        guard let listed = await ClientSealing.takeOffers(on: control, key: keyed?.0, client: keyed?.1, take: keyed != nil,
                                                          signed: { recipients in
                                                              if let lock { await lock.signed(recipients, on: control) } else { recipients }
                                                          }) else { return }
        offers = DaemonAPI.SecretOffer.waiting(listed.offers)
        offerAnswers.keep(only: offers)
    }

    /// Use the Agent's (true) or Keep Yours (false) for an offer whose name the person has (#5308).
    /// The card shows progress until the offers are read again, then goes, or says why not (#6626).
    func choose(_ offer: DaemonAPI.SecretOffer, useTheAgents: Bool, on control: DaemonClient?) async {
        offerAnswers.begin(offer)
        offerAnswers.end(offer, failure: await answer(offer, useTheAgents: useTheAgents, on: control))
    }

    /// Why the answer failed, or nil once it is done and the offers are read again.
    private func answer(_ offer: DaemonAPI.SecretOffer, useTheAgents: Bool, on control: DaemonClient?) async -> String? {
        guard let control else { return "The control plane can’t be reached." }
        do {
            if useTheAgents {
                guard let (key, client) = sealing?() else { return SecretOfferWords.noKey }
                let names = try await control.call(DaemonAPI.Method.userSecretNames, DaemonAPI.Empty(),
                                                   returning: DaemonAPI.UserSecretNames.self)
                let lock = lock
                let user = try await control.call(DaemonAPI.Method.userSecretRecipients, DaemonAPI.Empty(),
                                                  returning: DaemonAPI.SealRecipients.self).user ?? ""
                try await ClientSealing.take(offer, user: user, kept: names.secrets.first { $0.name == offer.name }?.version,
                                             on: control, key: key, client: client) { recipients in
                    if let lock { await lock.signed(recipients, on: control) } else { recipients }
                }
            } else {
                _ = try await control.call(DaemonAPI.Method.userDropSecretOffer, DaemonAPI.SecretOfferRequest(offer: offer.offer))
            }
            await load(control)
            return nil
        } catch {
            return UserScopeWords.refusal(error)
        }
    }

    /// A notification from the control plane. True when it was the user scope's.
    func heard(_ method: String, _ params: JSONValue?, on control: DaemonClient) async -> Bool {
        switch method {
        case DaemonAPI.Notification.controlUserSettingsChanged:
            await load(control)
        case DaemonAPI.Notification.controlSecretsLeft:
            left = try? params?.decode(DaemonAPI.SecretsLeft.self)
            await load(control)
        default:
            return false
        }
        return true
    }

    /// Sets a part at the user scope, or with `.null` gives it back to each machine. Nil
    /// once done, else why not.
    func set(_ part: DaemonAPI.UserSettingsPartName, to value: JSONValue, on control: DaemonClient?) async -> String? {
        guard let control else { return "The control plane can’t be reached." }
        do {
            _ = try await control.call(DaemonAPI.Method.userSetSettings, DaemonAPI.UserSettingsPart(part: part, value: value))
            await load(control)
            return nil
        } catch {
            return UserScopeWords.refusal(error)
        }
    }

    /// Seals the value here to each of the person's machines and apps (#3397), and sends only
    /// the envelopes: the control plane never has it. `kind` is `card` for a card (#3531).
    /// `replacing` is the row replaced, for its version.
    /// `askBeforeUse` (#4016) is sealed into every copy's associated data, so the control plane
    /// cannot change it. `description` (#4167) goes beside the name, in clear.
    func setSecret(_ name: String, value: String, kind: String? = nil, replacing: DaemonAPI.UserSecretRow? = nil,
                   askBeforeUse: Bool = false, description: String? = nil, on control: DaemonClient?) async -> String? {
        guard let control else { return "The control plane can’t be reached." }
        do {
            let to = try await control.call(DaemonAPI.Method.userSecretRecipients, DaemonAPI.Empty(),
                                            returning: DaemonAPI.SealRecipients.self)
            let version = UserScopeWords.nextVersion(replacing ?? secrets?.secrets.first { $0.name == name }, now: Date())
            // Only to the machines the lock names (#3491, D8): the control plane cannot add a key of its own.
            let recipients = if let lock { await lock.signed(to.recipients, on: control) } else { to.recipients }
            let values = try ClientSealing.sealSecret(user: to.user ?? "", name: name, version: version, value: value,
                                                      ask: askBeforeUse, to: recipients)
            _ = try await control.call(DaemonAPI.Method.userSetSecret,
                                       DaemonAPI.UserSecretSet(name: name, version: version, values: values, kind: kind,
                                                               needsApproval: askBeforeUse ? true : nil, description: description))
            await load(control)
            return nil
        } catch {
            return UserScopeWords.refusal(error)
        }
    }

    func removeSecret(_ name: String, on control: DaemonClient?) async -> String? {
        guard let control else { return "The control plane can’t be reached." }
        do {
            _ = try await control.call(DaemonAPI.Method.userRemoveSecret, DaemonAPI.UserSecretRequest(name: name))
            await load(control)
            return nil
        } catch {
            return UserScopeWords.refusal(error)
        }
    }
}
