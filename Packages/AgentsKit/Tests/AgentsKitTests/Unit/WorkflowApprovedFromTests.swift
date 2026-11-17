import Foundation
import Testing
@testable import AgentsKitCore

/// An approval taken from the person's other machine (#5462, #5543): the window and the
/// Remote read `approvedFrom` from the wire, as the web page does.
@Suite("A workflow approved on another of the person's machines")
struct WorkflowApprovedFromTests {
    private let workflow = Workflow(workflowID: "tidy", folder: URL(filePath: "/tmp/work/app"), name: "Tidy")

    @Test func itIsReadFromTheWire() throws {
        let summary = WorkflowSummary(workflow: workflow, approvedFrom: "Approved by you on Studio, 2026-11-14")
        let read = try JSONDecoder().decode(WorkflowSummary.self, from: JSONEncoder().encode(summary))
        #expect(read.approvedFrom == "Approved by you on Studio, 2026-11-14")
    }

    @Test func aSummaryWithoutItReadsAsNil() throws {
        let read = try JSONDecoder().decode(WorkflowSummary.self, from: JSONEncoder().encode(WorkflowSummary(workflow: workflow)))
        #expect(read.approvedFrom == nil)
    }
}
