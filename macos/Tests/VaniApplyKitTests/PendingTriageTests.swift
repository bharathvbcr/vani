import XCTest
@testable import VaniApplyKit

/// Postings awaiting triage are not listed; the Jobs tab must still say they
/// exist, or a company whose postings all wait reads as a company with none.
final class PendingTriageTests: XCTestCase {
    func testDecodesWhatGoPrints() throws {
        // The shape `vani jobs list --json` writes (cmd/vani/jobspending.go).
        let reply = """
        {"view":"new","query":"anthropic","jobs":[],"total":0,"shown":0,"counts":{"new":283},\
        "ledger":"/x/ledger","records":3027,"sort":"default",\
        "pending":{"total":2639,"matching":656,"companies":[{"company":"Databricks","n":895},{"company":"Anthropic","n":656}]}}
        """
        let page = try JobsClient.decoder.decode(JobsPage.self, from: Data(reply.utf8))
        let p = try XCTUnwrap(page.pending)
        XCTAssertEqual(p, PendingTriage(total: 2639, matching: 656, companies: [.init(company: "Databricks", n: 895), .init(company: "Anthropic", n: 656)]))
        XCTAssertEqual(p.footerNote, "· 2639 awaiting triage (see the Awaiting triage view)")
        XCTAssertTrue(p.footerHelp.contains("(Databricks 895, Anthropic 656)"), p.footerHelp)
        XCTAssertTrue(p.footerHelp.contains("Each run advances at most the config's advance_per_run postings."), p.footerHelp)
        XCTAssertFalse(p.footerHelp.contains("advance_per_run of them"), p.footerHelp)
        XCTAssertEqual(p.emptySearchNote(query: "anthropic"),
                       "656 postings matching “anthropic” are awaiting triage; see them, unranked, in the Awaiting triage view.")
    }

    func testAReplyWithoutPendingStillDecodes() throws {
        let reply = #"{"view":"new","jobs":[],"total":0,"shown":0,"counts":{}}"#
        let page = try JobsClient.decoder.decode(JobsPage.self, from: Data(reply.utf8))
        XCTAssertNil(page.pending, "a vani from before pending must still decode")
    }

    func testNothingWaitingSaysNothing() {
        let none = PendingTriage(total: 0, matching: 0, companies: [])
        XCTAssertNil(none.footerNote)
        XCTAssertNil(none.emptySearchNote(query: "anthropic"))
        let elsewhere = PendingTriage(total: 5, matching: 0, companies: [.init(company: "Acme", n: 5)])
        XCTAssertNil(elsewhere.emptySearchNote(query: "anthropic"), "the search names none of the waiting postings")
        XCTAssertNil(elsewhere.emptySearchNote(query: "   "), "no search, no search note")
    }

    func testOneWaitingIsSingular() {
        let one = PendingTriage(total: 1, matching: 1, companies: [.init(company: "Acme", n: 1)])
        XCTAssertEqual(one.emptySearchNote(query: "acme"),
                       "1 posting matching “acme” is awaiting triage; see it, unranked, in the Awaiting triage view.")
    }
}
