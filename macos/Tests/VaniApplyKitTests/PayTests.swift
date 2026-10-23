import XCTest
@testable import VaniApplyKit

final class PayTests: XCTestCase {
    func testPayFilterArguments() {
        var f = JobFilter()
        f.currency = "EUR"
        XCTAssertTrue(f.isEmpty, "a currency alone filters nothing")
        XCTAssertEqual(f.activeCount, 0)
        XCTAssertEqual(JobsClient.selection("", f), ["--currency", "EUR"], "the currency still steers the salary sort and stats")

        f.minSalary = 150_000
        f.salaryListed = true
        f.seniorities = ["senior", "none", "mid"]
        f.minRelevance = 50
        XCTAssertFalse(f.isEmpty)
        XCTAssertEqual(f.activeCount, 4)
        XCTAssertEqual(JobsClient.selection("", f),
                       ["--min-salary", "150000", "--salary-listed", "--currency", "EUR",
                        "--seniority", "mid,none,senior", "--min-relevance", "50"])

        // Zero floors send nothing; USD is the default and is not sent.
        var z = JobFilter()
        z.minSalary = 0
        z.minRelevance = 0
        XCTAssertEqual(JobsClient.selection("", z), [])

        f.clear()
        XCTAssertTrue(f.isEmpty)
        XCTAssertEqual(f.currency, "EUR", "clearing filters keeps the chosen currency")

        XCTAssertEqual(JobsClient.listArguments(.all, query: "", sort: .salary, filter: JobFilter(), limit: 5),
                       ["jobs", "list", "--json", "--view", "all", "--limit", "5", "--sort", "salary"])
    }

    func testDecodesPay() throws {
        let reply = """
        {"view":"all","jobs":[\
        {"key":"gh:a:1","company":"A","title":"T","status":"digested","first_seen":"2026-10-18T07:00:00Z",\
        "pay":[{"currency":"USD","period":"year","min":120000,"max":160000,"annual_min":120000,"annual_max":160000,"text":"$120,000 – $160,000 USD"},\
        {"currency":"GBP","period":"year","min":95000,"max":120000,"annual_min":95000,"annual_max":120000,"text":"£95,000 - £120,000"}]},\
        {"key":"gh:a:2","company":"A","title":"T","status":"digested","first_seen":"2026-10-18T07:00:00Z",\
        "pay":[{"currency":"USD","period":"hour","min":70,"max":90.5,"annual_min":145600,"annual_max":188240,"text":"$70–$90.50/hr"}]},\
        {"key":"gh:a:3","company":"A","title":"T","status":"digested","first_seen":"2026-10-18T07:00:00Z"}],\
        "total":3,"shown":3,"counts":{"all":3}}
        """
        let page = try JobsClient.decoder.decode(JobsPage.self, from: Data(reply.utf8))
        XCTAssertEqual(page.jobs[0].pay?.count, 2)
        XCTAssertEqual(page.jobs[0].payLabel(preferring: "USD"), "USD 120k–160k/yr")
        XCTAssertEqual(page.jobs[0].payLabel(preferring: "GBP"), "GBP 95k–120k/yr")
        XCTAssertEqual(page.jobs[0].payLabel(preferring: "EUR"), "USD 120k–160k/yr", "no pay in EUR: the first stated")
        XCTAssertEqual(page.jobs[1].payLabel(preferring: "USD"), "USD 70–90.5/hr")
        XCTAssertEqual(page.jobs[1].pay?.first?.annualMax, 188240)
        XCTAssertNil(page.jobs[2].pay)
        XCTAssertNil(page.jobs[2].payLabel(preferring: "USD"))
    }

    func testShortAmounts() {
        let cases: [(Double, String)] = [(0, "0"), (52.5, "52.5"), (999, "999"), (1000, "1k"), (153_200, "153.2k"),
                                         (1_000_000, "1M"), (2_500_000, "2.5M")]
        for (v, want) in cases { XCTAssertEqual(Job.Pay.short(v), want, "\(v)") }
        XCTAssertEqual(Job.Pay(currency: "EUR", period: "month", min: 4000, max: 4000, annualMin: 48000, annualMax: 48000).label, "EUR 4k/mo")
        XCTAssertEqual(Job.Pay(currency: "USD", period: "", min: 1, max: 2, annualMin: 1, annualMax: 2).label, "USD 1–2", "an unknown period shows no unit")
    }

