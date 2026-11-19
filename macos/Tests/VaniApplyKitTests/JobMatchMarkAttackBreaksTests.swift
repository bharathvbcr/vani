import Foundation
import XCTest
@testable import VaniApplyKit

final class JobMatchMarkAttackBreaksTests: XCTestCase {
    private func ref(_ mark: String?) throws -> JobMatch {
        let m = mark.map { #","mark":"\#($0)""# } ?? ""
        let json = #"{"url":"u","matches":1,"job":{"key":"k","company":"A","title":"T","status":"digested","matched_on":"url"\#(m)}}"#
        return try JobsClient.decoder.decode(JobMatch.self, from: Data(json.utf8))
    }

    /// Unknown, absent and empty marks all read as no mark; they should compare equal as values.
    func testUnknownAndAbsentMarksCompareEqual() throws {
        XCTAssertEqual(try ref("starred"), try ref(nil))
    }

}
