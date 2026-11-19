#if canImport(CryptoKit)
import Foundation

/// A host's link with each request checked end to end (#3492, `specs/3399-machine-lock` D6).
/// At each new transport it opens a channel with `lock/hello`, signed with this device's lock
/// key, and takes it only when the machine's answer is signed by a key its lock names. Then
/// every request carries the channel's MAC, and an answer is taken only with the machine's.
///
/// With no lock key, or a device not in its lock yet, it is the link as before. With no
/// channel (a machine with no lock, or one away), a prompt or a start goes signed, so the
/// control plane can keep it; and a machine the lock names gives no answer that is not MAC'd.
/// Once the channel is open, what the machine says unasked (`agent/changed`, a question card)
/// is taken only with its MAC too (#3541). A transport with no channel opens one again when
/// the machine refuses a request as not in its lock: the machine took its lock after the
/// transport opened (#3642), or the lock was made after it (#4134): with no lock key yet, the
/// transport is wrapped all the same, so that refusal opens one. A transport with a channel
/// opens one again when the machine refuses a request sealed on it: the machine's uplink went
/// and came back, so it forgot the channel while this transport stayed open (#4988). Such a
/// refusal does not reach the caller: the request goes again, once, on the new channel, and only
/// a second refusal is the caller's (#6227). While the new channel opens, a new request waits for
/// it, so none goes unsealed to the machine in the meantime.
/// The web page's is `Web/src/wire/link.ts`.
public struct LockedLink: DaemonLink {
    public typealias Source = @Sendable () async -> LockChannel.Signer?

    let inner: any DaemonLink
    /// The machine's id; "" for any machine of the lock (the window's `.mac`).
    let host: String
    let source: Source

    public init(_ inner: any DaemonLink, host: MachineRef, source: @escaping Source) {
        self.inner = inner
        self.host = host == .mac ? "" : host.rawValue
        self.source = source
    }

    public func start() async throws { try await inner.start() }
    public var startsSomething: Bool { inner.startsSomething }

    public func transport() async throws -> any LineTransport {
        let transport = try await inner.transport()
        let signer = await source()
        let locked = LockedTransport(inner: transport, signer: signer, host: host, source: source)
        if signer != nil { await locked.open(within: 15) }
        return locked
    }
}

/// What may be kept for a machine that is not there, and so goes signed with no channel.
let keptMethods: Set<String> = ["agents/start", "agents/prompt"]

final class LockedTransport: LineTransport, @unchecked Sendable {
    private let inner: any LineTransport
    private let host: String
    private let source: LockedLink.Source
    private let lock = NSLock()
    /// The lock key and lock of the last hello: a later one reads them again. None before the
    /// lock exists: the transport is the plain one until a refusal finds one (#4134).
    private var signer: LockChannel.Signer?
    private var required: Bool
    private var channel: LockChannel.Channel?
    /// A hello is on its way again, after a refusal (#3642).
    private var reopening = false
    /// The requests that went to the machine, by id, as the caller wrote them, until they are
    /// answered: one the machine refuses as not in its lock can go again (#6227).
    private var asked: [JSONRPCID: String] = [:]
    /// The ids of those that went sealed on a channel, with that channel's number, until they
    /// are answered.
    private var onChannel: [JSONRPCID: Int] = [:]
    /// The number of the channel as it is now: one more at each channel made.
    private var generation = 0
    /// The requests that went again after a refusal: a second refusal is the caller's.
    private var retried: Set<JSONRPCID> = []
    /// Refusals held while a new channel opens: the request, and the refusal that goes to the
    /// caller when no channel opens.
    private var held: [(request: String, refusal: String)] = []
    /// Requests written while a new channel opens: they go once it opens, or did not.
    private var waiting: [String] = []
    private var helloID = JSONRPCID.string("lock-hello-" + UUID().uuidString)
    /// The hello sent, so its answer makes the channel as it is read, before the next line.
    private var hello: LockChannel.Hello?
    private var helloWait: CheckedContinuation<JSONRPCMessage?, Never>?
    private let stream: AsyncThrowingStream<String, any Error>
    private let continuation: AsyncThrowingStream<String, any Error>.Continuation
    private var pump: Task<Void, Never>?

    init(inner: any LineTransport, signer: LockChannel.Signer?, host: String, source: @escaping LockedLink.Source) {
        self.inner = inner
        self.signer = signer
        self.host = host
        self.source = source
        required = signer.map { LockChannel.names($0.state, host: host) } ?? false
        var c: AsyncThrowingStream<String, any Error>.Continuation!
        stream = AsyncThrowingStream(bufferingPolicy: .unbounded) { c = $0 }
        continuation = c
        pump = Task.detached { [weak self, inner] in
            do {
                for try await line in inner.lines() { self?.heard(line) }
                self?.ended(nil)
            } catch {
                self?.ended(error)
            }
        }
    }

