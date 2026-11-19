import Foundation
import XCTest
@testable import VaniApplyKit

/// The extension bridge reads `jobs find`; a mark from a newer vani must not fail the whole find.
final class JobMatchUnknownMarkTests: XCTestCase {
    func testAFindWithAnUnknownMarkStillDecodes() throws {
        let json = #"{"url":"https://x.test/j","matches":1,"job":{"key":"gh:a:1","company":"A","title":"T","status":"digested","mark":"starred","matched_on":"url"}}"#
        let m = try JobsClient.decoder.decode(JobMatch.self, from: Data(json.utf8))
        XCTAssertEqual(m.job?.key, "gh:a:1")
        XCTAssertNil(m.job?.mark)
    }

    func testAKnownMarkStillDecodesAndAnOddTypeReadsAsNil() throws {
        let known = #"{"url":"u","matches":1,"job":{"key":"k","company":"A","title":"T","status":"digested","mark":"applied","matched_on":"url"}}"#
        XCTAssertEqual(try JobsClient.decoder.decode(JobMatch.self, from: Data(known.utf8)).job?.mark, .applied)
        let odd = #"{"url":"u","matches":1,"job":{"key":"k","company":"A","title":"T","status":"digested","mark":7,"matched_on":"url"}}"#
        XCTAssertNil(try JobsClient.decoder.decode(JobMatch.self, from: Data(odd.utf8)).job?.mark)
        let absent = #"{"url":"u","matches":1,"job":{"key":"k","company":"A","title":"T","status":"digested","matched_on":"url"}}"#
        XCTAssertNil(try JobsClient.decoder.decode(JobMatch.self, from: Data(absent.utf8)).job?.mark)
    }

    func testTheBridgeAnswersAnUnknownMarkWithoutAMarkField() throws {
        let json = #"{"url":"u","matches":1,"job":{"key":"k","company":"A","title":"T","status":"digested","mark":"starred","matched_on":"url"}}"#
        let m = try JobsClient.decoder.decode(JobMatch.self, from: Data(json.utf8))
        let job = try XCTUnwrap(m.asJSONObject["job"] as? [String: Any])
        XCTAssertNil(job["mark"])
        XCTAssertEqual(job["key"] as? String, "k")
    }
}
