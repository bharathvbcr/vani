import CryptoKit
import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// A fake `vani jobs resume` and the replies it gives, shared by the Studio window's model and view tests.
/// It records each argument list in calls.log and each render's stdin, and replies from files beside it. Every id,
/// company and sentence is made up; the PDFs are generated and carry no name.
@MainActor
class StudioFakeCase: XCTestCase {
    static let key = "fx:alpha:1"
    static let poolJSON = ResumeEditTestsData.pool

    struct Rig {
        let dir: URL
        let editor: ResumeStudioEditor
        let resume: ResumeStudioModel
        let bank: BankStore
        let jobs: JobsModel?
        let pdf3: Data
        let pdf4: Data
        var pdf3Path: String { dir.appendingPathComponent("v3.pdf").path }
        var pdf4Path: String { dir.appendingPathComponent("v4.pdf").path }
        var tmp: URL { dir.appendingPathComponent("resumes/.tmp", isDirectory: true) }

        func calls() -> [String] {
            ((try? String(contentsOf: dir.appendingPathComponent("calls.log"), encoding: .utf8)) ?? "").split(separator: "\n").map(String.init)
        }

        func stdin(_ kind: String) -> [String: Any]? {
            (try? Data(contentsOf: dir.appendingPathComponent("stdin-\(kind).json")))
                .flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
        }

        func put(_ name: String, _ text: String) throws { try Data(text.utf8).write(to: dir.appendingPathComponent(name)) }
        func touch(_ name: String) throws { try put(name, "") }
        func remove(_ name: String) { try? FileManager.default.removeItem(at: dir.appendingPathComponent(name)) }
    }

    func sha(_ d: Data) -> String { SHA256.hash(data: d).map { String(format: "%02x", $0) }.joined() }

    func pdf(_ fill: UInt8) -> Data {
        var d = Data("%PDF-1.4\n".utf8)
        d.append(Data(repeating: fill, count: 1500))
        return d
    }