    /// `lock/hello`: the channel, when the machine answers as one its lock names in time.
    func open(within seconds: Double) async {
        let (key, id) = lock.withLock { (signer?.key, helloID) }
        guard let key, let hello = try? LockChannel.hello(key), let params = try? JSONValue.encoding(hello.params),
              let line = try? JSONRPCCodec.encode(.request(id: id, method: DaemonAPI.Method.lockHello, params: params)) else { return }
        let reply: JSONRPCMessage? = await withCheckedContinuation { wait in
            lock.withLock {
                helloWait = wait
                self.hello = hello
            }
            do {
                try inner.write(line: line)
            } catch {
                resumeHello(nil)
                return
            }
            Task { [weak self] in
                try? await Task.sleep(for: .seconds(seconds))
                self?.resumeHello(nil)
            }
        }
        if reply == nil || lock.withLock({ channel }) == nil {
            WireLog.write("lock: no checked channel to \(host.isEmpty ? "this Mac" : host)")
        }
    }

    /// The machine refused a request as not in its lock, and this transport has no channel: it
    /// took its lock after the transport opened. A hello again, with the lock as it is now, so
    /// the next request goes on a channel (#3642), as the web page's next call opens one.
    private func reopen() {
        let start = lock.withLock { () -> Bool in
            guard helloWait == nil, !reopening else { return false }
            channel = nil
            reopening = true
            return true
        }
        guard start else { return }
        Task { [weak self] in
            guard let self else { return }
            if let signer = await source() {
                lock.withLock {
                    self.signer = signer
                    required = LockChannel.names(signer.state, host: host)
                    helloID = .string("lock-hello-" + UUID().uuidString)
                }
                await open(within: 15)
            }
            reopened()
        }
    }

    /// The new channel opened, or did not. On a channel, each held request goes again, once,
    /// and the caller does not see its first refusal (#6227); with none, the caller gets it.
    /// The requests that waited go as `write` sends them.
    private func reopened() {
        let (held, later, open) = lock.withLock { () -> ([(request: String, refusal: String)], [String], Bool) in
            defer {
                reopening = false
                self.held = []
                waiting = []
            }
            return (self.held, waiting, channel != nil)
        }
        for (request, refusal) in held {
            if !open {
                continuation.yield(refusal)
                continue
            }
            resend(request, refusal: refusal)
        }
        for line in later {
            do {
                try write(line: line)
            } catch {
                WireLog.write("lock: a request to \(host.isEmpty ? "this Mac" : host) could not go: \(error.localizedDescription)")
            }
        }
    }

    /// A refused request again, on the channel as it is now; its refusal to the caller when it
    /// cannot go.
    private func resend(_ request: String, refusal: String) {
        guard case .request(let id, _, _)? = try? JSONRPCCodec.decode(line: request) else {
            continuation.yield(refusal)
            return
        }
        lock.withLock { _ = retried.insert(id) }
        do {
            try write(line: request)
        } catch {
            lock.withLock {
                _ = retried.remove(id)
                asked[id] = nil
                onChannel[id] = nil
            }
            continuation.yield(refusal)
        }
    }

    /// A refusal as not in the lock: whether it is held, to go again on a new channel (#6227).
    /// It is the caller's when its request went again once already. When the request went on
    /// a channel that is gone and a newer one is open, it goes again at once; else a hello goes
    /// again (#3642, #4988), and it waits for that.
    private func hold(_ refusal: String, request: String, id: JSONRPCID, sealedOn: Int?) -> Bool {
        enum Step { case pass(reopen: Bool), hold, now }
        let step = lock.withLock { () -> Step in
            let current = sealedOn == generation && channel != nil
            // The first hello is on its way: nothing would send the held request again.
            if helloWait != nil, !reopening { return .pass(reopen: false) }
            if retried.remove(id) != nil { return .pass(reopen: channel == nil || current) }
            if reopening {
                held.append((request, refusal))
                return .hold
            }
            if sealedOn != nil, !current, channel != nil { return .now }
            held.append((request, refusal))
            return .hold
        }
        switch step {
        case .pass(let again):
            if again { reopen() }
            return false
        case .now:
            resend(request, refusal: refusal)
            return true
        case .hold:
            reopen()
            return true
        }
    }

    /// The channel from lock/hello's answer, made as the answer is read: the machine MACs every
    /// line after it, a notification too (#3541).
    private func finish(_ reply: JSONRPCMessage) {
        lock.withLock {
            guard let hello, let signer, case .success(_, let result) = reply,
                  let answer = try? result.decode(DaemonAPI.LockHelloReply.self) else { return }
            channel = try? LockChannel.finish(hello, state: signer.state, host: host, reply: answer)
            if channel != nil { generation += 1 }
        }
    }

