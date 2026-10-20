import ApplicationServices
import XCTest
import VaniApplyKit

/// The Insights "Skills to build" card, in the host process (see JobsTabHost):
/// what it says for each state of `vani jobs gaps`, and that clicking an
/// example role selects it without touching the list.
final class JobsGapsPanelTests: XCTestCase {
    private var session: JobsTabSession?

    override func tearDown() {
        session?.close()
        session = nil
        super.tearDown()
    }

    private func start(gaps: String? = nil) throws -> JobsTabSession {
        let fixture = try JobsFixture(self)
        if let gaps { try fixture.setGapsReply(gaps) }
        let s = try JobsTabSession(fixture: fixture)
        session = s
        try s.waitForState("the first list") { $0.jobs == JobsFixture.standardOrder }
        let toggle = try s.element("the Insights toolbar toggle") {
            ($0.title == "Insights" || $0.label == "Insights") && $0.role != kAXStaticTextRole && $0.role != kAXMenuItemRole
        }
        try s.press(toggle, "Insights")
        try s.waitForState("Insights open with stats") { $0.showInsights && $0.stats }
        return s
    }

    /// Every static text the card shows, in tree order.
    private func texts(_ s: JobsTabSession) -> [String] {
        s.elements { $0.role == kAXStaticTextRole }.compactMap { $0.attribute(kAXValueAttribute) as? String }
    }

    private func waitForText(_ s: JobsTabSession, _ what: String, _ ok: (String) -> Bool) throws -> String {
        let deadline = Date().addingTimeInterval(JobsTabSession.waitTimeout)
        while true {
            if let t = texts(s).first(where: ok) { return t }
            guard Date() < deadline else {
                throw JobsTabSession.Failure(description: "no text \(what) appeared; saw \(texts(s))")
            }
            Thread.sleep(forTimeInterval: 0.1)
        }
    }

    func testListsTheGapsAndAnExampleSelectsItsRoleWithoutTouchingTheList() throws {
        let s = try start()
        _ = try waitForText(s, "Showing 2 of 3") { $0 == "Showing 2 of 3" }
        let kube = try s.element("the Kubernetes row") { $0.role == kAXButtonRole && $0.label.hasPrefix("Kubernetes, infra,") }
        XCTAssertTrue(kube.label.contains("Found missing by DevPrism"), kube.label)
        let tf = try s.element("the Terraform row") { $0.role == kAXButtonRole && $0.label.hasPrefix("Terraform, infra,") }
        XCTAssertTrue(tf.label.contains("Not named by your target summary"), tf.label)
        XCTAssertNil(s.elements { $0.label.hasPrefix("Open ") }.first, "examples stay folded until the skill is opened")

        let callsBefore = s.vaniCalls().filter { $0.hasPrefix("jobs list") }.count
        // The cards sit in a horizontal scroll view: bring this one into the window first.
        _ = AXUIElementPerformAction(kube.ref, "AXScrollToVisible" as CFString)
        try s.click(kube, "the Kubernetes row")
        let example = try s.element("the Gamma example") { $0.role == kAXButtonRole && $0.label == "Open Site Reliability Engineer at Gamma Works" }
        try s.click(example, "the Gamma example")
        let st = try s.waitForState("selection fx:gamma:3") { $0.selection == "fx:gamma:3" }
        XCTAssertEqual(st.jobs, JobsFixture.standardOrder, "the list keeps its order")
        XCTAssertTrue(st.fits.isEmpty)
        XCTAssertEqual(s.vaniCalls().filter { $0.hasPrefix("jobs list") }.count, callsBefore, "selecting re-lists nothing")
        XCTAssertTrue(s.vaniCalls().contains("jobs gaps --json --view new"), "\(s.vaniCalls())")
    }

    func testNothingJudgedSaysSoAndNeverNoGaps() throws {
        let s = try start(gaps: #"{"view":"new","roles":4,"with_missing":0,"target_known":false,"total_gaps":0,"gaps":[],"insight":{"target":false,"warning":"the target summary was not read"}}"#)
        let status = try waitForText(s, "No gaps could be judged") { $0.hasPrefix("No gaps could be judged") }
        XCTAssertFalse(status.localizedCaseInsensitiveContains("no gaps found"), status)
        _ = try waitForText(s, "the warning") { $0 == "the target summary was not read" }
        _ = try waitForText(s, "the target note") { $0.hasPrefix("Only DevPrism gaps were counted") }
    }

    func testNoGapsFoundIsSaidOnlyWhenJudged() throws {
        let s = try start(gaps: #"{"view":"new","roles":4,"with_missing":1,"target_known":true,"total_gaps":0,"gaps":[],"insight":{"target":true}}"#)
        _ = try waitForText(s, "No gaps found.") { $0 == "No gaps found." }
    }

    func testAFailedGapsCallShowsTheErrorAndLeavesTheChartsAndListAlone() throws {
        let s = try start(gaps: #"{"error":"jobs gaps: the ledger is locked"}"#)
        _ = try waitForText(s, "the error") { $0.contains("the ledger is locked") || $0.contains("without a reply") }
        XCTAssertEqual(try s.state().jobs, JobsFixture.standardOrder)
        XCTAssertTrue(try s.state().stats, "the charts still load")
    }
}
