import CryptoKit
import Foundation
import Testing
@testable import AgentsKitCore

/// An agent's secret that becomes yours (#5308): the associated data is Go's
/// `controllink.AgentSecretAD` byte for byte, an offer opens only with what it was sealed with,
/// and the words are the web page's (`Web/test/secretOffers.test.mjs`).
@Suite("Secret offers")
struct SecretOfferTests {
    @Test func theAssociatedDataIsGos() {
        #expect(String(decoding: ClientSealing.agentSecretAD(user: "sam", machine: "MACHINE-30", recipient: "c:3D0C", name: "GITLAB_TOKEN",
                                                             offer: "OFFER-1"), as: UTF8.self)
            == "agentsecret.v1|sam|MACHINE-30|c:3D0C|GITLAB_TOKEN|OFFER-1")
        #expect(String(decoding: ClientSealing.agentSecretAD(user: "sam", machine: "MACHINE-30", recipient: "c:3D0C", name: "GITLAB_TOKEN",
                                                             offer: "OFFER-1", ask: true), as: UTF8.self)
            == "agentsecret.v1|sam|MACHINE-30|c:3D0C|GITLAB_TOKEN|OFFER-1|ask")
    }

    @Test func anOfferOpensOnlyAsItWasSealed() throws {
        let key = try DeviceKey.software(privateKey: P256.KeyAgreement.PrivateKey().rawRepresentation)
        let client = UUID()
        let ad = ClientSealing.agentSecretAD(user: "sam", machine: "MACHINE-30", recipient: ClientSealing.clientRecipient(client),
                                             name: "GITLAB_TOKEN", offer: "OFFER-1")
        let mine = try Envelope.seal(Data("glpat-agent".utf8), to: key.publicKey, info: ClientSealing.agentSecretPurpose, authenticating: ad)
        let offer = DaemonAPI.SecretOffer(offer: "OFFER-1", name: "GITLAB_TOKEN", machine: "MACHINE-30", machineName: "devbox",
                                          savedAt: "2026-11-15T10:00:00Z", mine: mine)
        #expect(String(decoding: try ClientSealing.open(offer, user: "sam", with: key, client: client), as: UTF8.self) == "glpat-agent")
        var moved = offer
        moved.offer = "OFFER-2"
        #expect(throws: (any Error).self) { try ClientSealing.open(moved, user: "sam", with: key, client: client) }
        moved = offer
        moved.machine = "MACHINE-31"
        #expect(throws: (any Error).self) { try ClientSealing.open(moved, user: "sam", with: key, client: client) }
        moved = offer
        moved.needsApproval = true
        #expect(throws: (any Error).self) { try ClientSealing.open(moved, user: "sam", with: key, client: client) }
    }

    @Test func theWords() {
        let now = ISO8601DateFormatter().date(from: "2026-11-15T12:00:00Z")!
        #expect(SecretOfferWords.savedBy(DaemonAPI.SecretSavedBy(machine: "MACHINE-30", name: "devbox", at: "2026-11-15T10:00:00Z"), now: now)
            == "Saved by an agent on devbox, 2 h ago")
        #expect(SecretOfferWords.savedBy(nil, now: now) == nil)
        let offer = DaemonAPI.SecretOffer(offer: "O", name: "N", machine: "M", machineName: "devbox", savedAt: "")
        #expect(SecretOfferWords.line(offer) == "Saved by an agent on devbox. It goes to your other machines when you next open Agents.")
        var conflict = offer
        conflict.conflict = true
        #expect(SecretOfferWords.line(conflict).hasPrefix("Saved by an agent on devbox. You have a secret with this name"))
        #expect(SecretOfferWords.keepYours == "Keep Yours")
        #expect(SecretOfferWords.useTheAgents == "Use the Agent's")
    }

    /// #6626: a new offer of the same name (a new id) keeps the card's progress and failure.
    @Test func answersFollowTheCardNotTheOffersID() {
        let first = DaemonAPI.SecretOffer(offer: "OFFER-1", name: "PADDLE_SANDBOX_PASSWORD", machine: "MACHINE-30", machineName: "Mac",
                                          savedAt: "2026-11-22T02:48:50Z", conflict: true)
        var again = first
        again.offer = "OFFER-2"
        again.savedAt = "2026-11-22T02:48:51Z"
        var other = first
        other.machine = "MACHINE-31"
        var answers = DaemonAPI.SecretOfferAnswers()
        answers.begin(first)
        #expect(answers.isAnswering(again))
        #expect(!answers.isAnswering(other))
        answers.end(first, failure: SecretOfferWords.noKey)
        #expect(!answers.isAnswering(again))
        #expect(answers.failure(again) == SecretOfferWords.noKey)
        answers.keep(only: [again, other])
        #expect(answers.failure(again) == SecretOfferWords.noKey)
        answers.begin(again)
        #expect(answers.failure(first) == nil)
        answers.end(again, failure: "refused")
        answers.keep(only: [other])
        #expect(answers.failure(first) == nil)
    }

    /// #6626: one card per machine and name, with its newest offer.
    @Test func oneCardPerMachineAndName() {
        let first = DaemonAPI.SecretOffer(offer: "OFFER-1", name: "OKTA_ADMIN_PASSWORD", machine: "MACHINE-30", machineName: "Mac",
                                          savedAt: "2026-11-22T02:48:50Z", conflict: true)
        var again = first
        again.offer = "OFFER-2"
        again.savedAt = "2026-11-22T02:48:51Z"
        var other = first
        other.offer = "OFFER-3"
        other.machine = "MACHINE-31"
        other.savedAt = "2026-11-22T02:00:00Z"
        #expect(DaemonAPI.SecretOffer.waiting([again, first, other]).map(\.offer) == ["OFFER-3", "OFFER-2"])
        #expect(DaemonAPI.SecretOffer.waiting([]).isEmpty)
    }
}
