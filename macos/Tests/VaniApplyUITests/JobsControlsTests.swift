import ApplicationServices
import XCTest
import VaniApplyKit

/// The Jobs tab's own controls, driven in a host process (see JobsTabHost):
/// each test works a control the way a user does and reads JobsModel back,
/// so it fails if the control stops calling its setter.
final class JobsControlsTests: XCTestCase {
    private var session: JobsTabSession?

    override func tearDown() {
        session?.close()
        session = nil
        super.tearDown()
    }

    private func start() throws -> JobsTabSession {
        let s = try JobsTabSession(fixture: JobsFixture(self))
        session = s
        try s.waitForState("the first list") { $0.jobs == JobsFixture.standardOrder }
        return s
    }

    /// Opens the Insights pane with its toolbar toggle and waits for the stats.
    private func showInsights(_ s: JobsTabSession) throws {
        let toggle = try s.element("the Insights toolbar toggle") {
            ($0.title == "Insights" || $0.label == "Insights") && $0.role != kAXStaticTextRole && $0.role != kAXMenuItemRole
        }
        try s.press(toggle, "Insights")
        try s.waitForState("Insights open with stats") { $0.showInsights && $0.stats }
    }

    // Toolbar Sort menu > Newest first.
    func testSortMenuNewestFirstSortsByFreshness() throws {
        let s = try start()
        let sort = try s.element("the toolbar Sort menu") { $0.role == kAXMenuButtonRole && $0.title == "Sort" }
        try s.press(sort, "Sort")
        let item = try s.element("the Newest first item") { $0.role == kAXMenuItemRole && $0.title == JobSort.fresh.title }
        try s.press(item, "Newest first")

        let st = try s.waitForState("sort fresh with the list re-read") { $0.sort == JobSort.fresh.rawValue && $0.jobs == JobsFixture.freshOrder }
        XCTAssertEqual(st.jobs, ["fx:beta:2", "fx:delta:4", "fx:alpha:1", "fx:gamma:3"], "newest fresh_at first")
        XCTAssertTrue(s.vaniCalls().contains { $0.hasPrefix("jobs list") && $0.contains("--sort fresh") }, "\(s.vaniCalls())")
        // The rows on screen follow: each row reads "Title, Company, …".
        let list = try s.element("the job list") { $0.role == kAXOutlineRole || $0.role == kAXTableRole }
        let rows = list.all(limit: 400) { $0.role == kAXStaticTextRole }.compactMap { $0.attribute(kAXValueAttribute) as? String }
        let shown = rows.compactMap { row in JobsFixture.roles.first { row.hasPrefix($0.title + ",") }?.key }
        XCTAssertEqual(shown, JobsFixture.freshOrder, "rows: \(rows)")
    }

    // Toolbar Filter menu > Role fit > Strong fit.
    func testFilterMenuStrongFitFiltersByFit() throws {
        let s = try start()
        let filter = try s.element("the toolbar Filter menu") { $0.role == kAXMenuButtonRole && $0.title.hasPrefix("Filter") }
        try s.press(filter, "Filter")
        let item = try s.element("the Strong fit item") { $0.role == kAXMenuItemRole && $0.title == "Strong fit" }
        try s.press(item, "Strong fit")

        let st = try s.waitForState("fits [strong_fit] with the list re-read") { $0.fits == ["strong_fit"] && $0.jobs == JobsFixture.strongOrder }
        XCTAssertEqual(st.fits, ["strong_fit"])
        XCTAssertTrue(s.vaniCalls().contains { $0.hasPrefix("jobs list") && $0.contains("--fit strong_fit") }, "\(s.vaniCalls())")
    }

