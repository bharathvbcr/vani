import Foundation
import XCTest
@testable import VaniApplyKit

final class JobMatchMarkAttackTests: XCTestCase {
    private func ref(_ mark: String?) throws -> JobMatch {
        let m = mark.map { #","mark":"\#($0)""# } ?? ""
        let json = #"{"url":"u","matches":1,"job":{"key":"k","company":"A","title":"T","status":"digested","matched_on":"url"\#(m)}}"#
        return try JobsClient.decoder.decode(JobMatch.self, from: Data(json.utf8))
    }

    func testTwoDifferentUnknownMarksCompareEqual() throws {
        XCTAssertEqual(try ref("starred"), try ref("pinned"))
    }

    func testReencodingAnUnknownMarkWritesNoMark() throws {
        let m = try ref("starred")
        let data = try JSONEncoder().encode(m)
        let obj = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let job = try XCTUnwrap(obj["job"] as? [String: Any])
        XCTAssertFalse((job["mark"] as? String) != nil, "\(job)")
    }

    func testSettingMarkOnRefRoundTrips() throws {
        var m = try ref("starred")
        m.job?.mark = .applied
        XCTAssertEqual(m.job?.mark, .applied)
        m.job?.mark = nil
        XCTAssertNil(m.job?.mark)
        XCTAssertEqual(m, try ref(nil))
    }
}
