import AgentsKitCore
import SwiftUI

/// Where Settings ▸ You ▸ Secrets ▸ On This Machine reads and writes (#5122): the window's model
/// and the Remote's, each through the Mac's host it reaches. Its Ask before use toggle shows when
/// the host keeps the flag (`sessionHostAsksBeforeUse`).
@MainActor
protocol LocalSecretsSource: SessionSecretsSource {
    /// Nil from a host too old to know the method.
    func localSecrets() async -> LocalSecrets?
    func setLocalSecretAsksBeforeUse(_ secret: LocalSecret, _ on: Bool) async -> Result<LocalSecrets?, Error>
}
