import Foundation

/// The guided DigitalOcean Connect (#6074, wireframes W1, W2, W2b and W6 in epic #6070): the
/// words of the Mac's Add a Provider sheet and card. The web page's are the same, in
/// Web/src/model/digitalOceanConnect.ts.
public enum DigitalOceanConnectWords {
    /// The preset an OAuth connection makes (#6072).
    public static let preset = "digitalocean-api"

    // W1: its row in Add a Provider's list.
    public static let guidedChip = "Guided"
    public static let rowSummary = "Connect your account; Agent Loops makes the droplet."

    // W2: Connect.
    public static let intro = "Agent Loops makes droplets for your agents, and starts and stops them."
    public static let howItWorks = "How it works"
    public static let connectLine = "Connect your DigitalOcean account. Your browser opens DigitalOcean: sign in and choose Authorize."
    public static let connect = "Connect DigitalOcean…"
    public static let noAccount = "No account yet?"
    public static let signUp = "Sign up on DigitalOcean"
    public static let signUpURL = URL(string: "https://cloud.digitalocean.com/registrations/new")!
    public static let keeps = "Keeps the connection"
    public static let keepsHelp = "The token stays on this machine."
    public static let noMachine = "Add a machine first. The connection stays on one of your machines."

    // W2: the fallback.
    public static let tokenInstead = "Use an API token instead"
    public static let tokenField = "API token"
    public static let tokenPage = "Make a token on DigitalOcean"
    public static let tokenPageURL = URL(string: "https://cloud.digitalocean.com/account/api/tokens")!
    public static let tokenScopes = "Custom scopes: droplet create, read, update and delete; ssh_key create and read; regions read; sizes read; tag create; account read."
    public static let connectWithToken = "Connect with Token"

    // W2b: waiting, and the refusals.
    public static let waiting = "Waiting for DigitalOcean…"
    public static let waitingHelp = "Finish in your browser, then come back here."
    public static let openAgain = "Open the Page Again"
    public static let addPayment = "Add a Payment Method"
    public static let billingURL = URL(string: "https://cloud.digitalocean.com/account/billing")!
    public static let checkAgain = "Check Again"

    /// What a refusal offers: Connect again, or the billing page and Check Again.
    public enum Fix: Equatable, Sendable { case connect, payment }

    /// A refusal's words and its one fix, by the control plane's data.reason (#6072).
    public static func refusal(reason: String?, words: String) -> (text: String, fix: Fix) {
        switch reason {
        case "denied": ("DigitalOcean did not connect: you chose Deny.", .connect)
        case "account_inactive": ("Your DigitalOcean account cannot make droplets yet, such as when it has no payment method.", .payment)
        case "scope_missing": ("DigitalOcean gave Agent Loops less than it needs. Connect again, and keep every permission it asks for.", .connect)
        case "expired": ("That sign-in took too long, or was started somewhere else. Connect again.", .connect)
        case "disconnected": (disconnected, .connect)
        default: (words, .connect)
        }
    }

    // W6: the card.
    public static let connectedChip = "Connected"
    public static let disconnectedChip = "Disconnected"
    public static let reconnect = "Reconnect…"
    public static let disconnected = "DigitalOcean disconnected Agent Loops. Reconnect to start and stop its droplets."

    /// "alex@example.com · team “Personal”", or nil when the provider said neither.
    public static func accountLine(_ oauth: DaemonAPI.ProviderOAuth) -> String? {
        let parts = [oauth.account, oauth.team.map { "team “\($0)”" }].compactMap { $0?.isEmpty == false ? $0 : nil }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    /// Remove's note for an OAuth provider: the connection is revoked, the droplets stay.
    public static let removeNote = "Agent Loops’s connection is revoked at DigitalOcean. Its droplets stay: delete them on DigitalOcean to stop paying for them."

    /// The card's chip, and Reconnect…, are for a provider connected with OAuth.
    public static func isOAuth(_ provider: DaemonAPI.ProviderSummary) -> Bool {
        provider.oauth != nil && provider.preset == preset
    }
}
