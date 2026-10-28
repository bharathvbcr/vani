import Foundation
import XCTest
@testable import VaniApplyKit

/// Leftover bank.json counters that do not fit in Int32, and values count() already turns into 0.
final class BankStoreInt32AdversarialTests: XCTestCase {
    private let sentence = "vani: bank.sqlite mirror skipped a row whose counter does not fit; bank.json is unaffected\n"
    private let max = 2_147_483_647
    private let onePast = 2_147_483_648

    private func quoted(_ s: String) throws -> String {
        let data = try JSONSerialization.data(withJSONObject: [s])
        let text = String(decoding: data, as: UTF8.self)
        return String(text.dropFirst().dropLast())
    }

    private func answerText(key: String, value: String, uses: String? = "1", fills: String? = "0") throws -> String {
        var fields = [
            "\"key\":\(try quoted(key))",
            "\"labels\":[\(try quoted(key))]",
            "\"kind\":\"text\"",
            "\"value\":[\(try quoted(value))]",
            "\"sensitivity\":\"standard\"",
            "\"signatures\":[]",
            "\"updatedAt\":\"2026-10-18T00:00:00.000Z\"",
            "\"review\":false",
        ]
        if let uses { fields.append("\"uses\":\(uses)") }
        if let fills { fields.append("\"fills\":\(fills)") }
        return "{" + fields.joined(separator: ",") + "}"
    }

    private func bankText(rev: Int = 1, answers: String) -> String {
        "{\"rev\":\(rev),\"bank\":{\"version\":1,\"answers\":{\(answers)}}}"
    }

    /// Writes bank.json, then opens the store. Migration logs during init, before read.
    private func openHandwritten(_ body: String, rev: Int = 1) throws -> (dir: URL, store: BankStore, err: String, snap: Snapshot, before: Data) {
        let dir = try tempDir(self)
        let url = dir.appendingPathComponent("bank.json")
        let before = Data(bankText(rev: rev, answers: body).utf8)
        try before.write(to: url)
        var store: BankStore!
        var snap: Snapshot!
        let err = try captureStderr {
            store = BankStore(directory: dir)
            snap = try store.read()
        }
        let after = try Data(contentsOf: url)
        XCTAssertEqual(after, before, "reading a bank must not rewrite bank.json")
        return (dir, store, err, snap, before)
    }

    private func jsonLiteral(_ literal: String) throws -> Any {
        let parsed = try JSONSerialization.jsonObject(with: Data("[\(literal)]".utf8))
        return try XCTUnwrap((parsed as? [Any])?.first)
    }

    private func mirror(_ store: BankStore) throws -> Snapshot {
        try XCTUnwrap(store.sqlite, "sqlite mirror did not open").read()
    }

    func testExplicitPutKeeps2147483647AndZeroes2147483648WithoutSkipLog() throws {
        XCTAssertEqual(max, Int(Int32.max))
        XCTAssertEqual(onePast, Int(Int32.max) + 1)
        let kept = try jsonLiteral("2147483647")
        let past = try jsonLiteral("2147483648")
        XCTAssertEqual(BankValidator.count(kept), max)
        XCTAssertEqual(BankValidator.count(past), 0)
        XCTAssertFalse(BankValidator.exceedsMirrorCounter(kept))
        XCTAssertTrue(BankValidator.exceedsMirrorCounter(past))

        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        let err = try captureStderr {
            XCTAssertEqual(try store.put(baseRev: 0, bank: bankJSON([
                answerJSON("role title", value: ["kept answer"], extra: ["uses": max, "fills": max]),
                answerJSON("city name", value: ["other answer"], extra: ["uses": onePast, "fills": onePast]),
            ])), .stored(rev: 1, droppedSecret: 0))
        }
        XCTAssertEqual(err, "", "the validator zeroed \(onePast) before the mirror, so the skip sentence is not logged")
        XCTAssertFalse(err.contains(sentence))
        let snap = try store.read()
        XCTAssertEqual(snap.bank.answers["role title"]?.uses, max)
        XCTAssertEqual(snap.bank.answers["role title"]?.fills, max)
        XCTAssertEqual(snap.bank.answers["city name"]?.uses, 0)
        XCTAssertEqual(snap.bank.answers["city name"]?.fills, 0)
        let sql = try mirror(store)
        XCTAssertEqual(sql.bank.answers["role title"]?.uses, max)
        XCTAssertEqual(sql.bank.answers["role title"]?.fills, max)
        XCTAssertEqual(sql.bank.answers["city name"]?.uses, 0)
        XCTAssertEqual(sql.bank.answers["city name"]?.fills, 0)
        XCTAssertNotNil(sql.bank.answers["city name"], "a zeroed counter is still mirrored")
        let onDisk = try String(contentsOf: dir.appendingPathComponent("bank.json"), encoding: .utf8)
        XCTAssertTrue(onDisk.contains("2147483647"))
        XCTAssertFalse(onDisk.contains("2147483648"))
    }

