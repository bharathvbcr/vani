import Foundation
import XCTest
@testable import VaniApplyKit

/// The ranking fields `vani jobs` adds (score_factors, semantic): decoded
/// additively and leniently, because they only explain the order. Fixtures are
/// synthetic postings; no personal data.
final class RankingTests: XCTestCase {
    private func job(_ extra: String) throws -> Job {
        let json = #"{"key":"gh:a:1","company":"Acme","title":"Inference Engineer","status":"digested","relevance":72.5,"priority":61.2,"relevance_basis":"rank","#  + extra + "}"
        return try JobsClient.decoder.decode(Job.self, from: Data(json.utf8))
    }

    private let allFactors = """
    "score_factors":[
    {"name":"fit","value":0.9,"weight":0.3,"known":true},
    {"name":"coverage","value":0.5,"weight":0.2,"known":false},
    {"name":"semantic","value":0.7,"weight":0.2,"known":true},
    {"name":"keyword","value":0.4,"weight":0.15,"known":true},
    {"name":"title","value":0.66,"weight":0.1,"known":true},
    {"name":"employer","value":0,"weight":0.05,"known":true},
    {"name":"gate","value":0.85,"weight":0,"known":true},
    {"name":"fresh","value":0.6,"weight":0,"known":true},
    {"name":"company","value":1.02,"weight":0,"known":true}]
    """

    func testDecodesFactorsAndSemanticEvidence() throws {
        let j = try job(allFactors + "," + """
        "semantic":{"score":0.74,"evidence":[
        {"snippet":"Build low-latency model serving","label":"ML platform engineer","cosine":0.61},
        {"snippet":"Own GPU kernels","label":"skills","cosine":0.72}]}
        """)
        XCTAssertEqual(j.priority, 61.2)
        XCTAssertEqual(j.relevance, 72.5)
        XCTAssertEqual(j.relevanceBasis, "rank", "the existing fields are untouched")
        let b = try XCTUnwrap(j.rankingBreakdown)
        XCTAssertEqual(b.factors.map(\.name), ScoreFactor.knownNames)
        XCTAssertEqual(b.semanticScore, 0.74)
        XCTAssertEqual(b.evidence.map(\.label), ["skills", "ML platform engineer"], "strongest match first")
    }

    func testUnknownFactorsReadAsNotKnownNeverAsZero() throws {
        let b = try XCTUnwrap(job(allFactors).rankingBreakdown)
        let coverage = try XCTUnwrap(b.factors.first { $0.name == "coverage" })
        XCTAssertFalse(coverage.known)
        XCTAssertEqual(coverage.value, 0.5, "the prior is kept, but never shown as a score")
        XCTAssertEqual(coverage.valueLabel, "not known")
        let fit = try XCTUnwrap(b.factors.first { $0.name == "fit" })
        XCTAssertEqual(fit.valueLabel, "90")
        XCTAssertEqual(fit.weightLabel, "30% of the blend")
        let employer = try XCTUnwrap(b.factors.first { $0.name == "employer" })
        XCTAssertEqual(employer.valueLabel, "0", "a known zero is a score, not unknown")
        XCTAssertEqual(try XCTUnwrap(b.factors.first { $0.name == "gate" }).valueLabel, "×0.85")
        XCTAssertEqual(try XCTUnwrap(b.factors.first { $0.name == "company" }).fraction, 1, "clamped for display")
    }

    func testMissingSemanticStillShowsFactors() throws {
        let j = try job(allFactors)
        XCTAssertNil(j.semantic)
        let b = try XCTUnwrap(j.rankingBreakdown)
        XCTAssertEqual(b.factors.count, 9)
        XCTAssertNil(b.semanticScore)
        XCTAssertTrue(b.evidence.isEmpty)
    }

