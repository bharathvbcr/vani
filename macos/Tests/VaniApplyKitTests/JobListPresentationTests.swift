import XCTest
@testable import VaniApplyKit

final class JobListPresentationTests: XCTestCase {
    private func job(_ key: String, _ company: String, _ title: String, age: Int? = nil) -> Job {
        var j = Job(key: key, company: company, title: title)
        j.ageDays = age
        return j
    }

    // MARK: grouping

    func testGroupsSameRoleAcrossCaseAccentsAndSpacing() {
        let jobs = [
            job("a", "Acme", "Senior Systems Engineer", age: 3),
            job("b", "Other", "Senior Systems Engineer"),
            job("c", "  ACME ", "senior   systems\tengineer", age: 40),
        ]
        let g = PostingGroups(jobs)
        XCTAssertEqual(g.groups, [["a", "c"], ["b"]], "groups keep first-appearance order")
        XCTAssertEqual(g.others(of: "a").map(\.key), ["c"])
        XCTAssertEqual(g.others(of: "c").map(\.key), ["a"])
        XCTAssertEqual(g.others(of: "b"), [])
        XCTAssertEqual(g.others(of: "nope"), [])
        XCTAssertEqual(PostingGroups([job("x", "Café", "Chef"), job("y", "Cafe", "chef")]).groups, [["x", "y"]])
    }

    func testBlankCompanyOrTitleNeverGroups() {
        let g = PostingGroups([job("a", "Acme", "   "), job("b", "Acme", ""), job("c", "", "Dev"), job("d", " ", "Dev")])
        XCTAssertEqual(g.groups, [["a"], ["b"], ["c"], ["d"]])
        XCTAssertEqual(g.others(of: "a"), [])
    }

    func testEmptyListAndDuplicateKeys() {
        XCTAssertEqual(PostingGroups([]).groups, [])
        // The same key listed twice is one posting, not its own sibling.
        let g = PostingGroups([job("a", "Acme", "Dev"), job("a", "Acme", "Dev")])
        XCTAssertEqual(g.others(of: "a"), [])
    }

    func testEmojiAndRTLNamesGroupWithoutTrapping() {
        let g = PostingGroups([job("a", "🚀 Rocket", "Dev"), job("b", "🚀 rocket", "DEV"),
                               job("c", "شركة النور", "مهندس"), job("d", "شركة  النور", "مهندس")])
        XCTAssertEqual(g.groups, [["a", "b"], ["c", "d"]])
    }

    func testTenThousandJobsGroupQuickly() {
        let jobs = (0..<10_000).map { job("k\($0)", "Company \($0 % 500)", "Role \($0 % 7)") }
        measure {
            let g = PostingGroups(jobs)
            XCTAssertEqual(g.groups.reduce(0) { $0 + $1.count }, 10_000)
        }
    }

    func testAlsoPostedWording() {
        XCTAssertNil(JobListPresentation.alsoPosted([]))
        XCTAssertEqual(JobListPresentation.alsoPosted([job("a", "A", "T", age: 12)]), "Also posted 12d ago")
        XCTAssertEqual(JobListPresentation.alsoPosted([job("a", "A", "T", age: 0)]), "Also posted today")
        XCTAssertEqual(JobListPresentation.alsoPosted([job("a", "A", "T")]), "Also posted once more")
        var seen = job("a", "A", "T", age: 3)
        seen.freshSource = .seen
        XCTAssertEqual(JobListPresentation.alsoPosted([seen]), "Also posted ~3d ago")
        XCTAssertEqual(JobListPresentation.alsoPosted([seen, seen, seen]), "3 other postings")
    }

    // MARK: monogram

    func testMonogramInitials() {
        XCTAssertEqual(CompanyMonogram(company: "Acme Robotics").initials, "AR")
        XCTAssertEqual(CompanyMonogram(company: "OpenAI").initials, "O")
        XCTAssertEqual(CompanyMonogram(company: "  the big  three corp ").initials, "TB")
        XCTAssertEqual(CompanyMonogram(company: "& - Acme").initials, "A", "punctuation-only words are skipped")
        XCTAssertEqual(CompanyMonogram(company: "").initials, "?")
        XCTAssertEqual(CompanyMonogram(company: " \n\t").initials, "?")
        XCTAssertEqual(CompanyMonogram(company: "🚀🚀").initials, "🚀", "no letter: the first visible character")
        XCTAssertEqual(CompanyMonogram(company: "شركة النور").initials.count, 2)
        XCTAssertEqual(CompanyMonogram(company: "élan vital").initials, "ÉV")
        XCTAssertEqual(CompanyMonogram(company: String(repeating: "x ", count: 10_000)).initials, "XX")
    }

    func testMonogramHueIsStableAndFolded() {
        // Golden values from an independent FNV-1a (32-bit) over the folded name, mod 360.
        XCTAssertEqual(CompanyMonogram(company: "Acme").hue, 55.0 / 360)
        XCTAssertEqual(CompanyMonogram(company: " ACME ").hue, 55.0 / 360)
        XCTAssertEqual(CompanyMonogram(company: "Anthropic").hue, 355.0 / 360)
        XCTAssertEqual(CompanyMonogram(company: "Café").hue, 256.0 / 360)
        XCTAssertEqual(CompanyMonogram(company: "Cafe").hue, CompanyMonogram(company: "Café").hue)
        for name in ["", " ", "🚀", "شركة", String(repeating: "z", count: 5000)] {
            let h = CompanyMonogram(company: name).hue
            XCTAssertTrue(h >= 0 && h < 1, name)
        }
    }

    // MARK: score

