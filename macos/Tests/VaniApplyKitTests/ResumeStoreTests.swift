import Foundation
import XCTest
@testable import VaniApplyKit

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
}
