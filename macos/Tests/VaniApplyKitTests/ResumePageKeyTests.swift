import Foundation
import XCTest
@testable import VaniApplyKit

/// pageKey and postingKey against the vectors the extension pins, plus Workday's routes.
final class ResumePageKeyTests: XCTestCase {
    struct File: Decodable {
        struct Case: Decodable {
            let `in`: String
            let out: String?
        }
        let cases: [Case]
    }

    func load() throws -> File {
        let url = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("extension/test/vectors/joburl.json")
        return try JSONDecoder().decode(File.self, from: Data(contentsOf: url))
    }

    func testPageKeyMatchesTheSharedVectors() throws {
        let v = try load()
        XCTAssertGreaterThan(v.cases.count, 40, "vector file looks truncated")
        for c in v.cases {
            XCTAssertEqual(ResumePageKey.pageKey(c.in), c.out, c.in)
            XCTAssertEqual(ResumePageKey.postingKey(c.in), c.out, "postingKey \(c.in)")
        }
    }

    func testWorkdayRoutesShareOnePostingKey() {
        let h = "https://utaustin.wd1.myworkdayjobs.com"
        let slug = "Forward-Deployed-Engineer_R_00049483"
        let one = "\(h)/UTstaff/job/\(slug)"
        let shapes = [
            "\(h)/UTstaff/job/AUSTIN-TX/\(slug)",
            "\(h)/en-US/UTstaff/job/AUSTIN-TX/\(slug)",
            "\(h)/en-US/UTstaff/details/\(slug)?q=Forward+Deployed+Engineer",
            "\(h)/en-US/UTstaff/details/\(slug)",
            "\(h)/fr-FR/UTstaff/job/AUSTIN-TX/\(slug)/",
            "\(h)/UTstaff/job/\(slug)",
            "\(h.uppercased().replacingOccurrences(of: "HTTPS", with: "https"))/UTstaff/job/AUSTIN-TX/\(slug)#x",
            "\(h)/en-US/UTstaff/job/AUSTIN%2C-TX/\(slug)/apply/applyManually",
            "\(h)/en-US/UTstaff/job/AUSTIN-TX/\(slug)/apply",
            "\(h)/en-US/UTstaff/job/AUSTIN-TX/\(slug)/apply/autofillWithResume",
            "\(h)/UTstaff/job/AUSTIN-TX/\(slug)/Apply/applyManually?source=x",
            "\(h)/en-US/UTstaff/job/\(slug)",
        ]
        for s in shapes {
            XCTAssertEqual(ResumePageKey.postingKey(s), one, s)
        }
        XCTAssertNotEqual(ResumePageKey.postingKey("\(h)/UTstaff/job/AUSTIN-TX/Forward-Deployed-Engineer_R_00049484"), one)
        XCTAssertNotEqual(ResumePageKey.postingKey("\(h)/Other/job/AUSTIN-TX/\(slug)"), one)
        XCTAssertNotEqual(ResumePageKey.postingKey("https://other.wd1.myworkdayjobs.com/UTstaff/job/AUSTIN-TX/\(slug)"), one)
    }

    func testAWorkdayPageThatIsNotAPostingKeepsItsPageKey() {
        let h = "https://utaustin.wd1.myworkdayjobs.com"
        let slug = "Forward-Deployed-Engineer_R_00049483"
        for s in ["\(h)/UTstaff?q=engineer", "\(h)/en-US/UTstaff", "\(h)/en-US/UTstaff/login", "\(h)/en-US/Jobs",
                  "\(h)/UTstaff/job", "\(h)/UTstaff/job/apply", "\(h)/en-US/UTstaff/details", h] {
            XCTAssertEqual(ResumePageKey.postingKey(s), ResumePageKey.pageKey(s), s)
        }
        XCTAssertEqual(ResumePageKey.postingKey("\(h)/ext/job/NY/\(slug)"), "\(h)/ext/job/\(slug)")
        XCTAssertEqual(ResumePageKey.postingKey("\(h)/details/job/NY/\(slug)"), "\(h)/details/job/\(slug)")
    }

    func testTheSharedWorkdayHostUsesRecruitingTenantSite() {
        let s = "https://wd1.myworkdaysite.com"
        let one = "\(s)/recruiting/snapchat/snap/job/ML_R1-1"
        for raw in [
            "\(s)/recruiting/snapchat/snap/job/Paris-France/ML_R1-1",
            "\(s)/en-US/recruiting/snapchat/snap/job/Paris-France/ML_R1-1",
            "\(s)/fr-FR/recruiting/snapchat/snap/details/ML_R1-1",
            "\(s)/recruiting/snapchat/snap/job/Paris-France/ML_R1-1/apply/applyManually?x=1",
        ] {
            XCTAssertEqual(ResumePageKey.postingKey(raw), one, raw)
        }
        XCTAssertNotEqual(ResumePageKey.postingKey("\(s)/recruiting/othertenant/snap/job/Paris-France/ML_R1-1"), one)
        XCTAssertNotEqual(ResumePageKey.postingKey("\(s)/recruiting/snapchat/other/job/Paris-France/ML_R1-1"), one)
        for raw in ["\(s)/recruiting/snapchat/snap", "\(s)/recruiting/snapchat/snap/login", "\(s)/snap/job/NY/ML_R1-1",
                    "\(s)/careers/snapchat/snap/job/NY/ML_R1-1", "\(s)/recruiting/snapchat/snap/job/apply",
                    "https://snapchat.wd1.myworkdaysite.com/recruiting/a/b/job/NY/R1"] {
            XCTAssertEqual(ResumePageKey.postingKey(raw), ResumePageKey.pageKey(raw), raw)
        }
    }

    func testAHostThatOnlyContainsTheSuffixIsNotWorkday() {
        let slug = "Forward-Deployed-Engineer_R_00049483"
        for h in ["https://myworkdayjobs.com.evil.example", "https://evilmyworkdayjobs.com", "https://x.myworkdayjobs.com.evil.example"] {
            let u = "\(h)/en-US/UTstaff/job/AUSTIN-TX/\(slug)"
            XCTAssertEqual(ResumePageKey.postingKey(u), ResumePageKey.pageKey(u), u)
        }
    }

    func testRefusesWhatTheExtensionRefuses() {
        XCTAssertNil(ResumePageKey.pageKey(""))
        XCTAssertNil(ResumePageKey.postingKey("file:///x"))
        XCTAssertNil(ResumePageKey.postingKey("https://"))
        XCTAssertNil(ResumePageKey.postingKey(String(repeating: "x", count: ResumePageKey.maxBytes + 1)))
        let long = "https://utaustin.wd1.myworkdayjobs.com/UTstaff/job/AUSTIN-TX/" + String(repeating: "a", count: ResumePageKey.maxBytes)
        XCTAssertNil(ResumePageKey.postingKey(long))
        XCTAssertNil(ResumePageKey.pageKey("https://example.com/jobs/1 2"))
        XCTAssertEqual(ResumePageKey.pageKey("https://example.com/jobs/1\n"), "https://example.com/jobs/1")
        XCTAssertNil(ResumePageKey.pageKey("https://exa\nmple.com/jobs/1"))
    }
}