    /// `meta` is extra record fields written after `source`, each ending in a comma: `"parent":3,"instruction":"x",`.
    func versionJSON(_ n: Int, source: String = "tailored", fit: String = "passed", eligibility: String = "current", pages: Int = 2,
                             pdfPath: String?, pdfSHA: String?, selection: String? = nil, current: Bool = false, used: Bool = false,
                             fresh: String? = "current", cost: String = #""cost":{"usd":0.031,"complete":false,"turns":2},"#,
                             notes: String = "", meta: String = "") -> String {
        var s = """
        {"schema":2,"key":"fx:alpha:1","version":\(n),"source":"\(source)",\(meta)\(cost)
         "checks":{"eligibility":"\(eligibility)","lint":"passed","fit":"\(fit)","figures":"verified"},"pages":\(pages),\(notes)
         "created":"2026-10-25T12:00:00Z","current":\(current),"used":\(used)
        """
        if let pdfPath, let pdfSHA { s += #","pdf_path":"\#(pdfPath)","pdf_sha256":"\#(pdfSHA)","fit_lines":{"j1":2,"p1":3}"# }
        if let fresh { s += #","eligibility":{"state":"\#(fresh)"}"# }
        if let selection { s += #","selection":"# + selection }
        return s + "}"
    }

    func shownJSON(version: Int, versions: [String], current: Int = 3, used: Int = 2, pool: String? = StudioFakeCase.poolJSON,
                           checkError: String? = nil, checked: Bool = true) -> String {
        var s = """
        {"key":"fx:alpha:1","eligibility_checked":\(checked),"current":\(current),"used":\(used),"version":\(version),
         "versions":[\(versions.joined(separator: ","))],
         "keywords":[{"term":"Kubernetes","n":3,"target":true},{"term":"CUDA","n":1,"gap":true}]
        """
        if let pool { s += #","pool":"# + pool }
        if let checkError { s += #","check_error":"\#(checkError)""# }
        return s + "}"
    }

    func rig(job: Bool = true, versions: Bool = true, pool: Bool = true, checkError: String? = nil, viaJobs: Bool = false) throws -> Rig {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-apply-studio-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir.appendingPathComponent("resumes/.tmp"), withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        let script = """
        #!/bin/sh
        d='\(dir.path)'
        printf '%s\\n' "$*" >> "$d/calls.log"
        v=''
        for a in "$@"; do case "$a" in v[0-9]*) v="$a" ;; esac; done
        # argv is always `jobs resume VERB ...`: dispatch on the verb, never on text an owner typed (an
        # instruction that says "show" or "v2" must not look like a verb or a version).
        case "$3" in
        show)
            f="$d/show-$v.json"; [ -n "$v" ] && [ -f "$f" ] || f="$d/show.json"
            cat "$f"; [ -f "$d/show.fail" ] && exit 1; exit 0 ;;
        use) cat "$d/use.json"; exit 0 ;;
        revalidate) cat "$d/revalidate.json"; exit 0 ;;
        render)
            case " $* " in *" --fit "*) kind=fit ;; *) kind=preview ;; esac
            cat > "$d/stdin-$kind.json"
            if [ -f "$d/slow" ]; then echo $$ > "$d/pid"; exec sleep 30; fi
            cat "$d/$kind.json"
            [ -f "$d/$kind.fail" ] && exit 1
            exit 0 ;;
        build)
            case " $* " in
            *" --dry-run "*) cat "$d/dry.json"; [ -f "$d/dry.fail" ] && exit 1; exit 0 ;;
            esac
            if [ -f "$d/slowbuild" ]; then echo $$ > "$d/pid"; exec sleep 30; fi
            cat "$d/build.json"; [ -f "$d/build.fail" ] && exit 1; exit 0 ;;
        diff) cat "$d/diff.json"; [ -f "$d/diff.fail" ] && exit 1; exit 0 ;;
        current) cat "$d/current.json"; [ -f "$d/current.fail" ] && exit 1; exit 0 ;;
        coverage) cat "$d/coverage.json"; [ -f "$d/coverage.fail" ] && exit 1; exit 0 ;;
        gaps)
            case " $* " in
            *" --dry-run "*) cat "$d/gaps-dry.json" ;;
            *) cat "$d/gaps.json" ;;
            esac
            [ -f "$d/gaps.fail" ] && exit 1
            exit 0 ;;
        esac
        exit 2
        """
        let vani = dir.appendingPathComponent("vani")
        try Data(script.utf8).write(to: vani)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: vani.path)
        let pdf3 = pdf(0x41), pdf4 = pdf(0x42)
        try pdf3.write(to: dir.appendingPathComponent("v3.pdf"))
        try pdf4.write(to: dir.appendingPathComponent("v4.pdf"))

        let runner = DraftRunner(vaniBinary: vani, key: { nil }, decisionKey: { nil })
        let store = BankStore(directory: dir.appendingPathComponent("bank", isDirectory: true))
        let jobsModel: JobsModel? = viaJobs ? JobsModel(client: { JobsClient(vaniBinary: vani, timeout: 10) }, resume: { ResumeStudioClient(runner: runner) }, bankStore: { store }) : nil
        let resume = jobsModel?.resume ?? ResumeStudioModel(client: { ResumeStudioClient(runner: runner) }, bankStore: { store })
        let j = try JSONDecoder().decode(Job.self, from: Data(#"{"key":"fx:alpha:1","company":"Alpha Labs","title":"Platform Engineer","status":"digested","url":"https://boards.greenhouse.io/acme/jobs/123"}"#.utf8))
        resume.candidate = { _ in j }
        let editor = ResumeStudioEditor(key: Self.key, resume: resume, job: job ? j : nil)
        let r = Rig(dir: dir, editor: editor, resume: resume, bank: store, jobs: jobsModel, pdf3: pdf3, pdf4: pdf4)

        let s3 = ResumeEditTestsData.selection
        let all = [
            versionJSON(3, pdfPath: r.pdf3Path, pdfSHA: sha(pdf3), selection: s3, current: true),
            versionJSON(2, source: "edited", pdfPath: nil, pdfSHA: nil, used: true, fresh: "stale"),
        ]
        if versions {
            try r.put("show.json", shownJSON(version: 3, versions: all, pool: pool ? Self.poolJSON : nil, checkError: checkError))
        } else {
            try r.put("show.json", shownJSON(version: 0, versions: [], current: 0, used: 0, pool: pool ? Self.poolJSON : nil, checkError: checkError))
        }
        try r.put("show-v2.json", shownJSON(version: 2, versions: [
            versionJSON(3, pdfPath: r.pdf3Path, pdfSHA: sha(pdf3), current: true, fresh: nil),
            versionJSON(2, source: "edited", pdfPath: nil, pdfSHA: nil, selection: ResumeEditTestsData.selection2, used: true, fresh: nil),
        ], pool: nil, checked: false))
        try r.put("preview.json", "")
        return r
    }

    func saved4(_ r: Rig, extra: String = "") -> String {
        """
        {"schema":1,"key":"fx:alpha:1","version":4,"parent":3,"source":"edited","checks":{"eligibility":"current","lint":"passed","fit":"passed","figures":"verified"},
         "pages":2,"pdf_sha256":"\(sha(r.pdf4))","created":"2026-10-25T12:30:00Z","current_moved":true,"pdf_path":"\(r.pdf4Path)"\(extra)}
        """
    }

    /// After a fit, `show` names v4 (and its choice) first.
    func showAfterFit(_ r: Rig) throws {
        try r.put("show-v4.json", shownJSON(version: 4, versions: [
            versionJSON(4, source: "edited", pdfPath: r.pdf4Path, pdfSHA: sha(r.pdf4), selection: ResumeEditTestsData.selection2, current: true),
            versionJSON(3, pdfPath: r.pdf3Path, pdfSHA: sha(r.pdf3)),
        ], current: 4, used: 2))
    }

    func rendered(path: String) -> String {
        """
        {"key":"fx:alpha:1","saved":false,"html_path":"\(path)","checks":{"eligibility":"current","lint":"passed","fit":"not_run","figures":"verified"},
         "notes":["layout not measured"],"keywords":["kubernetes"],"bullets":[{"id":"j1","text_hash":"111111111111"}]}
        """
    }

    func previewFile(_ r: Rig, _ body: String = "<html><body>made-up page</body></html>", name: String = "preview-424242.html") throws -> URL {
        let url = r.tmp.appendingPathComponent(name)
        try Data(body.utf8).write(to: url)
        return url
    }

}
