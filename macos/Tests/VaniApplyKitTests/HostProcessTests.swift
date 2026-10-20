import Foundation
import XCTest
@testable import VaniApplyKit

/// The built vani-apply-host binary, driven over real pipes as Chrome drives it.
final class HostProcessTests: XCTestCase {
    func hostBinary() throws -> URL {
        // swift test builds every product into the directory holding the test bundle.
        let dir = Bundle(for: Self.self).bundleURL.deletingLastPathComponent()
        let url = dir.appendingPathComponent("vani-apply-host")
        guard FileManager.default.isExecutableFile(atPath: url.path) else {
            throw XCTSkip("vani-apply-host was not built next to the tests at \(url.path)")
        }
        return url
    }

    func run(_ args: [String], input: Data, timeout: TimeInterval = 20) throws -> (status: Int32, out: Data, err: Data) {
        let p = Process()
        p.executableURL = try hostBinary()
        p.arguments = args
        let inPipe = Pipe(), outPipe = Pipe(), errPipe = Pipe()
        p.standardInput = inPipe
        p.standardOutput = outPipe
        p.standardError = errPipe
        try p.run()
        inPipe.fileHandleForWriting.write(input)
        try inPipe.fileHandleForWriting.close()
        let done = DispatchSemaphore(value: 0)
        nonisolated(unsafe) var out = Data(), err = Data()
        DispatchQueue.global().async {
            out = outPipe.fileHandleForReading.readDataToEndOfFile()
            err = errPipe.fileHandleForReading.readDataToEndOfFile()
            done.signal()
        }
        if done.wait(timeout: .now() + timeout) == .timedOut {
            p.terminate()
            XCTFail("host did not exit within \(timeout)s")
        }
        p.waitUntilExit()
        return (p.terminationStatus, out, err)
    }

    func frames(_ objs: [Any]) throws -> Data {
        try objs.reduce(into: Data()) { $0.append(NativeMessaging.frame(try JSONSerialization.data(withJSONObject: $1))) }
    }

    func replies(_ out: Data) throws -> [[String: Any]] {
        let h = try FileHandle(forReadingFrom: {
            let u = try tempDir(self).appendingPathComponent("out"); try out.write(to: u); return u
        }())
        var all: [[String: Any]] = []
        while let m = try NativeMessaging.readMessage(from: h) {
            all.append(try XCTUnwrap(JSONSerialization.jsonObject(with: m) as? [String: Any]))
        }
        return all
    }

    func testAnswersASessionAndExitsAtEndOfInput() throws {
        let store = try tempDir(self)
        let input = try frames([
            ["op": "ping"],
            ["op": "put", "baseRev": 0, "bank": bankJSON([answerJSON("first name", value: ["Ada"])])],
            ["op": "put", "baseRev": 0, "bank": bankJSON([])],
            ["op": "get"],
        ]) + NativeMessaging.frame(Data("not json".utf8))
        let r = try run(["chrome-extension://\(ChromeConnector.defaultExtensionID)/", "--store-dir", store.path], input: input)
        XCTAssertEqual(r.status, 0, String(decoding: r.err, as: UTF8.self))
        let got = try replies(r.out)
        XCTAssertEqual(got.count, 5)
        XCTAssertEqual(got[0]["rev"] as? Int, 0)
        XCTAssertEqual(got[1]["rev"] as? Int, 1)
        XCTAssertEqual(got[2]["conflict"] as? Bool, true)
        XCTAssertNotNil(((got[3]["bank"] as? [String: Any])?["answers"] as? [String: Any])?["first name"])
        XCTAssertNotNil(got[4]["error"])
        XCTAssertTrue(FileManager.default.fileExists(atPath: store.appendingPathComponent("bank.json").path))
    }

    /// The extension's persistent port matches replies to requests by position alone, so this is the
    /// contract it leans on: pipelined requests are answered one each, in order, and the host stays up
    /// until its input ends.
    func testPipelinedFramesGetOneReplyEachInOrderAndHostExitsOnlyAtEndOfInput() throws {
        let store = try tempDir(self)
        let p = Process()
        p.executableURL = try hostBinary()
        p.arguments = ["chrome-extension://\(ChromeConnector.defaultExtensionID)/", "--store-dir", store.path]
        let inPipe = Pipe(), outPipe = Pipe()
        p.standardInput = inPipe
        p.standardOutput = outPipe
        p.standardError = Pipe()
        try p.run()
        // A hung host must fail the test, not stall it.
        let watchdog = DispatchWorkItem { p.terminate() }
        DispatchQueue.global().asyncAfter(deadline: .now() + 30, execute: watchdog)
        defer { watchdog.cancel() }

        // Each put is accepted only at the revision the previous one produced, so the reply's rev
        // identifies which request it answers.
        let n = 50
        let input = try frames((0..<n).map { ["op": "put", "baseRev": $0, "bank": bankJSON([])] as [String: Any] })
        inPipe.fileHandleForWriting.write(input)
        for i in 0..<n {
            let m = try XCTUnwrap(try NativeMessaging.readMessage(from: outPipe.fileHandleForReading), "no reply for frame \(i)")
            let reply = try XCTUnwrap(JSONSerialization.jsonObject(with: m) as? [String: Any])
            XCTAssertEqual(reply["rev"] as? Int, i + 1, "reply \(i) answered a different request")
        }
        XCTAssertTrue(p.isRunning, "the host exited before its input ended")
        try inPipe.fileHandleForWriting.close()
        p.waitUntilExit()
        XCTAssertEqual(p.terminationStatus, 0)
        XCTAssertNil(try NativeMessaging.readMessage(from: outPipe.fileHandleForReading), "extra frames after the last reply")
    }

