import Foundation

/// The folders that a new session on the Remote reaches beyond its own, until each is removed.
/// Kept in defaults as `host|path`, since a path means nothing on another machine.
///
/// Bounded (#6798): `most` folders, each with a path of `pathMost` bytes and a machine id of
/// `MachinePins.idMost`; one past that is refused, as a folder is the person's choice. No age, on
/// purpose: a folder stays until it is removed, until the control plane no longer lists its
/// machine (`keep`), or until Forget.
public enum StartFolders {
    public static let key = "remote.startFolders"
    public static let most = 100
    /// The longest path macOS opens (`PATH_MAX`).
    public static let pathMost = 1024

    /// Said when a folder is refused.
    public static let fullWords = "At most \(most) folders can be added, on all machines together. Remove one first."
    public static let tooLongWords = "This path is too long. A path has at most \(pathMost) bytes."

    /// The folders kept in `store`. They were once paths alone, of no named machine: those are the home machine's.
    public static func read(_ store: UserDefaults) -> [FolderKey] {
        let kept = (store.stringArray(forKey: key) ?? []).compactMap { text in
            text.hasPrefix("/") ? FolderKey(folder: URL(filePath: text)) : FolderKey(stored: text)
        }
        return Array(kept.filter { refusal($0) == nil }.prefix(most))
    }

    public static func write(_ folders: [FolderKey], to store: UserDefaults) {
        guard !folders.isEmpty else { return store.removeObject(forKey: key) }
        // store-bounds: kind=key-value; count=100; age=until-deleted; action=refuse; record=1089B; applied=on write, when the machines are listed, and on Forget
        store.putEntry(folders.map(\.stored), forKey: key, most: most * (pathMost + MachinePins.idMost + 1))
    }

    /// Adds a folder; one there already changes nothing. Returns what to say when it is refused.
    public static func add(_ folder: FolderKey, to folders: inout [FolderKey]) -> String? {
        guard !folders.contains(folder) else { return nil }
        if let refused = refusal(folder) { return refused }
        guard folders.count < most else { return fullWords }
        folders.append(folder)
        return nil
    }

    /// `folders` less those of a machine the control plane does not list. An offline machine is
    /// listed, so its folders stay; so do the home machine's, which goes by a name of its own.
    public static func keep(_ folders: [FolderKey], listed: some Sequence<MachineRef>) -> [FolderKey] {
        let there = Set(listed).union([.mac])
        return folders.filter { there.contains($0.host) }
    }

    private static func refusal(_ folder: FolderKey) -> String? {
        let fits = folder.folder.path(percentEncoded: false).utf8.count <= pathMost
            && folder.host.rawValue.utf8.count <= MachinePins.idMost
        return fits ? nil : tooLongWords
    }
}
