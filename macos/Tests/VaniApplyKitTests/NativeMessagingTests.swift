import Foundation
import XCTest
@testable import VaniApplyKit

final class NativeMessagingTests: XCTestCase {
    func handle(_ bytes: Data) throws -> FileHandle {
        let url = try tempDir(self).appendingPathComponent("stream")
        try bytes.write(to: url)
        return try FileHandle(forReadingFrom: url)
    }

    func testFramesRoundTrip() throws {
        let a = Data(#"{"op":"ping"}"#.utf8), b = Data(#"{"op":"get"}"#.utf8)
        let h = try handle(NativeMessaging.frame(a) + NativeMessaging.frame(b))
        XCTAssertEqual(try NativeMessaging.readMessage(from: h), a)
        XCTAssertEqual(try NativeMessaging.readMessage(from: h), b)
        XCTAssertNil(try NativeMessaging.readMessage(from: h))
    }

    func testLengthIsLittleEndian() {
        XCTAssertEqual(Array(NativeMessaging.frame(Data(count: 258)).prefix(4)), [2, 1, 0, 0])
    }

    func testTruncatedInputIsAnError() throws {
        XCTAssertThrowsError(try NativeMessaging.readMessage(from: handle(Data([5, 0]))))
        XCTAssertThrowsError(try NativeMessaging.readMessage(from: handle(Data([5, 0, 0, 0, 0x7B]))))
    }

    func testOversizedRequestIsDrainedAndRefused() throws {
        let big = Data(repeating: 0x20, count: NativeMessaging.maxRequestBytes + 1)
        let next = Data(#"{"op":"ping"}"#.utf8)
        let h = try handle(NativeMessaging.frame(big) + NativeMessaging.frame(next))
        XCTAssertThrowsError(try NativeMessaging.readMessage(from: h)) { err in
            XCTAssertEqual(err as? NativeMessaging.ReadError, .tooLarge(big.count))
        }
        XCTAssertEqual(try NativeMessaging.readMessage(from: h), next, "the stream stays in step")
    }

    func testReplyOverChromesLimitBecomesAnError() throws {
        let reply = NativeMessaging.encodeReply(["blob": String(repeating: "x", count: NativeMessaging.maxReplyBytes)])
        XCTAssertLessThan(reply.count, 1024)
        let body = try JSONSerialization.jsonObject(with: reply.dropFirst(4)) as? [String: Any]
        XCTAssertNotNil(body?["error"])
    }
}