    func testDraftOpRunsVaniAndOutlivesTheIdleWatchdogLogic() throws {
        let store = try tempDir(self)
        let vani = try fakeVani(self)
        let input = try frames([["op": "draft", "request": ["question": "Why us?"]]])
        let r = try run(["chrome-extension://\(ChromeConnector.defaultExtensionID)/", "--store-dir", store.path,
                         "--vani", vani.path, "--no-keychain"], input: input)
        XCTAssertEqual(r.status, 0, String(decoding: r.err, as: UTF8.self))
        let got = try replies(r.out)
        XCTAssertEqual(got.first?["draft"] as? String, "key=unset cfg=none state=none ws=none inherited=none")
    }

    func testDraftOpLaunchesThroughTheSavedAccount() throws {
        let store = try tempDir(self)
        let cfg = try vaniConfig(self, provider: "anthropic")
        try DraftSettings(vaniConfig: cfg, anthropicAccount: "org500").save(to: store)
        let vani = try fakeVani(self), launcher = try fakeLauncher(self)
        let input = try frames([["op": "draft", "request": ["question": "Why us?"]]])
        let r = try run(["chrome-extension://\(ChromeConnector.defaultExtensionID)/", "--store-dir", store.path,
                         "--vani", vani.path, "--anthropic-account", launcher.url.path], input: input)
        XCTAssertEqual(r.status, 0, String(decoding: r.err, as: UTF8.self))
        XCTAssertEqual(try replies(r.out).first?["draft"] as? String, "key=match cfg=\(cfg) state=none ws=none inherited=none")
        XCTAssertEqual(launcher.recordedArguments, ["exec", "--account", "org500", "--", vani.path, "draft"])
        XCTAssertEqual(launcher.handedKey, "key=unset ws=unset")
    }

    func testNoKeychainStartsVaniWithoutTheLauncher() throws {
        let store = try tempDir(self)
        let cfg = try vaniConfig(self, provider: "anthropic")
        try DraftSettings(vaniConfig: cfg, anthropicAccount: "org500").save(to: store)
        let launcher = try fakeLauncher(self)
        let input = try frames([["op": "draft", "request": ["question": "Why us?"]]])
        let r = try run(["chrome-extension://\(ChromeConnector.defaultExtensionID)/", "--store-dir", store.path,
                         "--vani", try fakeVani(self).path, "--anthropic-account", launcher.url.path, "--no-keychain"], input: input)
        XCTAssertEqual(r.status, 0, String(decoding: r.err, as: UTF8.self))
        XCTAssertEqual(try replies(r.out).first?["draft"] as? String, "key=unset cfg=\(cfg) state=none ws=none inherited=none")
        XCTAssertNil(launcher.recordedArguments)
    }

    func testRefusesARelativeLauncherPath() throws {
        let r = try run(["chrome-extension://\(ChromeConnector.defaultExtensionID)/", "--store-dir", try tempDir(self).path,
                         "--anthropic-account", "anthropic-account"], input: Data())
        XCTAssertNotEqual(r.status, 0)
        XCTAssertTrue(r.out.isEmpty)
        XCTAssertTrue(String(decoding: r.err, as: UTF8.self).contains("--anthropic-account needs an absolute path"))
    }

    func testRefusesToRunWithoutAChromeOrigin() throws {
        // No origin, and origins Chrome's allowed_origins could never name: the
        // host refuses each before reading a request or opening the store.
        let store = try tempDir(self)
        for args in [[], ["https://evil.test/"], ["moz-extension://\(ChromeConnector.defaultExtensionID)/"],
                     ["--store-dir", store.path], [""]] {
            let r = try run(args, input: Data())
            XCTAssertNotEqual(r.status, 0, "\(args)")
            XCTAssertTrue(r.out.isEmpty, "nothing may reach stdout but replies: \(args)")
            XCTAssertTrue(String(decoding: r.err, as: UTF8.self).contains("chrome-extension://"), "\(args)")
        }
    }

    func testTruncatedInputFailsWithoutAReply() throws {
        let store = try tempDir(self)
        let r = try run(["chrome-extension://\(ChromeConnector.defaultExtensionID)/", "--store-dir", store.path], input: Data([9, 0, 0, 0, 0x7B]))
        XCTAssertNotEqual(r.status, 0)
        XCTAssertTrue(r.out.isEmpty)
    }
}
