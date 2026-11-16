import AgentsKitCore
import AppKit
import Foundation
import os

/// The user scope (#1760).
extension AppModel {
    // MARK: The user scope (#1760)

    /// Sets a part at the user scope: nil once done, else why not.
    func setUserPart(_ part: DaemonAPI.UserSettingsPartName, _ value: some Encodable) async -> String? {
        guard let encoded = try? JSONValue.encoding(value) else { return "That could not be written." }
        return await userScope.set(part, to: encoded, on: controlRequests)
    }

    /// Use the Agent's or Keep Yours for an agent's secret whose name the person has (#5308).
    func chooseSecretOffer(_ offer: DaemonAPI.SecretOffer, useTheAgents: Bool) async {
        await userScope.choose(offer, useTheAgents: useTheAgents, on: controlRequests)
    }

    /// The person's time zone (#4818): this Mac's, set at the user scope when it differs, so
    /// a workflow's `hours:` with no `time-zone:` go by it on a server too. It has no control.
    func keepTimeZone() async {
        guard let settings = userScope.settings else { return }
        let here = TimeZone.current.identifier
        if (try? settings.timeZone?.decode(DaemonAPI.UserTimeZone.self))?.identifier == here { return }
        _ = await setUserPart(.timeZone, DaemonAPI.UserTimeZone(identifier: here))
    }

    /// After an edit in Agent Configuration or Agent Runtimes: a part that follows the person
    /// is set again from this Mac, when it changed here.
    func keepFollowingParts() async {
        for part in [DaemonAPI.UserSettingsPartName.mcpServers, .agentConfig, .runtimes] {
            guard let here = try? await macClient.call(DaemonAPI.Method.settingsUserPart, DaemonAPI.UserPartRequest(part: part),
                                                       returning: DaemonAPI.UserPartHere.self),
                here.value != userScope.settings?[part] else { continue }
            _ = await userScope.set(part, to: here.value, on: controlRequests)
        }
    }

    // MARK: Settings that follow the person to each machine (#5573)

    /// The connected machines that do not take a part from the control plane, which get it
    /// from here, one call each: those that do not say `capability`, and all of them when the
    /// control plane did not keep the part (none here, or one older than the part).
    func machinesNotTaking(_ capability: String, kept: Bool) -> [MachineRef] {
        hosts.hosts.all.map(\.id).filter { !hosts.isOffline($0) && (!kept || !host($0, has: capability)) }
    }

    /// Sets a part that follows the person once, at the control plane, which sends it to each
    /// of their machines as it joins and when it changes (#5573): not one call to each machine
    /// from here. `each` is the call for a machine that does not take it from there.
    func sendToMachines(_ part: DaemonAPI.UserSettingsPartName, _ value: some Encodable,
                        each: (DaemonClient) async -> Void) async {
        let kept = await setUserPart(part, value) == nil
        for id in machinesNotTaking(Capability.hostUserParts1, kept: kept) { await each(client(for: id)) }
    }

    /// Each runtime's sandbox default and Cursor/Grok's permission mode, as the part runtimes
    /// (#1760, #5573): as this Mac's host has them, set once at the control plane.
    func sendRuntimeChoicesToMachines() async {
        var kept = false
        if let here = try? await macClient.call(DaemonAPI.Method.settingsUserPart, DaemonAPI.UserPartRequest(part: .runtimes),
                                                 returning: DaemonAPI.UserPartHere.self) {
            if here.value == userScope.settings?.runtimes {
                kept = true
            } else {
                kept = await userScope.set(.runtimes, to: here.value, on: controlRequests) == nil
            }
        }
        for id in machinesNotTaking(Capability.hostUserSettings1, kept: kept) {
            await sendRuntimeChoices(to: client(for: id))
        }
    }

    private func sendRuntimeChoices(to server: DaemonClient) async {
        _ = try? await server.call(DaemonAPI.Method.clientPermissionsSet, clientPermissions,
                                   returning: ClientPermissionSettings.self)
        _ = try? await server.call(DaemonAPI.Method.sandboxSet, sandboxSettings, returning: SandboxSettings.self)
    }

    /// When the control plane answers (#5573): each part that follows the person is set from
    /// this Mac's host when it is missing or not the same there, so a machine that joins gets
    /// it from the control plane, and `refreshServer` sends it no call of its own.
    func keepMacParts() async {
        guard let settings = userScope.settings else { return }
        if let here = try? await macClient.call(DaemonAPI.Method.personState, Optional<String>.none, returning: PersonSettings.self),
           let value = try? JSONValue.encoding(here.resolved()), settings.person != value {
            _ = await userScope.set(.person, to: value, on: controlRequests)
        }
        if let here = try? await macClient.call(DaemonAPI.Method.autoState, DaemonAPI.Empty(), returning: AutoSettings.self),
           let value = try? JSONValue.encoding(here), settings.auto != value {
            _ = await userScope.set(.auto, to: value, on: controlRequests)
        }
        if let here = try? await macClient.call(DaemonAPI.Method.retentionState, Optional<String>.none,
                                                 returning: DaemonAPI.RetentionState.self),
           let value = try? JSONValue.encoding(here.settings), settings.retention != value {
            _ = await userScope.set(.retention, to: value, on: controlRequests)
        }
        if let here = try? await macClient.call(DaemonAPI.Method.settingsUserPart, DaemonAPI.UserPartRequest(part: .runtimes),
                                                 returning: DaemonAPI.UserPartHere.self), settings.runtimes != here.value {
            _ = await userScope.set(.runtimes, to: here.value, on: controlRequests)
        }
    }

    /// The settings a machine that has just connected does not take from the control plane
    /// (#5573): an older host, or a part the control plane does not keep yet. One that takes
    /// them all gets no call here.
    func sendSettingsNotTaken(to host: MachineRef) async {
        let server = client(for: host)
        let kept = userScope.settings
        let parts = self.host(host, has: Capability.hostUserParts1)
        if kept?.runtimes == nil || !self.host(host, has: Capability.hostUserSettings1) {
            await sendRuntimeChoices(to: server)
        }
        if kept?.retention == nil || !parts, let settings = work.retentionState?.settings {
            _ = try? await server.call(DaemonAPI.Method.retentionSet,
                                       DaemonAPI.RetentionSetRequest(settings: settings, confirmed: true),
                                       returning: DaemonAPI.RetentionSetResult.self)
        }
        if kept?.person == nil || !parts, let person {
            _ = try? await server.call(DaemonAPI.Method.personSet, person.resolved(), returning: PersonSettings.self)
        }
        if kept?.auto == nil || !parts {
            _ = try? await server.call(DaemonAPI.Method.autoSet, autoSettings, returning: AutoSettings.self)
        }
    }

    func refreshCostState() async {
        guard let state = try? await macClient.call(DaemonAPI.Method.costState,
                                                 Optional<String>.none,
                                                 returning: DaemonAPI.CostState.self) else { return }
        work.replaceCostState(state)
    }

    /// The reader setting or clearing a limit. Theirs alone: nothing an agent or a
    /// workflow can reach calls this.
}
