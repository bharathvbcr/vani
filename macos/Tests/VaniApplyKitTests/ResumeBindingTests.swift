import CryptoKit
import Foundation
import XCTest
@testable import VaniApplyKit

/// The one bind-and-log shared by the extension's `resumePut` and Resume Studio's "Use for this job".
/// A generated PDF with a neutral name stands in for a resume: no personal data in any test.
final class ResumeBindingTests: XCTestCase {
    private let posting = "https://boards.greenhouse.io/acme/jobs/123"
    private let apply = "https://boards.greenhouse.io/acme/jobs/123/application"
    private let other = "https://jobs.lever.co/acme/abc-def"

    private func pdf(_ size: Int = 2048, fill: UInt8 = 0x41) -> Data {
        var d = Data("%PDF-1.4\n".utf8)
        d.append(Data(repeating: fill, count: max(0, size - d.count)))
        return d
    }

    private struct Rig {
        let store: BankStore
        let resumes: ResumeStore
        let center = NotificationCenter()
    }

    private func rig() throws -> Rig {
        let dir = try tempDir(self)
        return Rig(store: BankStore(directory: dir), resumes: ResumeStore(directory: dir.appendingPathComponent("resumes", isDirectory: true)))
    }

    /// The keys of every `vaniResumeBound` the center was sent.
    private final class Heard: @unchecked Sendable {
        private let lock = NSLock()
        private var posts: [[String]] = []
        var all: [[String]] { lock.lock(); defer { lock.unlock() }; return posts }
        func add(_ keys: [String]) { lock.lock(); posts.append(keys); lock.unlock() }
    }

    private func listen(_ center: NotificationCenter) -> Heard {
        let heard = Heard()
        let token = center.addObserver(forName: .vaniResumeBound, object: nil, queue: nil) { note in
            heard.add(note.userInfo?[ResumeBinding.keysUserInfoKey] as? [String] ?? ["<no keys>"])
        }
        addTeardownBlock { center.removeObserver(token) }
        return heard
    }

    private func resumeEvents(_ store: BankStore) throws -> [ActivityEntry] {
        try store.recentActivity().filter { $0.event == .resume }
    }

    func testAJobsPostingAndApplicationAddressesAreBothBound() throws {
        let r = try rig(), heard = listen(r.center)
        let targets = ResumeBinding.targets(url: posting, applyURL: other)
        XCTAssertEqual(targets.keys, [try XCTUnwrap(ResumePageKey.postingKey(posting)), try XCTUnwrap(ResumePageKey.postingKey(other))])
        XCTAssertEqual(targets.skipped, [])
        XCTAssertFalse(targets.sameKey)
        let data = pdf()
        let out = try ResumeBinding.bind(data: data, name: "Tailored_Test.pdf", keys: targets.keys, resumes: r.resumes, store: r.store, source: .app, center: r.center)
        XCTAssertEqual(out.bound.map(\.key), targets.keys)
        XCTAssertEqual(out.failures, [])
        for key in targets.keys {
            let got = try XCTUnwrap(try r.resumes.get(key: key), key)
            XCTAssertEqual(got.data, data)
            XCTAssertEqual(got.entry.name, "Tailored_Test.pdf")
        }
        XCTAssertEqual(try resumeEvents(r.store).count, 1, "one activity entry for one choice, however many addresses it is bound under")
        let event = try XCTUnwrap(try resumeEvents(r.store).first)
        XCTAssertEqual(event.source, .app)
        XCTAssertEqual(event.counts, ["chosen": 1])
        XCTAssertNil(event.host, "the activity log holds no address and no name")
        XCTAssertEqual(heard.all, [targets.keys], "one notification, naming the bound keys")
    }

    func testTheSameAddressTwiceIsBoundOnceAndAnAddressWithNoKeyIsSaidNotSilentlySkipped() throws {
        let same = ResumeBinding.targets(url: posting, applyURL: posting)
        XCTAssertEqual(same.keys.count, 1)
        XCTAssertTrue(same.sameKey)
        // A page of the same posting under a different path reduces to its own key: not the same key.
        XCTAssertEqual(ResumeBinding.targets(url: posting, applyURL: apply).keys.count, 2)

        let r = try rig(), heard = listen(r.center)
        let targets = ResumeBinding.targets(url: posting, applyURL: "mailto:jobs@example.invalid")
        XCTAssertEqual(targets.keys, [try XCTUnwrap(ResumePageKey.postingKey(posting))])
        XCTAssertEqual(targets.skipped.count, 1)
        XCTAssertTrue(targets.skipped[0].contains("application page"), targets.skipped[0])
        let out = try ResumeBinding.bind(data: pdf(), name: "T.pdf", keys: targets.keys, resumes: r.resumes, store: r.store, source: .app, center: r.center)
        XCTAssertEqual(out.bound.count, 1)
        let note = ResumeBinding.note(for: out, targets: targets)
        XCTAssertTrue(note.contains("1 address"), note)
        XCTAssertTrue(note.contains("The application page has no address"), "the address that was left out is said: \(note)")
        XCTAssertEqual(heard.all.count, 1)

        let none = ResumeBinding.targets(url: nil, applyURL: "  ")
        XCTAssertEqual(none.keys, [])
        XCTAssertEqual(none.skipped, [], "no address at all is not a skipped address")
    }