    func testAnOlderVaniSendsNeitherAndHasNoBreakdown() throws {
        let j = try job(#""keywords":[]"#)
        XCTAssertNil(j.scoreFactors)
        XCTAssertNil(j.semantic)
        XCTAssertNil(j.rankingBreakdown)
        XCTAssertEqual(j.priority, 61.2)
    }

    func testUnknownFactorNamesAreKeptAfterTheKnownOnes() throws {
        let j = try job(#"""
        "score_factors":[{"name":"vibes","value":0.3,"weight":0.1,"known":true},
        {"name":"fit","value":0.8,"weight":0.3,"known":true},
        {"name":"zeta_signal","value":0.2,"known":true}]
        """#)
        let b = try XCTUnwrap(j.rankingBreakdown)
        XCTAssertEqual(b.ordered.map(\.name), ["fit", "vibes", "zeta_signal"])
        XCTAssertEqual(b.ordered[1].title, "Vibes")
        XCTAssertEqual(b.ordered[2].title, "Zeta Signal")
        XCTAssertEqual(b.ordered[2].weight, 0, "a missing weight reads as a multiplier")
    }

    func testMalformedFactorsAreSkippedNotFatal() throws {
        let j = try job(#"""
        "score_factors":[{"name":"fit","value":0.8,"weight":0.3,"known":true},
        "oops", 7, null, {"name":"keyword"}, {"value":0.5,"known":true}, {"name":"","value":0.5},
        {"name":"title","value":"high","weight":0.1,"known":true},
        {"name":"fit","value":0.1,"weight":0.3,"known":true},
        {"name":"gate","value":0.9,"weight":0,"known":true}]
        """#)
        let names = try XCTUnwrap(j.scoreFactors).items.map(\.name)
        XCTAssertEqual(names, ["fit", "gate"], "bad elements are skipped and a repeated name keeps its first")
        XCTAssertEqual(j.priority, 61.2, "the rest of the job still decodes")
    }

    func testFactorNoteIsKeptAndAMalformedNoteIsDropped() throws {
        let j = try job(#"""
        "score_factors":[{"name":"fresh","value":0.78,"weight":0,"known":false,"note":"age_neutral"},
        {"name":"gate","value":1,"weight":0,"known":true,"note":7},
        {"name":"company","value":1,"weight":0,"known":true}]
        """#)
        let items = try XCTUnwrap(j.scoreFactors).items
        XCTAssertEqual(items.map(\.name), ["fresh", "gate", "company"], "a bad note does not drop its factor")
        XCTAssertEqual(items[0].note, "age_neutral")
        XCTAssertNil(items[1].note)
        XCTAssertNil(items[2].note)
    }

    func testAFactorWithoutKnownIsNotKnown() throws {
        let j = try job(#""score_factors":[{"name":"fit","value":0.8,"weight":0.3}]"#)
        XCTAssertEqual(try XCTUnwrap(j.scoreFactors).items.first?.known, false)
    }

    func testWrongTypedRankingFieldsNeverFailTheJob() throws {
        for extra in [#""score_factors":"nope","semantic":"nope""#,
                      #""score_factors":{"name":"fit"},"semantic":42"#,
                      #""score_factors":null,"semantic":null"#,
                      #""score_factors":[],"semantic":{}"#] {
            let j = try job(extra)
            XCTAssertEqual(j.priority, 61.2, extra)
            XCTAssertNil(j.rankingBreakdown, extra)
        }
    }

    func testMalformedEvidenceIsSkipped() throws {
        let j = try job(#"""
        "semantic":{"score":0.5,"evidence":[
        {"snippet":"Serve models at scale","label":"ML engineer","cosine":0.6},
        {"snippet":"no cosine","label":"x"},
        {"snippet":"","label":"empty snippet","cosine":0.9},
        {"snippet":"   ","label":"blank snippet","cosine":0.9},
        {"snippet":7,"label":"x","cosine":0.5},
        "text", null,
        {"snippet":"Debug distributed training","cosine":"high","label":"y"},
        {"snippet":"Optimise kernels","label":"skills","cosine":0.6}]}
        """#)
        let ev = try XCTUnwrap(j.semantic).evidence
        XCTAssertEqual(ev.map(\.snippet), ["Serve models at scale", "Optimise kernels"])
        XCTAssertEqual(try XCTUnwrap(j.semantic).topEvidence.map(\.label), ["ML engineer", "skills"], "a tie keeps Vani's order")
    }

    func testSemanticWithABadScoreOrEvidenceListKeepsWhatIsUsable() throws {
        let a = try XCTUnwrap(job(#""semantic":{"score":"high","evidence":[{"snippet":"A line","label":"r","cosine":0.5}]}"#).semantic)
        XCTAssertNil(a.score)
        XCTAssertEqual(a.evidence.count, 1)
        let b = try XCTUnwrap(job(#""semantic":{"score":0.4,"evidence":"none"}"#).semantic)
        XCTAssertEqual(b.score, 0.4)
        XCTAssertTrue(b.evidence.isEmpty)
        let c = try XCTUnwrap(job(#""semantic":{"score":0.4}"#).semantic)
        XCTAssertTrue(c.evidence.isEmpty, "evidence is optional")
    }

    func testOnlyTheTopThreeEvidenceLinesShow() throws {
        let lines = (1...6).map { #"{"snippet":"line \#($0)","label":"role","cosine":0.\#($0)}"# }.joined(separator: ",")
        let sem = try XCTUnwrap(job(#""semantic":{"score":0.9,"evidence":[\#(lines)]}"#).semantic)
        XCTAssertEqual(sem.evidence.count, 6)
        XCTAssertEqual(sem.topEvidence.map(\.snippet), ["line 6", "line 5", "line 4"])
    }

    func testAJobWithOnlyASemanticScoreStillExplainsItself() throws {
        let b = try XCTUnwrap(job(#""semantic":{"score":0.8}"#).rankingBreakdown)
        XCTAssertEqual(b.semanticScore, 0.8)
        XCTAssertTrue(b.factors.isEmpty)
    }

    func testFactorsRoundTripThroughEncode() throws {
        let j = try job(allFactors)
        let data = try JSONEncoder().encode(try XCTUnwrap(j.scoreFactors))
        let back = try JSONDecoder().decode(ScoreFactors.self, from: data)
        XCTAssertEqual(back, j.scoreFactors)
    }

    func testAListWithRankingFieldsDecodesAndKeepsItsOrder() throws {
        let reply = """
        {"view":"new","jobs":[
        {"key":"a","company":"A","title":"T","status":"digested","priority":80,"score_factors":[{"name":"fit","value":0.9,"weight":0.3,"known":true}],"semantic":{"score":0.7,"evidence":[{"snippet":"x","label":"y","cosine":0.5}]}},
        {"key":"b","company":"B","title":"T","status":"digested","priority":70,"score_factors":"bad"},
        {"key":"c","company":"C","title":"T","status":"digested","priority":60}],
        "total":3,"shown":3,"counts":{"new":3}}
        """
        let page = try JobsClient.decoder.decode(JobsPage.self, from: Data(reply.utf8))
        XCTAssertEqual(page.jobs.map(\.key), ["a", "b", "c"])
        XCTAssertEqual(page.jobs.map { $0.rankingBreakdown != nil }, [true, false, false])
    }

    func testFreshnessWithoutAnAgeIsFlagged() throws {
        var j = try job(allFactors)
        XCTAssertEqual(try XCTUnwrap(j.rankingBreakdown).ageKnown, false)
        j.ageDays = 3
        XCTAssertEqual(try XCTUnwrap(j.rankingBreakdown).ageKnown, true)
    }

    /// Needs a vani built from the rank-relevance branch at build/vani (an older
    /// one sends no score_factors, and the test says so rather than passing).
    func testAgainstTheRealVani() throws {
        let vani = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("build/vani")
        guard FileManager.default.isExecutableFile(atPath: vani.path) else {
            throw XCTSkip("no Go vani at \(vani.path); build it to run the contract test")
        }
        let state = try tempDir(self)
        let ledger = state.appendingPathComponent("ledger")
        try FileManager.default.createDirectory(at: ledger, withIntermediateDirectories: true)
        let line = #"{"v":1,"at":"2026-10-18T12:00:00.5Z","key":"gh:acme:1","status":"digested","posting":{"key":"gh:acme:1","ats":"gh","company":"Acme","title":"Inference Engineer","url":"https://boards.example.com/1","description":"Serve models.","seen_at":"2026-10-18T12:00:00Z"},"annotations":{"explanation":"Fits: Inference serving and runtimes (88%).","triage.role_fit":"strong_fit"}}"#
        try Data((line + "\n").utf8).write(to: ledger.appendingPathComponent("events.jsonl"))

        for days in [nil, 3.0] as [Double?] {
            let page = try JobsClient(vaniBinary: vani, stateDir: state.path, halfLifeDays: days).list(.new)
            let j = try XCTUnwrap(page.jobs.first)
            guard let factors = j.scoreFactors?.items, !factors.isEmpty else {
                throw XCTSkip("this vani sends no score_factors; build it from the rank-relevance branch")
            }
            XCTAssertEqual(factors.map(\.name), ScoreFactor.knownNames, "the nine components, in Score's order")
            XCTAssertEqual(page.insight?.halfLifeDays, days ?? JobsClient.defaultHalfLifeDays, "vani took the --half-life")
            XCTAssertEqual(factors.first { $0.name == "fit" }?.known, false, "a role_fit with no calibrated confidence is not known")
            XCTAssertEqual(factors.first { $0.name == "coverage" }?.known, false, "no DevPrism rank in this ledger")
            XCTAssertNil(j.semantic, "no knowledge-base match is cached for this state")
            XCTAssertNotNil(j.rankingBreakdown)
            XCTAssertNotNil(j.priority)
        }
        let stats = try JobsClient(vaniBinary: vani, stateDir: state.path, halfLifeDays: 3).stats(.new)
        XCTAssertEqual(stats.insight?.halfLifeDays, 3)
    }

    // MARK: half-life

    func testHalfLifeIsSentOnlyWhenItDiffersFromTheDefault() {
        let f = JobFilter()
        XCTAssertFalse(JobsClient.listArguments(.new, query: "", sort: .standard, filter: f, limit: 5).contains("--half-life"))
        XCTAssertFalse(JobsClient.listArguments(.new, query: "", sort: .standard, filter: f, limit: 5, halfLifeDays: 10).contains("--half-life"))
        XCTAssertTrue(JobsClient.listArguments(.new, query: "", sort: .standard, filter: f, limit: 5, halfLifeDays: 7)
            .joined(separator: " ").contains("--half-life 7"))
        XCTAssertTrue(JobsClient.statsArguments(.all, query: "", filter: f, halfLifeDays: 2.5)
            .joined(separator: " ").contains("--half-life 2.5"))
    }

    func testAnUnusableHalfLifeIsNeverSent() {
        for bad in [0.0, -3, 366, .infinity, .nan] {
            XCTAssertEqual(JobsClient.halfLifeArguments(bad), [], "\(bad)")
        }
        XCTAssertEqual(JobsClient.halfLifeArguments(365), ["--half-life", "365"])
    }

    func testDraftSettingsCarryTheHalfLifeToTheJobsClient() throws {
        let dir = try tempDir(self)
        try DraftSettings(jobsHalfLifeDays: 21).save(to: dir)
        let loaded = DraftSettings.load(from: dir)
        XCTAssertEqual(loaded.jobsHalfLifeDays, 21)
        XCTAssertEqual(loaded.jobsClient(vaniBinary: URL(fileURLWithPath: "/x/vani")).halfLifeDays, 21)
        XCTAssertNil(DraftSettings().jobsClient(vaniBinary: URL(fileURLWithPath: "/x/vani")).halfLifeDays)
    }

    func testDraftSettingsRefuseABadHalfLifeAndIgnoreOneOnDisk() throws {
        for bad in [0.0, -1, 400, .infinity] {
            XCTAssertThrowsError(try DraftSettings(jobsHalfLifeDays: bad).validated(), "\(bad)")
        }
        let old = try JSONDecoder().decode(DraftSettings.self, from: Data(#"{"vaniConfig":"/c","jobsHalfLifeDays":-5}"#.utf8))
        XCTAssertNil(old.jobsHalfLifeDays, "a bad value on disk reads as unset")
        XCTAssertEqual(old.vaniConfig, "/c")
        let before = try JSONDecoder().decode(DraftSettings.self, from: Data(#"{"vaniConfig":"/c"}"#.utf8))
        XCTAssertNil(before.jobsHalfLifeDays, "a file from before the setting still loads")
    }
}
