#if DEBUG
import AgentsKitCore
import Foundation

/// The walk socket's `meeting` command (#7584): a meeting heard from sound files in place of
/// the microphone and the call app, so a scratch app needs no permission and plays nothing.
/// Made-up speech only (the repo is public).
///
///     {"cmd": "meeting", "start": {"folder": "/tmp/…", "sources": ["mic", "call:us.zoom.xos"]},
///      "files": {"mic": "/tmp/…/mic.wav", "call:us.zoom.xos": "/tmp/…/call.wav"}}
///     {"cmd": "meeting"}                     what it is doing
///     {"cmd": "meeting", "pause": true}      also "resume", "stop"
@MainActor
enum WalkMeeting {
    /// This Mac's host, from the app model.
    static var client: (() -> DaemonClient?)?
    private static var listener: MeetingListener?
    private static var started: Task<Void, Never>?

    static func perform(_ request: [String: Any]) -> [String: Any] {
        if let start = request["start"] as? [String: Any] {
            guard let client = client?() ?? nil, let folder = start["folder"] as? String,
                  let sources = start["sources"] as? [String] else {
                return ["ok": false, "error": "want start: {folder, sources}"]
            }
            let files = (request["files"] as? [String: String] ?? [:]).mapValues { URL(filePath: $0) }
            MeetingTones.walked = []
            let listener = MeetingListener(client: client)
            listener.walkFiles = files
            self.listener = listener
            started = Task {
                do {
                    let made = try await client.call(
                        DaemonAPI.Method.meetingsStart,
                        DaemonAPI.MeetingStartRequest(folder: URL(filePath: folder, directoryHint: .isDirectory),
                                                      title: start["title"] as? String, sources: sources),
                        returning: DaemonAPI.MeetingStarted.self)
                    await listener.listen(to: made.meeting)
                } catch {
                    note("walk: meetings/start refused: \(error)")
                }
            }
        }
        if let listener {
            if request["pause"] as? Bool == true { Task { await listener.pause() } }
            if request["resume"] as? Bool == true { Task { await listener.resume() } }
            if request["stop"] as? Bool == true { Task { await listener.stop() } }
        }
        return [
            "ok": true,
            "phase": listener?.phase.rawValue ?? "none",
            "meetingID": listener?.meeting?.id.uuidString ?? "",
            "lastSeq": listener?.lastSeq ?? 0,
            "sent": listener?.sentCount ?? 0,
            "volatile": listener?.volatile ?? [:],
            "problem": listener?.problem ?? "",
            "tones": MeetingTones.walked ?? [],
        ]
    }
}
#endif
