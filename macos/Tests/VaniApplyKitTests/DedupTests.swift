import Foundation
import XCTest
@testable import VaniApplyKit

final class DedupTests: XCTestCase {
    private func row(_ key: String, labels: [String]? = nil, kind: FieldKind = .text, value: [String] = ["v"],
                     sens: Sensitivity = .standard, signatures: [String] = [], uses: Int = 1, fills: Int = 0,
                     updatedAt: String = "2026-10-18T00:00:00.000Z", review: Bool = false) -> Answer {
        Answer(key: key, labels: labels ?? [key], kind: kind, value: value, sensitivity: sens, signatures: signatures,
               uses: uses, fills: fills, updatedAt: updatedAt, review: review)
    }

    private func bank(_ rows: [Answer], selfID: Bool = false) -> Bank {
        var b = Bank(settings: Settings(captureSelfId: selfID))
        for r in rows { b.answers[r.key] = r }
        return b
    }

    private var linkedIn: [Answer] {
        [
            row("linkedin", value: ["https://linkedin.com/in/old"], signatures: ["a|1", "b|2"], uses: 5, fills: 2,
                updatedAt: "2026-01-01T00:00:00.000Z"),
            row("linkedin profile url", labels: ["linkedin profile url", "your linkedin"], value: ["https://linkedin.com/in/new"],
                signatures: ["b|2", "c|3"], uses: 3, fills: 4, updatedAt: "2026-09-01T00:00:00.000Z", review: true),
            row("your linkedin profile", value: ["https://linkedin.com/in/mid"], uses: 1, fills: 1,
                updatedAt: "2026-05-01T00:00:00.000Z"),
        ]
    }

    // MARK: finding

    func testLinkedInRowsFormOneClusterWithSurvivorAndNewestValue() throws {
        let clusters = Dedup.clusters(bank: bank(linkedIn), similar: [])
        XCTAssertEqual(clusters.count, 1)
        let c = try XCTUnwrap(clusters.first)
        XCTAssertEqual(c.keys, ["linkedin", "linkedin profile url", "your linkedin profile"])
        XCTAssertEqual(c.survivor, "linkedin", "most used")
        XCTAssertTrue(c.defaultSelected)
        XCTAssertNil(c.blocked)
        XCTAssertEqual(c.valueChoices.map(\.fromKey), ["linkedin profile url", "your linkedin profile", "linkedin"])
        XCTAssertEqual(c.valueChoices.filter(\.preselected).map(\.fromKey), ["linkedin profile url"], "newest is preselected")
        XCTAssertTrue(c.reasons.contains { $0.hasPrefix("same concept") }, "\(c.reasons)")
    }

    func testSurvivorTieBreaksOnNewestThenKey() throws {
        let tie = bank([row("your linkedin profile", uses: 2, updatedAt: "2026-01-01T00:00:00.000Z"),
                        row("linkedin", uses: 2, updatedAt: "2026-01-01T00:00:00.000Z"),
                        row("linkedin profile url", uses: 2, updatedAt: "2026-02-01T00:00:00.000Z")])
        XCTAssertEqual(Dedup.clusters(bank: tie, similar: []).first?.survivor, "linkedin profile url", "newest")
        let tie2 = bank([row("your linkedin profile", uses: 2), row("linkedin profile url", uses: 2)])
        XCTAssertEqual(Dedup.clusters(bank: tie2, similar: []).first?.survivor, "linkedin profile url", "then the key")
    }

    func testIntentGroupsDifferentWordings() throws {
        let b = bank([row("are you legally authorized to work in the united states"),
                      row("do you have the right to work in the us", uses: 2)])
        let c = try XCTUnwrap(Dedup.clusters(bank: b, similar: []).first)
        XCTAssertEqual(c.keys.count, 2)
        XCTAssertTrue(c.reasons.contains { $0.hasPrefix("same intent") }, "\(c.reasons)")
    }

    func testIdenticalAnswersUnderDifferentQuestionsAreShownButNotSelected() throws {
        let b = bank([row("favourite colour", value: ["Ultramarine blue"]), row("shirt shade", value: ["ultramarine  Blue"]),
                      row("short one", value: ["yes"]), row("short two", value: ["yes"])])
        let clusters = Dedup.clusters(bank: b, similar: [])
        XCTAssertEqual(clusters.count, 1, "a generic answer such as yes does not group")
        XCTAssertFalse(clusters[0].defaultSelected)
        XCTAssertEqual(clusters[0].reasons, ["same answer, different question"])
    }

