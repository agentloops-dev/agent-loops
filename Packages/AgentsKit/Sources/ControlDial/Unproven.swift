import Foundation
import NIOCore

/// The first handler on a server's connection, before TLS (#206). Until the upgrade it
/// closes a connection that stays silent for `idle`, or has not upgraded within
/// `deadline` (the TLS handshake and the request's head both count). Until the control
/// plane's `ok` it lets through at most `bytes` after the upgrade, so a frame of up to
/// 64 MB is never gathered for a peer that has proved nothing. Once proved, it passes
/// everything and costs nothing.
final class UnprovenGuard: ChannelInboundHandler, @unchecked Sendable {
    typealias InboundIn = ByteBuffer
    typealias InboundOut = ByteBuffer

    static let idle: TimeAmount = .seconds(10)
    static let deadline: TimeAmount = .seconds(20)
    /// What a peer may send between the upgrade and `ok`: its auth line is under a kilobyte.
    static let bytes = 64 * 1024

    private let idle: TimeAmount
    private let deadline: TimeAmount
    private var upgraded = false
    private var proven = false
    private var received = 0
    private var idleTimer: Scheduled<Void>?
    private var deadlineTimer: Scheduled<Void>?

    init(idle: TimeAmount = UnprovenGuard.idle, deadline: TimeAmount = UnprovenGuard.deadline) {
        self.idle = idle
        self.deadline = deadline
    }

    func handlerAdded(context: ChannelHandlerContext) {
        let channel = context.channel
        deadlineTimer = context.eventLoop.scheduleTask(in: deadline) { channel.close(promise: nil) }
        armIdle(context)
    }

    func handlerRemoved(context: ChannelHandlerContext) { settle() }

    func channelInactive(context: ChannelHandlerContext) {
        settle()
        context.fireChannelInactive()
    }

    func channelRead(context: ChannelHandlerContext, data: NIOAny) {
        if !proven {
            if upgraded {
                received += unwrapInboundIn(data).readableBytes
                if received > Self.bytes {
                    context.close(promise: nil)
                    return
                }
            } else {
                armIdle(context)
            }
        }
        context.fireChannelRead(data)
    }

    /// The WebSocket is open: the service's own 15 s wait for `auth` bounds it from here.
    func didUpgrade() {
        upgraded = true
        idleTimer?.cancel()
        deadlineTimer?.cancel()
    }

    /// The control plane said `ok`. On the channel's event loop.
    func didProve() {
        proven = true
        settle()
    }

    private func armIdle(_ context: ChannelHandlerContext) {
        idleTimer?.cancel()
        let channel = context.channel
        idleTimer = context.eventLoop.scheduleTask(in: idle) { channel.close(promise: nil) }
    }

    private func settle() {
        idleTimer?.cancel()
        deadlineTimer?.cancel()
    }
}
