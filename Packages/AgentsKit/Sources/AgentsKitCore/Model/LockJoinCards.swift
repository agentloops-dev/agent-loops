import Foundation

/// A signer's join requests, as the window's and the Remote's `LockState` keep them (#3490): the
/// open requests from `lock/joinRequests` and `control/lockJoinRequested`, and the nonce this
/// device sent for each one. The web page's is `rows` and `sent` in `Web/src/model/machineLock.ts`.
///
/// The cards outlive a lost connection and a read that fails (#6625): the control plane still
/// holds each request, and the nonce this device sent is still the one its digits need. Only a
/// read that answered, or the request's end, takes a card away. A notice heard while a read is
/// on its way is newer than that read, so the read does not undo it.
public struct LockJoinCards: Sendable, Equatable {
    public private(set) var requests: [DaemonAPI.LockJoinRequest] = []
    /// The nonce this device sent, by request: the digits use it, never the one sent back.
    private var nonces: [String: Data] = [:]
    /// While a read is on its way: each request heard since (true) or ended since (false).
    private var since: [String: Bool]?

    public init() {}

    /// A read of `lock/joinRequests` starts.
    public mutating func reading() { since = [:] }

    /// The read's answer; nil when it failed, and then the cards stay as they are.
    public mutating func read(_ listed: [DaemonAPI.LockJoinRequest]?) {
        let since = since ?? [:]
        self.since = nil
        guard let listed else { return }
        let heard = requests.filter { since[$0.request] == true }
        requests = listed.filter { since[$0.request] == nil } + heard
        let open = Set(requests.map(\.request))
        nonces = nonces.filter { open.contains($0.key) }
    }

    /// A request told by `control/lockJoinRequested`: new, or a newer copy of one kept.
    public mutating func heard(_ request: DaemonAPI.LockJoinRequest) {
        since?[request.request] = true
        if let index = requests.firstIndex(where: { $0.request == request.request }) {
            requests[index] = request
        } else {
            requests.append(request)
        }
    }

    /// A request decided, or denied from here: its card goes.
    public mutating func ended(_ request: String) {
        since?[request] = false
        requests.removeAll { $0.request == request }
        nonces[request] = nil
    }

    /// The nonce this device sent for a request; nil takes it back, when the send failed.
    public mutating func sent(_ nonce: Data?, for request: String) { nonces[request] = nonce }

    public func nonce(for request: String) -> Data? { nonces[request] }

    /// The requests a card may show for: open ones of other clients than `client`.
    public func open(at now: Date, client: String?) -> [DaemonAPI.LockJoinRequest] {
        requests.filter { $0.expires > now && $0.client.uppercased() != client?.uppercased() }
    }
}
