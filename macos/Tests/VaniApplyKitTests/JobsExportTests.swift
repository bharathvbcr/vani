import Foundation
import XCTest
@testable import VaniApplyKit

/// "Export to Excel": the arguments the app sends `vani jobs export`, the words it shows, and the
/// contract against the real vani.
final class JobsExportTests: XCTestCase {
    func testArguments() {
        var f = JobFilter()
        f.sponsorships = ["none"]
        XCTAssertEqual(JobsClient.exportArguments(.new, query: "", sort: .standard, filter: JobFilter(), .top(100), out: "/tmp/a.xlsx"),
                       ["jobs", "export", "--json", "--view", "new", "--out=/tmp/a.xlsx", "--top", "100"])
        XCTAssertEqual(JobsClient.exportArguments(.saved, query: "q", sort: .priority, filter: f, .keys(["gh:a:1"]), out: "/tmp/-b.xlsx",
                                                  halfLifeDays: 20),
                       ["jobs", "export", "--json", "--view", "saved", "--out=/tmp/-b.xlsx", "--keys-stdin", "--sort", "priority",
                        "--half-life", "20", "--q", "q", "--sponsorship", "none"],
                       "keys go on stdin, never in the arguments; the list's own selection flags follow")
        XCTAssertEqual(JobsClient.exportArguments(.new, query: "", sort: .standard, filter: JobFilter(), .top(100), out: "/tmp/a.xlsx",
                                                  already: .skip),
                       ["jobs", "export", "--json", "--view", "new", "--out=/tmp/a.xlsx", "--top", "100", "--already-exported", "skip"])
        XCTAssertEqual(JobsClient.exportArguments(.new, query: "", sort: .standard, filter: JobFilter(), .keys(["gh:a:1"]), out: nil,
                                                  dryRun: true),
                       ["jobs", "export", "--json", "--view", "new", "--keys-stdin", "--dry-run"],
                       "a dry run names no file and says nothing about exported roles, so it reports them all")
        XCTAssertEqual(JobsClient.exportSizes.last, JobsClient.maxExportRows)
        XCTAssertEqual(JobsClient.maxExportRows, 5000, "vani's --top ceiling (maxJobsLimit)")
    }

    func testSummary() {
        let first = JobsExport(path: "/x/Top.xlsx", rows: 100, total: 5961, view: "new", sort: "default")
        XCTAssertEqual(first.summary(.top(100)), "Saved the first 100 of 5,961 roles to Top.xlsx.")
        let all = JobsExport(path: "/x/All.xlsx", rows: 12, total: 12, view: "new", sort: "default")
        XCTAssertEqual(all.summary(.top(5000)), "Saved 12 roles to All.xlsx.")
        let picked = JobsExport(path: "/x/Sel.xlsx", rows: 2, total: 3, view: "new", sort: "default", missing: ["gh:a:9"])
        XCTAssertEqual(picked.summary(.keys(["gh:a:1", "gh:a:2", "gh:a:9"])),
                       "Saved 2 selected roles to Sel.xlsx. 1 is no longer in this list and was left out.")
        let one = JobsExport(path: "/x/One.xlsx", rows: 1, total: 1, view: "new", sort: "default")
        XCTAssertEqual(one.summary(.keys(["gh:a:1"])), "Saved 1 selected role to One.xlsx.")
        let next = JobsExport(path: "/x/Next.xlsx", rows: 100, total: 5961, view: "new", sort: "default", alreadyExported: 100, skipped: 100)
        XCTAssertEqual(next.summary(.top(100)), "Saved 100 roles to Next.xlsx. Left out 100 roles exported before.",
                       "skipped rows were filled from further down, so they are not the list's first 100")
        let fresh = JobsExport(path: "/x/S.xlsx", rows: 1, total: 2, view: "new", sort: "default", alreadyExported: 1, skipped: 1)
        XCTAssertEqual(fresh.summary(.keys(["gh:a:1", "gh:a:2"])), "Saved 1 selected role to S.xlsx. Left out 1 role exported before.")
    }