    func testSemanticPairsGroupAndBadPairsAreIgnored() throws {
        let b = bank([row("alpha"), row("beta"), row("gamma")])
        let pairs = [
            SimilarPair(a: "alpha", b: "beta", cos: 0.93),
            SimilarPair(a: "beta", b: "alpha", cos: 0.50),      // the same pair again
            SimilarPair(a: "alpha", b: "alpha", cos: 0.99),     // itself
            SimilarPair(a: "alpha", b: "missing", cos: 0.99),   // not in the bank
            SimilarPair(a: "beta", b: "gamma", cos: .nan),      // not a score
        ]
        let clusters = Dedup.clusters(bank: b, similar: pairs)
        XCTAssertEqual(clusters.count, 1)
        XCTAssertEqual(clusters[0].keys, ["alpha", "beta"])
        XCTAssertEqual(clusters[0].reasons, ["similar wording 0.93"])
    }

    func testSemanticMembersJoinOnlyOnADirectPairWithTheSurvivor() {
        let b = bank([row("alpha"), row("beta"), row("gamma"), row("delta")])
        // Both pairs touch alpha (the survivor): a star, not a chain.
        let c = Dedup.clusters(bank: b, similar: [SimilarPair(a: "alpha", b: "beta", cos: 0.95),
                                                  SimilarPair(a: "gamma", b: "alpha", cos: 0.91)])
        XCTAssertEqual(c.map(\.keys), [["alpha", "beta", "gamma"]])
        XCTAssertEqual(c[0].reasons, ["similar wording 0.91"], "the weakest link is the one reported")
    }

    func testSemanticChainDoesNotCarryThroughAMember() {
        let b = bank([row("alpha"), row("beta"), row("gamma")])
        // alpha~beta and beta~gamma, no alpha~gamma.
        let c = Dedup.clusters(bank: b, similar: [SimilarPair(a: "alpha", b: "beta", cos: 0.95),
                                                  SimilarPair(a: "beta", b: "gamma", cos: 0.93)])
        XCTAssertEqual(c.map(\.keys), [["alpha", "beta"]], "gamma has no direct pair with the survivor")
    }

    func testSemanticRowAttachesToADeterministicGroupByAPairWithAnyMember() throws {
        let b = bank(linkedIn + [row("zeta thing"), row("omega thing")])
        let c = Dedup.clusters(bank: b, similar: [SimilarPair(a: "your linkedin profile", b: "zeta thing", cos: 0.92),
                                                  SimilarPair(a: "zeta thing", b: "omega thing", cos: 0.9)])
        XCTAssertEqual(c.map(\.keys), [["linkedin", "linkedin profile url", "your linkedin profile", "zeta thing"]])
        XCTAssertTrue(try XCTUnwrap(c.first).reasons.contains { $0.hasPrefix("same concept") })
    }

    func testPairTierSetsDefaultSelection() throws {
        let b = bank([row("alpha"), row("beta"), row("gamma"), row("delta")])
        let c = Dedup.clusters(bank: b, similar: [SimilarPair(a: "alpha", b: "beta", cos: 0.95, tier: "likely"),
                                                  SimilarPair(a: "gamma", b: "delta", cos: 0.8, tier: "possible")])
        XCTAssertEqual(c.map(\.keys), [["alpha", "beta"], ["delta", "gamma"]], "checked clusters first")
        XCTAssertEqual(c.map(\.defaultSelected), [true, false])
    }

    func testPossiblePairNeverUnchecksADeterministicGroup() throws {
        let b = bank(linkedIn + [row("zeta thing")])
        let c = Dedup.clusters(bank: b, similar: [SimilarPair(a: "linkedin", b: "zeta thing", cos: 0.8, tier: "possible")])
        XCTAssertEqual(c.map(\.keys), [["linkedin", "linkedin profile url", "your linkedin profile"]])
        XCTAssertTrue(c[0].defaultSelected)
    }

    func testEmptyAndSingleRowBanksHaveNoClusters() {
        XCTAssertEqual(Dedup.clusters(bank: .empty, similar: []), [])
        XCTAssertEqual(Dedup.clusters(bank: bank([row("only")]), similar: [SimilarPair(a: "only", b: "x", cos: 1)]), [])
    }

