import Foundation
import XCTest
@testable import VaniApplyKit

/// The opener closure is `@Sendable`. A class box is how the test keeps the URL it was handed.
private final class URLLog: @unchecked Sendable {
    var urls: [URL] = []
}

/// The per-job resume ops: resumePut binds a PDF to a job page, resumeGet hands it back
/// to the tab being filled, resumeDelete unbinds it. A generated PDF with a neutral name
/// stands in for a real resume: no personal data in any test.
final class ResumeStoreTests: XCTestCase {
    let job = "https://boards.greenhouse.io/acme/jobs/123"
    let other = "https://jobs.lever.co/acme/abc-def"

    func pdf(_ size: Int = 2048, fill: UInt8 = 0x41) -> Data {
        var d = Data("%PDF-1.4\n".utf8)
        d.append(Data(repeating: fill, count: max(0, size - d.count)))
        return d
    }

    func call(_ req: [String: Any], _ store: BankStore) throws -> [String: Any] {
        let data = try JSONSerialization.data(withJSONObject: req)
        let reply = NativeProtocol.handle(try JSONSerialization.jsonObject(with: data), store: store, source: .chrome)
        return try JSONSerialization.jsonObject(with: JSONSerialization.data(withJSONObject: reply)) as! [String: Any]
    }

    func put(_ key: String, _ bytes: Data, name: String = "Resume_Test.pdf", _ store: BankStore) throws -> [String: Any] {
        try call(["op": "resumePut", "key": key, "name": name, "pdf": bytes.base64EncodedString()], store)
    }

    func testPutThenGetReturnsTheSameBytes() throws {
        let store = BankStore(directory: try tempDir(self))
        let bytes = pdf()
        let r = try put(job, bytes, store)
        XCTAssertEqual(r["ok"] as? Bool, true, "\(r)")
        XCTAssertEqual(r["bytes"] as? Int, bytes.count)

        let got = try call(["op": "resumeGet", "key": job], store)
        XCTAssertEqual(got["found"] as? Bool, true)
        XCTAssertEqual(got["name"] as? String, "Resume_Test.pdf")
        XCTAssertEqual(got["bytes"] as? Int, bytes.count)
        XCTAssertEqual(Data(base64Encoded: got["pdf"] as? String ?? ""), bytes)
        XCTAssertNotNil(got["pickedAt"] as? String)

        // The info op is what the popup shows: everything but the bytes.
        let info = try call(["op": "resumeInfo", "key": job], store)
        XCTAssertEqual(info["found"] as? Bool, true)
        XCTAssertEqual(info["name"] as? String, "Resume_Test.pdf")
        XCTAssertNil(info["pdf"])

        // Another job has nothing bound: no fallback to the one above.
        XCTAssertEqual(try call(["op": "resumeGet", "key": other], store)["found"] as? Bool, false)
        XCTAssertEqual(try call(["op": "resumeInfo", "key": other], store)["found"] as? Bool, false)
    }

