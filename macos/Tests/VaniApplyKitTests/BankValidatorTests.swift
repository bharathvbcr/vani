import Foundation
import XCTest
@testable import VaniApplyKit

final class BankValidatorTests: XCTestCase {
    func refused(_ raw: Any, _ contains: String, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertThrowsError(try BankValidator.validate(raw), file: file, line: line) { err in
            XCTAssertTrue(String(describing: err).contains(contains), "\(err) should mention \(contains)", file: file, line: line)
        }
    }

    func testAcceptsAWellFormedBank() throws {
        let r = try BankValidator.validate(bankJSON([answerJSON("first name", value: ["Ada"])],
                                                    settings: ["captureSelfId": true, "disabledHosts": ["x.example"]]))
        XCTAssertEqual(r.droppedSecret, 0)
        XCTAssertEqual(r.bank.answers["first name"]?.value, ["Ada"])
        XCTAssertTrue(r.bank.settings.captureSelfId)
        XCTAssertEqual(r.bank.settings.disabledHosts, ["x.example"])
    }

    func testRefusesMalformedShapes() {
        refused([1, 2], "not an object")
        refused(bankJSON([], version: 2), "version")
        refused(bankJSON([], version: "1"), "version")
        refused(bankJSON([], version: true), "version") // JSON true is not 1
        refused(["version": 1, "answers": []], "answers is not an object")
        refused(bankJSON([answerJSON("k")]).merging(["answers": ["ab": answerJSON("other")]]) { $1 }, "malformed")
        refused(bankJSON([answerJSON("x")]), "malformed") // key shorter than 2
        refused(bankJSON([answerJSON(String(repeating: "k", count: 201))]), "malformed")
        refused(bankJSON([answerJSON("ab", kind: "widget")]), "unknown kind")
        refused(bankJSON([answerJSON("ab", value: [1])]), "malformed")
        refused(bankJSON([answerJSON("ab", value: [String(repeating: "x", count: 10_001)])]), "malformed")
        refused(bankJSON([answerJSON("ab", value: Array(repeating: "x", count: 201))]), "malformed")
        refused(bankJSON([answerJSON("ab", labels: Array(repeating: "x", count: 21))]), "malformed")
    }

    func testRefusesTooManyAnswers() {
        let many = (0..<2001).map { answerJSON("question \($0)") }
        refused(bankJSON(many), "max 2000")
    }

    func testAcceptsABankOver1MBAndRefusesOverByteCap() throws {
        // 100 answers x 10 values x 1000 chars is ~1 MB: accepted now with chunking
        let big = (0..<100).map { answerJSON("question \($0)", value: Array(repeating: String(repeating: "y", count: 1000), count: 10)) }
        let result = try BankValidator.validate(bankJSON(big))
        XCTAssertEqual(result.bank.answers.count, 100)
    }

    func testDropsAndCountsSecretAnswers() throws {
        let r = try BankValidator.validate(bankJSON([
            answerJSON("social security number", value: ["123"]),
            answerJSON("notes", labels: ["Your password"]),
            answerJSON("first name"),
        ]))
        XCTAssertEqual(r.droppedSecret, 2)
        XCTAssertEqual(Array(r.bank.answers.keys), ["first name"])
    }

    func testRecomputesSensitivityInsteadOfTrustingIt() throws {
        let r = try BankValidator.validate(bankJSON([answerJSON("gender", extra: ["sensitivity": "standard"])]))
        XCTAssertEqual(r.bank.answers["gender"]?.sensitivity, .selfID)
    }

    func testNormalizesCountersFlagsAndTimes() throws {
        let r = try BankValidator.validate(bankJSON([
            answerJSON("aa", extra: ["uses": -1, "fills": 1.5, "review": 1]),
            answerJSON("bb", extra: ["uses": true, "fills": "3", "review": true, "updatedAt": String(repeating: "9", count: 60)]),
            answerJSON("cc", extra: ["uses": 9_007_199_254_740_992.0]),
        ], settings: ["captureSelfId": 1, "disabledHosts": "x"]))
        XCTAssertEqual(r.bank.answers["aa"]?.uses, 0)
        XCTAssertEqual(r.bank.answers["aa"]?.fills, 0)
        XCTAssertEqual(r.bank.answers["aa"]?.review, false)
        XCTAssertEqual(r.bank.answers["bb"]?.uses, 0)
        XCTAssertEqual(r.bank.answers["bb"]?.fills, 0)
        XCTAssertEqual(r.bank.answers["bb"]?.review, true)
        XCTAssertEqual(r.bank.answers["bb"]?.updatedAt.count, 40)
        XCTAssertEqual(r.bank.answers["cc"]?.uses, 0)
        XCTAssertFalse(r.bank.settings.captureSelfId)
        XCTAssertEqual(r.bank.settings.disabledHosts, [])
    }

    /// sqlite3_bind_int cannot take a counter past Int32.max. Keeping it would trap the mirror;
    /// clamping to Int32.max would store a lie. Int32.max stays. One past it, and the old safe-integer
    /// ceiling, become 0. A count that only steps by one from zero never approaches that ceiling.
    func testCountKeepsInt32MaxAndZeroesOnePastIt() throws {
        let max = Int(Int32.max)
        let r = try BankValidator.validate(bankJSON([
            answerJSON("aa", extra: ["uses": max, "fills": max]),
            answerJSON("bb", extra: ["uses": max + 1, "fills": max + 1]),
            answerJSON("cc", extra: ["uses": 9_007_199_254_740_992.0, "fills": 9_007_199_254_740_991.0]),
        ]))
        XCTAssertEqual(r.bank.answers["aa"]?.uses, max)
        XCTAssertEqual(r.bank.answers["aa"]?.fills, max)
        XCTAssertEqual(r.bank.answers["bb"]?.uses, 0)
        XCTAssertEqual(r.bank.answers["bb"]?.fills, 0)
        XCTAssertEqual(r.bank.answers["cc"]?.uses, 0)
        XCTAssertEqual(r.bank.answers["cc"]?.fills, 0)

        var uses = 0
        var fills = 0
        for _ in 0..<8 {
            uses += 1
            fills += 1
        }
        let stepped = try BankValidator.validate(bankJSON([
            answerJSON("dd", extra: ["uses": uses, "fills": fills]),
        ]))
        XCTAssertEqual(stepped.bank.answers["dd"]?.uses, 8)
        XCTAssertEqual(stepped.bank.answers["dd"]?.fills, 8)
        XCTAssertLessThan(uses, max)
    }

    func testImportFileLimits() {
        XCTAssertThrowsError(try BankValidator.validate(json: Data("{nope".utf8)))
        XCTAssertThrowsError(try BankValidator.validate(json: Data(repeating: 0x20, count: 8_000_001)))
    }

    func testRoundTripsThroughEncoding() throws {
        let r = try BankValidator.validate(bankJSON([answerJSON("why us", value: ["Because / of the team"])]))
        let again = try BankValidator.validate(r.bank)
        XCTAssertEqual(again.bank, r.bank)
        XCTAssertFalse(String(decoding: try r.bank.encoded(), as: UTF8.self).contains(#"\/"#))
    }
}
