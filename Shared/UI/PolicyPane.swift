import AgentsKitCore
import SwiftUI

/// Settings ▸ Agents ▸ Policy (#2957, FR-019 to FR-021, frames A and C): your layer of the
/// policy, in five sections, each row with the value that applies, its cap from a layer
/// above, and where it comes from. The machine resolves the layers (`policy/effective`) and
/// keeps your layer (`policy/yours`, `policy/setYours`); this pane draws and asks.
///
/// One view for the Mac's Settings pane and the Remote's (slice 6), which change every rule
/// (FR-027, #3344). The web page's is `Web/src/views/settings/AgentsPolicy.tsx`.
struct PolicyPane: View {
    /// The machine that keeps your layer (the Mac's own host), or nil while it can't be reached.
    let host: DaemonClient?
    /// Moves when the machine says a layer changed (`policy/effectiveChanged`).
    let revision: Int
    /// Opens Organisation Policy, where there is one to open.
    var openOrganisationPolicy: (() -> Void)?
    /// The online machines that do not follow Commit trailers yet (#6373), by name.
    var trailersBehind: [String] = []
    /// Opens a folder's Project Settings ▸ Policy, by its path (#6534), where the window can.
    var openFolderPolicy: ((String) -> Void)?

    @State private var effective: DaemonAPI.EffectivePolicy?
    @State private var yours: DaemonAPI.YourPolicyState?
    @State private var problem: String?
    @State private var isSaving = false
    /// When the machine was last read (#5139).
    @State private var readAt: Date?
    /// The trailer whose last save failed, and why (#6373).
    @State private var trailerFailure: (key: String, line: String)?
    /// The folders whose own rule changes the Commit trailers block (#6534).
    @State private var trailerFolders: [CommitTrailerWords.FolderChange] = []
    /// Whether the person turned the Commit trailers flag on (`FeatureFlag`, #6254).
    @AppStorage(FeatureFlag.commitTrailers.key, store: WindowDefaults.store) private var trailersOn = false

    private struct Reload: Hashable {
        var connected: Bool
        var revision: Int
    }

    var body: some View {
        Form {
            if let effective {
                banner(effective)
                #if os(macOS)
                // Rules signed on another Mac (#3230): tier mac+web, so not on the Remote.
                if let untrusted = yours?.untrusted {
                    untrustedSection(untrusted)
                }
                #endif
                ForEach(PolicyLayersWords.Section.allCases, id: \.self) { section in
                    Section {
                        ForEach(PolicyLayersWords.rows(in: section)) { row in
                            PolicyRuleRow(row: row, rule: effective.rules.first { $0.rule == row.rule },
                                          yours: yours.flatMap { PolicyLayersWords.value($0.policy.rules, row.rule) },
                                          editable: yours != nil && !isSaving) { value in
                                Task { await save(row.rule, value) }
                            }
                        }
                    } header: {
                        Text(section.title)
                    } footer: {
                        if section == .tools {
                            Text(PolicyLayersWords.toolsFoot).appText(.fine).foregroundStyle(.secondary)
                        } else if section == .attention {
                            Text(PolicyLayersWords.foot).appText(.fine).foregroundStyle(.secondary)
                        }
                    }
                    .paperListRow()
                }
                #if os(macOS)
                // Mac + web (#6373): the Remote has no Commit trailers group.
                if trailersOn, CommitTrailerWords.isFollowed(effective) {
                    CommitTrailersSection(effective: effective, yours: yours?.policy.rules,
                                          editable: yours != nil && !isSaving, failure: trailerFailure,
                                          readAt: readAt, folders: trailerFolders,
                                          openFolder: openFolderPolicy) { trailer, on in
                        Task { await save(trailer, on) }
                    }
                }
                #endif
            } else if let problem {
                Text(problem).appText(.supporting).tinted(.failure).paperListRow()
            } else {
                Text("Loading…").appText(.supporting).foregroundStyle(.secondary).paperListRow()
            }
        }
        .paperForm()
        .task(id: Reload(connected: host != nil, revision: revision)) { await load() }
        .refreshable { await load() }
    }