    func testClustersAreDeterministic() {
        let rows = linkedIn + [row("favourite colour", value: ["Ultramarine blue"]), row("shirt shade", value: ["Ultramarine blue"]),
                               row("alpha"), row("beta")]
        let first = Dedup.clusters(bank: bank(rows), similar: [SimilarPair(a: "alpha", b: "beta", cos: 0.9)])
        for _ in 0..<5 {
            XCTAssertEqual(Dedup.clusters(bank: bank(rows.shuffled()), similar: [SimilarPair(a: "beta", b: "alpha", cos: 0.9)]), first)
        }
        XCTAssertTrue(first[0].defaultSelected && !first.last!.defaultSelected, "selected-by-default clusters come first")
    }

    func testTwoThousandRowsClusterWithinABound() {
        var rows: [Answer] = []
        for i in 0..<2000 {
            rows.append(row("field\(i) question", labels: ["field\(i) question", "field\(i) wording"], value: ["answer text number \(i)"]))
        }
        let started = Date()
        let clusters = Dedup.clusters(bank: bank(rows), similar: [SimilarPair(a: "field1 question", b: "field2 question", cos: 0.9)])
        XCTAssertLessThan(Date().timeIntervalSince(started), 20)
        XCTAssertEqual(clusters.count, 1)
        XCTAssertEqual(clusters[0].keys, ["field1 question", "field2 question"])
    }

    // MARK: guards

    func testSecretRowsAreNeverOffered() {
        let b = bank([row("passport number", sens: .secret), row("passport no", sens: .secret), row("passport", sens: .secret)])
        XCTAssertEqual(Dedup.clusters(bank: b, similar: [SimilarPair(a: "passport number", b: "passport no", cos: 1)]), [])
        var copy = b
        XCTAssertThrowsError(try Dedup.merge(bank: &copy, plan: .init(survivor: "passport number", merged: ["passport no"], valueFrom: "passport no")))
        XCTAssertEqual(copy, b)
    }

    func testSelfIDAndStandardAreRefusedNamingTheRow() throws {
        let b = bank([row("gender", sens: .selfID), row("gender identity", sens: .selfID), row("gender note", sens: .standard)], selfID: true)
        var copy = b
        XCTAssertThrowsError(try Dedup.merge(bank: &copy, plan: .init(survivor: "gender", merged: ["gender note"], valueFrom: "gender"))) {
            XCTAssertTrue("\($0)".contains("“gender note”") && "\($0)".contains("standard"), "\($0)")
        }
        XCTAssertEqual(copy, b)
        let c = Dedup.clusters(bank: b, similar: [SimilarPair(a: "gender", b: "gender note", cos: 0.9)])
        XCTAssertNotNil(c.first?.blocked)
    }

    func testSelfIDClustersOnlyWhenTheSettingAllowsIt() throws {
        let rows = [row("gender", value: ["x"], sens: .selfID), row("gender identity", value: ["y"], sens: .selfID)]
        XCTAssertEqual(Dedup.clusters(bank: bank(rows, selfID: false), similar: []), [], "not offered while the setting is off")
        var off = bank(rows, selfID: false)
        XCTAssertThrowsError(try Dedup.merge(bank: &off, plan: .init(survivor: "gender", merged: ["gender identity"], valueFrom: "gender")))
        let on = bank(rows, selfID: true)
        let c = try XCTUnwrap(Dedup.clusters(bank: on, similar: []).first)
        XCTAssertNil(c.blocked)
        var merged = on
        try Dedup.merge(bank: &merged, plan: c.plan())
        XCTAssertEqual(merged.answers.count, 1)
    }

    func testMergeThatWouldReclassifyTheSurvivorIsRefusedNamingTheRow() throws {
        // Each row alone is standard; "social" + "security" read together as a secret.
        let b = bank([row("social", labels: ["social"], uses: 5), row("security", labels: ["security"])])
        var copy = b
        XCTAssertThrowsError(try Dedup.merge(bank: &copy, plan: .init(survivor: "social", merged: ["security"], valueFrom: "social"))) {
            XCTAssertTrue("\($0)".contains("“security”") && "\($0)".contains("sensitivity"), "\($0)")
        }
        XCTAssertEqual(copy, b)
        let c = try XCTUnwrap(Dedup.clusters(bank: b, similar: [SimilarPair(a: "social", b: "security", cos: 0.9)]).first)
        XCTAssertTrue(c.blocked?.contains("“security”") == true, "\(String(describing: c.blocked))")
    }

