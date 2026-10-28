import Foundation
import XCTest
@testable import VaniApplyKit

private func jobJSON(_ extra: String) -> String {
    #"{"key":"gh:acme:1","company":"Acme","title":"Inference Engineer","status":"digested"\#(extra)}"#
}

private func decode(_ json: String) throws -> Job {
    try JSONDecoder().decode(Job.self, from: Data(json.utf8))
}

private let repeatNote = #"{"count":2,"window_days":60,"latest_key":"gh:acme:9","latest_title":"Platform Engineer","latest_at":"2026-10-20T00:00:00Z","text":"You applied to 2 other roles at Acme in the last 60 days (most recent: Platform Engineer, 2026-10-20). Some application systems silently reject a repeat application to one company."}"#
private let repostNote = #"{"of_key":"gh:acme:3","title":"Inference Engineer","first_seen":"2026-08-01T00:00:00Z","text":"likely repost of Inference Engineer first seen 2026-08-01"}"#

/// The same-company notes (`repeat_apply`, `repost`; internal/jobview/same.go) as the Jobs detail reads them:
/// display only, so a note this app cannot read is no note and never fails the job, and with it the list.
final class JobSameCompanyTests: XCTestCase {
    func testDecodesBothNotesAndOrdersRepeatApplyFirst() throws {
        let j = try decode(jobJSON(#","repost":\#(repostNote),"repeat_apply":\#(repeatNote)"#))
        XCTAssertEqual(j.repeatApply?.count, 2)
        XCTAssertEqual(j.repeatApply?.latestTitle, "Platform Engineer")
        XCTAssertEqual(j.repeatApply?.latestAt, "2026-10-20T00:00:00Z")
        XCTAssertEqual(j.repost?.ofKey, "gh:acme:3")
        XCTAssertEqual(j.repost?.firstSeen, "2026-08-01T00:00:00Z")
        XCTAssertEqual(j.sameCompanyNotes.map(\.kind), [.repeatApply, .repost])
        XCTAssertEqual(j.sameCompanyNotes[1].text, "likely repost of Inference Engineer first seen 2026-08-01")
        XCTAssertTrue(j.sameCompanyNotes[0].text.hasPrefix("You applied to 2 other roles at Acme"))
    }

    func testEachNoteAloneShowsOnlyItself() throws {
        XCTAssertEqual(try decode(jobJSON(#","repeat_apply":\#(repeatNote)"#)).sameCompanyNotes.map(\.kind), [.repeatApply])
        XCTAssertEqual(try decode(jobJSON(#","repost":\#(repostNote)"#)).sameCompanyNotes.map(\.kind), [.repost])
    }

    /// An older vani sends neither: no note, and the job reads as before.
    func testAnOlderVaniSendsNoNotes() throws {
        let j = try decode(jobJSON(""))
        XCTAssertNil(j.repeatApply)
        XCTAssertNil(j.repost)
        XCTAssertEqual(j.sameCompanyNotes, [])
        XCTAssertEqual(j.key, "gh:acme:1")
    }

    func testAMalformedNoteIsNilAndNeverFailsTheJob() throws {
        for bad in ["42", #""text""#, "null", "[]", "{}", #"{"count":"two","text":"x"}"#, #"{"text":5}"#] {
            let j = try decode(jobJSON(#","repeat_apply":\#(bad),"repost":\#(bad)"#))
            XCTAssertEqual(j.key, "gh:acme:1", "\(bad)")
            XCTAssertNil(j.repeatApply, "\(bad)")
            XCTAssertNil(j.repost, "\(bad)")
            XCTAssertEqual(j.sameCompanyNotes, [], "\(bad)")
        }
    }

    func testOneMalformedNoteLeavesTheOtherReadable() throws {
        let j = try decode(jobJSON(#","repeat_apply":"junk","repost":\#(repostNote)"#))
        XCTAssertNil(j.repeatApply)
        XCTAssertEqual(j.sameCompanyNotes.map(\.kind), [.repost])
    }

    func testABlankOrOversizedSentenceIsBoundedOrDropped() throws {
        let blank = #"{"of_key":"k","title":"t","first_seen":"2026-08-01T00:00:00Z","text":"  \n "}"#
        XCTAssertEqual(try decode(jobJSON(#","repost":\#(blank)"#)).sameCompanyNotes, [])
        let long = String(repeating: "x", count: 5000)
        let big = #"{"of_key":"k","title":"t","first_seen":"2026-08-01T00:00:00Z","text":"\#(long)"}"#
        let notes = try decode(jobJSON(#","repost":\#(big)"#)).sameCompanyNotes
        XCTAssertEqual(notes.count, 1)
        XCTAssertEqual(notes[0].text.count, SameCompanyNote.maxRunes + 1)
        XCTAssertTrue(notes[0].text.hasSuffix("…"))
    }

    func testAListWithNotesDecodesAndRoundTrips() throws {
        let list = #"{"view":"all","jobs":[\#(jobJSON(#","repeat_apply":\#(repeatNote),"repost":\#(repostNote)"#)),\#(jobJSON(#","repost":7"#).replacingOccurrences(of: "gh:acme:1", with: "gh:acme:2"))],"total":2,"shown":2,"counts":{"all":2}}"#
        let page = try JSONDecoder().decode(JobsPage.self, from: Data(list.utf8))
        XCTAssertEqual(page.jobs.map { $0.sameCompanyNotes.count }, [2, 0])
        let again = try JSONDecoder().decode(Job.self, from: JSONEncoder().encode(page.jobs[0]))
        XCTAssertEqual(again, page.jobs[0])
    }
}