    func testOneFieldOverflowSkipsTheWholeRowAndKeepsTheSibling() throws {
        let wide = try answerText(key: "wide row", value: "wide answer", uses: "1", fills: "2147483648")
        let kept = try answerText(key: "kept role", value: "kept answer", uses: "4", fills: "5")
        let opened = try openHandwritten("\"wide row\":\(wide),\"kept role\":\(kept)", rev: 3)
        XCTAssertEqual(opened.err, sentence)
        XCTAssertEqual(opened.err.components(separatedBy: sentence).count - 1, 1)
        XCTAssertFalse(opened.err.contains("wide row"))
        XCTAssertFalse(opened.err.contains("wide answer"))
        XCTAssertFalse(opened.err.contains("kept role"))
        XCTAssertFalse(opened.err.contains("kept answer"))
        XCTAssertFalse(opened.err.contains("2147483648"))
        XCTAssertEqual(opened.snap.rev, 3)
        XCTAssertEqual(opened.snap.bank.answers["wide row"]?.uses, 1)
        XCTAssertEqual(opened.snap.bank.answers["wide row"]?.fills, 0)
        XCTAssertEqual(opened.snap.bank.answers["wide row"]?.value, ["wide answer"])
        XCTAssertEqual(opened.snap.bank.answers["kept role"]?.uses, 4)
        XCTAssertEqual(opened.snap.bank.answers["kept role"]?.fills, 5)
        let sql = try mirror(opened.store)
        XCTAssertEqual(sql.rev, 3)
        XCTAssertNil(sql.bank.answers["wide row"], "one unfit counter skips the whole row")
        XCTAssertEqual(Set(sql.bank.answers.keys), ["kept role"])
        XCTAssertEqual(sql.bank.answers["kept role"]?.uses, 4)
        XCTAssertEqual(sql.bank.answers["kept role"]?.fills, 5)
        let onDisk = try String(contentsOf: opened.dir.appendingPathComponent("bank.json"), encoding: .utf8)
        XCTAssertTrue(onDisk.contains("2147483648"))

        let saved = try captureStderr {
            let next = try opened.store.update { bank in
                bank.answers["kept role"]?.value = ["edited answer"]
            }
            XCTAssertEqual(next.rev, 4)
            XCTAssertEqual(next.bank.answers["wide row"]?.uses, 1)
            XCTAssertEqual(next.bank.answers["wide row"]?.fills, 0)
        }
        XCTAssertEqual(saved, "", "the later save writes the zeroed counter and does not skip it")
        let rewritten = try String(contentsOf: opened.dir.appendingPathComponent("bank.json"), encoding: .utf8)
        XCTAssertFalse(rewritten.contains("2147483648"))
        let after = try mirror(opened.store)
        XCTAssertEqual(after.bank.answers["wide row"]?.uses, 1)
        XCTAssertEqual(after.bank.answers["wide row"]?.fills, 0)
        XCTAssertEqual(after.bank.answers["kept role"]?.value, ["edited answer"])
        XCTAssertEqual(after.rev, 4)
    }

