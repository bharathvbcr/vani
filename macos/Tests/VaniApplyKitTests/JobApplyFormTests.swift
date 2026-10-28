import XCTest
@testable import VaniApplyKit

/// The application-form note of `vani jobs list|show --json` (`apply_form_note`, worded by vani).
/// The app shows the line and decodes nothing else of the form: it is display-only like the
/// duplicate and logo fields, so a value of the wrong type reads as absent and never fails the list.
final class JobApplyFormTests: XCTestCase {
    func testDecodesTheNoteBesideTheRestOfTheRow() throws {
        let page = try JobsClient.decoder.decode(JobsPage.self, from: Data(#"""
        {"view":"all","jobs":[
          {"key":"ab:acme:1","company":"Acme","title":"T","status":"digested",
           "apply_form_note":"Application form: 4 of 6 questions required; you must be in one of: Portugal, Spain.",
           "apply_flags":["places"],
           "apply_form":{"v":1,"questions":6,"required":4,"gates":[{"kind":"place","text":"Where?","flag":"place_list"}]}},
          {"key":"ab:acme:2","company":"Acme","title":"T","status":"digested","apply_form_note":"Application form not read."},
          {"key":"gh:acme:3","company":"Acme","title":"T","status":"digested"}],
         "total":3,"shown":3,"counts":{}}
        """#.utf8))
        XCTAssertEqual(page.jobs.map(\.key), ["ab:acme:1", "ab:acme:2", "gh:acme:3"])
        XCTAssertEqual(page.jobs[0].applyFormNote, "Application form: 4 of 6 questions required; you must be in one of: Portugal, Spain.")
        XCTAssertEqual(page.jobs[1].applyFormNote, "Application form not read.", "not read is said in words")
        XCTAssertNil(page.jobs[2].applyFormNote, "an ATS whose form vani cannot read, or an older vani, sends none")
    }

    func testWrongTypedNoteDecodesAsNilAndKeepsTheList() throws {
        let page = try JobsClient.decoder.decode(JobsPage.self, from: Data(#"""
        {"view":"all","jobs":[
          {"key":"ab:a:1","company":"A","title":"T","status":"digested","apply_form_note":7},
          {"key":"ab:a:2","company":"A","title":"T","status":"digested","apply_form_note":["x"]},
          {"key":"ab:a:3","company":"A","title":"T","status":"digested","apply_form_note":{"text":"x"}},
          {"key":"ab:a:4","company":"A","title":"T","status":"digested","apply_form_note":null},
          {"key":"ab:a:5","company":"A","title":"T","status":"digested","apply_form_note":"Application form not read."}],
         "total":5,"shown":5,"counts":{}}
        """#.utf8))
        XCTAssertEqual(page.jobs.map(\.key), (1...5).map { "ab:a:\($0)" }, "a bad field never fails the list")
        for j in page.jobs.prefix(4) { XCTAssertNil(j.applyFormNote, j.key) }
        XCTAssertEqual(page.jobs[4].applyFormNote, "Application form not read.", "a well-typed row is still read")
    }

    func testNoteRoundTripsThroughTheEncoderTheListPathUses() throws {
        var j = Job(key: "ab:a:1", company: "A", title: "T", status: "digested")
        j.applyFormNote = "Application form not read."
        let back = try JobsClient.decoder.decode(Job.self, from: try JSONEncoder().encode(j))
        XCTAssertEqual(back.applyFormNote, "Application form not read.")
        XCTAssertNil(try JobsClient.decoder.decode(Job.self, from: try JSONEncoder().encode(Job(key: "ab:a:1", company: "A", title: "T", status: "digested"))).applyFormNote)
    }
}
