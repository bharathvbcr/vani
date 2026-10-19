import Foundation
import XCTest
@testable import VaniApplyKit

final class EditingTests: XCTestCase {
    func testNewAnswerUsesTheNormalizedQuestionAsKey() throws {
        let a = try Editing.newAnswer(question: "Preferred First Name *", kind: .text, text: "  Ada  ", in: .empty)
        XCTAssertEqual(a.key, "preferred first name")
        XCTAssertEqual(a.value, ["Ada"])
        XCTAssertEqual(a.sensitivity, .standard)
        XCTAssertFalse(a.review)
        XCTAssertNoThrow(try BankValidator.validate(Bank(answers: [a.key: a])))
    }

    func testRefusesSecretsEmptiesDuplicatesAndOverflow() throws {
        XCTAssertThrowsError(try Editing.newAnswer(question: "Passport number", kind: .text, text: "X1", in: .empty)) {
            XCTAssertEqual($0 as? EditError, .secret)
        }
        XCTAssertThrowsError(try Editing.newAnswer(question: "?", kind: .text, text: "x", in: .empty)) {
            XCTAssertEqual($0 as? EditError, .emptyQuestion)
        }
        XCTAssertThrowsError(try Editing.newAnswer(question: "City", kind: .text, text: "  \n ", in: .empty)) {
            XCTAssertEqual($0 as? EditError, .emptyAnswer)
        }
        let bank = Bank(answers: ["city": answer("city")])
        XCTAssertThrowsError(try Editing.newAnswer(question: "City", kind: .text, text: "Paris", in: bank)) {
            XCTAssertEqual($0 as? EditError, .duplicate("city"))
        }
        XCTAssertThrowsError(try Editing.newAnswer(question: "Essay", kind: .textarea, text: String(repeating: "x", count: 10_001), in: .empty)) {
            XCTAssertEqual($0 as? EditError, .tooLong)
        }
        var full = Bank()
        for i in 0..<Limits.maxAnswers { full.answers["question \(i)"] = answer("question \(i)") }
        XCTAssertThrowsError(try Editing.newAnswer(question: "One more", kind: .text, text: "x", in: full)) {
            XCTAssertEqual($0 as? EditError, .full)
        }
    }

    func testChoiceAnswersAreOneOptionPerLine() throws {
        let a = try Editing.newAnswer(question: "Which languages?", kind: .checkbox, text: "Go\n\n Rust \nSwift", in: .empty)
        XCTAssertEqual(a.value, ["Go", "Rust", "Swift"])
        XCTAssertEqual(Editing.text(of: a), "Go\nRust\nSwift")
    }

    func testLongTextIsADraftAndSelfIDIsMarked() throws {
        let essay = try Editing.newAnswer(question: "Why us?", kind: .textarea, text: String(repeating: "word ", count: 80), in: .empty)
        XCTAssertTrue(essay.review)
        let edited = try Editing.edited(essay, text: "Short now.")
        XCTAssertFalse(edited.review)
        XCTAssertEqual(edited.key, essay.key)
        XCTAssertEqual(try Editing.newAnswer(question: "Veteran status", kind: .select, text: "I am not a veteran", in: .empty).sensitivity, .selfID)
    }

    func testSavedDraftsAreAlwaysForReview() throws {
        let fresh = try Editing.savingDraft(question: "Why Modal?", text: "Short.", in: .empty)
        XCTAssertTrue(fresh.review)
        XCTAssertEqual(fresh.kind, .textarea)
        var bank = Bank(answers: ["why modal": answer("why modal", "old")])
        let replaced = try Editing.savingDraft(question: "Why Modal?", text: "New draft.", in: bank)
        XCTAssertEqual(replaced.value, ["New draft."])
        XCTAssertTrue(replaced.review)
        bank.answers[replaced.key] = replaced
        XCTAssertNoThrow(try BankValidator.validate(bank))
        XCTAssertThrowsError(try Editing.savingDraft(question: "Passport number", text: "x", in: .empty))
    }

    func testHostnames() {
        XCTAssertEqual(Editing.hostname(" Jobs.Lever.co "), "jobs.lever.co")
        XCTAssertEqual(Editing.hostname("https://boards.greenhouse.io/acme/jobs/1"), "boards.greenhouse.io")
        for bad in ["", "localhost", "a b.com", ".x.com", "x.com.", "javascript:alert(1)", "evil.com/../x"] {
            XCTAssertNil(Editing.hostname(bad), bad)
        }
    }

    func testExportRoundTripsThroughImport() throws {
        let bank = Bank(answers: ["first name": answer("first name", "Ada")], settings: Settings(captureSelfId: true, disabledHosts: ["x.example"]))
        let back = try BankValidator.validate(json: Editing.exportData(bank))
        XCTAssertEqual(back.bank, bank)
    }
}
