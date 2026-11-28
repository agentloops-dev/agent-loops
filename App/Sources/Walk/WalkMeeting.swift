#if DEBUG
import AgentsKitCore
import Foundation
import SwiftUI

/// The walk socket's `meeting` command (#7584): a meeting heard from sound files in place of
/// the microphone and the call app, so a scratch app needs no permission and plays nothing.
/// Made-up speech only (the repo is public).
///
///     {"cmd": "meeting", "start": {"folder": "/tmp/…", "sources": ["mic", "call:us.zoom.xos"]},
///      "files": {"mic": "/tmp/…/mic.wav", "call:us.zoom.xos": "/tmp/…/call.wav"}}
///     {"cmd": "meeting"}                     what it is doing
///     {"cmd": "meeting", "pause": true}      also "resume", "stop"
///
/// The screens' states (#7585), with no listener and no sound: a meeting scripted line by line.
///
///     {"cmd": "meeting", "script": {"folder": "/tmp/…", "title": "…", "sources": ["mic", "call:us.zoom.xos"]}}
///     {"cmd": "meeting", "meetingID": "…", "append": [{"source": "mic", "speaker": 0, "at": 2, "text": "…"}]}
///     {"cmd": "meeting", "meetingID": "…", "capsule": "listening", "live": "…", "level": 0.6}
///     {"cmd": "meeting", "meetingID": "…", "state": "paused"}           also "ended"
///     {"cmd": "meeting", "meetingID": "…", "speaker": {"source": "…", "index": 1, "name": "…"}}
///     {"cmd": "meeting", "newMeeting": true, "calendar": {"title": "…", "attendees": ["…"]},
///      "callApps": [["us.zoom.xos", "Zoom"]]}                         New Meeting's sheet
@MainActor
enum WalkMeeting {
    /// This Mac's host, from the app model.
    static var client: (() -> DaemonClient?)?
    /// The window's meetings, from the app model (#7585).
    static weak var desk: MeetingDesk?
    /// The main window's requests, for New Meeting's sheet.
    static weak var requests: WindowRequests?
    /// The next seq of each scripted meeting.
    private static var nextSeq: [UUID: Int64] = [:]
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
        if let scripted = scripted(request) { return scripted }
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

    /// The scripted verbs (#7585); nil when the request has none of them.
    private static func scripted(_ request: [String: Any]) -> [String: Any]? {
        if request["newMeeting"] as? Bool == true {
            if let event = request["calendar"] as? [String: Any] {
                let start = Date().addingTimeInterval(-5 * 60)
                MeetingCalendar.walked = .init(title: event["title"] as? String ?? "", start: start,
                                               end: start.addingTimeInterval(3600),
                                               attendees: event["attendees"] as? [String] ?? [])
            }
            if let apps = request["callApps"] as? [[String]] {
                MeetingDesk.walkCallApps = apps.compactMap { $0.count == 2 ? ($0[0], $0[1]) : nil }
            }
            requests?.newMeeting = true
            return ["ok": true]
        }
        if request["last"] as? Bool == true { return last }
        guard let client = client?() ?? nil, let desk else { return nil }
        if let script = request["script"] as? [String: Any] {
            guard let folder = script["folder"] as? String, let sources = script["sources"] as? [String] else {
                return ["ok": false, "error": "want script: {folder, sources}"]
            }
            let start = DaemonAPI.MeetingStartRequest(folder: URL(filePath: folder, directoryHint: .isDirectory),
                                                      title: script["title"] as? String, sources: sources,
                                                      runtimeID: script["runtimeID"] as? String)
            return later {
                let started = try await client.call(DaemonAPI.Method.meetingsStart, start,
                                                    returning: DaemonAPI.MeetingStarted.self)
                desk.walkKeep(started.meeting)
                nextSeq[started.meeting.id] = 1
                return ["meetingID": started.meeting.id.uuidString, "agentID": started.agentID.uuidString]
            }
        }
        guard let idText = request["meetingID"] as? String, let id = UUID(uuidString: idText) else { return nil }
        if let lines = request["append"] as? [[String: Any]] {
            var seq = nextSeq[id] ?? 1
            let segments = lines.map { line -> DaemonAPI.MeetingSegment in
                let at = (line["at"] as? Double) ?? Double(line["at"] as? Int ?? 0)
                defer { seq += 1 }
                return DaemonAPI.MeetingSegment(seq: seq, source: line["source"] as? String ?? DaemonAPI.MeetingSource.mic,
                                                audioStart: at, audioEnd: at + max(1, (line["for"] as? Double) ?? 2),
                                                text: line["text"] as? String ?? "", speakerIndex: line["speaker"] as? Int,
                                                speakerSettled: true)
            }
            nextSeq[id] = seq
            return later {
                let kept = try await client.call(DaemonAPI.Method.meetingsAppend,
                                                 DaemonAPI.MeetingAppendRequest(meetingID: id, segments: segments),
                                                 returning: DaemonAPI.MeetingAppended.self)
                return ["lastSeq": kept.lastSeq, "sentThroughSeq": kept.sentThroughSeq]
            }
        }
        if let state = (request["state"] as? String).flatMap(DaemonAPI.MeetingState.init(rawValue:)) {
            return later {
                desk.walkKeep(try await client.call(DaemonAPI.Method.meetingsSetState,
                                                    DaemonAPI.MeetingSetStateRequest(meetingID: id, state: state),
                                                    returning: DaemonAPI.Meeting.self))
                return [:]
            }
        }
        if let speaker = request["speaker"] as? [String: Any], let source = speaker["source"] as? String,
           let index = speaker["index"] as? Int, let meeting = desk.meeting(id: id) {
            return later {
                await desk.setSpeaker(meeting, source: source, index: index, name: speaker["name"] as? String ?? "")
                return [:]
            }
        }
        if let capsule = request["capsule"] as? String {
            let states: [String: MeetingLines.CapsuleState] = [
                "not-listening": .notListening, "starting": .starting, "listening": .listening,
                "paused": .paused, "ended": .ended,
            ]
            desk.walkCapsule[id] = states[capsule]
            if let live = request["live"] as? String {
                desk.walkLive[id] = live.isEmpty ? nil : (request["liveSource"] as? String ?? DaemonAPI.MeetingSource.mic, live)
            }
            desk.walkMeter((request["level"] as? Double).map(Float.init))
            return ["ok": true]
        }
        return nil
    }

    /// What the last scripted call gave: `{"cmd": "meeting", "last": true}` reads it, as the
    /// walk socket's handler is not async.
    private static var last: [String: Any] = [:]

    private static func later(_ call: @escaping @MainActor () async throws -> [String: Any]) -> [String: Any] {
        last = ["ok": false, "pending": true]
        Task { @MainActor in
            do {
                last = try await call().merging(["ok": true]) { $1 }
            } catch {
                last = ["ok": false, "error": "\(error)"]
            }
        }
        return ["ok": true, "pending": true]
    }
}

extension View {
    /// The main window's requests, for the walk's New Meeting (#7585).
    func walkSeesRequests(_ requests: WindowRequests) -> some View {
        onAppear { WalkMeeting.requests = requests }
    }
}
#endif
