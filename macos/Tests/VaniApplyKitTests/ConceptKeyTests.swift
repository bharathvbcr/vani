import Foundation
import XCTest
@testable import VaniApplyKit

/// Classifier.conceptKey against the vectors generated from the TypeScript one,
/// and the duplicate guard in Editing that is built on it.
final class ConceptKeyTests: XCTestCase {
    struct Vectors: Decodable {
        struct Case: Decodable { let label: String; let autocomplete: String; let want: String }
        let cases: [Case]
    }

    func loadVectors() throws -> Vectors {
        // Tests/VaniApplyKitTests/<file> -> macos -> Vani/extension/test/vectors
        let url = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("extension/test/vectors/concept.json")
        return try JSONDecoder().decode(Vectors.self, from: Data(contentsOf: url))
    }

    func testConceptKeyMatchesTypeScript() throws {
        let v = try loadVectors()
        XCTAssertGreaterThanOrEqual(v.cases.count, 60, "vector file looks truncated")
        XCTAssertTrue(v.cases.contains { $0.want.isEmpty } && v.cases.contains { $0.label.utf16.count > 300 })
        for c in v.cases {
            XCTAssertEqual(Classifier.conceptKey(label: c.label, autocomplete: c.autocomplete), c.want,
                           "conceptKey(\(String(reflecting: c.label).prefix(50)), \(c.autocomplete))")
        }
    }

    private func bank(_ rows: Answer...) -> Bank {
        Bank(answers: Dictionary(uniqueKeysWithValues: rows.map { ($0.key, $0) }))
    }

    func testNewAnswerRefusesASiblingWordingAndNamesTheExistingKey() throws {
        let b = bank(answer("linkedin"))
        XCTAssertThrowsError(try Editing.newAnswer(question: "LinkedIn profile URL", kind: .text, text: "https://l/in/a", in: b)) {
            XCTAssertEqual($0 as? EditError, .duplicate("linkedin"))
        }
    }

    func testAnotherWordingOfAnIntentIsADuplicate() {
        var held = answer("authorized to work", "Yes")
        held.kind = .radio
        let b = bank(held)
        XCTAssertThrowsError(try Editing.newAnswer(question: "Are you legally authorized to work in the US?", kind: .select, text: "No", in: b)) {
            XCTAssertEqual($0 as? EditError, .duplicate("authorized to work"))
        }
    }

    func testChoiceAndTextKindsOfOneConceptAreNotDuplicates() throws {
        let b = bank(answer("country of residence"))
        let a = try Editing.newAnswer(question: "Residence country", kind: .select, text: "Peru", in: b)
        XCTAssertEqual(a.key, "residence country")
    }

    func testDifferentSensitivityIsNotADuplicate() throws {
        var held = answer("info status")
        held.sensitivity = .selfID
        // "Info status" classifies standard, the held row is self-ID: two rows.
        let a = try Editing.newAnswer(question: "Status of info", kind: .text, text: "x", in: bank(held))
        XCTAssertEqual(a.key, "status of info")
    }

    func testEmptyConceptNeverMatches() throws {
        var held = answer("ab")
        held.labels = ["ab", "url"]
        XCTAssertNil(Editing.owner(of: "Profile URL", kind: .text, in: bank(held)))
        let a = try Editing.newAnswer(question: "Profile URL", kind: .text, text: "x", in: bank(held))
        XCTAssertEqual(a.key, "profile url")
    }

    func testOwnerPrefersMostUsedThenNewestThenKey() {
        var a = answer("a1"); a.labels = ["a1", "linkedin"]; a.uses = 5; a.updatedAt = "2026-01-01T00:00:00.000Z"
        var b = answer("a0"); b.labels = ["a0", "linkedin"]; b.uses = 5; b.updatedAt = "2026-02-01T00:00:00.000Z"
        var c = answer("a2"); c.labels = ["a2", "linkedin"]; c.uses = 1; c.updatedAt = "2026-03-01T00:00:00.000Z"
        XCTAssertEqual(Editing.owner(of: "LinkedIn", kind: .text, in: bank(a, b, c))?.key, "a0")
        b.updatedAt = a.updatedAt
        XCTAssertEqual(Editing.owner(of: "LinkedIn profile", kind: .text, in: bank(a, b, c))?.key, "a0")
        a.uses = 6
        XCTAssertEqual(Editing.owner(of: "LinkedIn profile", kind: .text, in: bank(a, b, c))?.key, "a1")
    }

    func testSavingADraftUpdatesTheSiblingOwner() throws {
        var held = answer("linkedin", "old")
        held.labels = ["linkedin"]
        let saved = try Editing.savingDraft(question: "LinkedIn Profile URL", text: "new", in: bank(held))
        XCTAssertEqual(saved.key, "linkedin")
        XCTAssertEqual(saved.value, ["new"])
        XCTAssertTrue(saved.review)
    }
}