    func testAJobWithNoAddressTheStoreCanKeyIsRefusedAndNothingIsLoggedOrPosted() throws {
        let r = try rig(), heard = listen(r.center)
        XCTAssertThrowsError(try ResumeBinding.bind(data: pdf(), name: "T.pdf", keys: [], resumes: r.resumes, store: r.store, source: .app, center: r.center)) {
            XCTAssertEqual(($0 as? ResumeStore.Failure)?.code, "bad_request")
        }
        XCTAssertEqual(try resumeEvents(r.store).count, 0)
        XCTAssertEqual(heard.all.count, 0)
        XCTAssertEqual(try r.resumes.catalog().count, 0)
    }

    func testABindRefusedForEveryKeyThrowsAndLeavesNoActivityOrNotification() throws {
        let r = try rig(), heard = listen(r.center)
        XCTAssertThrowsError(try ResumeBinding.bind(data: Data("not a pdf".utf8), name: "T.pdf", keys: [posting], resumes: r.resumes, store: r.store, source: .app, center: r.center)) {
            XCTAssertEqual(($0 as? ResumeStore.Failure)?.code, "bad_request")
        }
        XCTAssertThrowsError(try ResumeBinding.bind(data: pdf(ResumeStore.maxBytes + 1), name: "T.pdf", keys: [posting], resumes: r.resumes, store: r.store, source: .app, center: r.center)) {
            XCTAssertEqual(($0 as? ResumeStore.Failure)?.code, "too_large")
        }
        XCTAssertEqual(try resumeEvents(r.store).count, 0)
        XCTAssertEqual(heard.all.count, 0)
    }

    func testWhenTheStoreFillsAfterOneKeyTheOtherIsReportedAndTheBoundOneIsLoggedOnce() throws {
        let r = try rig(), heard = listen(r.center)
        let filler = pdf(fill: 0x42)
        for i in 0..<(ResumeStore.maxEntries - 1) { try r.resumes.put(key: "https://example.invalid/job/\(i)", name: "F.pdf", data: filler) }
        let targets = ResumeBinding.targets(url: posting, applyURL: other)
        let out = try ResumeBinding.bind(data: pdf(), name: "T.pdf", keys: targets.keys, resumes: r.resumes, store: r.store, source: .app, center: r.center)
        XCTAssertEqual(out.bound.map(\.key), [targets.keys[0]])
        XCTAssertEqual(out.failures.map(\.key), [targets.keys[1]])
        XCTAssertEqual(out.failures.first?.code, "full")
        XCTAssertNotNil(try r.resumes.info(key: targets.keys[0]))
        XCTAssertNil(try r.resumes.info(key: targets.keys[1]))
        XCTAssertEqual(try resumeEvents(r.store).count, 1)
        XCTAssertEqual(heard.all, [[targets.keys[0]]], "only what was bound is announced")
        XCTAssertTrue(ResumeBinding.note(for: out, targets: targets).contains("One address was not saved"), "a partial bind says so")

        // Every key refused: the first failure is thrown, and nothing more is logged or posted.
        XCTAssertThrowsError(try ResumeBinding.bind(data: pdf(), name: "T.pdf", keys: ["https://example.invalid/new"], resumes: r.resumes, store: r.store, source: .app, center: r.center)) {
            XCTAssertEqual(($0 as? ResumeStore.Failure)?.code, "full")
        }
        XCTAssertEqual(try resumeEvents(r.store).count, 1)
        XCTAssertEqual(heard.all.count, 1)
    }

    func testTheExtensionsResumePutGoesThroughTheSameFunctionAndBehavesAsBefore() throws {
        let store = BankStore(directory: try tempDir(self))
        let heard = Heard()
        let token = NotificationCenter.default.addObserver(forName: .vaniResumeBound, object: nil, queue: nil) { note in
            heard.add(note.userInfo?[ResumeBinding.keysUserInfoKey] as? [String] ?? [])
        }
        defer { NotificationCenter.default.removeObserver(token) }
        let key = "https://boards.greenhouse.io/acme/jobs/9999"
        let data = pdf()
        let req: [String: Any] = ["op": "resumePut", "key": key, "name": "Resume_Test.pdf", "pdf": data.base64EncodedString()]
        let reply = NativeProtocol.handle(req, store: store, source: .safari)
        XCTAssertEqual(reply["ok"] as? Bool, true, "\(reply)")
        XCTAssertEqual(reply["bytes"] as? Int, data.count)
        XCTAssertEqual(reply["name"] as? String, "Resume_Test.pdf")
        let events = try resumeEvents(store)
        XCTAssertEqual(events.count, 1)
        XCTAssertEqual(events.first?.source, .safari, "the extension's own source is logged, not the app's")
        XCTAssertEqual(events.first?.counts, ["chosen": 1])
        XCTAssertTrue(heard.all.contains([key]))

        let bad = NativeProtocol.handle(["op": "resumePut", "key": key, "name": "Resume_Test.pdf", "pdf": Data("nope".utf8).base64EncodedString()], store: store, source: .safari)
        XCTAssertEqual(bad["code"] as? String, "bad_request")
        XCTAssertEqual(try resumeEvents(store).count, 1, "a refused put logs nothing")
    }

