import CryptoKit
import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// The Resume Studio flow in this process against a fake `vani jobs resume` (and the Jobs fixture for the list
/// and `jobs show`). Like RetriageModelTests these drive the models directly: no accessibility trust, no real
/// vani, no Keychain (every key lookup is a closure that returns nothing) and no owner state. The PDF is a
/// generated one with a neutral name: no personal data in any test.
@MainActor
final class ResumeStudioModelTests: XCTestCase {
    private static let key = "fx:alpha:1"
    private static let posting = "https://boards.greenhouse.io/acme/jobs/123"
    private static let apply = "https://jobs.lever.co/acme/abc-def"

    private static let dry = """
    {"key":"fx:alpha:1","dry_run":true,"model":"claude-opus-5-5","opus_usable":true,"current":0,
     "budget":{"daily_cap_usd":20,"spent_usd":1.25,"remaining_usd":18.75},
     "estimate":{"turns":2,"input_tokens":18400,"max_output_tokens":24000,"max_usd":0.42,"price_source":"rates","cap_reached":false}}
    """
    private static let noModel = """
    {"key":"fx:alpha:1","dry_run":true,"model":"claude-opus-5-5","opus_usable":false,"no_model":"the model provider is not available","budget":{"daily_cap_usd":20}}
    """

    private static func saved(version: Int, source: String = "tailored", fit: String = "passed", eligibility: String = "current",
                              moved: Bool = true, path: String, sha: String, extra: String = "") -> String {
        """
        {"schema":1,"key":"fx:alpha:1","version":\(version),"source":"\(source)","cost":{"usd":0.31,"complete":true,"turns":2},
         "checks":{"eligibility":"\(eligibility)","lint":"passed","fit":"\(fit)","figures":"verified"},"pages":2,
         "bullets":[{"id":"b1","text_hash":"abc123def456"}],"pdf_sha256":"\(sha)","created":"2026-10-25T12:00:00Z",
         "current_moved":\(moved),"pdf_path":"\(path)"\(extra)}
        """
    }

    private struct Fake {
        let dir: URL
        let pdf: URL
        let sha: String
        var vani: URL { dir.appendingPathComponent("vani") }

        func calls() -> [String] {
            ((try? String(contentsOf: dir.appendingPathComponent("calls.log"), encoding: .utf8)) ?? "")
                .split(separator: "\n").map(String.init)
        }

        func put(_ name: String, _ text: String) throws { try Data(text.utf8).write(to: dir.appendingPathComponent(name)) }
        func remove(_ name: String) { try? FileManager.default.removeItem(at: dir.appendingPathComponent(name)) }

        var childPID: pid_t? {
            (try? String(contentsOf: dir.appendingPathComponent("pid"), encoding: .utf8))
                .flatMap { pid_t($0.trimmingCharacters(in: .whitespacesAndNewlines)) }
        }
    }

    private func pdfData(fill: UInt8 = 0x41) -> Data {
        var d = Data("%PDF-1.4\n".utf8)
        d.append(Data(repeating: fill, count: 1500))
        return d
    }

    private func sha(_ d: Data) -> String { SHA256.hash(data: d).map { String(format: "%02x", $0) }.joined() }

