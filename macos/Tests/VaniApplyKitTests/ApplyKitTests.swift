import XCTest
@testable import VaniApplyKit

final class ApplyKitTests: XCTestCase {
    private func job(url: String? = nil, apply: String? = nil, description: String? = nil, mark: JobMark? = nil) -> Job {
        var j = Job(key: "k", company: "Acme", title: "Engineer")
        j.url = url
        j.applyURL = apply
        j.description = description
        j.mark = mark
        return j
    }

    func testReadinessLine() {
        XCTAssertEqual(ApplyKit.readinessLine(total: 27, review: 3), "27 saved answers · 3 to review")
        XCTAssertEqual(ApplyKit.readinessLine(total: 1, review: 0), "1 saved answer")
        XCTAssertEqual(ApplyKit.readinessLine(total: 0, review: 0), "No saved answers yet")
    }

    func testReadinessLineClampsNonsense() {
        XCTAssertEqual(ApplyKit.readinessLine(total: -4, review: 2), "No saved answers yet")
        XCTAssertEqual(ApplyKit.readinessLine(total: 2, review: 9), "2 saved answers · 2 to review")
        XCTAssertEqual(ApplyKit.readinessLine(total: 2, review: -1), "2 saved answers")
    }

    func testApplyLinkOnlyWhenDistinct() {
        XCTAssertNil(ApplyKit.distinctApplyLink(job()))
        XCTAssertNil(ApplyKit.distinctApplyLink(job(url: "https://a.example/j/1", apply: "https://a.example/j/1")))
        XCTAssertNil(ApplyKit.distinctApplyLink(job(url: "https://a.example/j/1", apply: "HTTPS://A.example/j/1/#apply")))
        XCTAssertEqual(ApplyKit.distinctApplyLink(job(url: "https://a.example/j/1", apply: "https://a.example/j/1/apply"))?.absoluteString,
                       "https://a.example/j/1/apply")
        // No posting link: the apply link is the only one, so it is offered.
        XCTAssertNotNil(ApplyKit.distinctApplyLink(job(apply: "https://a.example/apply")))
    }

    func testApplyLinkRefusesNonHTTP() {
        XCTAssertNil(ApplyKit.distinctApplyLink(job(url: "https://a.example/j", apply: "javascript:alert(1)")))
        XCTAssertNil(ApplyKit.distinctApplyLink(job(apply: "file:///etc/passwd")))
        XCTAssertNil(ApplyKit.distinctApplyLink(job(apply: "")))
        XCTAssertNil(ApplyKit.distinctApplyLink(job(apply: "https://")))
    }

    func testDraftPrefillCarriesFields() {
        let p = ApplyKit.draftPrefill(job(description: "Build things"))
        XCTAssertEqual(p, .init(company: "Acme", title: "Engineer", posting: "Build things"))
        XCTAssertEqual(ApplyKit.draftPrefill(job()).posting, "")
    }

    /// A list row may carry no description; the detail pane loads it separately and passes it in.
    func testDraftPrefillPrefersLoadedDescription() {
        XCTAssertEqual(ApplyKit.draftPrefill(job(), description: "Loaded text").posting, "Loaded text")
        XCTAssertEqual(ApplyKit.draftPrefill(job(description: "Row text"), description: "Loaded text").posting, "Loaded text")
        XCTAssertEqual(ApplyKit.draftPrefill(job(description: "Row text"), description: nil).posting, "Row text")
        let huge = String(repeating: "y", count: ApplyKit.maxPostingChars * 3)
        XCTAssertEqual(ApplyKit.draftPrefill(job(), description: huge).posting.count, ApplyKit.maxPostingChars)
    }

    func testDraftPrefillBoundsHugeDescription() {
        let p = ApplyKit.draftPrefill(job(description: String(repeating: "x", count: ApplyKit.maxPostingChars * 50)))
        XCTAssertEqual(p.posting.count, ApplyKit.maxPostingChars)
    }

    func testStepsDraftStates() {
        func draft(_ r: Reading<ApplyKit.AnswerCounts>) -> ApplyKit.StepState { ApplyKit.steps(job: job(), answers: r)[0].state }
        XCTAssertEqual(draft(.pending), .todo)
        XCTAssertEqual(draft(.ready(.init(total: 0, review: 0))), .todo)
        XCTAssertEqual(draft(.ready(.init(total: 5, review: 2))), .todo)
        XCTAssertEqual(draft(.ready(.init(total: 5, review: 0))), .done)
        // An unreadable bank is not "no answers".
        XCTAssertEqual(draft(.failed("locked")), .unavailable("locked"))
    }

    func testStepsOrderResumeAndMark() {
        let s = ApplyKit.steps(job: job(), answers: .pending)
        XCTAssertEqual(s.map(\.number), [1, 2, 3, 4])
        XCTAssertEqual(s[1].state, .manual)
        XCTAssertEqual(s[3].state, .todo)
        XCTAssertEqual(ApplyKit.steps(job: job(mark: .applied), answers: .pending)[3].state, .done)
        XCTAssertEqual(ApplyKit.steps(job: job(mark: .saved), answers: .pending)[3].state, .todo)
    }

    func testResumeNoteSaysWhereItIsChosen() {
        XCTAssertTrue(ApplyKit.resumeNote.contains("browser popup"))
        XCTAssertTrue(ApplyKit.resumeNote.contains("View"))
    }

    func testResumeStepIsDoneOnlyWhenOneIsSaved() {
        let pending = Reading<ApplyKit.AnswerCounts>.pending
        XCTAssertEqual(ApplyKit.steps(job: job(), answers: pending)[1].state, .manual)
        XCTAssertEqual(ApplyKit.steps(job: job(), answers: pending, resume: .none)[1].state, .manual)
        XCTAssertEqual(ApplyKit.steps(job: job(), answers: pending, resume: .saved("Resume_Acme.pdf"))[1].state, .done)
        XCTAssertEqual(ApplyKit.steps(job: job(), answers: pending, resume: .unavailable("locked"))[1].state, .unavailable("locked"))
        // A store failure is not "no resume", and it does not change the other steps.
        let failed = ApplyKit.steps(job: job(mark: .applied), answers: .failed("locked"), resume: .unavailable("locked"))
        XCTAssertEqual(failed[0].state, .unavailable("locked"))
        XCTAssertEqual(failed[1].state, .unavailable("locked"))
        XCTAssertEqual(failed[3].state, .done)
    }

    func testResumeDetailNamesTheFileTheSizeAndTheDay() {
        XCTAssertEqual(ApplyKit.resumeDetail(name: "Resume_Acme.pdf", bytes: 1200, pickedAt: "2026-10-23T01:02:03Z"),
                       "Resume_Acme.pdf · 1 KB · 2026-10-23")
        XCTAssertEqual(ApplyKit.resumeDetail(name: "Resume_Acme.pdf", bytes: 0, pickedAt: "not-a-date"),
                       "Resume_Acme.pdf · 1 KB")
    }
}