    // MARK: A version's PDF

    private func file(_ t: Data, name: String = "v1.pdf") throws -> URL {
        let url = try tempDir(self).appendingPathComponent(name)
        try t.write(to: url)
        return url
    }

    private func sha(_ d: Data) -> String { SHA256.hash(data: d).map { String(format: "%02x", $0) }.joined() }

    func testLoadPDFChecksThePathTheSizeTheMagicAndTheChecksum() throws {
        let data = pdf()
        let url = try file(data)
        XCTAssertEqual(try ResumeBinding.loadPDF(path: url.path, sha256: sha(data)), data)
        XCTAssertEqual(try ResumeBinding.loadPDF(path: url.path, sha256: sha(data).uppercased()), data)

        func code(_ body: () throws -> Void) -> String? {
            do { try body(); return nil } catch let f as ResumeStore.Failure { return f.code } catch { return "other" }
        }
        XCTAssertEqual(code { _ = try ResumeBinding.loadPDF(path: "relative/v1.pdf", sha256: sha(data)) }, "bad_request")
        XCTAssertEqual(code { _ = try ResumeBinding.loadPDF(path: url.path.replacingOccurrences(of: ".pdf", with: ".txt"), sha256: "x") }, "bad_request")
        XCTAssertEqual(code { _ = try ResumeBinding.loadPDF(path: url.deletingLastPathComponent().appendingPathComponent("gone.pdf").path, sha256: sha(data)) }, "missing")
        XCTAssertEqual(code { _ = try ResumeBinding.loadPDF(path: url.deletingLastPathComponent().path + "/", sha256: sha(data)) }, "bad_request", "a folder is not a PDF")
        XCTAssertEqual(code { _ = try ResumeBinding.loadPDF(path: url.path, sha256: sha(pdf(fill: 0x43))) }, "unreadable", "a checksum that does not match is a damaged file")

        let link = url.deletingLastPathComponent().appendingPathComponent("link.pdf")
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: url)
        XCTAssertEqual(code { _ = try ResumeBinding.loadPDF(path: link.path, sha256: sha(data)) }, "bad_request", "a link is not followed")

        let big = pdf(ResumeStore.maxBytes + 1)
        XCTAssertEqual(code { _ = try ResumeBinding.loadPDF(path: try self.file(big).path, sha256: self.sha(big)) }, "too_large")
        let notPDF = Data("hello".utf8)
        XCTAssertEqual(code { _ = try ResumeBinding.loadPDF(path: try self.file(notPDF).path, sha256: self.sha(notPDF)) }, "bad_request")
    }

    func testBindUsedBindsTheVersionsPDFUnderTheJobsKeysAndRefusesWhatItCannotRead() throws {
        let r = try rig(), heard = listen(r.center)
        let data = pdf()
        let url = try file(data, name: "v3.pdf")
        let targets = ResumeBinding.targets(url: posting, applyURL: other)
        let used = ResumeUsed(key: "k", version: 3, pdfPath: url.path, name: "Acme_Resume.pdf", pdfSHA256: sha(data))
        let out = try ResumeBinding.bindUsed(used, targets: targets, resumes: r.resumes, store: r.store, center: r.center)
        XCTAssertEqual(out.bound.count, 2)
        XCTAssertEqual(try r.resumes.get(key: targets.keys[1])?.entry.name, "Acme_Resume.pdf")
        XCTAssertEqual(heard.all.count, 1)

        var badName = used
        badName.name = "../escape.pdf"
        XCTAssertThrowsError(try ResumeBinding.bindUsed(badName, targets: targets, resumes: r.resumes, store: r.store, center: r.center))
        var wrongSum = used
        wrongSum.pdfSHA256 = sha(pdf(fill: 0x44))
        XCTAssertThrowsError(try ResumeBinding.bindUsed(wrongSum, targets: targets, resumes: r.resumes, store: r.store, center: r.center))
        XCTAssertThrowsError(try ResumeBinding.bindUsed(used, targets: ResumeBinding.targets(url: nil, applyURL: "mailto:x@example.invalid"), resumes: r.resumes, store: r.store, center: r.center))
        XCTAssertEqual(heard.all.count, 1, "nothing more was announced")
        XCTAssertEqual(try resumeEvents(r.store).count, 1)
    }
}