    func testRepeatPrompt() throws {
        let at = try Date("2026-10-27T14:05:00Z", strategy: .iso8601)
        let utc = TimeZone(identifier: "UTC")!
        XCTAssertNil(JobsExport(path: "/x", rows: 100, total: 200, view: "new", sort: "default").repeatPrompt(.top(100)),
                     "nothing exported before: nothing to ask")
        let some = JobsExport(path: "/x", rows: 100, total: 200, view: "new", sort: "default", alreadyExported: 37,
                              lastExportedAt: at, lastFile: "/Users/o/Vani jobs (new, top 100) 2026-10-27.xlsx")
        let p = try XCTUnwrap(some.repeatPrompt(.top(100), in: utc))
        XCTAssertEqual(p.title, "37 of these 100 roles were exported before")
        XCTAssertTrue(p.message.hasPrefix("Last exported "), p.message)
        XCTAssertTrue(p.message.contains("to Vani jobs (new, top 100) 2026-10-27.xlsx."), "the file's name, not its folder: \(p.message)")
        XCTAssertTrue(p.message.hasSuffix("the next roles in the list take their place."), p.message)
        XCTAssertEqual([p.skip, p.include, p.cancel], ["Skip Exported Roles", "Export Them Again", "Cancel"])

        let all = JobsExport(path: "/x", rows: 2, total: 2, view: "new", sort: "default", alreadyExported: 2, lastExportedAt: at)
        let q = try XCTUnwrap(all.repeatPrompt(.keys(["a", "b"]), in: utc))
        XCTAssertEqual(q.title, "All 2 of these roles were exported before")
        XCTAssertFalse(q.message.contains(".xlsx"), "no file named when vani no longer knows it: \(q.message)")
        XCTAssertTrue(q.message.hasSuffix("only the selected roles not yet exported."), q.message)
        let one = JobsExport(path: "/x", rows: 1, total: 1, view: "new", sort: "default", alreadyExported: 1)
        XCTAssertEqual(one.repeatPrompt(.keys(["a"]))?.title, "This role was exported before")
    }

    func testSuggestedName() throws {
        let day = try Date("2026-10-27T23:30:00Z", strategy: .iso8601)
        let utc = TimeZone(identifier: "UTC")!
        XCTAssertEqual(JobsExport.suggestedName(view: .new, .top(250), on: day, in: utc), "Vani jobs (new, top 250) 2026-10-27.xlsx")
        XCTAssertEqual(JobsExport.suggestedName(view: .saved, .keys(["a", "b"]), on: day, in: TimeZone(identifier: "Asia/Tokyo")!),
                       "Vani jobs (saved, 2 selected) 2026-10-28.xlsx", "the owner's own day")
    }

    func testDecodesTheReply() throws {
        let reply = #"{"path":"/x/a.xlsx","rows":2,"total":3,"view":"new","sort":"priority","missing":["gh:a:9"],"ledger":"/s/ledger","records":9}"#
        let r = try JobsClient.decoder.decode(JobsExport.self, from: Data(reply.utf8))
        XCTAssertEqual(r, JobsExport(path: "/x/a.xlsx", rows: 2, total: 3, view: "new", sort: "priority", missing: ["gh:a:9"]),
                       "a vani that keeps no export record: nothing exported before")
        let checked = #"{"path":"/x/a.xlsx","rows":2,"total":3,"view":"new","sort":"default","missing":[],"already_exported":1,"last_exported_at":"2026-10-27T14:05:00.123456789Z","last_file":"/x/old.xlsx","skipped":0,"dry_run":true,"ledger":"/s/ledger","records":9}"#
        let c = try JobsClient.decoder.decode(JobsExport.self, from: Data(checked.utf8))
        XCTAssertEqual(c.alreadyExported, 1)
        XCTAssertEqual(c.lastFile, "/x/old.xlsx")
        XCTAssertNotNil(c.lastExportedAt, "Go's nanosecond RFC 3339 time")
    }

