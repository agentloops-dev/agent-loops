import Foundation
import Testing
@testable import AgentsKitCore

/// Swift Testing has an `Attachment` of its own; this file means the prompt's.
private typealias Attachment = AgentsKitCore.Attachment

/// 025 US5: what you had half-typed is still there.
///
/// The rules, tested without a window: where a draft is kept, when it goes, and what
/// happens to a pasted picture too large to be worth keeping. Each test has a defaults
/// store of its own, in memory, so nothing here reads or writes the app's or leaves a
/// domain on disk (#1028).
@Suite("Drafts")
struct DraftStoreTests {
    private func store() -> (DraftStore, UserDefaults) {
        let defaults = MemoryDefaults()
        return (DraftStore(defaults: defaults), defaults)
    }

    private func draft(_ text: String = "Three paragraphs about the job",
                       attachments: [Attachment] = [],
                       at: Date = Date()) -> Draft {
        Draft(text: text, attachments: attachments, editedAt: at)
    }

    /// One attachment, made once: each carries an id of its own, so making it twice is two.
    private let aFile = Attachment.file(URL(filePath: "/tmp/somewhere/notes.md"))

    private func picture(bytes: Int) -> Attachment {
        .image(Data(repeating: 0x7F, count: bytes), mimeType: "image/png", name: "pasted.png")
    }

    // MARK: Kept against the conversation it was typed for

    @Test func aDraftComesBackForItsConversationAndNoOther() {
        let (store, _) = store()
        let one = UUID(), other = UUID()
        let written = draft(attachments: [aFile])

        store.save(written, for: .agent(one))

        #expect(store.draft(for: .agent(one)) == written)
        #expect(store.draft(for: .agent(other)) == nil)
        #expect(store.draft(for: .newAgent(folder: nil)) == nil)
    }

    /// Another store on the same defaults is another window, or the app next week.
    @Test func aDraftSurvivesTheStoreThatWroteIt() {
        let (store, defaults) = store()
        let id = UUID()
        store.save(draft(), for: .agent(id))

        #expect(DraftStore(defaults: defaults).draft(for: .agent(id))?.text == "Three paragraphs about the job")
    }

