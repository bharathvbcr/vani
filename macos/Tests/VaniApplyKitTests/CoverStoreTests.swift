import Foundation
import XCTest
@testable import VaniApplyKit

private final class CoverURLLog: @unchecked Sendable {
    var urls: [URL] = []
}

final class CoverStoreTests: XCTestCase {
    let job = "https://boards.greenhouse.io/acme/jobs/1"
    let other = "https://jobs.lever.co/acme/other"

    func pdf() -> Data { Data("%PDF-1.4\ncover\n".utf8) }

    func mode(_ url: URL) throws -> Int {
        let attrs = try FileManager.default.attributesOfItem(atPath: url.path)
        return (attrs[.posixPermissions] as? NSNumber)?.intValue ?? -1
    }

    func testPutThenGetReturnsTheBodyAndNotTheOtherJob() throws {
        let store = BankStore(directory: try tempDir(self))
        let letters = CoverStore(directory: store.directory.appendingPathComponent("letters", isDirectory: true))
        let entry = try letters.put(key: job, name: "Cover_Letter.pdf", pdf: pdf(), body: "I want this role at Acme.")
        XCTAssertEqual(entry.bytes, pdf().count)
        XCTAssertEqual(try mode(letters.directory.appendingPathComponent(entry.textFile)), 0o600)
        XCTAssertEqual(try mode(letters.directory.appendingPathComponent(entry.file)), 0o600)
        let got = try letters.get(key: job)
        XCTAssertEqual(got?.body, "I want this role at Acme.")
        XCTAssertEqual(got?.pdf, pdf())
        XCTAssertNil(try letters.get(key: other))

        let info = NativeProtocol.handle(["op": "coverInfo", "key": job], store: store, source: .chrome)
        XCTAssertEqual(info["found"] as? Bool, true)
        XCTAssertNil(info["body"])
        XCTAssertNil(info["pdf"])

        let full = NativeProtocol.handle(["op": "coverGet", "key": job], store: store, source: .chrome)
        XCTAssertEqual(full["body"] as? String, "I want this role at Acme.")
        XCTAssertEqual(Data(base64Encoded: full["pdf"] as? String ?? ""), pdf())
        XCTAssertEqual(NativeProtocol.handle(["op": "coverGet", "key": other], store: store, source: .chrome)["found"] as? Bool, false)

        let log = try store.recentActivity(limit: 20)
        XCTAssertTrue(log.allSatisfy { entry in
            entry.event != .cover || (entry.counts?["chosen"] == 1 && entry.host == nil)
        })
        let logged = try JSONEncoder().encode(log)
        XCTAssertFalse(String(decoding: logged, as: UTF8.self).contains("I want this role"))
        XCTAssertFalse(String(decoding: logged, as: UTF8.self).contains("Cover_Letter"))
    }

    func testATextOnlyLetterHasNoPDFAndABadFileIsRefused() throws {
        let letters = CoverStore(directory: try tempDir(self))
        let entry = try letters.put(key: job, name: "Cover_Letter.pdf", pdf: nil, body: "I want this role at Acme.")
        XCTAssertEqual(entry.bytes, 0)
        let got = try letters.get(key: job)
        XCTAssertNil(got?.pdf)
        XCTAssertEqual(got?.body, "I want this role at Acme.")
        XCTAssertThrowsError(try letters.put(key: job, name: "Cover_Letter.pdf", pdf: Data("nope".utf8), body: "text"))
        XCTAssertThrowsError(try letters.put(key: "", name: "Cover_Letter.pdf", pdf: nil, body: "text"))
        XCTAssertThrowsError(try letters.put(key: job, name: "Cover_Letter.pdf", pdf: nil, body: ""))
        XCTAssertThrowsError(try letters.put(key: job, name: "Cover_Letter.pdf", pdf: nil, body: String(repeating: "a", count: CoverStore.maxTextBytes + 1)))
        let again = try letters.put(key: job, name: "Cover_Letter.pdf", pdf: nil, body: "I want this role at Acme.")
        XCTAssertEqual(again.textSHA256, entry.textSHA256)
        XCTAssertEqual(try letters.info(key: job)?.textFile, entry.textFile)
    }

    func testOpenDoesNotLogTheText() throws {
        let store = BankStore(directory: try tempDir(self))
        let letters = CoverStore(directory: store.directory.appendingPathComponent("letters", isDirectory: true))
        _ = try letters.put(key: job, name: "Cover_Letter.pdf", pdf: pdf(), body: "I want this role at Acme.")
        let box = CoverURLLog()
        let reply = NativeProtocol.handle(["op": "coverOpen", "key": job], store: store, source: .app, openResume: { url in
            box.urls.append(url)
            return true
        })
        XCTAssertEqual(reply["ok"] as? Bool, true)
        XCTAssertNil(reply["pdf"])
        XCTAssertNil(reply["body"])
        XCTAssertEqual(box.urls.count, 1)
        let logged = try store.recentActivity(limit: 20)
        XCTAssertFalse(logged.contains { $0.event == .cover })
        let text = String(decoding: try JSONEncoder().encode(logged), as: UTF8.self)
        XCTAssertFalse(text.contains("I want this role"))
        XCTAssertFalse(text.contains("Cover_Letter"))
    }
}
