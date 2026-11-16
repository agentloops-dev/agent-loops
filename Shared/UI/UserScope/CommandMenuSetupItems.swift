import AgentsKitCore
import Foundation

/// The ⌘K command menu's secrets, accounts and environment variables (#5261), from what the client keeps of the user
/// scope and the files of variables its first machine read when the menu opened. The window and the Remote list them
/// alike; the rules are `CommandMenuSetup`.
extension UserScopeState {
    /// The menu's setup items: the person's secrets, their accounts, then the variables.
    var commandMenuItems: [CommandFinder.Item] {
        CommandMenuSetup.items(secrets: secrets?.secrets ?? [], accounts: accounts, files: menuFiles,
                               yoursText: (try? settings?.environment?.decode(UserEnvironment.self))?.text)
    }

    /// Reads the files of variables of the client's first machine (this Mac, or the Mac the Remote reaches), for the
    /// menu, while it is open. A machine that cannot be reached leaves what was read before.
    func readMenuFiles(_ source: some EnvironmentSource) async {
        guard let machine = source.environmentMachines.first, let client = source.environmentClient(machine.id),
              let list = try? await client.call(DaemonAPI.Method.envList, EnvListRequest(), returning: EnvList.self)
        else { return }
        menuFiles = list.files
    }
}