    func testNonNumbersBecomeZeroAndAreNotMirrorOverflow() throws {
        let cases: [(String, Int, Bool)] = [
            ("-1", 0, false),
            ("-2147483649", 0, false),
            ("1.5", 0, false),
            ("true", 0, false),
            ("false", 0, false),
            ("\"2147483648\"", 0, false),
            ("null", 0, false),
            ("0", 0, false),
            ("1.0", 1, false),
        ]
        for (literal, want, overflow) in cases {
            let value = try jsonLiteral(literal)
            XCTAssertEqual(BankValidator.count(value), want, literal)
            XCTAssertEqual(BankValidator.exceedsMirrorCounter(value), overflow, literal)
        }
        XCTAssertEqual(BankValidator.count(nil), 0)
        XCTAssertFalse(BankValidator.exceedsMirrorCounter(nil))

        let negative = answerJSON("neg row", value: ["neg value"], extra: ["uses": -1, "fills": 2])
        let fraction = answerJSON("frac row", value: ["frac value"], extra: ["uses": 3, "fills": 1.5])
        let yes = answerJSON("yes row", value: ["yes value"], extra: ["uses": true, "fills": 4])
        let no = answerJSON("no row", value: ["no value"], extra: ["uses": 5, "fills": false])
        let text = answerJSON("text row", value: ["text value"], extra: ["uses": "2147483648", "fills": 6])
        var absent = answerJSON("null row", value: ["null value"], extra: ["fills": 7])
        absent["uses"] = NSNull()
        var missing = answerJSON("omit row", value: ["omit value"])
        missing.removeValue(forKey: "uses")
        missing.removeValue(forKey: "fills")
        let raw = bankJSON([negative, fraction, yes, no, text, absent, missing])
        let dir = try tempDir(self)
        let url = dir.appendingPathComponent("bank.json")
        let before = try JSONSerialization.data(withJSONObject: ["rev": 1, "bank": raw])
        try before.write(to: url)
        var store: BankStore!
        var snap: Snapshot!
        let err = try captureStderr {
            store = BankStore(directory: dir)
            snap = try store.read()
        }
        XCTAssertEqual(try Data(contentsOf: url), before)
        XCTAssertEqual(err, "", "bool, fraction, string, null and negative are 0, which fits, so nothing is skipped")
        XCTAssertFalse(err.contains("2147483648"))
        XCTAssertFalse(err.contains("neg value") || err.contains("frac value") || err.contains("yes value"))
        let want: [String: (uses: Int, fills: Int)] = [
            "neg row": (0, 2),
            "frac row": (3, 0),
            "yes row": (0, 4),
            "no row": (5, 0),
            "text row": (0, 6),
            "null row": (0, 7),
            "omit row": (0, 0),
        ]
        for (key, counters) in want {
            XCTAssertEqual(snap.bank.answers[key]?.uses, counters.uses, key)
            XCTAssertEqual(snap.bank.answers[key]?.fills, counters.fills, key)
        }
        XCTAssertEqual(snap.bank.answers["yes row"]?.uses, 0, "JSON true is not the integer 1")
        let sql = try mirror(store)
        XCTAssertEqual(Set(sql.bank.answers.keys), Set(want.keys))
        for (key, counters) in want {
            let row = try XCTUnwrap(sql.bank.answers[key], key)
            XCTAssertEqual(row.uses, counters.uses, key)
            XCTAssertEqual(row.fills, counters.fills, key)
            XCTAssertGreaterThanOrEqual(row.uses, 0)
            XCTAssertLessThanOrEqual(row.uses, Int(Int32.max))
            XCTAssertGreaterThanOrEqual(row.fills, 0)
            XCTAssertLessThanOrEqual(row.fills, Int(Int32.max))
        }
    }

    func testVeryLargeFiniteNumbersAreSkippedWithoutLoggingDigits() throws {
        XCTAssertEqual(BankValidator.count(try jsonLiteral("9007199254740991")), 0)
        XCTAssertEqual(BankValidator.count(try jsonLiteral("1e20")), 0)
        XCTAssertTrue(BankValidator.exceedsMirrorCounter(try jsonLiteral("9007199254740991")))
        XCTAssertTrue(BankValidator.exceedsMirrorCounter(try jsonLiteral("1e20")))

        let safeInt = try answerText(key: "safe int", value: "safe int value", uses: "9007199254740991", fills: "1")
        let scientific = try answerText(key: "scientific", value: "scientific value", uses: "1", fills: "1e20")
        let kept = try answerText(key: "kept role", value: "kept answer", uses: "2", fills: "3")
        let opened = try openHandwritten("\"safe int\":\(safeInt),\"scientific\":\(scientific),\"kept role\":\(kept)")
        XCTAssertEqual(opened.err, sentence + sentence)
        XCTAssertFalse(opened.err.contains("9007199254740991"))
        XCTAssertFalse(opened.err.contains("1e20"))
        XCTAssertFalse(opened.err.contains("1e+20"))
        XCTAssertFalse(opened.err.contains("100000000000000000000"))
        XCTAssertFalse(opened.err.contains("safe int"))
        XCTAssertFalse(opened.err.contains("scientific"))
        XCTAssertFalse(opened.err.contains("safe int value"))
        XCTAssertFalse(opened.err.contains("scientific value"))
        XCTAssertEqual(opened.snap.bank.answers["safe int"]?.uses, 0)
        XCTAssertEqual(opened.snap.bank.answers["safe int"]?.fills, 1)
        XCTAssertEqual(opened.snap.bank.answers["scientific"]?.uses, 1)
        XCTAssertEqual(opened.snap.bank.answers["scientific"]?.fills, 0)
        XCTAssertEqual(opened.snap.bank.answers["safe int"]?.value, ["safe int value"])
        let sql = try mirror(opened.store)
        XCTAssertEqual(Set(sql.bank.answers.keys), ["kept role"])
        XCTAssertEqual(sql.bank.answers["kept role"]?.uses, 2)
        XCTAssertEqual(sql.bank.answers["kept role"]?.fills, 3)
        let onDisk = try String(contentsOf: opened.dir.appendingPathComponent("bank.json"), encoding: .utf8)
        XCTAssertTrue(onDisk.contains("9007199254740991"))
        XCTAssertTrue(onDisk.contains("1e20"))
    }