    func testScoreDisplayClampsAndRefusesNonFinite() {
        XCTAssertEqual(JobScore.display(87.4), 87)
        XCTAssertEqual(JobScore.display(87.5), 88)
        XCTAssertEqual(JobScore.display(-5), 0)
        XCTAssertEqual(JobScore.display(250), 100)
        XCTAssertNil(JobScore.display(nil))
        XCTAssertNil(JobScore.display(.nan))
        XCTAssertNil(JobScore.display(.infinity))
        XCTAssertNil(JobScore.display(-.infinity))
    }

    func testScoreGradeCutPointsMatchTheBars() {
        XCTAssertEqual(JobScore.grade(60), .high)
        XCTAssertEqual(JobScore.grade(59), .medium)
        XCTAssertEqual(JobScore.grade(35), .medium)
        XCTAssertEqual(JobScore.grade(34), .low)
        XCTAssertEqual(JobScore.grade(0), .low)
    }

    func testRowScoreFollowsTheSort() {
        var j = job("a", "A", "T")
        XCTAssertNil(JobScore.forRow(j, sort: .standard), "no score is no badge, not a zero")
        j.relevance = 80
        XCTAssertEqual(JobScore.forRow(j, sort: .standard)?.basis, .relevance, "priority absent: relevance")
        j.priority = 45
        XCTAssertEqual(JobScore.forRow(j, sort: .standard), JobScore(value: 45, basis: .priority, grade: .medium))
        XCTAssertEqual(JobScore.forRow(j, sort: .fresh)?.value, 45)
        XCTAssertEqual(JobScore.forRow(j, sort: .relevance), JobScore(value: 80, basis: .relevance, grade: .high))
        j.priority = .nan
        XCTAssertEqual(JobScore.forRow(j, sort: .priority)?.basis, .relevance, "NaN priority falls back")
        j.relevance = .nan
        XCTAssertNil(JobScore.forRow(j, sort: .priority))
    }

    // MARK: state lines

    func testSortCaptionNamesSortAndBadge() {
        XCTAssertEqual(JobListPresentation.sortCaption(.standard), "Sorted by Rank, then fit. Badge: priority.")
        XCTAssertEqual(JobListPresentation.sortCaption(.relevance), "Sorted by Relevance. Badge: relevance.")
        for s in JobSort.allCases { XCTAssertTrue(JobListPresentation.sortCaption(s).contains(s.title)) }
    }

    func testCountsAreUnknownNotZero() {
        XCTAssertNil(JobListPresentation.count(nil, .new))
        var page = JobsPage(view: "new", jobs: [], total: 0, shown: 0, counts: ["new": 0])
        XCTAssertEqual(JobListPresentation.count(page, .new), 0, "a reported zero is a zero")
        XCTAssertNil(JobListPresentation.count(page, .saved), "a count vani did not send is unknown")
        page.counts["saved"] = 4
        XCTAssertEqual(JobListPresentation.count(page, .saved), 4)
    }

    func testTriageBannerAndCapNotice() {
        let us = Locale(identifier: "en_US")
        XCTAssertNil(JobListPresentation.triageBanner(nil, locale: us), "unknown pending is not shown as zero")
        XCTAssertNil(JobListPresentation.triageBanner(PendingTriage(total: 0, matching: 0, companies: []), locale: us))
        XCTAssertEqual(JobListPresentation.triageBanner(PendingTriage(total: 1186, matching: 1186, companies: []), locale: us),
                       "1,186 awaiting triage")
        XCTAssertEqual(JobListPresentation.capNotice(shown: 1000, total: 1682, locale: us), "Showing 1,000 of 1,682")
        XCTAssertNil(JobListPresentation.capNotice(shown: 5, total: 5, locale: us))
        XCTAssertNil(JobListPresentation.capNotice(shown: 9, total: 3, locale: us))
        XCTAssertNil(JobListPresentation.capNotice(shown: -1, total: 3, locale: us))
    }

    private func insight(_ json: String) throws -> JobsInsight {
        try JobsClient.decoder.decode(JobsInsight.self, from: Data(json.utf8))
    }

    func testInsightWarningShownOnlyWhenPresent() throws {
        let w = "knowledge-base matching has no scores yet; run `vani semantic warm`"
        XCTAssertEqual(JobListPresentation.insightWarning(try insight(#"{"target":true,"warning":"\#(w)"}"#)), w)
        XCTAssertEqual(JobListPresentation.insightWarning(try insight(#"{"target":true,"warning":"  \#(w)\n"}"#)), w, "trimmed")
        XCTAssertNil(JobListPresentation.insightWarning(nil), "no insight block: an older vani")
        XCTAssertNil(JobListPresentation.insightWarning(try insight(#"{"target":true}"#)))
        XCTAssertNil(JobListPresentation.insightWarning(try insight(#"{"target":true,"warning":null}"#)))
        XCTAssertNil(JobListPresentation.insightWarning(try insight(#"{"target":true,"warning":""}"#)))
        XCTAssertNil(JobListPresentation.insightWarning(try insight(#"{"target":true,"warning":" \t\n "}"#)))
    }

    func testParagraphs() {
        XCTAssertEqual(JobListPresentation.paragraphs(""), [])
        XCTAssertEqual(JobListPresentation.paragraphs(" \n \n\t"), [])
        XCTAssertEqual(JobListPresentation.paragraphs("One\r\nstill one\r\n\r\n\r\nTwo\n  \nThree "),
                       ["One\nstill one", "Two", "Three"])
        XCTAssertEqual(JobListPresentation.paragraphs("- a\n- b"), ["- a\n- b"])
        let big = (0..<10_000).map { "p\($0)" }.joined(separator: "\n\n")
        XCTAssertEqual(JobListPresentation.paragraphs(big).count, 10_000)
    }
}
