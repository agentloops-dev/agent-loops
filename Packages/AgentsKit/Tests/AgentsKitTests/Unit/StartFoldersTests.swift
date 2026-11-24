import Foundation
import Testing
@testable import AgentsKitCore

@Suite("A new session's added folders, bounded (#6798)")
struct StartFoldersTests {
    private let box = MachineRef(rawValue: "box")

    @Test func oneFolderPastTheMostIsRefused() {
        var folders: [FolderKey] = []
        for n in 0..<StartFolders.most {
            #expect(StartFolders.add(FolderKey(host: box, folder: URL(filePath: "/f\(n)")), to: &folders) == nil)
        }
        #expect(StartFolders.add(FolderKey(folder: URL(filePath: "/over")), to: &folders) == StartFolders.fullWords)
        #expect(StartFolders.add(FolderKey(host: box, folder: URL(filePath: "/f0")), to: &folders) == nil)
        #expect(folders.count == StartFolders.most)
        folders.removeFirst()
        #expect(StartFolders.add(FolderKey(folder: URL(filePath: "/over")), to: &folders) == nil)
    }

    @Test func aPathThatIsTooLongIsRefused() {
        var folders: [FolderKey] = []
        let longest = "/" + String(repeating: "a", count: StartFolders.pathMost - 1)
        #expect(StartFolders.add(FolderKey(folder: URL(filePath: longest + "a")), to: &folders) == StartFolders.tooLongWords)
        #expect(StartFolders.add(FolderKey(folder: URL(filePath: longest)), to: &folders) == nil)
    }

    @Test func pathsKeptBeforeAreTheHomeMachinesAndAreBounded() {
        let store = MemoryDefaults()
        store.set(["/a b|c", "box|/d"] + (0..<200).map { "/f\($0)" }, forKey: StartFolders.key)
        let read = StartFolders.read(store)
        #expect(read.prefix(2) == [FolderKey(folder: URL(filePath: "/a b|c")), FolderKey(host: box, folder: URL(filePath: "/d"))])
        #expect(read.count == StartFolders.most)
        StartFolders.write(read, to: store)
        #expect(StartFolders.read(store) == read)
        StartFolders.write([], to: store)
        #expect(store.object(forKey: StartFolders.key) == nil)
    }

    @Test func foldersGoWithTheirMachine() {
        let mac = FolderKey(folder: URL(filePath: "/m"))
        let there = FolderKey(host: box, folder: URL(filePath: "/b"))
        let gone = FolderKey(host: MachineRef(rawValue: "gone"), folder: URL(filePath: "/g"))
        #expect(StartFolders.keep([mac, there, gone], listed: [box]) == [mac, there])
    }
}