    func testRefusesNoKeysAndTooManyWithoutRunningVani() {
        let c = JobsClient(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"))
        let out = URL(fileURLWithPath: "/tmp/never.xlsx")
        XCTAssertThrowsError(try c.export(.new, .keys([]), to: out)) {
            XCTAssertTrue(String(describing: $0).contains("Select at least one role"), "\($0)")
        }
        let many = (0...JobsClient.maxExportRows).map { "gh:a:\($0)" }
        XCTAssertThrowsError(try c.export(.new, .keys(many), to: out)) {
            XCTAssertTrue(String(describing: $0).contains("At most 5,000"), "\($0)")
        }
    }

    // MARK: against the real vani (VANI_TEST_BINARY, else build/vani; skipped when neither is built)

    private func realVani() throws -> URL {
        if let path = ProcessInfo.processInfo.environment["VANI_TEST_BINARY"], !path.isEmpty {
            return URL(fileURLWithPath: path)
        }
        let vani = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("build/vani")
        guard FileManager.default.isExecutableFile(atPath: vani.path) else {
            throw XCTSkip("no Go vani at \(vani.path); build it to run the contract test")
        }
        return vani
    }

    private func event(_ n: Int, fit: String) -> String {
        let key = "gh:acme:\(n)", at = "2026-10-2\(n)T12:00:00Z"
        return #"{"v":1,"at":"\#(at)","key":"\#(key)","status":"digested","reason":"new","posting":{"key":"\#(key)","ats":"gh","company":"Acme","title":"Engineer \#(n)","seen_at":"\#(at)"},"annotations":{"triage.role_fit":"\#(fit)"}}"#
    }

    func testAgainstTheRealVani() throws {
        let vani = try realVani()
        let state = try tempDir(self)
        let dir = state.appendingPathComponent("ledger")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let lines = [event(1, fit: "strong_fit"), event(2, fit: "partial_fit"), event(3, fit: "adjacent")]
        try Data((lines.joined(separator: "\n") + "\n").utf8).write(to: dir.appendingPathComponent("events.jsonl"))
        let c = JobsClient(vaniBinary: vani, stateDir: state.path)
        let out = try tempDir(self)

        let top = try c.export(.new, .top(2), to: out.appendingPathComponent("top.xlsx"))
        XCTAssertEqual(top.rows, 2)
        XCTAssertEqual(top.total, 3)
        XCTAssertEqual(top.missing, [])
        XCTAssertTrue(FileManager.default.fileExists(atPath: top.path))
        let listed = try c.list(.new).jobs.map(\.key)
        XCTAssertEqual(listed.count, 3)

        // gh:acme:1 or 2 went out in the top 2: unasked, the export refuses and writes nothing.
        let selected: JobsExportScope = .keys(["gh:acme:3", "gh:acme:9", "gh:acme:1"])
        let check = try c.exportCheck(.new, selected)
        XCTAssertEqual(check.rows, 2)
        XCTAssertGreaterThanOrEqual(check.alreadyExported, 1)
        XCTAssertEqual(check.lastFile, top.path)
        XCTAssertNotNil(check.repeatPrompt(selected))
        XCTAssertThrowsError(try c.export(.new, selected, to: out.appendingPathComponent("refused.xlsx"))) {
            XCTAssertTrue(String(describing: $0).contains("exported before"), "\($0)")
        }
        XCTAssertFalse(FileManager.default.fileExists(atPath: out.appendingPathComponent("refused.xlsx").path))
        let next = try c.export(.new, .top(2), to: out.appendingPathComponent("next.xlsx"), already: .skip)
        XCTAssertEqual(next.rows, 1, "one role was never exported")
        XCTAssertEqual(next.skipped, 2)

        let picked = try c.export(.new, selected, to: out.appendingPathComponent("sel.xlsx"), already: .include)
        XCTAssertEqual(picked.rows, 2)
        XCTAssertEqual(picked.total, 3)
        XCTAssertEqual(picked.missing, ["gh:acme:9"], "a key the list lacks is named, not dropped silently")
        let head = try FileHandle(forReadingFrom: URL(fileURLWithPath: picked.path)).read(upToCount: 2)
        XCTAssertEqual(head, Data("PK".utf8), "an .xlsx is a zip package")

        XCTAssertThrowsError(try c.export(.new, .top(1), to: out.appendingPathComponent("no/such/dir/x.xlsx"))) {
            XCTAssertTrue(String(describing: $0).contains("does not exist"), "\($0)")
        }
    }
}