    private func resumeHello(_ message: JSONRPCMessage?) {
        let wait = lock.withLock { () -> CheckedContinuation<JSONRPCMessage?, Never>? in
            defer { helloWait = nil }
            return helloWait
        }
        wait?.resume(returning: message)
    }

    func write(line: String) throws {
        guard case .request(let id, let method, let params)? = try? JSONRPCCodec.decode(line: line),
              Capability.control[method] == nil else {
            // The control plane's own methods, on the Remote's one connection, and notifications.
            try inner.write(line: line)
            return
        }
        try lock.withLock {
            if reopening, channel == nil {
                // A new channel is opening (#6227): the request goes on it, not unsealed now.
                waiting.append(line)
                return
            }
            asked[id] = line
            if let channel {
                onChannel[id] = generation
                try channel.sealThen(line) { try inner.write(line: $0) }
                return
            }
            if !host.isEmpty, keptMethods.contains(method), let signer, let params,
               let text = try? LockChannel.text(params),
               let signature = try? LockChannel.sign(signer.key, to: host, method: method, params: text),
               let signed = try? LockChannel.text(signature), let idText = try? LockChannel.text(id),
               let methodText = try? LockChannel.text(method) {
                try inner.write(line: "{\"id\":\(idText),\"jsonrpc\":\"2.0\",\"method\":\(methodText),\"params\":\(text),\"lock\":\(signed)}")
                return
            }
            try inner.write(line: line)
        }
    }

    private func heard(_ line: String) {
        guard let message = try? JSONRPCCodec.decode(line: line) else {
            continuation.yield(line)
            return
        }
        if lock.withLock({ message.id == helloID && helloWait != nil }) {
            finish(message)
            resumeHello(message)
            return
        }
        if case .notification(let method, _) = message, Capability.control[method] == nil {
            if let line = notice(line, method) { continuation.yield(line) }
            return
        }
        guard let id = message.id, let request = lock.withLock({ asked.removeValue(forKey: id) }) else {
            continuation.yield(line)
            return
        }
        let sealedOn = lock.withLock { onChannel.removeValue(forKey: id) }
        switch message {
        case .success, .failure:
            if let answer = checked(line, message, id: id, request: request, sealedOn: sealedOn) { continuation.yield(answer) }
        default:
            continuation.yield(line)
        }
    }

    /// An answer from the machine: taken with its MAC; with none only when it is a refusal (the
    /// control plane's own, offline say), or from a machine with no channel the lock does not name.
    /// A refusal of a request sealed on the channel says the machine no longer has it (#4988):
    /// it is held, and the request goes again on a new channel (#6227); nil then.
    private func checked(_ line: String, _ message: JSONRPCMessage, id: JSONRPCID, request: String, sealedOn: Int?) -> String? {
        let channel = lock.withLock { self.channel }
        if LockChannel.sealed(line) {
            lock.withLock { _ = retried.remove(id) }
            if let channel, let body = try? channel.open(line) { return body }
            return refusal(id, "The machine's answer does not check, so it was not taken.")
        }
        if case .success = message, channel != nil || lock.withLock({ required }) {
            lock.withLock { _ = retried.remove(id) }
            return refusal(id, "The machine's answer was not signed, so it was not taken.")
        }
        if case .failure(_, let error) = message, error.code == DaemonAPI.Failure.notInLock {
            return hold(line, request: request, id: id, sealedOn: sealedOn) ? nil : line
        }
        lock.withLock { _ = retried.remove(id) }
        return line
    }

    /// What the machine says unasked: with no channel, as it came; with one, only with its MAC,
    /// so the control plane cannot show a card the machine did not raise (#3541). The control
    /// plane's own notifications, on the Remote's one connection, are not the machine's.
    private func notice(_ line: String, _ method: String) -> String? {
        guard let channel = lock.withLock({ self.channel }) else { return line }
        if LockChannel.sealed(line), let body = try? channel.open(line) { return body }
        WireLog.write("lock: \(method) from \(host.isEmpty ? "this Mac" : host) does not check, so it was not taken")
        return nil
    }

    private func refusal(_ id: JSONRPCID, _ words: String) -> String {
        (try? JSONRPCCodec.encode(.failure(id: id, error: JSONRPCError(code: DaemonAPI.Failure.notInLock, message: words)))) ?? ""
    }

    private func ended(_ error: (any Error)?) {
        resumeHello(nil)
        continuation.finish(throwing: error)
    }

    func lines() -> AsyncThrowingStream<String, any Error> { stream }

    func close() {
        pump?.cancel()
        inner.close()
        resumeHello(nil)
        continuation.finish()
    }
}
#endif
