import AgentsKitCore
import SwiftUI

/// Where Settings ▸ You ▸ Secrets ▸ On This Machine reads and writes (#5122): the window's model
/// and the Remote's, each through the Mac's host it reaches. Its Ask before use toggle shows when
/// the host keeps the flag (`sessionHostAsksBeforeUse`).
@MainActor
protocol LocalSecretsSource: SessionSecretsSource {
    /// Nil from a host too old to know the method. An error is why the list did not load, which the page says (#6335):
    /// an empty list would hide a secret that agents still use.
    func localSecrets() async throws -> LocalSecrets?
    func setLocalSecretAsksBeforeUse(_ secret: LocalSecret, _ on: Bool) async -> Result<LocalSecrets?, Error>
    /// Take a secret this machine keeps for itself out of its store (#6335): the list after, or why not.
    func removeLocalSecret(_ secret: LocalSecret) async -> Result<LocalSecrets, Error>
    /// Whether that host takes one out (`host.local-secrets/2`): it offers Remove.
    var localSecretsRemovable: Bool { get }
}
