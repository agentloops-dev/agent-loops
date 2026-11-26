#if canImport(CryptoKit)
import CryptoKit
import Foundation
import Testing
@testable import AgentsKitCore

/// Signed records, against what Go signed (`Fixtures/lock/records.json`, from
/// TestTheRecordVectors, which the web page's lockRecords.test.mjs reads too).
@Suite("Lock records")
struct LockRecordTests {
    struct Vector: Decodable {
        var name: String
        var record: DaemonAPI.LockRecord
        var signed: Data
        var host: String
        var made: Bool
        var why: String
    }

    struct Step: Decodable {
        var had: Int64
        var seq: Int64
        var why: String
    }

    struct Foreign: Decodable {
        var domain: String
        var data: Data
        var sig: Data
    }

    struct Vectors: Decodable {
        var lock: DaemonAPI.Lock
        var kinds: [String: [String]]
        var records: [Vector]
        var steps: [Step]
        var foreign: [Foreign]
        var request: DaemonAPI.LockSignature
        var entry: DaemonAPI.LockEntry
    }

    static func vectors() throws -> Vectors {
        let url = URL(filePath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .appending(path: "Fixtures/lock/records.json")
        return try JSONDecoder().decode(Vectors.self, from: Data(contentsOf: url))
    }

    static func why(_ v: Vector, _ state: MachineLock.State) -> String {
        do throws(LockRecords.Failure) {
            try LockRecords.check(v.record, state: state)
        } catch {
            return error.rawValue
        }
        return LockRecords.takes(v.record, host: v.host, made: v.made) ? "" : LockRecords.Failure.otherPlace.rawValue
    }

    @Test func gosRecordsCheckHereAndSoDoItsRefusals() throws {
        let v = try Self.vectors()
        let state = try MachineLock.check(v.lock.entries)
        #expect(LockRecords.kinds == v.kinds)
        #expect(!v.records.isEmpty)
        for one in v.records {
            #expect(LockRecords.signedData(one.record) == one.signed, "\(one.name)")
            #expect(Self.why(one, state) == one.why, "\(one.name)")
        }
    }

    @Test func aNumberFollowsAnotherAsGoSays() throws {
        let v = try Self.vectors()
        #expect(!v.steps.isEmpty)
        for step in v.steps {
            var why = ""
            do throws(LockRecords.Failure) {
                try LockRecords.checkSeq(had: step.had, seq: step.seq)
            } catch {
                why = error.rawValue
            }
            #expect(why == step.why, "\(step.seq) after \(step.had)")
        }
    }

    @Test func aSignatureOfOneDomainIsNotOneOfAnother() throws {
        let v = try Self.vectors()
        let state = try MachineLock.check(v.lock.entries)
        let base = try #require(v.records.first).record
        #expect(v.foreign.count == 5)
        for one in v.foreign {
            #expect(one.data.starts(with: Data("\(one.domain)\n".utf8)))
            #expect(LockChannel.verify(base.key, one.data, one.sig), "\(one.domain)")
            #expect(!LockChannel.verify(base.key, one.data, base.sig), "\(one.domain)")
            var r = base
            r.sig = one.sig
            #expect(throws: LockRecords.Failure.forged, "\(one.domain)") { try LockRecords.check(r, state: state) }
        }
        let data = LockChannel.requestData(to: "dev-1", at: v.request.at, method: "agents/start", params: "{}")
        #expect(!LockChannel.verify(v.request.key, data, v.request.sig))
        #expect(throws: MachineLock.Failure.self) { try MachineLock.check([v.entry]) }
    }

    @Test func aRecordSignedHereChecks() throws {
        let key = LockSigningKey.ephemeral()
        let root = try MachineLock.sign(DaemonAPI.LockBody(kind: .root, person: "sam", key: key.publicKey), after: nil, with: key)
        let state = try MachineLock.check([root])
        let r = try LockRecords.sign(key, state: state, kind: "setup.script", subject: "setup", scope: "made", seq: 1, content: Data("{}".utf8))
        #expect(try LockRecords.check(r, state: state).isClient)
        #expect(LockRecords.takes(r, host: "a", made: true) && !LockRecords.takes(r, host: "a"))
        #expect(throws: LockRecords.Failure.invalid) {
            try LockRecords.sign(key, state: state, kind: "setup.script", subject: "setup", scope: "all", seq: 1, content: Data())
        }
        #expect(throws: LockRecords.Failure.invalid) {
            try LockRecords.sign(key, state: state, kind: "setup.script", subject: "setup", scope: "m:a", seq: 1,
                                 content: Data(count: LockRecords.mostContent + 1))
        }
    }
}
#endif
