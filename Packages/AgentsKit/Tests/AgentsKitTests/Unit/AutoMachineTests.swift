import Foundation
import Testing
@testable import AgentsKitCore

/// New Session's Auto machine row (#5375, specs/5373-auto-machine): the same rules as the web
/// page's `Web/src/model/autoMachine.ts` (`Web/test/autoMachine.test.mjs`).
struct AutoMachineTests {
    typealias Pick = DaemonAPI.AutoMachinePick

    let here = FolderKey(host: .mac, folder: URL(filePath: "/Users/me/app"))
    let mini = FolderKey(host: MachineRef(rawValue: "mini"), folder: URL(filePath: "/Users/me/src/app"))
    let picked = Pick(machine: MachineRef(rawValue: "mini"), machineName: "Mac mini", tier: .ok, reason: .leastLoaded,
                      text: "Auto chose Mac mini: least busy, load 0.4 per core.")
    let none = Pick(text: "Every machine with this project is offline: devbox.", none: .offline, offline: ["devbox"])

    @Test func aPickIsOfferedUsableAndChosenAtEveryStart() {
        #expect(AutoMachine.offered(picked))
        #expect(AutoMachine.usable(picked))
        #expect(AutoMachine.chosen(here, named: nil, pick: picked))
        #expect(AutoMachine.rowWords(picked) == "Auto · Mac mini")
        #expect(AutoMachine.offWords(picked) == nil)
    }

    @Test func aMachineNamedByHandIsChosenOnlyForItsOwnForm() {
        #expect(!AutoMachine.chosen(here, named: here, pick: picked))
        // Named for one folder, so the form on any other is Auto again.
        #expect(AutoMachine.chosen(mini, named: here, pick: picked))
    }

    @Test func oneMachineHasNoRow() {
        let single = Pick(machine: .mac, machineName: "This Mac", single: true)
        #expect(!AutoMachine.offered(single))
        #expect(!AutoMachine.usable(single))
        #expect(!AutoMachine.chosen(here, named: nil, pick: single))
        #expect(!AutoMachine.offered(nil))
    }

    @Test func noMachineFitsGreysTheRowWithTheControlPlanesWords() {
        #expect(AutoMachine.offered(none))
        #expect(!AutoMachine.usable(none))
        #expect(!AutoMachine.chosen(here, named: nil, pick: none))
        #expect(AutoMachine.rowWords(none) == "Auto")
        #expect(AutoMachine.offWords(none) == "Every machine with this project is offline: devbox.")
    }

    @Test func aStartGoesToThePickedMachinesFolderOfTheProject() {
        let target = AutoMachine.target(from: here, project: [here, mini], pick: picked)
        #expect(target == AutoMachine.Target(folder: mini, moved: true,
                                             autoChose: "Auto chose Mac mini: least busy, load 0.4 per core."))
    }

    @Test func aPickOfTheFormsOwnMachineStaysWithTheLine() {
        var own = picked
        own.machine = .mac
        let target = AutoMachine.target(from: here, project: [here, mini], pick: own)
        #expect(target == AutoMachine.Target(folder: here, moved: false, autoChose: own.text))
    }

    @Test func thisMacByTheControlPlanesIdIsTheFormsOwnMachine() {
        var own = picked
        own.machine = MachineRef(rawValue: "abcd1234")
        let isThisMac: (MachineRef) -> Bool = { $0 == .mac || $0.rawValue == "abcd1234" }
        let target = AutoMachine.target(from: here, project: [here, mini], pick: own) { a, b in
            a == b || (isThisMac(a) && isThisMac(b))
        }
        #expect(target == AutoMachine.Target(folder: here, moved: false, autoChose: own.text))
    }

    @Test func aFolderWithThePickedIdWinsOverThisMacsOtherName() {
        // Two hosts on one Mac (a walk): `same` says both are this Mac, but a folder has the id.
        let target = AutoMachine.target(from: here, project: [here, mini], pick: picked) { _, _ in true }
        #expect(target?.folder == mini)
        #expect(target?.moved == true)
    }

    @Test func aPickWithNoFolderOfTheProjectOrNoneLeavesTheStartWhereItIs() {
        #expect(AutoMachine.target(from: here, project: [here], pick: picked) == nil)
        #expect(AutoMachine.target(from: here, project: [here, mini], pick: none) == nil)
        #expect(AutoMachine.target(from: here, project: [here, mini], pick: nil) == nil)
    }

    @Test func theStartCarriesTheLine() throws {
        let request = DaemonAPI.StartRequest(runtimeID: "claude", cwd: mini.folder, prompt: "hi", autoChose: picked.text)
        let back = try JSONDecoder().decode(DaemonAPI.StartRequest.self, from: JSONEncoder().encode(request))
        #expect(back.autoChose == picked.text)
    }
}
