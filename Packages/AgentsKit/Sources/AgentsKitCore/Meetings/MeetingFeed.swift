#if canImport(AVFoundation)
@preconcurrency import AVFoundation

/// One stream's audio (#7584), from the realtime thread that gives it to the stream's
/// transcriber and its diarizer, each in its own format. Audio is never kept: each buffer is
/// converted, handed on and let go.
///
/// The fed clock is the seconds of audio handed on since the stream opened. The transcriber
/// is given each buffer's start on it, and the diarizer each chunk's, so both put the same
/// words at the same time; `MeetingAudioClock` makes it the meeting's time.
///
/// A converter and the consumers' closures are not things Swift lets a realtime closure hold,
/// and using them on that thread is what they are for, so the promise is made here, once.
public final class MeetingFeed: @unchecked Sendable {
    /// What the diarizer takes: 16 kHz mono, as LS-EEND wants.
    public static let diarizerFormat = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 16_000,
                                                     channels: 1, interleaved: false)!

    public typealias ToTranscriber = @Sendable (_ buffer: AVAudioPCMBuffer, _ fedStart: TimeInterval) -> Void
    public typealias ToDiarizer = @Sendable (_ samples: [Float], _ fedStart: TimeInterval) -> Void

    private let lock = NSLock()
    private var transcriberFormat: AVAudioFormat?
    private var toTranscriber: ToTranscriber?
    private var toDiarizer: ToDiarizer?
    private var transcriberConverter: AVAudioConverter?
    private var diarizerConverter: AVAudioConverter?
    private var fed: TimeInterval = 0
    private var level: Float = 0
    private var isOpen = true

    public init() {}

    /// The transcriber the audio goes to, in its format; nil for none.
    public func transcriber(_ format: AVAudioFormat?, _ to: ToTranscriber?) {
        lock.withLock {
            transcriberFormat = format
            toTranscriber = to
            transcriberConverter = nil
        }
    }

    /// The diarizer the audio goes to; nil for none.
    public func diarizer(_ to: ToDiarizer?) {
        lock.withLock {
            toDiarizer = to
            diarizerConverter = nil
        }
    }

    /// The seconds of audio handed on so far.
    public var fedSeconds: TimeInterval { lock.withLock { fed } }

    /// The loudest sample since the last read, 0 to 1, and back to 0: for the capsule's level.
    public func takeLevel() -> Float {
        lock.withLock {
            defer { level = 0 }
            return level
        }
    }

    /// Nothing more is handed on; a buffer still on its way is let go.
    public func close() {
        lock.withLock {
            isOpen = false
            toTranscriber = nil
            toDiarizer = nil
        }
    }

    /// A buffer from the stream, in any format. Called on the realtime thread.
    public func append(_ buffer: AVAudioPCMBuffer) {
        lock.lock()
        defer { lock.unlock() }
        guard isOpen, buffer.frameLength > 0, buffer.format.sampleRate > 0 else { return }
        let start = fed
        fed += Double(buffer.frameLength) / buffer.format.sampleRate
        level = max(level, Self.peak(of: buffer))
        if let toTranscriber, let transcriberFormat {
            if buffer.format == transcriberFormat {
                toTranscriber(buffer, start)
            } else {
                if transcriberConverter?.inputFormat != buffer.format {
                    transcriberConverter = DictationAudio.converter(from: buffer.format, to: transcriberFormat)
                }
                if let transcriberConverter, let converted = DictationAudio.convert(buffer, with: transcriberConverter) {
                    toTranscriber(converted, start)
                }
            }
        }
        if let toDiarizer {
            if diarizerConverter?.inputFormat != buffer.format {
                diarizerConverter = DictationAudio.converter(from: buffer.format, to: Self.diarizerFormat)
                diarizerConverter?.primeMethod = .none
            }
            if let diarizerConverter, let converted = DictationAudio.convert(buffer, with: diarizerConverter),
               let channel = converted.floatChannelData {
                toDiarizer(Array(UnsafeBufferPointer(start: channel[0], count: Int(converted.frameLength))), start)
            }
        }
    }

    private static func peak(of buffer: AVAudioPCMBuffer) -> Float {
        guard let channel = buffer.floatChannelData else { return 0 }
        var peak: Float = 0
        for index in 0..<Int(buffer.frameLength) { peak = max(peak, abs(channel[0][index])) }
        return min(peak, 1)
    }
}
#endif
