import CryptoKit
import Foundation
import XCTest
@testable import VaniApplyKit

/// The agent's keyed resume job: `vani jobs resume build`, then `use` only when the build is usable, then the
/// PDF read from the store and checked. A fake `vani` answers from files and records every argv, so the tests
/// say what was asked, what was not asked, and what reached the reply. Neutral names and bytes only.
final class ResumeJobTests: XCTestCase {
    let key = "greenhouse:acme:4012"
    // A 64-hex folder name, as the store's sha256(key).
    let folder = String(repeating: "ab", count: 32)

    /// A state folder, a fake vani and the files it answers from.
    struct Fixture {
        let dir: URL
        var state: URL { dir.appendingPathComponent("state") }
        var vani: URL { dir.appendingPathComponent("vani") }
        var log: URL { dir.appendingPathComponent("argv.log") }
        func pdfPath(_ version: Int = 1, folder: String) -> String {
            state.appendingPathComponent("resumes/\(folder)/v\(version).pdf").path
        }
        /// Every `vani` argument list the job ran, one per line.
        var argv: [String] {
            ((try? String(contentsOf: log, encoding: .utf8)) ?? "").split(separator: "\n").map(String.init)
        }
    }

    static func sha(_ d: Data) -> String { SHA256.hash(data: d).map { String(format: "%02x", $0) }.joined() }

    static func pdf(size: Int? = nil) -> Data {
        let head = Data("%PDF-1.4\n%neutral test file\n".utf8), tail = Data("\n%%EOF\n".utf8)
        return head + Data(repeating: 0x20, count: max(0, (size ?? 0) - head.count - tail.count)) + tail
    }

    /// The state folder, with `pdf` stored as `version` when given.
    func fixture(pdf: Data? = nil, version: Int = 1) throws -> Fixture {
        let f = Fixture(dir: try tempDir(self))
        let folderURL = f.state.appendingPathComponent("resumes/\(folder)")
        try FileManager.default.createDirectory(at: folderURL, withIntermediateDirectories: true)
        if let pdf { try pdf.write(to: folderURL.appendingPathComponent("v\(version).pdf")) }
        return f
    }

