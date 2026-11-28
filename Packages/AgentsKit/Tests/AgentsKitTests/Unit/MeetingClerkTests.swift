import Foundation
import Testing
@testable import AgentsKitCore

/// The listening Mac's segments (#7584): seqs, speakers per stream (#7580) and the gate.
@Suite("Meeting clerk")
struct MeetingClerkTests {
    static let mic = DaemonAPI.MeetingSource.mic
    static let call = DaemonAPI.MeetingSource.call("us.zoom.xos")

    static func words(_ source: String, _ start: Double, _ end: Double, _ text: String) -> HeardWords {
        HeardWords(source: source, start: start, end: end, text: text)
    }

    static func turn(_ slot: Int, _ start: Double, _ end: Double) -> DiarizerNews {
        .turn(SpeakerTurn(speakerIndex: slot, start: start, end: end))
    }

    @Test func eachFinalPieceIsTheNextSeqAndSettlesWhenItsDiarizerIsPastIt() {
        var clerk = MeetingClerk(options: .init(diarized: [Self.mic, Self.call], micCancelsEcho: true), lastSeq: 4)
        let first = clerk.heard(Self.words(Self.mic, 1, 3, "Shall we start?"))
        #expect(first == [DaemonAPI.MeetingSegment(seq: 5, source: Self.mic, audioStart: 1, audioEnd: 3,
                                                   text: "Shall we start?", speakerSettled: false)])
        let second = clerk.heard(Self.words(Self.call, 3.5, 5, "Yes, go ahead."))
        #expect(second.map(\.seq) == [6])
        // The mic's diarizer reaches 3 s: seq 5 again, with its voice, settled.
        let settled = clerk.diarizer(Self.turn(1, 0.8, 3.1), of: Self.mic)
        #expect(settled == [DaemonAPI.MeetingSegment(seq: 5, source: Self.mic, audioStart: 1, audioEnd: 3,
                                                     text: "Shall we start?", speakerIndex: 1, speakerSettled: true)])
        // The call's slot 0 is not the mic's slot 0: the source tells them apart.
        let call = clerk.diarizer(Self.turn(0, 3.4, 5.2), of: Self.call)
        #expect(call.map(\.speakerIndex) == [0])
        #expect(call.map(\.source) == [Self.call])
        #expect(clerk.unsettled.isEmpty)
    }

    @Test func aPieceHeardAfterItsTurnIsSettledAtOnce() {
        var clerk = MeetingClerk(options: .init(diarized: [Self.mic], micCancelsEcho: true))
        #expect(clerk.diarizer(Self.turn(2, 0, 4), of: Self.mic).isEmpty)
        let out = clerk.heard(Self.words(Self.mic, 0.5, 3.5, "Late words."))
        #expect(out.first?.speakerIndex == 2)
        #expect(out.first?.speakerSettled == true)
    }

    @Test func silenceSettlesAPieceWithNoVoice() {
        var clerk = MeetingClerk(options: .init(diarized: [Self.mic], micCancelsEcho: true))
        _ = clerk.heard(Self.words(Self.mic, 1, 2, "Hm."))
        let out = clerk.diarizer(.through(2.5), of: Self.mic)
        #expect(out.count == 1)
        #expect(out.first?.speakerIndex == nil)
        #expect(out.first?.speakerSettled == true)
    }

    @Test func aStreamWithNoDiarizerIsSettledWithNoSpeaker() {
        var clerk = MeetingClerk(options: .init(diarized: [], micCancelsEcho: true))
        let out = clerk.heard(Self.words(Self.mic, 0, 1, "No model."))
        #expect(out.first?.speakerSettled == true)
        #expect(out.first?.speakerIndex == nil)
        #expect(clerk.unsettled.isEmpty)
    }

    @Test func withoutVoiceProcessingTheMicsCopyOfTheCallIsDropped() {
        var clerk = MeetingClerk(options: .init(diarized: [Self.mic, Self.call], micCancelsEcho: false))
        // The call says it; the speakers play it and the mic hears it too, 60 ms late.
        #expect(clerk.heard(Self.words(Self.call, 10, 12, "The release is on Friday.")).map(\.seq) == [1])
        #expect(clerk.heard(Self.words(Self.mic, 10.06, 12.06, "The release is on Friday.")).isEmpty)
        // Someone in the room, while the call is quiet.
        #expect(clerk.heard(Self.words(Self.mic, 13, 14, "Friday works.")).isEmpty)
        #expect(clerk.gated.count == 2)
        // The call's diarizer passes both: the copy goes, the room's words get seq 2.
        var out = clerk.diarizer(Self.turn(0, 9.9, 12.1), of: Self.call)
        out += clerk.diarizer(.through(14.5), of: Self.call)
        let new = out.filter { $0.source == Self.mic }
        #expect(new.map(\.seq) == [2])
        #expect(new.map(\.text) == ["Friday works."])
        #expect(clerk.dropped == 1)
    }

    @Test func withVoiceProcessingTheMicIsNotGated() {
        var clerk = MeetingClerk(options: .init(diarized: [Self.mic, Self.call], micCancelsEcho: true))
        _ = clerk.diarizer(Self.turn(0, 9.9, 12.1), of: Self.call)
        #expect(clerk.heard(Self.words(Self.mic, 10, 12, "Over the call.")).map(\.seq) == [1])
    }

    @Test func finishSendsWhatIsHeldAndSettlesTheRest() {
        var clerk = MeetingClerk(options: .init(diarized: [Self.mic, Self.call], micCancelsEcho: false))
        _ = clerk.heard(Self.words(Self.call, 0, 2, "Bye then."))
        _ = clerk.diarizer(Self.turn(1, 0, 1), of: Self.call)
        _ = clerk.heard(Self.words(Self.mic, 3, 4, "Bye."))
        let out = clerk.finish()
        #expect(out.map(\.seq).sorted() == [1, 2])
        #expect(out.allSatisfy { $0.speakerSettled })
        #expect(out.first { $0.seq == 1 }?.speakerIndex == 1)
        #expect(clerk.unsettled.isEmpty)
        #expect(clerk.gated.isEmpty)
    }

    @Test func longWordsAreCutAtASpaceWithTheirShareOfTheTime() throws {
        let long = String(repeating: "word ", count: 500)
        let pieces = MeetingClerk.split(Self.words(Self.mic, 0, 100, long))
        try #require(pieces.count == 2)
        #expect(pieces.allSatisfy { $0.text.count <= DaemonAPI.MeetingSegment.textLimit })
        #expect(pieces[0].start == 0)
        #expect(abs(pieces[1].end - 100) < 0.001)
        #expect(pieces[0].end == pieces[1].start)
        #expect(MeetingClerk.split(Self.words(Self.mic, 0, 1, "  \n")).isEmpty)
    }

    @Test func theTimelineGivesTheVoiceOfMostOfAStretch() {
        var timeline = SpeakerTimeline()
        timeline.add(SpeakerTurn(speakerIndex: 0, start: 0, end: 2))
        timeline.add(SpeakerTurn(speakerIndex: 1, start: 1.5, end: 5))
        #expect(timeline.speaker(from: 0, to: 1.8) == 0)
        #expect(timeline.speaker(from: 1.6, to: 4) == 1)
        // A cough at the edge does not claim it.
        #expect(timeline.speaker(from: 4.9, to: 7) == nil)
        #expect(timeline.speech(from: 0, to: 6) == 5)
        // A slot above the most is dropped.
        timeline.add(SpeakerTurn(speakerIndex: 9, start: 6, end: 8))
        #expect(timeline.speaker(from: 6, to: 8) == nil)
    }

    @Test func theClockGoesOnThroughAPause() {
        var clock = MeetingAudioClock()
        clock.startRun(fed: 0, meeting: 2)
        #expect(clock.meetingTime(10) == 12)
        // Paused after 30 s of audio, resumed 100 s into the meeting.
        clock.startRun(fed: 30, meeting: 100)
        #expect(clock.meetingTime(29) == 31)
        #expect(clock.meetingTime(35) == 105)
    }
}