    private func banner(_ effective: DaemonAPI.EffectivePolicy) -> some View {
        Section {
            VStack(alignment: .leading, spacing: 4) {
                Label(PolicyLayersWords.banner(effective),
                      icon: effective.organisation == nil ? "person" : OrgPolicyWords.symbol)
                    .appText(.reading).fontWeight(.semibold)
                if effective.organisation != nil {
                    if let openOrganisationPolicy {
                        Button(PolicyLayersWords.organisationTitle, action: openOrganisationPolicy)
                            .buttonStyle(.borderless)
                    }
                } else {
                    Text(PolicyLayersWords.foldersLine(effective.foldersWithRules.count))
                        .appText(.fine).foregroundStyle(.secondary)
                }
                if let behind = PolicyLayersWords.behindLine(effective.behind) {
                    Text(behind).appText(.fine).tinted(.warning)
                        .fixedSize(horizontal: false, vertical: true)
                }
                #if os(macOS)
                if trailersOn, let behind = trailersBehindLine(effective) {
                    Text(behind).appText(.fine).tinted(.warning)
                        .fixedSize(horizontal: false, vertical: true)
                }
                #endif
                if let problem {
                    Text(problem).appText(.fine).tinted(.failure)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .accessibilityElement(children: .contain)
        }
        .paperListRow()
    }

    /// The rules of a copy of your layer this Mac can't trust (#3230), and the person's choice:
    /// sign them with this Mac's person key, or set the copy aside. Nothing signs them without it.
    private func untrustedSection(_ copy: DaemonAPI.UntrustedYourPolicy) -> some View {
        Section {
            VStack(alignment: .leading, spacing: 6) {
                Label(PolicyLayersWords.untrustedTitle, icon: "exclamationmark.shield")
                    .appText(.reading).fontWeight(.semibold).tinted(.warning)
                Text(PolicyLayersWords.untrustedLine(copy)).appText(.fine)
                    .fixedSize(horizontal: false, vertical: true)
                if copy.lines.isEmpty {
                    Text(PolicyLayersWords.untrustedNone).appText(.fine).foregroundStyle(.secondary)
                } else {
                    ForEach(copy.lines, id: \.rule) { line in
                        Text("• " + PolicyLayersWords.untrustedRule(line)).appText(.fine)
                    }
                }
                HStack {
                    Button(PolicyLayersWords.signAgain) { Task { await settle(copy, sign: true) } }
                        .buttonStyle(.paperProminent)
                    Button(PolicyLayersWords.setAside) { Task { await settle(copy, sign: false) } }
                }
                .disabled(isSaving)
            }
            .accessibilityElement(children: .contain)
        }
        .paperListRow()
    }

    private func load() async {
        guard let host else {
            problem = "This Mac’s host can’t be reached."
            return
        }
        do {
            effective = try await host.call(DaemonAPI.Method.policyEffective, DaemonAPI.PolicyEffectiveRequest(),
                                            returning: DaemonAPI.EffectivePolicy.self)
            yours = try await host.call(DaemonAPI.Method.policyYours, DaemonAPI.Empty(),
                                        returning: DaemonAPI.YourPolicyState.self)
            readAt = .now
            problem = nil
            #if os(macOS)
            if let effective { trailerFolders = await trailerFolders(host, outside: effective) }
            #endif
        } catch {
            problem = PeopleWords.refusal(error)
        }
    }

    #if os(macOS)
    /// The folders with rules of their own whose rule changes the Commit trailers block: a
    /// folder's `.agents/policy.json` can turn a trailer on or off for its agents (#6534). One
    /// read for each such folder, only with the flag on; a folder that can't be read is left out.
    private func trailerFolders(_ host: DaemonClient, outside: DaemonAPI.EffectivePolicy) async -> [CommitTrailerWords.FolderChange] {
        guard trailersOn, CommitTrailerWords.isFollowed(outside) else { return [] }
        var out: [CommitTrailerWords.FolderChange] = []
        for folder in outside.foldersWithRules {
            guard let inFolder = try? await host.call(DaemonAPI.Method.policyEffective,
                                                      DaemonAPI.PolicyEffectiveRequest(folder: folder.folder),
                                                      returning: DaemonAPI.EffectivePolicy.self),
                  let change = CommitTrailerWords.folderChange(folder: folder.folder, name: folder.name,
                                                               inFolder: inFolder, outside: outside) else { continue }
            out.append(change)
        }
        return out
    }

    /// The machines that do not follow Commit trailers: the online ones that do not say
    /// `host.commit-trailers/1`, or this Mac when its own host gives no such rule and no other is named.
    private func trailersBehindLine(_ effective: DaemonAPI.EffectivePolicy) -> String? {
        var names = trailersBehind
        if !CommitTrailerWords.isFollowed(effective), names.isEmpty { names = [CommitTrailerWords.thisMac] }
        return CommitTrailerWords.behindLine(names)
    }

    /// Sets one trailer in your layer. A save that fails says so under the trailer's switch,
    /// which the read after it puts back where it was.
    private func save(_ trailer: CommitTrailerWords.Trailer, _ on: Bool) async {
        guard let host, let yours else { return }
        let wasOn = CommitTrailerWords.isOn(trailer, effective?.rules.first { $0.rule == trailer.rule })
        isSaving = true
        defer { isSaving = false }
        do {
            _ = try await host.call(DaemonAPI.Method.policySetYours,
                                    DaemonAPI.SetYourPolicyRequest(rules: CommitTrailerWords.setting(yours.policy.rules, trailer, on: on),
                                                                   revision: yours.policy.revision),
                                    returning: DaemonAPI.YourPolicy.self)
            trailerFailure = nil
        } catch {
            trailerFailure = (trailer.key, CommitTrailerWords.failed(trailer, wasOn: wasOn, reason: PeopleWords.refusal(error)))
        }
        await load()
    }
    #endif

    private func save(_ rule: String, _ value: JSONValue?) async {
        guard let host, let yours else { return }
        isSaving = true
        defer { isSaving = false }
        let rules = PolicyLayersWords.setting(yours.policy.rules, rule, to: value)
        do {
            _ = try await host.call(DaemonAPI.Method.policySetYours,
                                    DaemonAPI.SetYourPolicyRequest(rules: rules, revision: yours.policy.revision),
                                    returning: DaemonAPI.YourPolicy.self)
            problem = nil
        } catch {
            problem = PeopleWords.refusal(error)
        }
        await load()
    }

    private func settle(_ copy: DaemonAPI.UntrustedYourPolicy, sign: Bool) async {
        guard let host, let yours else { return }
        isSaving = true
        defer { isSaving = false }
        do {
            _ = try await host.call(DaemonAPI.Method.policySettleUntrustedYours,
                                    DaemonAPI.SettleUntrustedYourPolicyRequest(file: copy.file, revision: yours.policy.revision,
                                                                               sign: sign),
                                    returning: DaemonAPI.YourPolicyState.self)
            problem = nil
        } catch {
            problem = PeopleWords.refusal(error)
        }
        await load()
    }
}