    /// Install the fake vani. `build` and `use` are the JSON it prints for each step (nil: that step must never run).
    func install(_ f: Fixture, build: [String: Any]?, use: [String: Any]?) throws {
        func put(_ name: String, _ obj: [String: Any]?) throws {
            let text = try obj.map { String(data: try JSONSerialization.data(withJSONObject: $0), encoding: .utf8)! }
                ?? #"{"error":"this step must not run","code":"bad_request"}"#
            try Data(text.utf8).write(to: f.dir.appendingPathComponent(name))
        }
        try put("build.json", build)
        try put("use.json", use)
        let script = """
        #!/bin/sh
        echo "$@" >> '\(f.log.path)'
        [ "$1 $2" = "jobs resume" ] || { echo '{"error":"wrong subcommand","code":"bad_request"}'; exit 1; }
        case "$3" in
          build) cat '\(f.dir.appendingPathComponent("build.json").path)' ;;
          use) cat '\(f.dir.appendingPathComponent("use.json").path)' ;;
          *) echo '{"error":"wrong step","code":"bad_request"}'; exit 1 ;;
        esac
        """
        try Data(script.utf8).write(to: f.vani)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: f.vani.path)
    }

    /// `vani jobs resume build`'s reply (a version record plus `pdf_path`), with the fields a usability check reads.
    func built(_ f: Fixture, version: Int = 1, fit: String = "passed", eligibility: String = "current", path: String? = "default", source: String = "tailored") -> [String: Any] {
        var b: [String: Any] = [
            "version": version, "source": source, "pages": 2, "reason": "", "notes": ["one orphan line"],
            "checks": ["fit": fit, "eligibility": eligibility, "lint": "passed", "figures": "verified"],
            "current_moved": true,
        ]
        if path == "default" { b["pdf_path"] = f.pdfPath(version, folder: folder) } else if let path { b["pdf_path"] = path }
        return b
    }

    /// `vani jobs resume use`'s reply.
    func used(_ f: Fixture, pdf: Data, version: Int = 1, name: String = "Resume_Acme.pdf", sha: String? = nil, path: String? = nil) -> [String: Any] {
        ["key": key, "version": version, "pdf_path": path ?? f.pdfPath(version, folder: folder), "name": name, "pdf_sha256": sha ?? Self.sha(pdf)]
    }

    func runner(_ f: Fixture, keys: DraftRunner.KeySource = .noKey, configPath: String? = nil) -> DraftRunner {
        DraftRunner(vaniBinary: f.vani, configPath: configPath, stateDir: f.state.path, keys: keys)
    }

    func run(_ r: DraftRunner, _ request: String? = nil) throws -> [String: Any] {
        let body = request ?? #"{"key":"\#(key)"}"#
        return try XCTUnwrap(JSONSerialization.jsonObject(with: r.runJob(.resumeJob, input: Data(body.utf8))) as? [String: Any])
    }

    /// A usable build of `pdf`, run to its reply.
    func runUsable(_ pdf: Data, version: Int = 1, fit: String = "passed", source: String = "tailored", reason: String = "") throws -> (reply: [String: Any], f: Fixture) {
        let f = try fixture(pdf: pdf, version: version)
        var b = built(f, version: version, fit: fit, source: source)
        b["reason"] = reason
        try install(f, build: b, use: used(f, pdf: pdf, version: version))
        return (try run(runner(f)), f)
    }

    // MARK: - the contract

    func testABuildThenAUseGivesTheStatelessReplyShapeWithTheKeyAndVersion() throws {
        let pdf = Self.pdf()
        let (reply, f) = try runUsable(pdf)
        XCTAssertNil(reply["error"], "\(reply)")
        XCTAssertEqual(f.argv, ["jobs resume build --json \(key)", "jobs resume use --json \(key) v1"], "build, then use of exactly that version")
        XCTAssertEqual(reply["name"] as? String, "Resume_Acme.pdf")
        XCTAssertEqual(reply["bytes"] as? Int, pdf.count, "the extension refuses a reply whose bytes differ from the decoded PDF")
        XCTAssertEqual(Data(base64Encoded: try XCTUnwrap(reply["pdf"] as? String)), pdf)
        XCTAssertEqual(reply["pages"] as? Int, 2)
        XCTAssertEqual(reply["selection"] as? String, "tailored")
        XCTAssertEqual(reply["notes"] as? [String], ["one orphan line"])
        XCTAssertEqual(reply["key"] as? String, key)
        XCTAssertEqual(reply["version"] as? Int, 1)
        XCTAssertNil(reply["reason"], "an empty reason is not sent")
    }

    func testASoftFitAndABaseFallbackAreUsable() throws {
        let (reply, _) = try runUsable(Self.pdf(), version: 4, fit: "soft", source: "base", reason: "the model's answer was not usable")
        XCTAssertNil(reply["error"], "\(reply)")
        XCTAssertEqual(reply["version"] as? Int, 4)
        XCTAssertEqual(reply["selection"] as? String, "base", "the reply says it is the base resume, as the stateless build does")
        XCTAssertEqual(reply["reason"] as? String, "the model's answer was not usable")
    }

    func testThePDFAtTheCapIsSentAndOneByteMoreIsRefused() throws {
        let max = ResumeStore.maxBytes
        let atCap = Self.pdf(size: max)
        XCTAssertEqual(atCap.count, max)
        let (ok, _) = try runUsable(atCap)
        XCTAssertNil(ok["error"], "\(ok["error"] ?? "")")
        XCTAssertEqual(ok["bytes"] as? Int, max)
        let wire = try JSONSerialization.data(withJSONObject: ok)
        XCTAssertLessThan(wire.count, JobKind.resumeJob.maxReplyBytes, "the capped PDF's reply fits the agent's reply cap")

        let over = Self.pdf(size: max + 1)
        XCTAssertEqual(over.count, max + 1)
        let (refused, _) = try runUsable(over)
        XCTAssertEqual(refused["code"] as? String, "too_large", "\(refused)")
        XCTAssertNil(refused["pdf"])
        XCTAssertEqual(refused["version"] as? Int, 1, "a saved version that could not be sent is named")
    }

    func testAShaMismatchIsRefusedAndNeverSent() throws {
        let pdf = Self.pdf()
        let f = try fixture(pdf: pdf)
        try install(f, build: built(f), use: used(f, pdf: pdf, sha: Self.sha(Data("another file".utf8))))
        let reply = try run(runner(f))
        XCTAssertEqual(reply["code"] as? String, "damaged", "\(reply)")
        XCTAssertNil(reply["pdf"])
        XCTAssertEqual(reply["version"] as? Int, 1)

        // The recorded hash is right, but the file changed after `use` vouched for it.
        let g = try fixture(pdf: pdf + Data("x".utf8))
        try install(g, build: built(g), use: used(g, pdf: pdf))
        XCTAssertEqual(try run(runner(g))["code"] as? String, "damaged")
    }

    func testNotAPDFIsRefusedEvenWithAMatchingHash() throws {
        let html = Data("<html>not a pdf</html>".utf8)
        let f = try fixture(pdf: html)
        try install(f, build: built(f), use: used(f, pdf: html))
        XCTAssertEqual(try run(runner(f))["code"] as? String, "damaged")
    }

    // MARK: - use only when usable

    func testUseIsNeverAskedForAVersionThatIsNotUsable() throws {
        let cases: [(fit: String, eligibility: String, path: String?, code: String)] = [
            ("failed", "current", "default", "fit"),
            ("not_run", "current", "default", "fit"),
            ("could_not_run", "current", "default", "fit"),
            ("passed", "stale", "default", "stale"),
            ("passed", "unknown", "default", "unchecked"),
            ("passed", "current", nil, "not_usable"),
            ("passed", "current", "", "not_usable"),
        ]
        for c in cases {
            let f = try fixture(pdf: Self.pdf(), version: 3)
            try install(f, build: built(f, version: 3, fit: c.fit, eligibility: c.eligibility, path: c.path), use: nil)
            let reply = try run(runner(f))
            XCTAssertEqual(reply["code"] as? String, c.code, "fit \(c.fit), eligibility \(c.eligibility), path \(c.path ?? "nil"): \(reply)")
            XCTAssertEqual(reply["version"] as? Int, 3, "the saved version is named: the build was paid for")
            XCTAssertNil(reply["pdf"])
            XCTAssertEqual(f.argv, ["jobs resume build --json \(key)"], "use was not asked for fit \(c.fit), eligibility \(c.eligibility)")
        }
    }

    func testAMissingChecksBlockIsNotUsable() throws {
        let f = try fixture()
        try install(f, build: ["version": 1, "pdf_path": f.pdfPath(1, folder: folder)], use: nil)
        XCTAssertEqual(try run(runner(f))["code"] as? String, "fit")
        XCTAssertEqual(f.argv.count, 1)
    }

    func testABuildThatNamesNoVersionIsRefused() throws {
        for version in [Optional<Any>.none, 0, -1, 1.5, "1", true] {
            let f = try fixture()
            var b = built(f)
            b["version"] = version
            try install(f, build: b, use: nil)
            XCTAssertEqual(try run(runner(f))["code"] as? String, "bad_reply", "\(String(describing: version))")
            XCTAssertEqual(f.argv.count, 1)
        }
    }

    // MARK: - vani's refusals pass through

    func testABuildRefusalPassesThroughAndNothingIsUsed() throws {
        for (code, message) in [("busy", "another resume build is running"), ("no_model", "the model could not run (no key)"),
                                ("kb_changed", "the knowledge base changed during the build; build again"), ("unknown_key", "no such posting"),
                                ("bad_request", "this posting has no text to build from"), ("timeout", "the build ran out of time")] {
            let f = try fixture()
            try install(f, build: ["error": message, "code": code], use: nil)
            let reply = try run(runner(f))
            XCTAssertEqual(reply["code"] as? String, code)
            XCTAssertEqual(reply["error"] as? String, message)
            XCTAssertNil(reply["version"], "no version was saved, so none is named")
            XCTAssertEqual(f.argv.count, 1, "\(code): use was not asked for")
        }
    }

    func testAUseRefusalPassesThroughWithTheVersionItConcerns() throws {
        for code in ["stale", "unchecked", "not_usable", "damaged", "timeout"] {
            let f = try fixture(pdf: Self.pdf(), version: 2)
            try install(f, build: built(f, version: 2), use: ["error": "v2 is \(code)", "code": code])
            let reply = try run(runner(f))
            XCTAssertEqual(reply["code"] as? String, code)
            XCTAssertEqual(reply["error"] as? String, "v2 is \(code)")
            XCTAssertEqual(reply["version"] as? Int, 2)
            XCTAssertNil(reply["pdf"])
            XCTAssertEqual(f.argv, ["jobs resume build --json \(key)", "jobs resume use --json \(key) v2"])
        }
    }

    func testVaniSayingNothingUsefulIsAProviderErrorNotAPass() throws {
        let f = try fixture()
        try install(f, build: [:], use: [:])
        try Data("#!/bin/sh\necho 'not json'\nexit 3\n".utf8).write(to: f.vani)
        let reply = try run(runner(f))
        XCTAssertEqual(reply["code"] as? String, "provider")
        XCTAssertTrue((reply["error"] as? String)?.contains("exited 3") == true, "\(reply)")

        let missing = DraftRunner(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"), stateDir: f.state.path)
        XCTAssertEqual(try run(missing)["code"] as? String, "config")
    }

    func testAnUnreadableUseReplyIsRefused() throws {
        let pdf = Self.pdf()
        let f = try fixture(pdf: pdf)
        let path = f.pdfPath(1, folder: folder), sha = Self.sha(pdf)
        let replies: [[String: Any]] = [
            ["version": 1],
            ["version": 2, "pdf_path": path, "name": "R.pdf", "pdf_sha256": sha],
            ["version": 1, "pdf_path": path, "name": "R.docx", "pdf_sha256": sha],
            ["version": 1, "pdf_path": path, "name": "R.pdf", "pdf_sha256": "zz"],
            ["version": 1, "pdf_path": path, "name": "R.pdf"],
        ]
        for use in replies {
            try install(f, build: built(f), use: use)
            let reply = try run(runner(f))
            XCTAssertEqual(reply["code"] as? String, "bad_reply", "\(use): \(reply)")
            XCTAssertNil(reply["pdf"])
        }
    }

    // MARK: - the path is the store's own

    func testOnlyTheStoresOwnFileForThisVersionIsRead() throws {
        let pdf = Self.pdf()
        let f = try fixture(pdf: pdf)
        let outside = f.dir.appendingPathComponent("elsewhere.pdf")
        try pdf.write(to: outside)
        let otherFolder = f.dir.appendingPathComponent("other-state/resumes/\(folder)")
        try FileManager.default.createDirectory(at: otherFolder, withIntermediateDirectories: true)
        try pdf.write(to: otherFolder.appendingPathComponent("v1.pdf"))
        let linkFolder = f.state.appendingPathComponent("resumes/\(String(repeating: "cd", count: 32))")
        try FileManager.default.createDirectory(at: linkFolder, withIntermediateDirectories: true)
        try FileManager.default.createSymbolicLink(at: linkFolder.appendingPathComponent("v1.pdf"), withDestinationURL: URL(fileURLWithPath: f.pdfPath(1, folder: folder)))

        let paths = [
            "relative/resumes/\(folder)/v1.pdf",
            outside.path,
            f.state.appendingPathComponent("resumes/\(folder)/../\(folder)/v1.pdf").path,
            f.state.appendingPathComponent("resumes/\(folder)/v9.pdf").path,
            f.state.appendingPathComponent("resumes/short/v1.pdf").path,
            f.dir.appendingPathComponent("other-state/resumes/\(folder)/v1.pdf").path,
            linkFolder.appendingPathComponent("v1.pdf").path,
            "/etc/hosts",
        ]
        for path in paths {
            try install(f, build: built(f, path: path), use: used(f, pdf: pdf, path: path))
            let reply = try run(runner(f))
            XCTAssertNotNil(reply["error"], "refused: \(path)")
            XCTAssertNil(reply["pdf"], "never sent: \(path)")
            XCTAssertEqual(reply["version"] as? Int, 1, path)
        }
        // The same folder, named correctly, is read.
        try install(f, build: built(f), use: used(f, pdf: pdf))
        XCTAssertNil(try run(runner(f))["error"])
    }

    func testWithoutAKnownStateFolderTheStoreShapeStillHolds() throws {
        // The app's Settings left the folder to vani's default, so no state folder is passed to vani.
        let pdf = Self.pdf()
        let f = try fixture(pdf: pdf)
        try install(f, build: built(f), use: used(f, pdf: pdf))
        let ok = try run(DraftRunner(vaniBinary: f.vani))
        XCTAssertNil(ok["error"], "\(ok)")

        let outside = f.dir.appendingPathComponent("elsewhere.pdf")
        try pdf.write(to: outside)
        try install(f, build: built(f, path: outside.path), use: used(f, pdf: pdf, path: outside.path))
        let refused = try run(DraftRunner(vaniBinary: f.vani))
        XCTAssertNotNil(refused["error"])
        XCTAssertNil(refused["pdf"])
    }

    // MARK: - the request

    func testTheRequestIsAPostingKeyAndNothingElse() throws {
        let f = try fixture()
        try install(f, build: nil, use: nil)
        for body in [
            "{}", #"{"key":""}"#, #"{"key":"nocolon"}"#, #"{"key":"-rf:x"}"#, #"{"key":"a b:c"}"#, #"{"key":"a:b\n"}"#, #"{"key":5}"#,
            #"{"key":"\#(key)","header":{"name":"Test Applicant"}}"#, #"{"key":"\#(key)","job":{}}"#, "[]", "not json", "",
            #"{"key":"\#(String(repeating: "k", count: 600)):x"}"#,
        ] {
            let reply = try run(runner(f), body)
            XCTAssertEqual(reply["code"] as? String, "bad_request", body)
        }
        XCTAssertTrue(f.argv.isEmpty, "vani never ran for a bad request")
    }

    func testTheModelKeyIsForTheBuildOnlyNeverTheUse() throws {
        let pdf = Self.pdf()
        let f = try fixture(pdf: pdf)
        try install(f, build: built(f), use: used(f, pdf: pdf))
        let cfg = try vaniConfig(self, provider: "anthropic")
        let launcher = try fakeLauncher(self)
        let reply = try run(runner(f, keys: .anthropicAccount(launcher.url), configPath: cfg))
        XCTAssertNil(reply["error"], "\(reply)")
        XCTAssertEqual(f.argv.count, 2)
        // The launcher's file holds its last start: the build's. The use never went through it.
        XCTAssertEqual(launcher.recordedArguments, ["exec", "--", f.vani.path, "jobs", "resume", "build", "--json", key])

        let none = try fakeLauncher(self)
        let g = try fixture(pdf: pdf)
        try install(g, build: built(g), use: used(g, pdf: pdf))
        let missing = try run(runner(g, keys: .anthropicAccount(URL(fileURLWithPath: "/nonexistent/anthropic-account")), configPath: cfg))
        XCTAssertEqual(missing["code"] as? String, "account", "a missing launcher refuses the build, as it does a draft")
        XCTAssertNil(none.recordedArguments)
        XCTAssertTrue(g.argv.isEmpty, "and nothing was run")
    }

    // MARK: - the job table and the protocol

    func testTheKindsBoundsAreTheBuildsPlusTheUsesAndTheRequestIsSmall() {
        XCTAssertEqual(JobKind(rawValue: "resumeJob"), .resumeJob)
        XCTAssertEqual(JobKind.resumeJob.timeout, 375 + 25)
        XCTAssertEqual(DraftRunner.resumeBuildTimeout, 375)
        XCTAssertEqual(DraftRunner.resumeUseTimeout, 25)
        XCTAssertGreaterThan(JobKind.resumeJob.timeout, JobKind.resume.timeout)
        XCTAssertEqual(JobKind.resumeJob.maxRequestBytes, 4 * 1024)
        XCTAssertTrue(JobKind.resumeJob.usesModel)
        XCTAssertTrue(JobKind.resumeJob.buildsResume && JobKind.resume.buildsResume)
        XCTAssertFalse(JobKind.draft.buildsResume || JobKind.suggest.buildsResume)
        XCTAssertGreaterThan(JobKind.resumeJob.maxReplyBytes, (ResumeStore.maxBytes + 2) / 3 * 4 + 4096)
        XCTAssertLessThan(JobKind.resumeJob.maxReplyBytes, NativeMessaging.maxReplyBytes - 4096)
    }

    func testTheAgentRefusesAnOversizedKeyedRequestBeforeVaniStarts() {
        let jobs = AgentJobs { _, _ in Data("{}".utf8) }
        let big = Data(#"{"key":"a:"#.utf8) + Data(repeating: 0x61, count: JobKind.resumeJob.maxRequestBytes) + Data(#""}"#.utf8)
        XCTAssertNotNil(jobs.start(kind: "resumeJob", request: big).error)
        XCTAssertNotNil(jobs.start(kind: "resumeJob", request: Data()).error)
        XCTAssertNil(jobs.start(kind: "resumeJob", request: Data(#"{"key":"a:b"}"#.utf8)).error)
    }

    func testAKeyedJobStartedFromABrowserIsLoggedAsAResumeBuildAndNothingElse() throws {
        let store = BankStore(directory: try tempDir(self))
        let agent = LocalAgent(jobs: AgentJobs { _, _ in Data("{}".utf8) })
        let started = NativeProtocol.handle(["op": "jobStart", "kind": "resumeJob", "request": ["key": key]], store: store, source: .chrome, agent: agent)
        XCTAssertNotNil(started["job"], "\(started)")
        let raw = try String(contentsOf: store.directory.appendingPathComponent("activity.jsonl"), encoding: .utf8)
        XCTAssertTrue(raw.contains("\"building\""), raw)
        XCTAssertFalse(raw.contains("acme"), "the posting key must not reach the log")
        XCTAssertFalse(raw.contains("\"started\""), "it is counted as a resume build, not as a draft")
    }
}
