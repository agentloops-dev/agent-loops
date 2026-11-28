import AgentsKitCore
@preconcurrency import AVFoundation
import CoreAudio
import Foundation

/// Where a stream's sound comes from (#7584). Each gives its buffers to a `MeetingFeed` on
/// its own realtime thread, from `open` until `close`. Audio is never kept.
@MainActor
protocol MeetingInput: AnyObject {
    /// Starts giving buffers to `feed`. Throws when the sound cannot be had.
    func open(into feed: MeetingFeed) throws
    func close()
    /// For the log: the route, no words.
    var route: String { get }
}

/// The microphone, with the system's voice processing on, so the call's sound from the
/// speakers is taken out (#7580). Its own engine: voice mode's is not touched.
@MainActor
final class MicrophoneInput: MeetingInput {
    private let engine = AVAudioEngine()
    /// Voice processing is on. Without it the clerk's gate drops the call heard twice.
    private(set) var cancelsEcho = false

    var route: String {
        let format = engine.inputNode.outputFormat(forBus: 0)
        return "mic: echo cancelled \(cancelsEcho), \(format.sampleRate) Hz × \(format.channelCount)"
    }

    func open(into feed: MeetingFeed) throws {
        let input = engine.inputNode
        do {
            try input.setVoiceProcessingEnabled(true)
            // Other sound stays as loud as it is: the call must still be heard.
            input.voiceProcessingOtherAudioDuckingConfiguration =
                AVAudioVoiceProcessingOtherAudioDuckingConfiguration(enableAdvancedDucking: false, duckingLevel: .min)
            cancelsEcho = true
        } catch {
            cancelsEcho = false
        }
        // Voice processing wants the output joined, at the microphone's rate.
        let rate = cancelsEcho ? input.outputFormat(forBus: 0).sampleRate : 0
        let joined = rate > 0 ? AVAudioFormat(standardFormatWithSampleRate: rate, channels: 1) : nil
        try engine.connectNode(engine.mainMixerNode, to: engine.outputNode, format: joined)
        let format = input.outputFormat(forBus: 0)
        guard format.sampleRate > 0, format.channelCount > 0 else { throw MeetingInputError.noMicrophone }
        let tap: @Sendable (AVAudioPCMBuffer, AVAudioTime) -> Void = { buffer, _ in feed.append(buffer) }
        input.removeTap(onBus: 0)
        try input.__installTap(onBus: 0, bufferSize: 1_024, format: format, error: (), block: tap)
        engine.prepare()
        do {
            try engine.start()
        } catch where cancelsEcho {
            // Voice processing can still refuse to start: then without it, and the gate.
            input.removeTap(onBus: 0)
            try? input.setVoiceProcessingEnabled(false)
            cancelsEcho = false
            try input.__installTap(onBus: 0, bufferSize: 1_024, format: input.outputFormat(forBus: 0), error: (), block: tap)
            engine.prepare()
            try engine.start()
        }
    }

    func close() {
        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
        if cancelsEcho { try? engine.inputNode.setVoiceProcessingEnabled(false) }
    }
}

/// The call app's sound, by a Core Audio process tap of every process with its bundle id
/// (its helpers too), not muted: the person still hears the call (#4945's probe, test 1).
/// macOS asks for System Audio Recording the first time.
@MainActor
final class CallAppInput: MeetingInput {
    let bundleID: String
    private var tap = AudioObjectID(kAudioObjectUnknown)
    private var device = AudioObjectID(kAudioObjectUnknown)
    private var proc: AudioDeviceIOProcID?
    private var format: AVAudioFormat?

    init(bundleID: String) {
        self.bundleID = bundleID
    }

    var route: String {
        "call \(bundleID): \(format.map { "\($0.sampleRate) Hz × \($0.channelCount)" } ?? "not open")"
    }

    func open(into feed: MeetingFeed) throws {
        let processes = CoreAudioProcesses.objects(bundleID: bundleID)
        guard !processes.isEmpty else { throw MeetingInputError.callAppSilent(bundleID) }
        let description = CATapDescription(stereoMixdownOfProcesses: processes)
        description.muteBehavior = .unmuted
        description.isPrivate = true
        description.name = "Agent Loops meeting"
        try check(AudioHardwareCreateProcessTap(description, &tap), "process tap")
        let aggregate: [String: Any] = [
            kAudioAggregateDeviceNameKey: "Agent Loops meeting",
            kAudioAggregateDeviceUIDKey: "dev.agentloops.meeting.\(UUID().uuidString)",
            kAudioAggregateDeviceIsPrivateKey: true,
            kAudioAggregateDeviceTapAutoStartKey: true,
            kAudioAggregateDeviceTapListKey: [[kAudioSubTapUIDKey: description.uuid.uuidString,
                                               kAudioSubTapDriftCompensationKey: true]],
        ]
        do {
            try check(AudioHardwareCreateAggregateDevice(aggregate as CFDictionary, &device), "aggregate device")
            var stream = try CoreAudioProcesses.tapFormat(tap)
            guard let format = AVAudioFormat(streamDescription: &stream) else { throw MeetingInputError.noFormat }
            self.format = format
            try check(AudioDeviceCreateIOProcIDWithBlock(&proc, device, nil, Self.reader(format, into: feed)), "io proc")
            try check(AudioDeviceStart(device, proc), "device start")
        } catch {
            close()
            throw error
        }
    }