    func testChoiceAndTextNeedAllowCrossKind() throws {
        let b = bank([row("work authorization", kind: .select, value: ["Yes"], uses: 3),
                      row("authorized to work", kind: .text, value: ["Yes, I am"], uses: 1)])
        let c = try XCTUnwrap(Dedup.clusters(bank: b, similar: []).first)
        XCTAssertTrue(c.crossKind)
        XCTAssertNotNil(c.blocked)
        var copy = b
        XCTAssertThrowsError(try Dedup.merge(bank: &copy, plan: c.plan()))
        XCTAssertEqual(copy, b)
        try Dedup.merge(bank: &copy, plan: c.plan(allowCrossKind: true))
        XCTAssertEqual(copy.answers.count, 1)
        XCTAssertEqual(copy.answers["work authorization"]?.kind, .select, "the survivor's kind stays")
    }

    func testSelectRadioCheckboxMayMergeTogether() throws {
        let b = bank([row("linkedin", kind: .select, uses: 2), row("your linkedin profile", kind: .radio, uses: 1)])
        XCTAssertNil(Dedup.clusters(bank: b, similar: []).first?.blocked)
    }

    func testPlanValidationNeverPartiallyApplies() throws {
        var b = bank(linkedIn)
        let before = b
        let ok = Dedup.MergePlan(survivor: "linkedin", merged: ["your linkedin profile"], valueFrom: "linkedin")
        let bad = Dedup.MergePlan(survivor: "linkedin", merged: ["gone"], valueFrom: "linkedin")
        XCTAssertThrowsError(try Dedup.merge(bank: &b, plans: [ok, bad]))
        XCTAssertEqual(b, before, "the first plan was not applied either")
        for plan in [Dedup.MergePlan(survivor: "linkedin", merged: [], valueFrom: "linkedin"),
                     Dedup.MergePlan(survivor: "linkedin", merged: ["linkedin"], valueFrom: "linkedin"),
                     Dedup.MergePlan(survivor: "linkedin", merged: ["your linkedin profile", "your linkedin profile"], valueFrom: "linkedin"),
                     Dedup.MergePlan(survivor: "linkedin", merged: ["your linkedin profile"], valueFrom: "linkedin profile url")] {
            XCTAssertThrowsError(try Dedup.merge(bank: &b, plan: plan))
        }
        XCTAssertEqual(b, before)
    }

    // MARK: merging

    func testMergeThroughTheStoreWritesOneRevision() throws {
        let store = BankStore(directory: try tempDir(self))
        let seeded = try store.update { $0 = self.bank(self.linkedIn) }
        let c = try XCTUnwrap(Dedup.clusters(bank: seeded.bank, similar: []).first)
        let after = try store.update { try Dedup.merge(bank: &$0, plan: c.plan()) }
        XCTAssertEqual(after.rev, seeded.rev + 1)
        XCTAssertEqual(try store.read(), after)
        XCTAssertEqual(Array(after.bank.answers.keys), ["linkedin"], "the others are deleted")
        let m = try XCTUnwrap(after.bank.answers["linkedin"])
        XCTAssertEqual(m.labels, ["linkedin", "linkedin profile url", "your linkedin profile", "your linkedin"],
                       "own wordings, then the merged keys, then their wordings")
        XCTAssertEqual(m.signatures, ["a|1", "b|2", "c|3"], "union, survivor's first")
        XCTAssertEqual(m.uses, 9)
        XCTAssertEqual(m.fills, 7)
        XCTAssertEqual(m.value, ["https://linkedin.com/in/new"], "the newest, preselected")
        XCTAssertTrue(m.review, "the chosen row's draft flag")
        XCTAssertEqual(m.kind, .text)
        XCTAssertGreaterThan(m.updatedAt, "2026-09-01T00:00:00.000Z")
    }

    func testChosenValueAndItsReviewFlagComeFromTheChosenRow() throws {
        var b = bank(linkedIn)
        var plan = try XCTUnwrap(Dedup.clusters(bank: b, similar: []).first).plan()
        plan.valueFrom = "your linkedin profile"
        try Dedup.merge(bank: &b, plan: plan)
        XCTAssertEqual(b.answers["linkedin"]?.value, ["https://linkedin.com/in/mid"])
        XCTAssertEqual(b.answers["linkedin"]?.review, false)
    }