    func testDedupSumOfTwoInt32MaxStaysAtInt32MaxInTheStore() throws {
        let max = Int(Int32.max)
        for (left, right) in [(max, max), (1, max)] {
            var bank = Bank(answers: [
                "alpha linkedin": Answer(key: "alpha linkedin", labels: ["alpha linkedin"], kind: .text,
                                         value: ["synthetic answer alpha"], sensitivity: .standard, signatures: [],
                                         uses: left, fills: 0, updatedAt: "2026-10-18T00:00:00.000Z", review: false),
                "beta linkedin": Answer(key: "beta linkedin", labels: ["beta linkedin"], kind: .text,
                                        value: ["synthetic answer beta"], sensitivity: .standard, signatures: [],
                                        uses: right, fills: 0, updatedAt: "2026-10-18T00:00:00.000Z", review: false),
            ])
            try Dedup.merge(bank: &bank, plan: .init(survivor: "alpha linkedin", merged: ["beta linkedin"], valueFrom: "alpha linkedin"))
            let merged = try XCTUnwrap(bank.answers["alpha linkedin"]).uses
            XCTAssertEqual(merged, max, "\(left)+\(right) saturates at Int32.max")
            XCTAssertNotEqual(merged, 0)
            XCTAssertGreaterThan(merged, 1, "the sum is not a wrap back to a small counter")
            let store = BankStore(directory: try tempDir(self))
            let err = try captureStderr {
                XCTAssertEqual(try store.put(baseRev: 0, bank: bank), .stored(rev: 1, droppedSecret: 0))
            }
            XCTAssertEqual(err, "")
            XCTAssertEqual(try store.read().bank.answers["alpha linkedin"]?.uses, max)
            XCTAssertEqual(try mirror(store).bank.answers["alpha linkedin"]?.uses, max)
            XCTAssertNotEqual(try store.read().bank.answers["alpha linkedin"]?.uses, 0)
        }
    }

    func testTwoOverflowRowsLogTwiceAndASecondOpenDoesNotDuplicate() throws {
        let one = try answerText(key: "overflow one", value: "one value", uses: "2147483648", fills: "1")
        let two = try answerText(key: "overflow two", value: "two value", uses: "2", fills: "2147483648")
        let kept = try answerText(key: "kept role", value: "kept answer", uses: "6", fills: "7")
        let body = "\"overflow one\":\(one),\"overflow two\":\(two),\"kept role\":\(kept)"
        let opened = try openHandwritten(body, rev: 5)
        XCTAssertEqual(opened.err, sentence + sentence)
        XCTAssertFalse(opened.err.contains("overflow one") || opened.err.contains("overflow two"))
        XCTAssertFalse(opened.err.contains("one value") || opened.err.contains("two value") || opened.err.contains("kept answer"))
        XCTAssertFalse(opened.err.contains("2147483648"))
        let first = try mirror(opened.store)
        XCTAssertEqual(Set(first.bank.answers.keys), ["kept role"])
        XCTAssertEqual(first.bank.answers["kept role"]?.uses, 6)
        XCTAssertEqual(first.bank.answers["kept role"]?.fills, 7)
        XCTAssertEqual(first.rev, 5)

        var again: BankStore!
        let second = try captureStderr {
            again = BankStore(directory: opened.dir)
            _ = try again.read()
        }
        XCTAssertEqual(second, "", "opening the same file again does not migrate or log a second time")
        let rows = try mirror(again)
        XCTAssertEqual(rows.bank.answers.count, 1)
        XCTAssertEqual(rows.bank.answers["kept role"]?.uses, 6)
        XCTAssertEqual(rows.bank.answers["kept role"]?.fills, 7)
        XCTAssertEqual(rows.rev, 5)
        XCTAssertNil(rows.bank.answers["overflow one"])
        XCTAssertNil(rows.bank.answers["overflow two"])
    }