    private func fake(dry: String = ResumeStudioModelTests.dry) throws -> Fake {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-apply-resume-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        let script = """
        #!/bin/sh
        d='\(dir.path)'
        printf '%s\\n' "$*" >> "$d/calls.log"
        case " $* " in
        *" --dry-run "*) cat "$d/dry.json"; exit 0 ;;
        *" use "*) cat "$d/use.json"; [ -f "$d/use.fail" ] && exit 1; exit 0 ;;
        *" revalidate "*) cat "$d/revalidate.json"; [ -f "$d/revalidate.fail" ] && exit 1; exit 0 ;;
        esac
        if [ -f "$d/slow" ]; then echo $$ > "$d/pid"; exec sleep 30; fi
        cat "$d/build.json"
        [ -f "$d/build.fail" ] && exit 1
        exit 0
        """
        let data = pdfData()
        let pdf = dir.appendingPathComponent("v3.pdf")
        try data.write(to: pdf)
        let f = Fake(dir: dir, pdf: pdf, sha: sha(data))
        try Data(script.utf8).write(to: f.vani)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: f.vani.path)
        try f.put("dry.json", dry)
        try f.put("build.json", Self.saved(version: 3, path: pdf.path, sha: f.sha))
        try f.put("use.json", #"{"key":"fx:alpha:1","version":3,"pdf_path":"\#(pdf.path)","name":"Alpha_Resume.pdf","pdf_sha256":"\#(f.sha)"}"#)
        try f.put("revalidate.json", Self.saved(version: 4, source: "revalidated", moved: true, path: pdf.path, sha: f.sha,
                                                extra: #","dropped_stale":["b7"]"#))
        return f
    }

    /// What `jobs show` says about the first role: its addresses and, when given, its `resume` summary.
    private func show(_ jobs: JobsFixture, resume: String?, url: String? = ResumeStudioModelTests.posting,
                      apply: String? = ResumeStudioModelTests.apply) throws {
        var obj: [String: Any] = ["key": Self.key, "company": "Alpha Labs", "title": "Platform Engineer", "status": "digested",
                                  "triage": ["role_fit": "strong_fit"], "fresh_at": "2026-10-10T12:00:00Z", "fresh_src": "posted",
                                  "age_days": 9, "relevance": 85, "relevance_basis": "fit", "description": "A made-up posting."]
        if let url { obj["url"] = url }
        if let apply { obj["apply_url"] = apply }
        if let resume { obj["resume"] = try JSONSerialization.jsonObject(with: Data(resume.utf8)) }
        try JSONSerialization.data(withJSONObject: obj, options: [.sortedKeys]).write(to: jobs.dir.appendingPathComponent("show-\(Self.key).json"))
    }

    private struct Rig {
        let m: JobsModel
        let bank: BankStore
        var resumesDir: URL { bank.directory.appendingPathComponent("resumes", isDirectory: true) }
        var resumes: ResumeStore { ResumeStore(directory: resumesDir) }
    }

    private func rig(_ jobs: JobsFixture, _ f: Fake?, bank: Bool = true) throws -> Rig {
        let vani = jobs.dir.appendingPathComponent("vani")
        let store = BankStore(directory: try tempDirectory())
        guard let f else { return Rig(m: JobsModel { JobsClient(vaniBinary: vani, timeout: 10) }, bank: store) }
        let runner = DraftRunner(vaniBinary: f.vani, key: { nil }, decisionKey: { nil })
        let m = JobsModel(client: { JobsClient(vaniBinary: vani, timeout: 10) }, resume: { ResumeStudioClient(runner: runner) },
                          bankStore: { bank ? store : nil })
        return Rig(m: m, bank: store)
    }

    private func tempDirectory() throws -> URL {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("vani-apply-resume-bank-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: url) }
        return url
    }

    private func eventually(_ what: String, _ done: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(10)
        while !done() {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    private func showCalls(_ jobs: JobsFixture) -> Int { jobs.calls().filter { $0.hasPrefix("jobs show") }.count }

    /// Select the first role and wait for `jobs show` to give it its addresses.
    private func open(_ r: Rig, _ jobs: JobsFixture) async throws -> Job {
        await r.m.refresh()
        let first = try XCTUnwrap(r.m.jobs.first)
        r.m.selection = first.key
        try await eventually("the detail") { r.m.detail?.key == first.key }
        return try XCTUnwrap(r.m.detail)
    }

    private func phaseIs(_ m: JobsModel, _ matches: (ResumeStudioModel.Phase) -> Bool) -> Bool { matches(m.resume.phase) }

    // MARK: Happy path

    func testEstimateConfirmBuildThenUseSpendsOnlyOnConfirmAndBindsBothAddresses() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        try show(jobs, resume: nil)
        let r = try rig(jobs, f), m = r.m
        let job = try await open(r, jobs)
        XCTAssertNil(job.resume)

        m.startResume(job)
        try await eventually("the confirmation") { m.resume.confirming != nil }
        XCTAssertEqual(f.calls(), ["jobs resume build --json --dry-run fx:alpha:1"], "only the estimate has run: nothing is spent")
        XCTAssertTrue(m.resume.isBusy)
        XCTAssertNotNil(m.resume.blockedReason(for: job), "nothing else starts while one waits")

        // The build saves v3 and the job now reports it.
        try show(jobs, resume: #"{"current":3,"versions":1,"pages":2,"fit":"passed","eligibility":"current","source":"tailored"}"#)
        let shows = showCalls(jobs)
        m.resume.confirm()
        try await eventually("the build") { if case .built = m.resume.phase { true } else { false } }
        guard case let .built(key, saved) = m.resume.phase else { return XCTFail("\(m.resume.phase)") }
        XCTAssertEqual(key, Self.key)
        XCTAssertEqual(saved.version.version, 3)
        XCTAssertEqual(f.calls(), ["jobs resume build --json --dry-run fx:alpha:1", "jobs resume build --json fx:alpha:1"])
        XCTAssertEqual(m.resume.knownPDF[key]?.version, 3)
        try await eventually("the job to be shown again") { self.showCalls(jobs) > shows && m.detail?.resume?.current == 3 }
        XCTAssertFalse(m.resume.isBusy, "a shown result does not block the next step")
        XCTAssertEqual(ResumeCardPresentation.card(for: m.detail?.resume).use, .available(version: 3))

        // Use for this job: vani checks it afresh, then the PDF is saved for the browsers under both addresses.
        let heard = BoundHeard()
        let token = NotificationCenter.default.addObserver(forName: .vaniResumeBound, object: nil, queue: nil) { heard.add($0) }
        defer { NotificationCenter.default.removeObserver(token) }
        m.resume.use(try XCTUnwrap(m.detail), version: 3)
        try await eventually("the use") { if case .used = m.resume.phase { true } else { false } }
        guard case let .used(_, version, note) = m.resume.phase else { return XCTFail("\(m.resume.phase)") }
        XCTAssertEqual(version, 3)
        XCTAssertTrue(note.contains("2 addresses"), note)
        XCTAssertEqual(f.calls().last, "jobs resume use --json fx:alpha:1 v3")
        let targets = ResumeBinding.targets(url: Self.posting, applyURL: Self.apply)
        for k in targets.keys {
            let got = try XCTUnwrap(try r.resumes.get(key: k), k)
            XCTAssertEqual(got.data, pdfData())
            XCTAssertEqual(got.entry.name, "Alpha_Resume.pdf")
        }
        XCTAssertEqual(try r.bank.recentActivity().filter { $0.event == .resume }.count, 1, "one activity entry for one choice")
        XCTAssertEqual(heard.keys, [targets.keys], "the notification names the bound keys")
        XCTAssertEqual(m.resume.knownPDF[Self.key]?.sha256, f.sha)
    }

    func testClosingTheConfirmationSpendsNothingAndAskedTwiceIsTwoEstimates() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        let r = try rig(jobs, f), m = r.m
        await m.refresh()
        let job = try XCTUnwrap(m.jobs.first)
        m.startResume(job)
        try await eventually("the confirmation") { m.resume.confirming != nil }
        m.resume.dismissConfirmation()
        XCTAssertEqual(m.resume.phase, .idle)
        XCTAssertEqual(f.calls().count, 1, "closing the confirmation asked nothing")
        m.startResume(job)
        try await eventually("the second confirmation") { m.resume.confirming != nil }
        XCTAssertEqual(f.calls().filter { $0.contains("--dry-run") }.count, 2)
        XCTAssertEqual(f.calls().filter { !$0.contains("--dry-run") }.count, 0)
    }

    // MARK: Cancel and busy

    func testCancellingABuildStopsTheChildSaysNothingAndLooksForTheVersion() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        try f.put("slow", "")
        let r = try rig(jobs, f), m = r.m
        let job = try await open(r, jobs)
        m.startResume(job)
        try await eventually("the confirmation") { m.resume.confirming != nil }
        let shows = showCalls(jobs)
        m.resume.confirm()
        try await eventually("the child to start") { f.childPID != nil }
        let child = try XCTUnwrap(f.childPID)
        // Closing the confirmation sheet (the binding's setter) must never stop a build.
        m.resume.dismissConfirmation()
        XCTAssertEqual(m.resume.phase, .building(key: Self.key, allowBase: false, cancelling: false))
        XCTAssertTrue(m.resume.isBusy)

        m.resume.cancel()
        XCTAssertEqual(m.resume.phase, .building(key: Self.key, allowBase: false, cancelling: true), "still busy until the child is gone")
        try await eventually("the build to end") { m.resume.phase == .idle }
        XCTAssertTrue(kill(child, 0) == -1 && errno == ESRCH, "the child was terminated and reaped")
        try await eventually("the job to be shown again") { self.showCalls(jobs) > shows }
        XCTAssertNil(m.actionError)
    }

    func testOnlyOneStepRunsAtATimeAndVanisBusyIsWordedWithNothingSpent() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        let r = try rig(jobs, f), m = r.m
        await m.refresh()
        let first = try XCTUnwrap(m.jobs.first), second = try XCTUnwrap(m.jobs.dropFirst().first)
        m.startResume(first)
        try await eventually("the confirmation") { m.resume.confirming != nil }
        m.startResume(second)
        m.resume.use(second, version: 1)
        m.resume.revalidate(second, version: 1)
        XCTAssertEqual(m.resume.phase.key, first.key, "the second posting did not start")
        XCTAssertEqual(f.calls().count, 1)
        m.resume.cancel()

        try f.put("build.json", #"{"error":"another build is running","code":"busy"}"#)
        try f.put("build.fail", "")
        m.startResume(first)
        try await eventually("the confirmation") { m.resume.confirming != nil }
        m.resume.confirm()
        try await eventually("the failure") { if case .failed = m.resume.phase { true } else { false } }
        guard case let .failed(_, failure) = m.resume.phase else { return XCTFail("\(m.resume.phase)") }
        XCTAssertEqual(failure.code, "busy")
        let words = try XCTUnwrap(ResumeWords.failure(failure))
        XCTAssertTrue(words.message.contains("Nothing was spent"), words.message)
        XCTAssertTrue(words.canRetry)
        XCTAssertNil(m.actionError, "an error is inline in the detail, never the blocking alert")
        XCTAssertFalse(m.resume.isBusy)
    }

    // MARK: No model

    func testWhenOpusCannotRunTheBaseSelectionIsOnlyAnExplicitChoiceOnTheConfirmation() async throws {
        let jobs = try JobsFixture(self), f = try fake(dry: Self.noModel)
        let r = try rig(jobs, f), m = r.m
        await m.refresh()
        let job = try XCTUnwrap(m.jobs.first)
        m.startResume(job)
        try await eventually("the confirmation") { m.resume.confirming != nil }
        let c = ResumeWords.confirmation(for: try XCTUnwrap(m.resume.confirming).dry)
        XCTAssertFalse(c.canConfirm)
        XCTAssertTrue(c.offersBase)

        m.resume.confirm()
        try await Task.sleep(nanoseconds: 150_000_000)
        XCTAssertNotNil(m.resume.confirming, "Build is refused while Opus cannot run")
        XCTAssertEqual(f.calls().count, 1, "no build ran, with or without the flag")

        try f.put("build.json", Self.saved(version: 1, source: "base", moved: false, path: f.pdf.path, sha: f.sha,
                                           extra: #","current_note":"v2 stays current: this is the owner's base selection""#))
        m.resume.confirmBase()
        try await eventually("the base build") { if case .built = m.resume.phase { true } else { false } }
        XCTAssertEqual(f.calls().last, "jobs resume build --json --allow-base fx:alpha:1")
        guard case let .built(_, saved) = m.resume.phase else { return XCTFail("\(m.resume.phase)") }
        XCTAssertEqual(saved.version.source, "base")
        XCTAssertFalse(saved.currentMoved)
        XCTAssertTrue(ResumeWords.savedLine(saved).contains("v2 stays current"), "a base build that did not take the pointer says so")
    }

    func testAUsableOpusNeverTakesTheBaseFlagAndANoModelRefusalOffersItOnlyThroughANewEstimate() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        let r = try rig(jobs, f), m = r.m
        let job = try await open(r, jobs)
        m.startResume(job)
        try await eventually("the confirmation") { m.resume.confirming != nil }
        m.resume.confirmBase()
        try await Task.sleep(nanoseconds: 150_000_000)
        XCTAssertNotNil(m.resume.confirming, "the base choice is not on a confirmation whose model can run")
        XCTAssertEqual(f.calls().count, 1)

        try f.put("build.json", #"{"error":"the model provider is not available","code":"no_model"}"#)
        try f.put("build.fail", "")
        m.resume.confirm()
        try await eventually("the refusal") { if case .failed = m.resume.phase { true } else { false } }
        guard case let .failed(_, failure) = m.resume.phase else { return XCTFail() }
        let words = try XCTUnwrap(ResumeWords.failure(failure))
        XCTAssertEqual(words.offer, .buildBase)
        XCTAssertFalse(f.calls().contains { $0.contains("--allow-base") }, "offered, never taken")

        m.resume.take(.buildBase, for: job)
        try await eventually("a new confirmation") { m.resume.confirming != nil }
        XCTAssertEqual(f.calls().filter { $0.contains("--dry-run") }.count, 2, "the base build is chosen from a fresh estimate")
        XCTAssertFalse(f.calls().contains { $0.contains("--allow-base") })
    }

    // MARK: Stale, revalidate

    func testAStaleVersionOnUseOffersRevalidateAndThatRebuildsIt() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        try show(jobs, resume: #"{"current":3,"versions":1,"pages":2,"fit":"passed","eligibility":"current","source":"tailored"}"#)
        try f.put("use.json", #"{"error":"v3 is stale","code":"stale","eligibility":{"state":"stale","reworded":["b1"],"gone":["b2"]}}"#)
        try f.put("use.fail", "")
        let r = try rig(jobs, f), m = r.m
        let job = try await open(r, jobs)
        m.resume.use(job, version: 3)
        try await eventually("the refusal") { if case .failed = m.resume.phase { true } else { false } }
        guard case let .failed(_, failure) = m.resume.phase else { return XCTFail("\(m.resume.phase)") }
        XCTAssertEqual(failure.code, "stale")
        let words = try XCTUnwrap(ResumeWords.failure(failure))
        XCTAssertEqual(words.offer, .revalidate)
        XCTAssertTrue(words.message.contains("1 reworded, 1 gone"), words.message)
        XCTAssertEqual(try r.resumes.catalog().count, 0, "a refused version is bound nowhere")
        XCTAssertEqual(try r.bank.recentActivity().filter { $0.event == .resume }.count, 0)

        let shows = showCalls(jobs)
        m.resume.take(.revalidate, for: job)
        try await eventually("the revalidation") { if case .built = m.resume.phase { true } else { false } }
        XCTAssertEqual(f.calls().last, "jobs resume revalidate --json fx:alpha:1 v3", "it revalidates the version Use refused")
        guard case let .built(_, saved) = m.resume.phase else { return XCTFail() }
        XCTAssertEqual(saved.version.version, 4)
        XCTAssertEqual(saved.droppedStale, ["b7"])
        XCTAssertTrue(ResumeWords.savedLine(saved).contains("Dropped: b7"))
        try await eventually("the job to be shown again") { self.showCalls(jobs) > shows }
    }

    func testAnUnusableVersionOffersRevalidateOrANewBuildNeverUse() async throws {
        // The card decides from what vani recorded; the model carries out only what the owner picks.
        let stale = ResumeCardPresentation.card(for: ResumeSummary(current: 2, versions: 2, pages: 2, fit: "passed", eligibility: "stale", source: "tailored"))
        guard case let .blocked(_, _, offer) = stale.use else { return XCTFail("\(stale.use)") }
        XCTAssertEqual(offer, .revalidate)
        let jobs = try JobsFixture(self), f = try fake()
        try show(jobs, resume: #"{"current":2,"versions":2,"pages":2,"fit":"passed","eligibility":"stale","source":"tailored"}"#)
        let r = try rig(jobs, f), m = r.m
        let job = try await open(r, jobs)
        XCTAssertEqual(m.detail?.resume?.eligibility, "stale")
        m.resume.take(.revalidate, for: job)
        try await eventually("the revalidation") { if case .built = m.resume.phase { true } else { false } }
        XCTAssertEqual(f.calls(), ["jobs resume revalidate --json fx:alpha:1 v2"], "revalidate, not use")
    }

    // MARK: Damaged store, bind failures

    func testADamagedStoreIsShownAsDamagedNotAsNoResume() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        try show(jobs, resume: #"{"versions":0,"error":"unreadable"}"#)
        let r = try rig(jobs, f), m = r.m
        let job = try await open(r, jobs)
        let card = ResumeCardPresentation.card(for: m.detail?.resume)
        guard case let .unreadable(words) = card.kind else { return XCTFail("a damaged store must not read as no resume: \(card.kind)") }
        XCTAssertTrue(words.contains("not the same as having none"), words)
        XCTAssertEqual(card.use, .none)

        // A build is refused by vani before anything is paid for, and it says damaged.
        try f.put("dry.json", #"{"error":"v2.json is damaged","code":"damaged"}"#)
        m.startResume(job)
        try await eventually("the refusal") { if case .failed = m.resume.phase { true } else { false } }
        guard case let .failed(_, failure) = m.resume.phase else { return XCTFail() }
        XCTAssertEqual(failure.code, "damaged")
        let text = try XCTUnwrap(ResumeWords.failure(failure)).message
        XCTAssertTrue(text.contains("damaged"), text)
        XCTAssertTrue(text.contains("not the same as having no resume"), text)
        XCTAssertEqual(f.calls().count, 1, "nothing was spent")

        // Use on a damaged store says the same.
        try f.put("use.json", #"{"error":"v3.json is damaged","code":"damaged"}"#)
        try f.put("use.fail", "")
        m.resume.dismiss()
        m.resume.use(job, version: 3)
        try await eventually("the use refusal") { if case .failed = m.resume.phase { true } else { false } }
        XCTAssertTrue(try XCTUnwrap(ResumeWords.failure(try XCTUnwrap({ if case let .failed(_, f) = m.resume.phase { f } else { nil } }()))).message.contains("damaged"))
    }

    func testAPDFThatCannotBeSavedForTheBrowsersIsSaidAfterVaniBoundItAndNothingIsLoggedOrPosted() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        try show(jobs, resume: nil)
        let r = try rig(jobs, f), m = r.m
        let job = try await open(r, jobs)
        let heard = BoundHeard()
        let token = NotificationCenter.default.addObserver(forName: .vaniResumeBound, object: nil, queue: nil) { heard.add($0) }
        defer { NotificationCenter.default.removeObserver(token) }
        let shows = showCalls(jobs)

        // The file is changed after vani checked it: the checksum no longer matches.
        try pdfData(fill: 0x42).write(to: f.pdf)
        m.resume.use(job, version: 3)
        try await eventually("the failure") { if case .failed = m.resume.phase { true } else { false } }
        guard case let .failed(_, failure) = m.resume.phase else { return XCTFail() }
        XCTAssertEqual(failure.code, "bind")
        XCTAssertTrue(failure.message.contains("marked in use in Vani"), failure.message)
        XCTAssertTrue(failure.message.contains("checksum"), failure.message)
        XCTAssertTrue(try XCTUnwrap(ResumeWords.failure(failure)).canRetry)
        XCTAssertEqual(try r.resumes.catalog().count, 0)
        XCTAssertEqual(try r.bank.recentActivity().filter { $0.event == .resume }.count, 0)
        XCTAssertEqual(heard.keys.count, 0)
        try await eventually("the job to be shown again, since vani did mark it used") { self.showCalls(jobs) > shows }

        // The file is gone.
        try FileManager.default.removeItem(at: f.pdf)
        m.resume.retry()
        try await eventually("the second failure") { if case .failed = m.resume.phase { true } else { false } }
        guard case let .failed(_, again) = m.resume.phase else { return XCTFail() }
        XCTAssertEqual(again.code, "bind")
        XCTAssertEqual(f.calls().filter { $0.contains(" use ") }.count, 2, "Retry used the version again")
    }

    func testWithNoOpenAnswerBankThereIsNowhereToSaveAndItIsSaid() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        let r = try rig(jobs, f, bank: false), m = r.m
        let job = try await open(r, jobs)
        m.resume.use(job, version: 3)
        try await eventually("the failure") { if case .failed = m.resume.phase { true } else { false } }
        guard case let .failed(_, failure) = m.resume.phase else { return XCTFail() }
        XCTAssertEqual(failure.code, "bind")
        XCTAssertTrue(failure.message.contains("answer bank is not open"), failure.message)
    }

    func testAnAddressWithNoKeyIsLeftOutAndSaid() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        try show(jobs, resume: nil, apply: "mailto:jobs@example.invalid")
        let r = try rig(jobs, f), m = r.m
        let job = try await open(r, jobs)
        m.resume.use(job, version: 3)
        try await eventually("the use") { if case .used = m.resume.phase { true } else { false } }
        guard case let .used(_, _, note) = m.resume.phase else { return XCTFail() }
        XCTAssertTrue(note.contains("1 address"), note)
        XCTAssertTrue(note.contains("The application page has no address"), note)
        XCTAssertEqual(try r.resumes.catalog().count, 1)
    }

    // MARK: Preview, hooks, availability

    func testPreviewOpensTheKnownPDFThenTheBrowsersCopyAndOtherwiseSaysWhy() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        try show(jobs, resume: nil)
        let r = try rig(jobs, f), m = r.m
        let job = try await open(r, jobs)
        let summary = ResumeSummary(current: 3, used: 3, versions: 1, pages: 2, fit: "passed", eligibility: "current", source: "tailored")

        // Nothing known yet and nothing used: a sentence, never an empty success.
        let none = await m.resume.previewCopy(for: job, summary: ResumeSummary(current: 3, versions: 1, pages: 2, fit: "passed", eligibility: "current"))
        guard case let .failure(why) = none else { return XCTFail("a PDF that is not on hand cannot be opened") }
        XCTAssertTrue(why.message.contains("not on hand"), why.message)

        m.resume.use(job, version: 3)
        try await eventually("the use") { if case .used = m.resume.phase { true } else { false } }
        guard case let .success(url) = await m.resume.previewCopy(for: job, summary: summary) else { return XCTFail("known PDF") }
        XCTAssertEqual(try Data(contentsOf: url), pdfData())

        // A fresh model (a new session) finds the browsers' copy when that version is the one in use.
        let vani = jobs.dir.appendingPathComponent("vani")
        let runner = DraftRunner(vaniBinary: f.vani, key: { nil }, decisionKey: { nil })
        let later = JobsModel(client: { JobsClient(vaniBinary: vani, timeout: 10) }, resume: { ResumeStudioClient(runner: runner) },
                              bankStore: { r.bank })
        guard case let .success(copy) = await later.resume.previewCopy(for: job, summary: summary) else { return XCTFail("browser copy") }
        XCTAssertEqual(try Data(contentsOf: copy), pdfData())
        guard case .failure = await later.resume.previewCopy(for: job, summary: ResumeSummary(current: 4, used: 3, versions: 2, pages: 2, fit: "passed", eligibility: "current")) else {
            return XCTFail("the browsers' copy is of v3, not v4")
        }
    }

    func testTheStudioHookIsAbsentUntilTheWindowSetsIt() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        let r = try rig(jobs, f)
        XCTAssertNil(r.m.resume.openStudio, "no window yet: the card's button is disabled and opens nothing")
        let opened = KeyBox()
        r.m.resume.openStudio = { opened.add($0) }
        r.m.resume.openStudio?("fx:alpha:1")
        XCTAssertEqual(opened.all, ["fx:alpha:1"])
    }

    func testWithoutARunnerTheActionsAreAbsent() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        let bare = try rig(jobs, nil)
        await bare.m.refresh()
        XCTAssertFalse(bare.m.resume.isAvailable)
        bare.m.startResume(try XCTUnwrap(bare.m.jobs.first))
        XCTAssertEqual(bare.m.resume.phase, .idle)
        XCTAssertTrue(f.calls().isEmpty)
    }

    func testRetryAfterAFailedBuildGoesBackThroughTheConfirmation() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        try f.put("build.json", #"{"error":"the bank changed","code":"kb_changed"}"#)
        try f.put("build.fail", "")
        let r = try rig(jobs, f), m = r.m
        await m.refresh()
        let job = try XCTUnwrap(m.jobs.first)
        m.startResume(job)
        try await eventually("the confirmation") { m.resume.confirming != nil }
        m.resume.confirm()
        try await eventually("the failure") { if case .failed = m.resume.phase { true } else { false } }
        f.remove("build.fail")
        try f.put("build.json", Self.saved(version: 1, path: f.pdf.path, sha: f.sha))
        m.resume.retry()
        try await eventually("a new confirmation") { m.resume.confirming != nil }
        XCTAssertEqual(f.calls().filter { $0.contains("--dry-run") }.count, 2, "a retry asks for the cost again")
        XCTAssertEqual(f.calls().filter { !$0.contains("--dry-run") }.count, 1, "and spends nothing until confirmed")
    }
}

private final class BoundHeard: @unchecked Sendable {
    private let lock = NSLock()
    private var posts: [[String]] = []
    var keys: [[String]] { lock.lock(); defer { lock.unlock() }; return posts }
    func add(_ note: Notification) {
        lock.lock(); posts.append(note.userInfo?[ResumeBinding.keysUserInfoKey] as? [String] ?? []); lock.unlock()
    }
}

private final class KeyBox: @unchecked Sendable {
    private var items: [String] = []
    var all: [String] { items }
    func add(_ s: String) { items.append(s) }
}