    func testTheFileIsACopyInTheStoreNotAReferenceToTheOriginal() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        _ = try put(job, pdf(), store)
        let files = try FileManager.default.contentsOfDirectory(atPath: dir.appendingPathComponent("resumes").path)
        XCTAssertEqual(files.filter { $0.hasSuffix(".pdf") }.count, 1, "\(files)")
        XCTAssertTrue(files.contains("index.json"))
        // The stored name is not the file name on disk (the name is the owner's; the path is a hash).
        XCTAssertFalse(files.contains("Resume_Test.pdf"))
    }

    func testRebindingReplacesAndSharedBytesAreKeptUntilUnused() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        let a = pdf(fill: 0x41), b = pdf(fill: 0x42)
        _ = try put(job, a, store)
        _ = try put(other, a, store)
        _ = try put(job, b, name: "Resume_Other.pdf", store)
        XCTAssertEqual(Data(base64Encoded: try call(["op": "resumeGet", "key": job], store)["pdf"] as? String ?? ""), b)
        XCTAssertEqual(Data(base64Encoded: try call(["op": "resumeGet", "key": other], store)["pdf"] as? String ?? ""), a)

        XCTAssertEqual(try call(["op": "resumeDelete", "key": other], store)["ok"] as? Bool, true)
        XCTAssertEqual(try call(["op": "resumeGet", "key": other], store)["found"] as? Bool, false)
        let pdfs = try FileManager.default.contentsOfDirectory(atPath: dir.appendingPathComponent("resumes").path).filter { $0.hasSuffix(".pdf") }
        XCTAssertEqual(pdfs.count, 1, "the unused copy is removed: \(pdfs)")

        // Deleting what is not bound is not an error.
        XCTAssertEqual(try call(["op": "resumeDelete", "key": other], store)["ok"] as? Bool, true)
    }

    func testRefusalsStoreNothing() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        let good = pdf().base64EncodedString()
        let cases: [[String: Any]] = [
            ["op": "resumePut", "name": "a.pdf", "pdf": good],
            ["op": "resumePut", "key": "not a url", "name": "a.pdf", "pdf": good],
            ["op": "resumePut", "key": "file:///etc/passwd", "name": "a.pdf", "pdf": good],
            ["op": "resumePut", "key": job, "pdf": good],
            ["op": "resumePut", "key": job, "name": "a.docx", "pdf": good],
            ["op": "resumePut", "key": job, "name": "../a.pdf", "pdf": good],
            ["op": "resumePut", "key": job, "name": "a\n.pdf", "pdf": good],
            ["op": "resumePut", "key": job, "name": String(repeating: "x", count: 300) + ".pdf", "pdf": good],
            ["op": "resumePut", "key": job, "name": "a.pdf"],
            ["op": "resumePut", "key": job, "name": "a.pdf", "pdf": "%%% not base64"],
            ["op": "resumePut", "key": job, "name": "a.pdf", "pdf": Data("<html>".utf8).base64EncodedString()],
            ["op": "resumePut", "key": job, "name": "a.pdf", "pdf": ""],
            ["op": "resumePut", "key": job, "name": "a.pdf", "pdf": pdf(ResumeStore.maxBytes + 1).base64EncodedString()],
            ["op": "resumeGet"],
            ["op": "resumeGet", "key": 7],
            ["op": "resumeDelete", "key": "javascript:alert(1)"],
        ]
        for req in cases {
            let r = try call(req, store)
            XCTAssertNotNil(r["error"] as? String, "\(req["op"]!) \(req["key"] ?? "-") should be refused, got \(r)")
            XCTAssertNotNil(r["code"] as? String, "every refusal is coded: \(r)")
        }
        let tooBig = try call(["op": "resumePut", "key": job, "name": "a.pdf", "pdf": pdf(ResumeStore.maxBytes + 1).base64EncodedString()], store)
        XCTAssertEqual(tooBig["code"] as? String, "too_large")
        XCTAssertEqual(try call(["op": "resumeGet", "key": job], store)["found"] as? Bool, false)
        let resumes = dir.appendingPathComponent("resumes")
        let left = (try? FileManager.default.contentsOfDirectory(atPath: resumes.path)) ?? []
        XCTAssertTrue(left.allSatisfy { !$0.hasSuffix(".pdf") }, "nothing stored: \(left)")
    }

    func testAMissingOrTamperedFileIsReportedNeverSentAsIs() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        _ = try put(job, pdf(), store)
        let resumes = dir.appendingPathComponent("resumes")
        let file = try XCTUnwrap(try FileManager.default.contentsOfDirectory(atPath: resumes.path).first { $0.hasSuffix(".pdf") })

        // Replaced by something that is not a PDF.
        try Data("<html>".utf8).write(to: resumes.appendingPathComponent(file))
        let bad = try call(["op": "resumeGet", "key": job], store)
        XCTAssertEqual(bad["code"] as? String, "unreadable", "\(bad)")
        XCTAssertNil(bad["pdf"])

        // Grown past the cap behind the app's back.
        try pdf(ResumeStore.maxBytes + 10).write(to: resumes.appendingPathComponent(file))
        XCTAssertEqual(try call(["op": "resumeGet", "key": job], store)["code"] as? String, "too_large")

        // Deleted.
        try FileManager.default.removeItem(at: resumes.appendingPathComponent(file))
        let gone = try call(["op": "resumeGet", "key": job], store)
        XCTAssertEqual(gone["code"] as? String, "missing", "\(gone)")
        XCTAssertNotNil(gone["error"] as? String)
    }

    func testACorruptIndexIsReportedAndLeftInPlace() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        let resumes = dir.appendingPathComponent("resumes")
        try FileManager.default.createDirectory(at: resumes, withIntermediateDirectories: true)
        try Data("{not json".utf8).write(to: resumes.appendingPathComponent("index.json"))
        let r = try call(["op": "resumeGet", "key": job], store)
        XCTAssertEqual(r["code"] as? String, "unreadable", "\(r)")
        XCTAssertNotNil(try put(job, pdf(), store)["error"], "a write over a corrupt index would drop every binding")
        XCTAssertEqual(try Data(contentsOf: resumes.appendingPathComponent("index.json")), Data("{not json".utf8))
    }

    func testTheLargestResumeStillFitsInOneChromeReply() throws {
        let store = BankStore(directory: try tempDir(self))
        _ = try put(job, pdf(ResumeStore.maxBytes), store)
        let reply = NativeProtocol.handle(["op": "resumeGet", "key": job], store: store, source: .chrome)
        let framed = NativeMessaging.encodeReply(reply)
        XCTAssertLessThanOrEqual(framed.count - 4, NativeMessaging.maxReplyBytes)
        let decoded = try JSONSerialization.jsonObject(with: framed.dropFirst(4)) as? [String: Any]
        XCTAssertEqual(decoded?["found"] as? Bool, true, "the reply was not replaced by the too-large error")
    }

    func testTheBindingCountIsBounded() throws {
        let store = BankStore(directory: try tempDir(self))
        let bytes = pdf()
        for i in 0..<ResumeStore.maxEntries {
            XCTAssertEqual(try put("https://boards.greenhouse.io/acme/jobs/\(i)", bytes, store)["ok"] as? Bool, true)
        }
        let over = try put("https://boards.greenhouse.io/acme/jobs/over", bytes, store)
        XCTAssertEqual(over["code"] as? String, "full", "\(over)")
        // Rebinding a job already in the index is still allowed when full.
        XCTAssertEqual(try put("https://boards.greenhouse.io/acme/jobs/0", pdf(fill: 0x43), store)["ok"] as? Bool, true)
    }

    func testTheLogGetsAnEventAndACountNeverTheNameOrTheJob() throws {
        let store = BankStore(directory: try tempDir(self))
        _ = try put(job, pdf(), name: "Resume_Neutral_Name.pdf", store)
        _ = try call(["op": "resumeGet", "key": job], store)
        _ = try call(["op": "resumeDelete", "key": job], store)
        let entries = try store.recentActivity()
        XCTAssertEqual(entries.filter { $0.event == .resume }.count, 2, "put and delete are logged; a read is not")
        let raw = try String(contentsOf: store.directory.appendingPathComponent("activity.jsonl"), encoding: .utf8)
        XCTAssertFalse(raw.contains("Neutral"))
        XCTAssertFalse(raw.contains("greenhouse"))
    }

    func testResumeOpenWritesAPreviewCopyAndDoesNotLogOrReturnThePDF() throws {
        let store = BankStore(directory: try tempDir(self))
        let bytes = pdf()
        _ = try put(job, bytes, name: "Resume_Neutral_Name.pdf", store)
        let before = try store.recentActivity().count
        let opened = URLLog()
        let reply = NativeProtocol.handle(["op": "resumeOpen", "key": job], store: store, source: .chrome, openResume: { url in
            opened.urls.append(url)
            return true
        })
        XCTAssertEqual(reply["ok"] as? Bool, true, "\(reply)")
        XCTAssertNil(reply["pdf"])
        XCTAssertEqual(reply["name"] as? String, "Resume_Neutral_Name.pdf")
        XCTAssertEqual(reply["bytes"] as? Int, bytes.count)
        XCTAssertEqual(opened.urls.count, 1)
        XCTAssertEqual(try Data(contentsOf: opened.urls[0]), bytes)
        XCTAssertEqual(opened.urls[0].lastPathComponent, "Resume_Neutral_Name.pdf")
        XCTAssertFalse(opened.urls[0].path.contains(job))
        XCTAssertEqual(try store.recentActivity().count, before, "opening is not an activity-log event")
        let raw = try String(contentsOf: store.directory.appendingPathComponent("activity.jsonl"), encoding: .utf8)
        XCTAssertFalse(raw.contains("Neutral"))
        XCTAssertFalse(raw.contains("greenhouse"))
    }

    func testResumeOpenWithoutAnOpenerWritesNothing() throws {
        let store = BankStore(directory: try tempDir(self))
        _ = try put(job, pdf(), store)
        let reply = NativeProtocol.handle(["op": "resumeOpen", "key": job], store: store, source: .chrome)
        XCTAssertEqual(reply["code"] as? String, "unsupported", "\(reply)")
        XCTAssertNil(reply["pdf"])
        let missing = NativeProtocol.handle(["op": "resumeOpen", "key": other], store: store, source: .chrome, openResume: { _ in true })
        XCTAssertEqual(missing["found"] as? Bool, false)
        let refused = NativeProtocol.handle(["op": "resumeOpen", "key": job], store: store, source: .chrome, openResume: { _ in false })
        XCTAssertEqual(refused["code"] as? String, "io", "\(refused)")
        XCTAssertNil(refused["pdf"])
    }

    func testResumeOpenDataDoesNotStoreTheFile() throws {
        let store = BankStore(directory: try tempDir(self))
        let bytes = pdf()
        let opened = URLLog()
        let reply = NativeProtocol.handle(
            ["op": "resumeOpenData", "name": " Resume_Built.pdf ", "pdf": bytes.base64EncodedString()],
            store: store, source: .chrome, openResume: { opened.urls.append($0); return true })
        XCTAssertEqual(reply["ok"] as? Bool, true, "\(reply)")
        XCTAssertNil(reply["pdf"])
        XCTAssertEqual(reply["name"] as? String, "Resume_Built.pdf")
        XCTAssertEqual(opened.urls.count, 1)
        XCTAssertEqual(try Data(contentsOf: opened.urls[0]), bytes)
        XCTAssertEqual(try call(["op": "resumeInfo", "key": job], store)["found"] as? Bool, false, "a preview is not a saved resume")
        let resumes = ResumeStore(directory: store.directory.appendingPathComponent("resumes"))
        XCTAssertEqual(try resumes.catalog(), [])

        opened.urls.removeAll()
        for bad in [
            ["op": "resumeOpenData", "name": "../a.pdf", "pdf": bytes.base64EncodedString()],
            ["op": "resumeOpenData", "name": "Resume_\u{202E}fdp.pdf", "pdf": bytes.base64EncodedString()],
            ["op": "resumeOpenData", "name": "a\u{200B}b.pdf", "pdf": bytes.base64EncodedString()],
            ["op": "resumeOpenData", "name": "a.docx", "pdf": bytes.base64EncodedString()],
            ["op": "resumeOpenData", "name": "a.pdf", "pdf": Data("<html>".utf8).base64EncodedString()],
            ["op": "resumeOpenData", "name": "a.pdf", "pdf": ""],
            ["op": "resumeOpenData", "name": "a.pdf"],
            ["op": "resumeOpenData", "pdf": bytes.base64EncodedString()],
        ] {
            let r = NativeProtocol.handle(bad, store: store, source: .chrome, openResume: { _ in true })
            XCTAssertEqual(r["code"] as? String, "bad_request", "\(bad) -> \(r)")
            XCTAssertNil(r["pdf"])
        }
        XCTAssertEqual(opened.urls.count, 0)
        let huge = NativeProtocol.handle(
            ["op": "resumeOpenData", "name": "a.pdf", "pdf": pdf(ResumeStore.maxBytes + 1).base64EncodedString()],
            store: store, source: .chrome, openResume: { _ in true })
        XCTAssertEqual(huge["code"] as? String, "too_large", "\(huge)")
        XCTAssertEqual(opened.urls.count, 0)
        XCTAssertEqual(try store.recentActivity().filter { $0.event == .resume }.count, 0)
    }

    func testPreviewCopyRefusesAPathAndCapsTheCopies() throws {
        let bytes = pdf(fill: 0x42)
        XCTAssertNil(ResumeStore.checkedName("Resume_\u{202E}fdp.pdf"))
        XCTAssertNil(ResumeStore.checkedName("a\u{200B}b.pdf"))
        XCTAssertEqual(ResumeStore.checkedName("résumé.PDF"), "résumé.PDF")
        let url = try ResumeStore.previewCopy(name: "../Resume_Secret.pdf", data: bytes)
        XCTAssertEqual(url.lastPathComponent, "Resume.pdf")
        let disguised = try ResumeStore.previewCopy(name: "Resume_\u{202E}fdp.pdf", data: bytes)
        XCTAssertEqual(disguised.lastPathComponent, "Resume.pdf")
        XCTAssertFalse(url.pathComponents.contains(".."))
        XCTAssertEqual(url.deletingLastPathComponent().deletingLastPathComponent().lastPathComponent, ResumeStore.previewRootName)
        XCTAssertEqual(try Data(contentsOf: url), bytes)
        var last = url
        for _ in 0..<12 {
            last = try ResumeStore.previewCopy(name: "Resume_Acme.pdf", data: bytes)
        }
        let root = last.deletingLastPathComponent().deletingLastPathComponent()
        let keys: Set<URLResourceKey> = [.isDirectoryKey]
        let kids = try FileManager.default.contentsOfDirectory(at: root, includingPropertiesForKeys: Array(keys), options: [.skipsHiddenFiles])
            .filter { (try? $0.resourceValues(forKeys: keys).isDirectory) == true }
        XCTAssertGreaterThan(kids.count, 0, "the directory listing has to see the copy that was just written")
        XCTAssertLessThanOrEqual(kids.count, ResumeStore.maxPreviewCopies)
        XCTAssertEqual(try Data(contentsOf: last), bytes)
        XCTAssertThrowsError(try ResumeStore.previewCopy(name: "a.pdf", data: Data("<html>".utf8)))
        XCTAssertThrowsError(try ResumeStore.previewCopy(name: "a.pdf", data: Data()))
    }

    func testCatalogAndMatch() throws {
        let store = BankStore(directory: try tempDir(self))
        let h = "https://utaustin.wd1.myworkdayjobs.com"
        let slug = "Forward-Deployed-Engineer_R_00049483"
        let canon = "\(h)/UTstaff/job/\(slug)"
        let apply = "\(h)/en-US/UTstaff/job/AUSTIN%2C-TX/\(slug)/apply/applyManually"
        _ = try put(canon, pdf(), name: "Resume_Acme.pdf", store)
        _ = try put(other, pdf(fill: 0x43), name: "Resume_Other.pdf", store)
        let resumes = ResumeStore(directory: store.directory.appendingPathComponent("resumes"))
        let listed = try resumes.catalog()
        XCTAssertEqual(Set(listed.map(\.name)), ["Resume_Acme.pdf", "Resume_Other.pdf"])
        XCTAssertEqual(ResumeStore.match(urls: [apply], listed: listed)?.name, "Resume_Acme.pdf")
        XCTAssertEqual(ResumeStore.match(urls: ["javascript:alert(1)", other], listed: listed)?.name, "Resume_Other.pdf")
        XCTAssertNil(ResumeStore.match(urls: ["https://example.com/nope"], listed: listed))
        XCTAssertNil(ResumeStore.match(urls: [], listed: listed))
    }
}