    func testAnotherSurvivorKeepsItsOwnRow() throws {
        var b = bank(linkedIn)
        let plan = Dedup.MergePlan(survivor: "your linkedin profile", merged: ["linkedin", "linkedin profile url"], valueFrom: "linkedin")
        try Dedup.merge(bank: &b, plan: plan)
        XCTAssertEqual(Array(b.answers.keys), ["your linkedin profile"])
        XCTAssertEqual(b.answers["your linkedin profile"]?.labels.prefix(3).map { $0 },
                       ["your linkedin profile", "linkedin", "linkedin profile url"])
    }

    func testLabelCapKeepsMergedKeysFirst() throws {
        let many = (0..<19).map { "survivor wording \($0)" }
        let survivor = row("alpha", labels: ["alpha"] + many, uses: 9)
        let a = row("beta", labels: ["beta"] + (0..<10).map { "beta wording \($0)" })
        let b = row("gamma", labels: ["gamma", "gamma wording"])
        var bank = bank([survivor, a, b])
        try Dedup.merge(bank: &bank, plan: .init(survivor: "alpha", merged: ["beta", "gamma"], valueFrom: "alpha"))
        let labels = try XCTUnwrap(bank.answers["alpha"]).labels
        XCTAssertEqual(labels.count, Limits.maxLabels)
        XCTAssertTrue(labels.contains("beta") && labels.contains("gamma"), "merged keys survive the cap")
        XCTAssertEqual(labels.first, "alpha", "order is kept")
        XCTAssertFalse(labels.contains("beta wording 9"), "wordings are what is cut")
        XCTAssertNoThrow(try BankValidator.validate(bank))
    }

    func testSignatureCapKeepsSurvivorsFirst() throws {
        let s = row("alpha linkedin", signatures: (0..<40).map { "s|\($0)" }, uses: 5)
        let o = row("beta linkedin", signatures: (0..<40).map { "o|\($0)" })
        var b = bank([s, o])
        try Dedup.merge(bank: &b, plan: .init(survivor: "alpha linkedin", merged: ["beta linkedin"], valueFrom: "alpha linkedin"))
        let sigs = try XCTUnwrap(b.answers["alpha linkedin"]).signatures
        XCTAssertEqual(sigs.count, Limits.maxSignatures)
        XCTAssertEqual(Array(sigs.prefix(40)), (0..<40).map { "s|\($0)" })
    }

    func testCountsSaturateAtASafeInteger() throws {
        let big = Dedup.maxCount - 1
        var b = bank([row("alpha linkedin", uses: big, fills: big), row("beta linkedin", uses: big, fills: 5)])
        try Dedup.merge(bank: &b, plan: .init(survivor: "alpha linkedin", merged: ["beta linkedin"], valueFrom: "alpha linkedin"))
        XCTAssertEqual(b.answers["alpha linkedin"]?.uses, Dedup.maxCount)
        let checked = try BankValidator.validate(b)
        XCTAssertEqual(checked.bank.answers["alpha linkedin"]?.uses, Dedup.maxCount, "the validator does not zero it")
    }

    // MARK: a new wording

    func testAddingAWording() throws {
        let b = bank(linkedIn)
        let a = try Dedup.addingWording("LinkedIn Profile Link", to: "linkedin", in: b)
        XCTAssertEqual(a.labels, ["linkedin", "linkedin profile link"])
        XCTAssertEqual(try Dedup.addingWording("linkedin", to: "linkedin", in: b), b.answers["linkedin"], "already there: unchanged")
        XCTAssertThrowsError(try Dedup.addingWording("x", to: "linkedin", in: b))
        XCTAssertThrowsError(try Dedup.addingWording("whatever", to: "missing", in: b))
        XCTAssertThrowsError(try Dedup.addingWording("your linkedin profile", to: "linkedin", in: b), "that is another row's key")
    }

    func testAddingAWordingRefusesAReclassifyingOrOverfullRow() throws {
        let b = bank([row("social", labels: ["social"]), row("full", labels: (0..<Limits.maxLabels).map { "w\($0)" })])
        XCTAssertThrowsError(try Dedup.addingWording("security", to: "social", in: b)) { XCTAssertTrue("\($0)".contains("sensitivity")) }
        XCTAssertThrowsError(try Dedup.addingWording("one more", to: "full", in: b))
    }
}
