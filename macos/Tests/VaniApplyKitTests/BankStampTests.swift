import Foundation
import XCTest
@testable import VaniApplyKit

/// The stamp the app's timer compares instead of reading the bank.
final class BankStampTests: XCTestCase {
    func testStampIsStableUntilTheBankIsWritten() throws {
        let store = BankStore(directory: try tempDir(self))
        let empty = store.bankStamp()
        XCTAssertEqual(empty, BankStore.BankStamp(), "a missing file stamps as nothing")
        _ = try store.update { $0.settings.captureSelfId = true }
        let first = store.bankStamp()
        XCTAssertNotEqual(first, empty)
        XCTAssertEqual(store.bankStamp(), first)
        XCTAssertEqual(store.bankStamp(), first)
        _ = try store.update { $0.settings.captureSelfId = false }
        XCTAssertNotEqual(store.bankStamp(), first, "a write of the same size is still a different file")
    }

    func testReadingLeavesTheStampAlone() throws {
        let store = BankStore(directory: try tempDir(self))
        _ = try store.update { $0.settings.captureSelfId = true }
        let stamp = store.bankStamp()
        for _ in 0..<5 { _ = try store.read() }
        XCTAssertEqual(store.bankStamp(), stamp)
    }
}