    func testEmptyAnswersObjectDoesNotLog() throws {
        let dir = try tempDir(self)
        let url = dir.appendingPathComponent("bank.json")
        let text = bankText(answers: "")
        try Data(text.utf8).write(to: url)
        var first: BankStore!
        let err = try captureStderr {
            first = BankStore(directory: dir)
            let snap = try first.read()
            XCTAssertEqual(snap.rev, 1)
            XCTAssertTrue(snap.bank.answers.isEmpty)
        }
        XCTAssertEqual(err, "")
        XCTAssertEqual(try mirror(first).bank.answers.count, 0)
        XCTAssertEqual(try mirror(first).rev, 1, "migration ran and still did not log")
        var second: BankStore!
        let again = try captureStderr {
            second = BankStore(directory: dir)
            XCTAssertTrue(try second.read().bank.answers.isEmpty)
        }
        XCTAssertEqual(again, "")
        XCTAssertEqual(try mirror(second).bank.answers.count, 0)
        XCTAssertEqual(try mirror(second).rev, 1)
    }

    func testUnicodeOverflowDoesNotLeakIntoTheSkipLog() throws {
        let key = "rôle café"
        let value = "λ-label"
        let row = try answerText(key: key, value: value, uses: "1", fills: "2147483648")
        let opened = try openHandwritten("\(try quoted(key)):\(row)")
        XCTAssertEqual(opened.err, sentence)
        XCTAssertFalse(opened.err.contains(key))
        XCTAssertFalse(opened.err.contains(value))
        XCTAssertFalse(opened.err.contains("2147483648"))
        XCTAssertFalse(opened.err.contains("café"))
        XCTAssertFalse(opened.err.contains("λ"))
        XCTAssertEqual(opened.snap.bank.answers[key]?.value, [value])
        XCTAssertEqual(opened.snap.bank.answers[key]?.uses, 1)
        XCTAssertEqual(opened.snap.bank.answers[key]?.fills, 0)
        XCTAssertNil(try mirror(opened.store).bank.answers[key])
        let onDisk = try String(contentsOf: opened.dir.appendingPathComponent("bank.json"), encoding: .utf8)
        XCTAssertTrue(onDisk.contains(key))
        XCTAssertTrue(onDisk.contains(value))
        XCTAssertTrue(onDisk.contains("2147483648"))
    }

    func testZeroAndMissingCountersAreMirrored() throws {
        let zero = try answerText(key: "zero row", value: "zero value", uses: "0", fills: "0")
        var missing = answerJSON("omit row", value: ["omit value"])
        missing.removeValue(forKey: "uses")
        missing.removeValue(forKey: "fills")
        let omit = String(decoding: try JSONSerialization.data(withJSONObject: missing), as: UTF8.self)
        XCTAssertFalse(omit.contains("\"uses\""))
        XCTAssertFalse(omit.contains("\"fills\""))
        XCTAssertEqual(BankValidator.count(try jsonLiteral("0")), 0)
        XCTAssertFalse(BankValidator.exceedsMirrorCounter(try jsonLiteral("0")))

        let opened = try openHandwritten("\"zero row\":\(zero),\"omit row\":\(omit)")
        XCTAssertEqual(opened.err, "")
        XCTAssertEqual(opened.snap.bank.answers["zero row"]?.uses, 0)
        XCTAssertEqual(opened.snap.bank.answers["zero row"]?.fills, 0)
        XCTAssertEqual(opened.snap.bank.answers["omit row"]?.uses, 0)
        XCTAssertEqual(opened.snap.bank.answers["omit row"]?.fills, 0)
        let sql = try mirror(opened.store)
        XCTAssertEqual(Set(sql.bank.answers.keys), ["zero row", "omit row"])
        XCTAssertEqual(sql.bank.answers["zero row"]?.uses, 0)
        XCTAssertEqual(sql.bank.answers["zero row"]?.fills, 0)
        XCTAssertEqual(sql.bank.answers["omit row"]?.uses, 0)
        XCTAssertEqual(sql.bank.answers["omit row"]?.fills, 0)
        let onDisk = try Data(contentsOf: opened.dir.appendingPathComponent("bank.json"))
        let root = try XCTUnwrap(JSONSerialization.jsonObject(with: onDisk) as? [String: Any])
        let answers = try XCTUnwrap((root["bank"] as? [String: Any])?["answers"] as? [String: Any])
        let zeroRow = try XCTUnwrap(answers["zero row"] as? [String: Any])
        XCTAssertEqual(BankValidator.count(zeroRow["uses"]), 0)
        XCTAssertEqual(BankValidator.count(zeroRow["fills"]), 0)
        let omitRow = try XCTUnwrap(answers["omit row"] as? [String: Any])
        XCTAssertNil(omitRow["uses"], "a missing counter stays missing on disk")
        XCTAssertNil(omitRow["fills"], "a missing counter stays missing on disk")
    }
}