    func close() {
        if let proc {
            AudioDeviceStop(device, proc)
            AudioDeviceDestroyIOProcID(device, proc)
        }
        proc = nil
        if device != kAudioObjectUnknown { AudioHardwareDestroyAggregateDevice(device) }
        device = AudioObjectID(kAudioObjectUnknown)
        if tap != kAudioObjectUnknown { AudioHardwareDestroyProcessTap(tap) }
        tap = AudioObjectID(kAudioObjectUnknown)
    }

    /// The IOProc, on Core Audio's realtime thread: made outside the main actor, so Swift
    /// does not check for it there and stop the app.
    private nonisolated static func reader(_ format: AVAudioFormat, into feed: MeetingFeed) -> AudioDeviceIOBlock {
        { _, input, _, _, _ in
            // No copy: the feed converts it before this returns.
            guard let buffer = AVAudioPCMBuffer(pcmFormat: format, bufferListNoCopy: input, deallocator: nil) else { return }
            feed.append(buffer)
        }
    }

    private func check(_ status: OSStatus, _ what: String) throws {
        guard status == noErr else { throw MeetingInputError.coreAudio(what, status) }
    }
}

/// The audio processes Core Audio knows, by bundle id.
enum CoreAudioProcesses {
    static func objects(bundleID: String) -> [AudioObjectID] {
        all().filter { object in
            let id = string(object, kAudioProcessPropertyBundleID)
            return id == bundleID || id.hasPrefix(bundleID + ".")
        }
    }

    static func tapFormat(_ tap: AudioObjectID) throws -> AudioStreamBasicDescription {
        var address = AudioObjectPropertyAddress(mSelector: kAudioTapPropertyFormat, mScope: kAudioObjectPropertyScopeGlobal,
                                                 mElement: kAudioObjectPropertyElementMain)
        var format = AudioStreamBasicDescription()
        var size = UInt32(MemoryLayout<AudioStreamBasicDescription>.size)
        let status = AudioObjectGetPropertyData(tap, &address, 0, nil, &size, &format)
        guard status == noErr else { throw MeetingInputError.coreAudio("tap format", status) }
        return format
    }

    private static func all() -> [AudioObjectID] {
        var address = AudioObjectPropertyAddress(mSelector: kAudioHardwarePropertyProcessObjectList,
                                                 mScope: kAudioObjectPropertyScopeGlobal, mElement: kAudioObjectPropertyElementMain)
        var size: UInt32 = 0
        let system = AudioObjectID(kAudioObjectSystemObject)
        guard AudioObjectGetPropertyDataSize(system, &address, 0, nil, &size) == noErr, size > 0 else { return [] }
        var ids = [AudioObjectID](repeating: 0, count: Int(size) / MemoryLayout<AudioObjectID>.size)
        guard AudioObjectGetPropertyData(system, &address, 0, nil, &size, &ids) == noErr else { return [] }
        return ids
    }

    private static func string(_ object: AudioObjectID, _ selector: AudioObjectPropertySelector) -> String {
        var address = AudioObjectPropertyAddress(mSelector: selector, mScope: kAudioObjectPropertyScopeGlobal,
                                                 mElement: kAudioObjectPropertyElementMain)
        var value: Unmanaged<CFString>?
        var size = UInt32(MemoryLayout<Unmanaged<CFString>?>.size)
        guard AudioObjectGetPropertyData(object, &address, 0, nil, &size, &value) == noErr,
              let value else { return "" }
        return value.takeRetainedValue() as String
    }
}

enum MeetingInputError: LocalizedError {
    case noMicrophone
    case noFormat
    case callAppSilent(String)
    case coreAudio(String, OSStatus)

    var errorDescription: String? {
        switch self {
        case .noMicrophone: "There is no microphone to listen to."
        case .noFormat: "The call app's sound has no format this Mac can read."
        case .callAppSilent(let id): "\(id) is not playing any sound yet."
        case .coreAudio(let what, let status): "Core Audio refused the \(what) (\(status))."
        }
    }
}

#if DEBUG
/// A walk's stream (#7584): a sound file played into the feed at its own pace, as if heard,
/// with no microphone, no call app and no permission. Made-up speech only.
@MainActor
final class FileInput: MeetingInput {
    let file: URL
    private var reader: Task<Void, Never>?

    init(file: URL) {
        self.file = file
    }

    var route: String { "file \(file.lastPathComponent)" }

    func open(into feed: MeetingFeed) throws {
        let audio = try AVAudioFile(forReading: file)
        let format = audio.processingFormat
        let chunk: AVAudioFrameCount = 1_024
        let handle = FileHandleBox(audio)
        reader = Task.detached(priority: .userInitiated) {
            let started = ContinuousClock.now
            var read: Double = 0
            while !Task.isCancelled {
                guard let buffer = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: chunk),
                      (try? handle.file.read(into: buffer, frameCount: chunk)) != nil, buffer.frameLength > 0 else { break }
                feed.append(buffer)
                read += Double(buffer.frameLength) / format.sampleRate
                // At the pace it was said, as a live stream comes.
                let due = started + .milliseconds(Int(read * 1_000))
                try? await Task.sleep(until: due, clock: .continuous)
            }
        }
    }

    func close() {
        reader?.cancel()
        reader = nil
    }
}

/// The file, handed to the reading task: only that task reads it.
private struct FileHandleBox: @unchecked Sendable {
    let file: AVAudioFile
    init(_ file: AVAudioFile) { self.file = file }
}
#endif