    /// FR-021: a draft exists only until it becomes a prompt, and an emptied field is not
    /// a draft.
    @Test func clearingOrEmptyingItLeavesNothing() {
        let (store, defaults) = store()
        let id = UUID()
        store.save(draft(), for: .agent(id))
        store.clear(.agent(id))
        #expect(store.draft(for: .agent(id)) == nil)

        store.save(draft(), for: .agent(id))
        store.save(draft(""), for: .agent(id))
        #expect(store.draft(for: .agent(id)) == nil)
        #expect(defaults.dictionaryRepresentation().keys.filter { $0.hasPrefix(DraftStore.prefix) }.isEmpty,
                "nothing left behind under the draft prefix")
    }

    @Test func eachKeyIsItsOwnPlace() {
        let one = UUID()
        let keys: [DraftKey] = [.agent(one), .agent(UUID()),
                                .newAgent(folder: nil),
                                .newAgent(folder: URL(filePath: "/tmp/somewhere/api")),
                                .newAgent(folder: URL(filePath: "/tmp/somewhere/web"))]
        let store = DraftStore(defaults: MemoryDefaults())
        #expect(Set(keys.map { store.defaultsKey(for: $0) }).count == keys.count)
        #expect(keys.allSatisfy { store.defaultsKey(for: $0).hasPrefix(DraftStore.prefix) })
        #expect(DraftKey.agent(one).name == DraftKey.agent(one).name, "stable")
        #expect(DraftKey.newAgent(folder: URL(filePath: "/tmp/somewhere/api/"))
                == DraftKey.newAgent(folder: URL(filePath: "/tmp/somewhere/api")),
                "a folder is the same folder with or without its trailing slash")
    }

    // MARK: FR-022: when a draft goes

    @Test func theSweepDropsADraftWhoseConversationIsArchived() {
        let (store, _) = store()
        let archived = UUID(), working = UUID()
        store.save(draft(), for: .agent(archived))
        store.save(draft(), for: .agent(working))

        store.sweep(archived: [archived], now: Date())

        #expect(store.draft(for: .agent(archived)) == nil)
        #expect(store.draft(for: .agent(working)) != nil)
    }

    /// The window sweeps once, then keeps the agents this names, so an agent archived
    /// later costs a look at the drafts that exist rather than another pass (#176).
    @Test func theSweepSaysWhichAgentsStillHaveADraft() {
        let (store, _) = store()
        let now = Date()
        let archived = UUID(), working = UUID(), old = UUID()
        store.save(draft(), for: .agent(archived))
        store.save(draft(), for: .agent(working))
        store.save(draft(at: now.addingTimeInterval(-DraftStore.horizon - 60)), for: .agent(old))
        store.save(draft(), for: .newAgent(folder: nil))

        #expect(store.sweep(archived: [archived], now: now) == [working])
    }

    /// The sweep runs before the agent list may have arrived. An agent it has not heard
    /// of is not evidence of anything, so it costs no draft — only "known, and archived"
    /// does, and the thirty days catches whatever is truly gone.
    @Test func anAgentTheSweepHasNotHeardOfCostsNothing() {
        let (store, _) = store()
        let id = UUID()
        store.save(draft(), for: .agent(id))

        store.sweep(archived: [], now: Date())

        #expect(store.draft(for: .agent(id)) != nil)
    }

    @Test func aDraftUntouchedForThirtyDaysGoes() {
        let (store, _) = store()
        let now = Date()
        let old = UUID(), recent = UUID()
        store.save(draft(at: now.addingTimeInterval(-DraftStore.horizon - 60)), for: .agent(old))
        store.save(draft(at: now.addingTimeInterval(-60)), for: .agent(recent))
        store.save(draft(at: now.addingTimeInterval(-DraftStore.horizon - 60)), for: .newAgent(folder: nil))

        store.sweep(archived: [], now: now)

        #expect(store.draft(for: .agent(old)) == nil)
        #expect(store.draft(for: .newAgent(folder: nil)) == nil)
        #expect(store.draft(for: .agent(recent)) != nil)
    }

    /// FR-026 in the window's terms: a draft is never worth failing over.
    @Test func anEntryThatWillNotDecodeIsDiscarded() {
        let (store, defaults) = store()
        let id = UUID()
        defaults.set(Data("{ not a draft".utf8), forKey: store.defaultsKey(for: .agent(id)))

        #expect(store.draft(for: .agent(id)) == nil)
        #expect(defaults.object(forKey: store.defaultsKey(for: .agent(id))) == nil, "and not left to fail again")
    }

    // MARK: A pasted picture too large to keep

    @Test func inlineDataOverTheCapIsDroppedAndSaysSo() {
        let (store, _) = store()
        let id = UUID()
        let big = picture(bytes: DraftStore.inlineCap + 1)
        store.save(draft(attachments: [aFile, big]), for: .agent(id))

        let back = store.draft(for: .agent(id))
        #expect(back?.text == "Three paragraphs about the job", "the words are always kept")
        #expect(back?.attachments == [aFile], "and anything sent by reference")
        #expect(back?.droppedInlineData == true, "and it says what it could not keep")
    }

    @Test func inlineDataUnderTheCapIsKept() {
        let (store, _) = store()
        let id = UUID()
        let small = picture(bytes: 2_048)
        store.save(draft(attachments: [small, aFile]), for: .agent(id))

        let back = store.draft(for: .agent(id))
        #expect(back?.attachments == [small, aFile])
        #expect(back?.droppedInlineData == false)
    }

    /// A draft whose only content was too big still says so rather than vanishing.
    @Test func aDraftThatWasOnlyAPictureStillSaysSo() {
        let (store, _) = store()
        let id = UUID()
        store.save(draft("", attachments: [picture(bytes: DraftStore.inlineCap + 1)]), for: .agent(id))

        #expect(store.draft(for: .agent(id))?.droppedInlineData == true)
    }

    // MARK: How many, and how big (#6794)

    private func fill(_ held: inout HeldDrafts, at: Date = Date()) -> [DraftKey] {
        let keys = (0..<DraftStore.draftsKept).map { _ in DraftKey.agent(UUID()) }
        #expect(held.save(Dictionary(uniqueKeysWithValues: keys.map { ($0, draft(at: at)) })) == nil)
        return keys
    }

    @Test func aFullStoreRefusesANewDraftAndSaysSoOnce() throws {
        let (store, _) = store()
        var held = HeldDrafts(store: store)
        let keys = fill(&held), new = DraftKey.newAgent(folder: nil)
        let first = try #require(keys.first), last = try #require(keys.last)

        #expect(held.save([first: draft("edited")]) == nil, "an edit of a kept draft is not refused")
        #expect(held.save([new: draft("on")]) == HeldDrafts.full)
        #expect(held.save([new: draft("one more")]) == nil)
        #expect(held.draft(for: new)?.text == "one more", "held while the app runs")
        #expect(store.draft(for: new) == nil)
        #expect(store.draft(for: first)?.text == "edited")
        #expect(keys.allSatisfy { store.draft(for: $0) != nil }, "nothing made room for it")

        held.clear(last)
        #expect(store.draft(for: new)?.text == "one more")
    }

    /// An emptied draft makes room for one typed in the same pause.
    @Test func anEmptiedDraftMakesRoom() {
        let (store, _) = store()
        var held = HeldDrafts(store: store)
        let keys = fill(&held), new = DraftKey.agent(UUID())
        #expect(held.save([new: draft(), keys[0]: draft("")]) == nil)
        #expect(store.draft(for: new) != nil)
    }

    @Test func aFullStoreLetsGoOfOldDraftsFirst() {
        let (store, _) = store()
        var held = HeldDrafts(store: store)
        _ = fill(&held, at: Date().addingTimeInterval(-DraftStore.horizon - 60))
        let new = DraftKey.agent(UUID())
        #expect(held.save([new: draft()]) == nil)
        #expect(store.draft(for: new) != nil)
    }

    @Test func aDraftTooBigIsRefusedAndTheOneKeptStays() {
        let (store, _) = store()
        var held = HeldDrafts(store: store)
        let key = DraftKey.agent(UUID())
        _ = held.save([key: draft("short")])
        let long = String(repeating: "a", count: DraftStore.draftBytes)
        #expect(held.save([key: draft(long)]) == HeldDrafts.tooBig)
        #expect(held.draft(for: key)?.text == long)
        #expect(store.draft(for: key)?.text == "short")
        #expect(held.save([key: draft("short again")]) == nil)
        #expect(store.draft(for: key)?.text == "short again")
    }

    /// The start form and another root's drafts take no place; a move takes none either.
    @Test func onlyThisRootsDraftsCount() {
        let defaults = MemoryDefaults()
        let store = DraftStore(defaults: defaults)
        var held = HeldDrafts(store: store)
        store.saveStartDraft(StartDraft(runtimeID: "claude"))
        DraftStore(defaults: defaults, scope: "run-count").save(draft(), for: .newAgent(folder: nil))
        let folder = URL(filePath: "/src/Agents")
        store.save(draft("From before"), for: .newAgent(folder: folder))
        let keys = (1..<DraftStore.draftsKept).map { _ in DraftKey.agent(UUID()) }
        #expect(held.save(Dictionary(uniqueKeysWithValues: keys.map { ($0, draft()) })) == nil)
        let project = DraftKey.newSession(project: "p1", folder: folder)
        #expect(held.draft(for: project)?.text == "From before")
        #expect(held.save([.agent(UUID()): draft()]) == HeldDrafts.full)
    }

    @Test func aForgetLetsGoOfWhatWasRefused() {
        let (store, _) = store()
        var held = HeldDrafts(store: store)
        _ = fill(&held)
        let new = DraftKey.agent(UUID())
        _ = held.save([new: draft()])
        held.clearAll()
        held.retry()
        #expect(held.draft(for: new) == nil)
    }

    // MARK: The start form

    @Test func theStartFormComesBack() {
        let (store, defaults) = store()
        let form = StartDraft(cwd: URL(filePath: "/tmp/somewhere/api"), runtimeID: "grok",
                              folders: [URL(filePath: "/tmp/somewhere/shared")],
                              servers: [], chosen: ["model": "grok-4"])
        store.saveStartDraft(form)

        #expect(DraftStore(defaults: defaults).startDraft() == form)
    }

    // MARK: Forget (#3670)

    /// Forgetting the control plane clears every draft and the start form, and only this
    /// root's: a scratch root's stay, and so does anything that is not a draft.
    @Test func aForgetClearsThisRootsDraftsAndNoOthers() {
        let defaults = MemoryDefaults()
        let ordinary = DraftStore(defaults: defaults)
        let scratch = DraftStore(defaults: defaults, scope: "run-forget")
        ordinary.save(draft("to the old control plane"), for: .agent(UUID()))
        ordinary.save(draft("a new one"), for: .newAgent(folder: URL(filePath: "/tmp/somewhere/api")))
        ordinary.saveStartDraft(StartDraft(runtimeID: "claude"))
        scratch.save(draft("a test's"), for: .newAgent(folder: nil))
        defaults.set(true, forKey: "sidebar.folded")

        ordinary.clearAll()

        #expect(defaults.dictionaryRepresentation().keys.filter { $0.hasPrefix(ordinary.scopePrefix) }
            .allSatisfy { $0.hasPrefix(scratch.scopePrefix) })
        #expect(ordinary.startDraft() == nil)
        #expect(scratch.draft(for: .newAgent(folder: nil))?.text == "a test's")
        #expect(defaults.bool(forKey: "sidebar.folded"))

        scratch.clearAll()
        #expect(scratch.draft(for: .newAgent(folder: nil)) == nil)
    }

    // MARK: One root's drafts are that root's

    /// A copy of the app on a scratch root shares the ordinary app's defaults. Neither may
    /// see, overwrite or sweep the other's drafts or start form.
    @Test func aScratchRootsDraftsAreItsOwn() {
        let defaults = MemoryDefaults()
        let ordinary = DraftStore(defaults: defaults)
        let scratch = DraftStore(defaults: defaults, scope: "run-drafts")
        let id = UUID()
        ordinary.save(draft("the real one"), for: .newAgent(folder: nil))
        scratch.save(draft("a test's"), for: .newAgent(folder: nil))
        ordinary.saveStartDraft(StartDraft(runtimeID: "claude"))
        scratch.saveStartDraft(StartDraft(runtimeID: "grok"))
        scratch.save(draft(at: Date().addingTimeInterval(-DraftStore.horizon - 60)), for: .agent(id))

        #expect(ordinary.draft(for: .newAgent(folder: nil))?.text == "the real one")
        #expect(scratch.draft(for: .newAgent(folder: nil))?.text == "a test's")
        #expect(ordinary.startDraft()?.runtimeID == "claude")
        #expect(scratch.startDraft()?.runtimeID == "grok")

        // The ordinary store's sweep does not reach into a scratch root's keys, however
        // old they are, and the scratch one's does not touch the ordinary drafts.
        ordinary.sweep(archived: [id], now: Date())
        #expect(defaults.object(forKey: scratch.defaultsKey(for: .agent(id))) != nil)
        scratch.sweep(archived: [], now: Date())
        #expect(defaults.object(forKey: scratch.defaultsKey(for: .agent(id))) == nil)
        #expect(ordinary.draft(for: .newAgent(folder: nil))?.text == "the real one")
        #expect(ordinary.startDraft()?.runtimeID == "claude")
    }

    /// Clean up worktrees on the Remote (#1863): its words go after a half-typed draft, and
    /// the draft's files stay.
    @Test func wordsGivenBackGoAfterAKeptDraft() {
        let words = "Remove worktrees."
        let alone = Draft.givingBack(words, to: nil)
        #expect(alone.text == words && alone.attachments.isEmpty)
        let given = Draft.givingBack(words, to: draft("Half a thought", attachments: [aFile]))
        #expect(given.text == "Half a thought\n\n" + words)
        #expect(given.attachments == [aFile])
    }

    // MARK: New Session's draft is its project's (#5376)

    private func summary(_ path: String, on host: MachineRef, project: String?) -> DaemonAPI.FolderSummary {
        var summary = DaemonAPI.FolderSummary(folder: Folder(folder: URL(filePath: path)), name: URL(filePath: path).lastPathComponent,
                                              exists: true, lastActivityAt: Date(), counts: [:])
        summary.host = host
        summary.project = project
        return summary
    }

    /// On ▾ to the project's folder on another machine keeps the words; a folder in no
    /// project keeps a draft of its own, under the key it had.
    @Test func newSessionsDraftIsTheProjectsOnEveryMachine() {
        let devbox = MachineRef(rawValue: "devbox")
        let here = summary("/src/Agents", on: .mac, project: "p1")
        let there = summary("/home/alex/Agents", on: devbox, project: "p1")
        let loose = summary("/src/www", on: .mac, project: nil)
        let folders = [here, there, loose]
        let (store, _) = store()
        store.save(draft("Fix the sidebar"), for: .newSession(in: here.key, folders: folders))

        #expect(DraftKey.newSession(in: here.key, folders: folders) == .newSession(in: there.key, folders: folders))
        #expect(store.draft(for: .newSession(in: there.key, folders: folders))?.text == "Fix the sidebar")
        #expect(DraftKey.newSession(in: loose.key, folders: folders) == .newAgent(folder: loose.key.folder))
        #expect(store.draft(for: .newSession(in: loose.key, folders: folders)) == nil)
    }

    /// A draft typed before #5376, under its folder, moves to the project's once.
    @Test func aFoldersOldDraftMovesToItsProject() {
        let here = summary("/src/Agents", on: .mac, project: "p1")
        let key = DraftKey.newSession(in: here.key, folders: [here])
        let (store, _) = store()
        store.save(draft("From before"), for: .newAgent(folder: here.key.folder))

        #expect(store.draft(for: key)?.text == "From before")
        #expect(store.draft(for: .newAgent(folder: here.key.folder)) == nil)
        #expect(store.draft(for: key)?.text == "From before")
    }

    /// The thirty-day sweep takes a project's draft as it takes a folder's.
    @Test func theSweepTakesAnOldProjectDraft() {
        let here = summary("/src/Agents", on: .mac, project: "p1")
        let key = DraftKey.newSession(in: here.key, folders: [here])
        let (store, _) = store()
        store.save(draft(at: Date(timeIntervalSinceNow: -DraftStore.horizon - 60)), for: key)
        store.sweep(archived: [], now: Date())
        #expect(store.draft(for: key) == nil)
    }
}
