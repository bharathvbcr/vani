import Foundation
import XCTest
@testable import VaniApplyKit

/// A run whose watchlist has an Adzuna board carries the owner's Adzuna app id and key
/// (Keychain item `adzuna-api-key`, "APP_ID:APP_KEY") into the vani the app starts, which
/// reads no Keychain itself. Injected lookups only: no test reads the real Keychain.
final class AdzunaKeyTests: XCTestCase {
    private func config(_ watchlist: String) throws -> String {
        let url = try tempDir(self).appendingPathComponent("config.json")
        try Data(#"{"version":1,"watchlist":\#(watchlist),"llm":{"provider":"openrouter","model":"m"}}"#.utf8).write(to: url)
        return url.path
    }

    private func environment(_ cfg: String, adzuna: @escaping KeyLookup, command: [String] = ["daemon"], withDecisionKey: Bool = true,
                             keys: DraftRunner.KeySource = .anthropicAccount(nil)) throws -> [String: String] {
        let runner = DraftRunner(vaniBinary: try fakeVani(self), configPath: cfg, keys: keys,
                                 decisionKey: { nil }, adzunaKey: adzuna)
        guard case let .launch(l) = runner.prepare(command, withDecisionKey: withDecisionKey) else {
            XCTFail("launch refused"); return [:]
        }
        return l.environment
    }

    func testAnAdzunaBoardCarriesTheKeyPair() throws {
        let cfg = try config(#"[{"ats":"gh","token":"acme"},{"ats":"ad","token":"us"}]"#)
        XCTAssertTrue(VaniConfig.hasAdzunaBoard(configPath: cfg))
        let env = try environment(cfg, adzuna: { "  app123:key/with+chars=  " })
        XCTAssertEqual(env["VANI_ADZUNA_APP_ID"], "app123")
        XCTAssertEqual(env["VANI_ADZUNA_APP_KEY"], "key/with+chars=")
        XCTAssertEqual(env["VANI_NO_KEYCHAIN"], "1")
    }

    func testNoAdzunaBoardReadsNothing() throws {
        let cfg = try config(#"[{"ats":"gh","token":"acme"}]"#)
        XCTAssertFalse(VaniConfig.hasAdzunaBoard(configPath: cfg))
        let env = try environment(cfg, adzuna: { XCTFail("the Adzuna item was read without an Adzuna board"); return "a:b" })
        XCTAssertNil(env["VANI_ADZUNA_APP_ID"])
        XCTAssertNil(env["VANI_ADZUNA_APP_KEY"])
    }

    /// Only the commands that read boards get the pair, with or without the decision key;
    /// `.noKey` (the host's --no-keychain) reads no item.
    func testOnlyBoardReadingRunsGetThePair() throws {
        let cfg = try config(#"[{"ats":"ad","token":"us"}]"#)
        XCTAssertEqual(try environment(cfg, adzuna: { "i:k" }, command: ["run-once", "--list-only"], withDecisionKey: false)["VANI_ADZUNA_APP_KEY"], "k")
        XCTAssertEqual(try environment(cfg, adzuna: { "i:k" }, withDecisionKey: false)["VANI_ADZUNA_APP_KEY"], "k")
        let never: KeyLookup = { XCTFail("read"); return "a:b" }
        XCTAssertNil(try environment(cfg, adzuna: never, command: ["suggest"])["VANI_ADZUNA_APP_KEY"])
        XCTAssertNil(try environment(cfg, adzuna: never, command: ["run-request"])["VANI_ADZUNA_APP_KEY"])
        XCTAssertNil(try environment(cfg, adzuna: never, keys: .noKey)["VANI_ADZUNA_APP_KEY"])
    }

    /// A missing, refused or malformed item never refuses the launch: the run goes ahead and
    /// the Adzuna board fails with its own message.
    func testABadItemLaunchesWithoutTheKey() throws {
        let cfg = try config(#"[{"ats":"ad","token":"us"}]"#)
        let lookups: [KeyLookup] = [{ nil }, { "" }, { "no-colon" }, { ":key" }, { "id:" }, { "id:has space" },
                                    { "id:" + String(repeating: "k", count: 129) }, { "id:k\u{0007}" },
                                    { throw BankError("denied") }]
        for lookup in lookups {
            let env = try environment(cfg, adzuna: lookup)
            XCTAssertNil(env["VANI_ADZUNA_APP_ID"])
            XCTAssertNil(env["VANI_ADZUNA_APP_KEY"])
        }
    }

    func testUnreadableConfigsHaveNoAdzunaBoard() throws {
        let dir = try tempDir(self)
        let bad = dir.appendingPathComponent("bad.json")
        try Data(#"{"watchlist":[{"ats":"ad""#.utf8).write(to: bad)
        let wrongType = dir.appendingPathComponent("type.json")
        try Data(#"{"watchlist":{"ats":"ad"}}"#.utf8).write(to: wrongType)
        for path in ["/nonexistent/config.json", bad.path, wrongType.path, dir.path] {
            XCTAssertFalse(VaniConfig.hasAdzunaBoard(configPath: path), path)
        }
    }
}