    func testDecodesSalaryStats() throws {
        let reply = """
        {"view":"all","total":2,"fit":{},"work_mode":{},"sponsorship":{},"seniority":{"senior":1},"basis":{},"fresh_source":{},\
        "age":[],"relevance":[0,0,0,0,0,0,0,0,0,0],"priority":[0,0,0,0,0,0,0,0,0,0],"companies":[],"keywords":[],\
        "salary":{"currency":"USD","listed":1,"other":0,"unlisted":1,"median":140000,\
        "buckets":[{"label":"<100k","min":0,"max":100000,"n":0},{"label":"≥400k","min":400000,"max":-1,"n":1}]}}
        """
        let s = try JobsClient.decoder.decode(JobsStats.self, from: Data(reply.utf8))
        XCTAssertEqual(s.salary?.currency, "USD")
        XCTAssertEqual(s.salary?.listed, 1)
        XCTAssertEqual(s.salary?.median, 140_000)
        XCTAssertEqual(s.salary?.buckets.last?.max, -1)
        // A vani from before pay reading sends no salary.
        let old = try JobsClient.decoder.decode(JobsStats.self, from: Data(reply.replacingOccurrences(
            of: #","salary":{"currency":"USD","listed":1,"other":0,"unlisted":1,"median":140000,"buckets":[{"label":"<100k","min":0,"max":100000,"n":0},{"label":"≥400k","min":400000,"max":-1,"n":1}]}"#,
            with: "").utf8))
        XCTAssertNil(old.salary)
    }

    /// The contract against the Go binary, when one is built at build/vani.
    func testPayAgainstTheRealVani() throws {
        let vani = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("build/vani")
        guard FileManager.default.isExecutableFile(atPath: vani.path) else {
            throw XCTSkip("no Go vani at \(vani.path); build it to run the contract test")
        }
        let state = FileManager.default.temporaryDirectory.appendingPathComponent("vani-pay-\(UUID().uuidString)")
        addTeardownBlock { try? FileManager.default.removeItem(at: state) }
        let ledger = state.appendingPathComponent("ledger")
        try FileManager.default.createDirectory(at: ledger, withIntermediateDirectories: true)
        // Titles differ by key: vani folds exact company+title duplicates into one row
        // in the ranked "new" view (jobs list, b4518a6), so a shared title would hide rows.
        func line(_ key: String, _ desc: String) -> String {
            #"{"v":1,"at":"2026-10-18T12:00:00Z","key":"\#(key)","status":"digested","posting":{"key":"\#(key)","ats":"gh","company":"Acme","title":"Engineer \#(key)","url":"https://boards.example.com/1","description":"\#(desc)","seen_at":"2026-10-18T12:00:00Z"},"annotations":{"triage.seniority":"senior"}}"#
        }
        let lines = [line("gh:acme:hi", "Salary: $180,000 - $220,000"), line("gh:acme:lo", "Salary: $90,000 - $110,000"),
                     line("gh:acme:none", "Competitive pay.")]
        try Data((lines.joined(separator: "\n") + "\n").utf8).write(to: ledger.appendingPathComponent("events.jsonl"))

        let c = JobsClient(vaniBinary: vani, stateDir: state.path)
        var f = JobFilter()
        f.minSalary = 150_000
        let page = try c.list(.new, sort: .salary, filter: f)
        XCTAssertEqual(page.jobs.map(\.key), ["gh:acme:hi", "gh:acme:none"], "below the floor goes; no stated pay stays")
        XCTAssertEqual(page.jobs[0].payLabel(preferring: "USD"), "USD 180k–220k/yr")
        f.salaryListed = true
        f.seniorities = ["senior"]
        XCTAssertEqual(try c.list(.new, filter: f).jobs.map(\.key), ["gh:acme:hi"])
        let stats = try c.stats(.new)
        XCTAssertEqual(stats.salary?.listed, 2)
        XCTAssertEqual(stats.salary?.unlisted, 1)
        XCTAssertEqual(stats.salary?.median, 150_000)
    }
}