    // Insights > Role fit card: a click on the Strong fit bar toggles that fit.
    // A click, not AXPress: the row's tap gesture and its accessibility action
    // are separate closures, and this test is about the tap.
    func testFitRowClickTogglesTheFit() throws {
        let s = try start()
        try showInsights(s)
        let row = { try s.element("the Strong fit row") { $0.role == kAXButtonRole && $0.label.hasPrefix("Strong fit:") } }
        try s.click(row(), "the Strong fit row")
        try s.waitForState("fits [strong_fit]") { $0.fits == ["strong_fit"] && $0.jobs == JobsFixture.strongOrder }
        try s.click(row(), "the Strong fit row")
        try s.waitForState("fits cleared again") { $0.fits.isEmpty && $0.jobs == JobsFixture.standardOrder }
    }

    // The same row for VoiceOver: its accessibility action toggles the fit too.
    func testFitRowAccessibilityActionTogglesTheFit() throws {
        let s = try start()
        try showInsights(s)
        let row = try s.element("the Adjacent row") { $0.role == kAXButtonRole && $0.label.hasPrefix("Adjacent:") }
        try s.press(row, "the Adjacent row")
        try s.waitForState("fits [adjacent]") { $0.fits == ["adjacent"] }
    }

    // Insights > Relevance × age: a click on a dot selects that role. Gamma
    // stands alone; alpha is above delta at nearly the same age, so a plot
    // read upside down would pick delta instead.
    func testScatterDotClickSelectsTheRole() throws {
        let s = try start()
        try showInsights(s)
        let chart = try s.element("the relevance × age chart") { $0.label.hasPrefix("Relevance against age") }
        let plot = try ScatterPlot(chart)
        for key in ["fx:gamma:3", "fx:alpha:1"] {
            let r = try XCTUnwrap(JobsFixture.roles.first { $0.key == key })
            try s.click(at: plot.point(ageDays: r.ageDays, relevance: r.relevance))
            try s.waitForState("selection \(key)") { $0.selection == key }
        }
    }
}

/// Where the relevance × age chart draws a point, read from its
/// accessibility elements. Swift Charts exposes the scatter as age bins
/// ("0 to 50", "50 to 100 …"), each spanning its ages across the full plot
/// height. The bin starting at 0 gives the plot's left edge and, with the
/// chart's square-root age axis over 0...365 days (RelevanceScatter.maxAge),
/// its width; every other bin must agree with that, or the reading is refused.
struct ScatterPlot {
    static let maxAge = 365.0
    static let tolerance = 1.5

    let left: Double, width: Double, top: Double, bottom: Double

    init(_ chart: AX) throws {
        struct Bin { var lo, hi: Double; var frame: CGRect }
        var bins: [Bin] = []
        for el in chart.children {
            let words = el.label.split(separator: " ")
            guard words.count >= 3, words[1] == "to", let lo = Double(words[0]), let hi = Double(words[2]),
                  hi > lo, let f = el.frame else { continue }
            bins.append(Bin(lo: lo, hi: hi, frame: f))
        }
        guard let first = bins.first(where: { $0.lo == 0 }), first.hi <= Self.maxAge else {
            throw JobsTabSession.Failure(description: "the chart exposes no age bin from 0: \(chart.dump(limit: 40))")
        }
        left = first.frame.minX
        width = first.frame.width / (first.hi / Self.maxAge).squareRoot()
        top = first.frame.minY
        bottom = first.frame.maxY
        for b in bins {
            let x0 = left + width * (b.lo / Self.maxAge).squareRoot()
            let x1 = left + width * (min(b.hi, Self.maxAge) / Self.maxAge).squareRoot()
            guard abs(x0 - b.frame.minX) <= Self.tolerance, abs(x1 - b.frame.maxX) <= Self.tolerance,
                  abs(b.frame.minY - top) <= Self.tolerance, abs(b.frame.maxY - bottom) <= Self.tolerance else {
                throw JobsTabSession.Failure(description: "bin \(b.lo)-\(b.hi) at \(b.frame) does not fit a √-age plot from x \(left), width \(width), y \(top)-\(bottom)")
            }
        }
    }

    func point(ageDays: Int, relevance: Double) -> CGPoint {
        let x = left + width * (min(Double(ageDays), Self.maxAge) / Self.maxAge).squareRoot()
        let y = bottom - (bottom - top) * relevance / 100
        return CGPoint(x: x, y: y)
    }
}
