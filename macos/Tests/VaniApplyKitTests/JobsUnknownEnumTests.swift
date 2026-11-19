import Foundation
import XCTest
@testable import VaniApplyKit

/// One value this app does not know (a mark or an age source from a newer vani) degrades that field of
/// that row to nil; it never fails the row's neighbours or the whole list.
final class JobsUnknownEnumTests: XCTestCase {
    private func page(_ rows: [String]) -> Data {
        Data(("{\"view\":\"new\",\"jobs\":[" + rows.joined(separator: ",") + "],\"total\":\(rows.count),\"shown\":\(rows.count),\"counts\":{\"new\":\(rows.count)}}").utf8)
    }

    func testAnUnknownMarkOrFreshSourceDegradesOnlyThatField() throws {
        let good = #"{"key":"gh:a:1","company":"A","title":"T","status":"digested","mark":"saved","fresh_src":"posted"}"#
        let oddMark = #"{"key":"gh:b:2","company":"B","title":"U","status":"digested","mark":"starred","fresh_src":"seen"}"#
        let oddSource = #"{"key":"gh:c:3","company":"C","title":"V","status":"digested","mark":"applied","fresh_src":"guessed"}"#
        let p = try JobsClient.decoder.decode(JobsPage.self, from: page([good, oddMark, oddSource]))
        XCTAssertEqual(p.jobs.map(\.key), ["gh:a:1", "gh:b:2", "gh:c:3"], "no row is lost")
        XCTAssertEqual(p.jobs[0].mark, .saved)
        XCTAssertEqual(p.jobs[0].freshSource, .posted)
        XCTAssertNil(p.jobs[1].mark)
        XCTAssertEqual(p.jobs[1].freshSource, .seen)
        XCTAssertEqual(p.jobs[1].company, "B", "the rest of the row survives")
        XCTAssertEqual(p.jobs[2].mark, .applied)
        XCTAssertNil(p.jobs[2].freshSource)
    }

    func testMalformedMarkTypesDoNotFailTheList() throws {
        let rows = [#"{"key":"k:a:1","company":"A","title":"T","status":"live","mark":7,"fresh_src":{"x":1}}"#,
                    #"{"key":"k:b:2","company":"B","title":"T","status":"live","mark":null,"fresh_src":null}"#]
        let p = try JobsClient.decoder.decode(JobsPage.self, from: page(rows))
        XCTAssertEqual(p.jobs.count, 2)
        XCTAssertNil(p.jobs[0].mark)
        XCTAssertNil(p.jobs[1].mark)
    }

    func testAKnownMarkRoundTrips() throws {
        var j = Job(key: "gh:a:1", company: "A", title: "T")
        j.mark = .hidden
        j.freshSource = .seen
        let back = try JobsClient.decoder.decode(Job.self, from: JSONEncoder().encode(j))
        XCTAssertEqual(back.mark, .hidden)
        XCTAssertEqual(back.freshSource, .seen)
    }
}
