import Foundation
import XCTest
@testable import VaniApplyKit

/// A stand-in for `vani config exclude`: records its arguments, config path
/// and stdin beside itself, then replies with `reply` and exits `status`.
private func fakeExcludeVani(_ t: XCTestCase, reply: String, status: Int32 = 0) throws -> URL {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("vani")
    let script = """
    #!/bin/sh
    d=$(dirname "$0")
    printf '%s\\n' "$*" > "$d/args"
    printf '%s' "${VANI_CONFIG:-none}" > "$d/config"
    cat > "$d/stdin"
    cat <<'EOF'
    \(reply)
    EOF
    exit \(status)
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return url
}

private func recorded(_ vani: URL, _ name: String) throws -> String {
    try String(contentsOf: vani.deletingLastPathComponent().appendingPathComponent(name), encoding: .utf8)
}

/// A stand-in for `vani config exclude` that keeps a config: `--set` stores
/// stdin as the saved lists (unless a `refuse` file sits beside it), and every
/// call replies with what is saved. Each call's arguments go to `calls`.
private func configVani(_ t: XCTestCase, saved: String? = nil) throws -> URL {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("vani")
    let empty = #"{"titles":[],"title_unless":[],"companies":[],"locations":[],"only_locations":[],"descriptions":[]}"#
    try Data((saved ?? empty).utf8).write(to: dir.appendingPathComponent("saved"))
    try Data().write(to: dir.appendingPathComponent("calls"))
    let script = """
    #!/bin/sh
    d=$(dirname "$0")
    printf '%s\\n' "$*" >> "$d/calls"
    if [ "$3" = "--set" ]; then
      if [ -f "$d/refuse" ]; then
        cat > /dev/null
        printf '%s\\n' '{"exclude":\(empty),"max_entries":128,"max_descriptions":32,"max_entry_bytes":100,"error":"refused by the fake"}'
        exit 1
      fi
      cat > "$d/saved"
    fi
    printf '{"exclude":%s,"max_entries":128,"max_descriptions":32,"max_entry_bytes":100}\\n' "$(cat "$d/saved")"
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return url
}

private func savedLists(_ vani: URL) throws -> ExclusionLists {
    try JSONDecoder().decode(ExclusionLists.self, from: Data(try recorded(vani, "saved").utf8))
}

private let emptyReply = #"{"exclude":{"titles":[],"title_unless":[],"companies":[],"locations":[],"only_locations":[],"descriptions":[]},"max_entries":128,"max_descriptions":32,"max_entry_bytes":100}"#

final class ExclusionsTests: XCTestCase {
    func testLoadReadsTheConfigDraftingUses() throws {
        let vani = try fakeExcludeVani(self, reply: #"{"exclude":{"titles":["intern"],"title_unless":[],"companies":["Acme"],"locations":["India"],"only_locations":[],"descriptions":[]},"max_entries":128,"max_descriptions":32,"max_entry_bytes":100}"#)
        let x = try DraftSettings(vaniConfig: "/tmp/vani-test/config.json").exclusionsClient(vaniBinary: vani).load()
        XCTAssertEqual(x.lists, ExclusionLists(titles: ["intern"], companies: ["Acme"], locations: ["India"]))
        XCTAssertEqual(x.maxEntries, 128)
        XCTAssertEqual(x.maxDescriptions, 32)
        XCTAssertEqual(x.maxEntryBytes, 100)
        XCTAssertEqual(try recorded(vani, "args"), "config exclude\n")
        XCTAssertEqual(try recorded(vani, "config"), "/tmp/vani-test/config.json")
        XCTAssertEqual(try recorded(vani, "stdin"), "", "a read sends nothing")
    }

    func testSaveSendsTheListsOnStdinNeverAsArguments() throws {
        let vani = try fakeExcludeVani(self, reply: emptyReply)
        let lists = ExclusionLists(titles: ["intern", "--set; $(echo injected)"], titleUnless: ["internal tools"])
        _ = try ExclusionsClient(vaniBinary: vani).save(lists)
        XCTAssertEqual(try recorded(vani, "args"), "config exclude --set\n")
        let sent = try JSONDecoder().decode(ExclusionLists.self, from: Data(try recorded(vani, "stdin").utf8))
        XCTAssertEqual(sent, lists)
        XCTAssertTrue(try recorded(vani, "stdin").contains(#""title_unless""#), "Vani's key names")
        XCTAssertEqual(try recorded(vani, "config"), "none", "an empty config path means Vani's default")
    }

    func testVaniRefusalIsShownAsItsError() throws {
        let vani = try fakeExcludeVani(self, reply: #"{"exclude":{"titles":[],"title_unless":[],"companies":[],"locations":[],"only_locations":[],"descriptions":[]},"max_entries":128,"max_descriptions":32,"max_entry_bytes":100,"error":"config invalid: exclude.titles[1]: duplicate \"Intern\""}"#, status: 1)
        XCTAssertThrowsError(try ExclusionsClient(vaniBinary: vani).save(ExclusionLists(titles: ["intern", "Intern"]))) {
            XCTAssertTrue(String(describing: $0).contains("duplicate"), "\($0)")
        }
    }

    func testOldVaniIsNamed() throws {
        let vani = try fakeExcludeVani(self, reply: "", status: 2)
        XCTAssertThrowsError(try ExclusionsClient(vaniBinary: vani).load()) {
            XCTAssertTrue(String(describing: $0).contains("too old for exclusions"), "\($0)")
            XCTAssertTrue(String(describing: $0).contains("vani config exclude"), "\($0)")
        }
    }

    func testOnlyLocationsRoundTrips() throws {
        let vani = try fakeExcludeVani(self, reply: #"{"exclude":{"titles":[],"title_unless":[],"companies":[],"locations":[],"only_locations":["United States","Canada"],"descriptions":[]},"max_entries":128,"max_descriptions":32,"max_entry_bytes":100}"#)
        let x = try ExclusionsClient(vaniBinary: vani).load()
        XCTAssertEqual(x.lists.onlyLocations, ["United States", "Canada"])
        _ = try ExclusionsClient(vaniBinary: vani).save(x.lists)
        XCTAssertTrue(try recorded(vani, "stdin").contains(#""only_locations":["United States","Canada"]"#),
                      "a save sends back the list it was shown")
    }

    /// A reply without only_locations (a vani from before it) is refused, not
    /// read as an empty list the next save would write.
    func testReplyMissingAListIsRefused() throws {
        let vani = try fakeExcludeVani(self, reply: emptyReply.replacingOccurrences(of: #""only_locations":[],"#, with: ""))
        XCTAssertThrowsError(try ExclusionsClient(vaniBinary: vani).load())
    }

    // MARK: ExclusionsEditor

    @MainActor
    func testEditorSavesEachChangeAndTellsJobs() async throws {
        let vani = try configVani(self)
        var reloads = 0
        let editor = ExclusionsEditor(client: { ExclusionsClient(vaniBinary: vani) }, onSaved: { reloads += 1 })
        editor.lists.titles = ["intern"]
        await editor.settled()
        XCTAssertEqual(try recorded(vani, "calls"), "", "nothing is saved before the config is read")
        await editor.load()
        XCTAssertEqual(editor.lists, ExclusionLists(), "a read shows the config, not an edit made before it")

        editor.lists.titles = ["intern"]
        await editor.settled()
        XCTAssertEqual(try savedLists(vani).titles, ["intern"])
        XCTAssertFalse(editor.unsaved)
        XCTAssertEqual(reloads, 1)

        editor.lists.titles.removeAll()
        await editor.settled()
        XCTAssertEqual(try savedLists(vani).titles, [], "a removal is saved too")
        XCTAssertEqual(reloads, 2)
    }

    /// Changes made while a save runs go out together in one more save, so a
    /// burst of edits never reaches the config out of order.
    @MainActor
    func testEditorCoalescesChangesMadeDuringASave() async throws {
        let vani = try configVani(self)
        let editor = ExclusionsEditor(client: { ExclusionsClient(vaniBinary: vani) })
        await editor.load()
        editor.lists.titles = ["intern"]
        editor.lists.locations = ["China"]
        editor.lists.descriptions = ["security clearance"]
        XCTAssertTrue(editor.unsaved)
        await editor.settled()
        XCTAssertEqual(try savedLists(vani), ExclusionLists(titles: ["intern"], locations: ["China"], descriptions: ["security clearance"]))
        XCTAssertEqual(try recorded(vani, "calls").components(separatedBy: "--set").count - 1, 1)
        XCTAssertFalse(editor.unsaved)
    }

    /// A refused save keeps what the owner entered (a pane switch or a refresh
    /// does not throw it away) and is retried only when asked.
    @MainActor
    func testEditorKeepsRefusedEditsUntilRetriedOrUndone() async throws {
        let vani = try configVani(self)
        let editor = ExclusionsEditor(client: { ExclusionsClient(vaniBinary: vani) })
        await editor.load()
        try Data().write(to: vani.deletingLastPathComponent().appendingPathComponent("refuse"))
        editor.lists.companies = ["Acme"]
        await editor.settled()
        XCTAssertTrue(editor.unsaved)
        XCTAssertTrue(editor.problem?.contains("refused by the fake") == true, "\(editor.problem ?? "nil")")
        XCTAssertEqual(try savedLists(vani).companies, [])

        await editor.refresh()
        XCTAssertEqual(editor.lists.companies, ["Acme"], "refresh never drops edits the config has not taken")

        try FileManager.default.removeItem(at: vani.deletingLastPathComponent().appendingPathComponent("refuse"))
        editor.retry()
        await editor.settled()
        XCTAssertFalse(editor.unsaved)
        XCTAssertNil(editor.problem)
        XCTAssertEqual(try savedLists(vani).companies, ["Acme"])

        editor.lists.companies = []
        try Data().write(to: vani.deletingLastPathComponent().appendingPathComponent("refuse"))
        await editor.settled()
        try FileManager.default.removeItem(at: vani.deletingLastPathComponent().appendingPathComponent("refuse"))
        await editor.load()
        XCTAssertEqual(editor.lists.companies, ["Acme"], "undo shows the config again")
        XCTAssertFalse(editor.unsaved)
    }

    /// Jobs' "Exclude Company": reads the config first when Settings never
    /// did, keeps every other entry, and keeps a posting's company one entry.
    @MainActor
    func testEditorAddFromJobs() async throws {
        let vani = try configVani(self, saved: #"{"titles":["intern"],"title_unless":[],"companies":["Acme"],"locations":[],"only_locations":["US"],"descriptions":[]}"#)
        let editor = ExclusionsEditor(client: { ExclusionsClient(vaniBinary: vani) })
        let problem = await editor.add("  Big\n  Corp\n--set ", to: \.companies)
        XCTAssertNil(problem)
        XCTAssertEqual(try savedLists(vani), ExclusionLists(titles: ["intern"], companies: ["Acme", "Big Corp --set"], onlyLocations: ["US"]),
                       "a posting's newlines never split it into several entries")
        let saves = try recorded(vani, "calls")

        for (entry, want) in [("ACME", "already listed"), (" \n ", "empty")] {
            let why = await editor.add(entry, to: \.companies)
            XCTAssertTrue(why?.contains(want) == true, "\(entry): \(why ?? "nil")")
        }
        XCTAssertEqual(try recorded(vani, "calls"), saves, "a refused entry is never saved")
    }

    @MainActor
    func testEditorAddReportsAnUnreadableConfig() async throws {
        let vani = try fakeExcludeVani(self, reply: "", status: 2)
        let editor = ExclusionsEditor(client: { ExclusionsClient(vaniBinary: vani) })
        let why = await editor.add("Acme", to: \.companies)
        XCTAssertTrue(why?.contains("too old for exclusions") == true, "\(why ?? "nil")")
        XCTAssertNil(editor.saved)
    }

    func testAddTrimsAndSplitsPastedLines() {
        var list: [String] = []
        XCTAssertNil(ExclusionLists.add("  intern \n\n co-op\r\n\t\nCo Op  ", to: &list, cap: nil, maxBytes: nil))
        XCTAssertEqual(list, ["intern", "co-op", "Co Op"])
        XCTAssertNil(ExclusionLists.add("", to: &list, cap: nil, maxBytes: nil))
        XCTAssertNil(ExclusionLists.add(" \n ", to: &list, cap: nil, maxBytes: nil))
        XCTAssertEqual(list.count, 3)
        // Commas belong to entries: "New York, NY" is one place.
        XCTAssertNil(ExclusionLists.add("New York, NY", to: &list, cap: nil, maxBytes: nil))
        XCTAssertEqual(list.last, "New York, NY")
    }

    func testAddRefusesWhatVaniWould() {
        var list = ["Intern"]
        XCTAssertEqual(ExclusionLists.add("  INTERN ", to: &list, cap: nil, maxBytes: nil), "\u{201C}INTERN\u{201D} is already listed")
        list = ["security clearance"]
        XCTAssertNotNil(ExclusionLists.add("Security   Clearance", to: &list, cap: nil, maxBytes: nil), "whitespace runs fold")
        XCTAssertNotNil(ExclusionLists.add("in\u{0}tern", to: &list, cap: nil, maxBytes: nil))
        XCTAssertNotNil(ExclusionLists.add(String(repeating: "a", count: 101), to: &list, cap: nil, maxBytes: 100))
        XCTAssertNil(ExclusionLists.add(String(repeating: "a", count: 100), to: &list, cap: nil, maxBytes: 100), "the cap is inclusive")
        XCTAssertEqual(list.count, 2)
    }

    func testAddKeepsTheGoodLinesAndNamesTheFirstRefusal() {
        var list = ["a"]
        let why = ExclusionLists.add("b\na\nc\nd", to: &list, cap: 3, maxBytes: nil)
        XCTAssertEqual(list, ["a", "b", "c"])
        XCTAssertEqual(why, "\u{201C}a\u{201D} is already listed")
        XCTAssertEqual(ExclusionLists.add("e", to: &list, cap: 3, maxBytes: nil), "this list holds at most 3 entries")
        XCTAssertEqual(list, ["a", "b", "c"])
    }

    // MARK: revision, stale saves and load/save overlap

    func testSaveNamesTheRevisionItWasBuiltOn() throws {
        let vani = try fakeExcludeVani(self, reply: emptyReply)
        _ = try ExclusionsClient(vaniBinary: vani).save(ExclusionLists(titles: ["intern"]), revision: "abc123")
        XCTAssertEqual(try recorded(vani, "args"), "config exclude --set --revision abc123\n")
        XCTAssertEqual(try JSONDecoder().decode(ExclusionLists.self, from: Data(try recorded(vani, "stdin").utf8)),
                       ExclusionLists(titles: ["intern"]), "the lists still travel on stdin")
    }

    func testReadDecodesTheRevision() throws {
        let with = try fakeExcludeVani(self, reply: emptyReply.replacingOccurrences(of: #""max_entries""#, with: #""revision":"r1","max_entries""#))
        XCTAssertEqual(try ExclusionsClient(vaniBinary: with).load().revision, "r1")
        let without = try fakeExcludeVani(self, reply: emptyReply)
        XCTAssertNil(try ExclusionsClient(vaniBinary: without).load().revision, "a vani that predates revisions")
    }

    func testStaleRefusalIsItsOwnError() throws {
        let vani = try fakeExcludeVani(self, reply: revisionReply("", error: staleText), status: 1)
        XCTAssertThrowsError(try ExclusionsClient(vaniBinary: vani).save(ExclusionLists(), revision: "old")) {
            XCTAssertEqual($0 as? ExclusionsStale, ExclusionsStale(), "\($0)")
        }
        let other = try fakeExcludeVani(self, reply: revisionReply("", error: "config invalid: exclude.titles[1]: duplicate"), status: 1)
        XCTAssertThrowsError(try ExclusionsClient(vaniBinary: other).save(ExclusionLists(), revision: "old")) {
            XCTAssertFalse($0 is ExclusionsStale, "only a stale refusal is stale: \($0)")
        }
    }

    func testRebaseAppliesTheOwnersChangesToTheCurrentLists() {
        let base = ExclusionLists(titles: ["intern", "trainee"], locations: ["India"])
        let edited = ExclusionLists(titles: ["intern", "co-op"], locations: ["India"], descriptions: ["Security  Clearance"])
        let current = ExclusionLists(titles: ["INTERN", "trainee"], companies: ["Acme"], descriptions: ["security clearance"])
        let merged = ExclusionLists.rebase(edited, from: base, onto: current)
        XCTAssertEqual(merged.titles, ["INTERN", "co-op"], "trainee removed, co-op added, the rest kept")
        XCTAssertEqual(merged.companies, ["Acme"], "an entry added elsewhere stays")
        XCTAssertEqual(merged.locations, [], "the owner did not touch India, so removing it elsewhere stands")
        XCTAssertEqual(merged.descriptions, ["security clearance"], "an add already present is not doubled")
        XCTAssertEqual(ExclusionLists.rebase(base, from: base, onto: current), current, "no change is no change")
    }

    @MainActor
    func testEditorSendsTheRevisionItLoaded() async throws {
        let vani = try revisionVani(self)
        let editor = ExclusionsEditor(client: { ExclusionsClient(vaniBinary: vani) })
        await editor.load()
        XCTAssertEqual(editor.saved?.revision, "r0")
        editor.lists.titles = ["intern"]
        await editor.settled()
        XCTAssertEqual(try recorded(vani, "calls"), "config exclude\nconfig exclude --set --revision r0\n")
        XCTAssertEqual(editor.saved?.revision, "r1", "the next save builds on the reply's revision")
        editor.lists.titles = ["intern", "co-op"]
        await editor.settled()
        XCTAssertTrue(try recorded(vani, "calls").hasSuffix("config exclude --set --revision r1\n"))
        XCTAssertNil(editor.notice)
    }

    /// An exclusion saved elsewhere after the pane read: the pane's save is
    /// refused, it reads again, merges its change onto what is there and saves
    /// that. Both entries survive and the owner is told.
    @MainActor
    func testEditorMergesAfterAStaleRefusal() async throws {
        let vani = try revisionVani(self)
        let editor = ExclusionsEditor(client: { ExclusionsClient(vaniBinary: vani) })
        await editor.load()
        try elsewhere(vani, saved: #"{"titles":[],"title_unless":[],"companies":["Acme"],"locations":[],"only_locations":[],"descriptions":[]}"#, revision: "r7")

        editor.lists.titles = ["intern"]
        await editor.settled()
        XCTAssertEqual(try savedLists(vani), ExclusionLists(titles: ["intern"], companies: ["Acme"]))
        XCTAssertEqual(editor.lists, ExclusionLists(titles: ["intern"], companies: ["Acme"]))
        XCTAssertFalse(editor.unsaved)
        XCTAssertNil(editor.problem)
        XCTAssertTrue(editor.notice?.contains("changed elsewhere") == true, "\(editor.notice ?? "nil")")
        XCTAssertEqual(try recorded(vani, "calls"),
                       "config exclude\nconfig exclude --set --revision r0\nconfig exclude\nconfig exclude --set --revision r7\n")
        editor.lists.locations = ["India"]
        XCTAssertNil(editor.notice, "the next edit clears the notice")
        await editor.settled()
    }

    /// A removal is carried over too: it does not bring back what the owner deleted.
    @MainActor
    func testEditorKeepsARemovalAcrossAStaleRefusal() async throws {
        let vani = try revisionVani(self, saved: #"{"titles":["intern","trainee"],"title_unless":[],"companies":[],"locations":[],"only_locations":[],"descriptions":[]}"#)
        let editor = ExclusionsEditor(client: { ExclusionsClient(vaniBinary: vani) })
        await editor.load()
        try elsewhere(vani, saved: #"{"titles":["intern","trainee","co-op"],"title_unless":[],"companies":[],"locations":[],"only_locations":[],"descriptions":[]}"#, revision: "r4")
        editor.lists.titles = ["intern"]
        await editor.settled()
        XCTAssertEqual(try savedLists(vani).titles, ["intern", "co-op"])
    }

    /// A config that changes on every save is given up on, with the owner's
    /// edit kept and the reason shown, not retried forever.
    @MainActor
    func testEditorGivesUpWhenTheConfigKeepsChanging() async throws {
        let vani = try revisionVani(self)
        try Data().write(to: vani.deletingLastPathComponent().appendingPathComponent("alwaysstale"))
        let editor = ExclusionsEditor(client: { ExclusionsClient(vaniBinary: vani) })
        await editor.load()
        editor.lists.titles = ["intern"]
        await editor.settled()
        XCTAssertTrue(editor.problem?.contains("keep changing") == true, "\(editor.problem ?? "nil")")
        XCTAssertTrue(editor.unsaved)
        XCTAssertEqual(editor.lists.titles, ["intern"])
        XCTAssertEqual(try recorded(vani, "calls").components(separatedBy: "--set").count - 1, ExclusionsEditor.maxStaleRetries + 1)
    }

    /// An edit made while a read runs is neither lost nor written over: the
    /// save waits for the read, then goes out on the read's revision.
    @MainActor
    func testEditorKeepsAnEditMadeWhileAReadRuns() async throws {
        let vani = try revisionVani(self)
        let editor = ExclusionsEditor(client: { ExclusionsClient(vaniBinary: vani) })
        await editor.load()
        try elsewhere(vani, saved: #"{"titles":[],"title_unless":[],"companies":["Acme"],"locations":[],"only_locations":[],"descriptions":[]}"#, revision: "r5")
        try Data().write(to: vani.deletingLastPathComponent().appendingPathComponent("slowread"))

        let reading = Task { await editor.load() }
        while !editor.busy { await Task.yield() }
        editor.lists.titles = ["intern"]
        await reading.value
        await editor.settled()

        XCTAssertEqual(try savedLists(vani), ExclusionLists(titles: ["intern"], companies: ["Acme"]))
        XCTAssertEqual(editor.lists, ExclusionLists(titles: ["intern"], companies: ["Acme"]))
        XCTAssertFalse(editor.unsaved)
        XCTAssertFalse(try recorded(vani, "calls").contains("--revision r0"), "the save was built on the read, not the old copy")
    }
}

private let staleText = "config: the exclusions changed since they were read; read them again"

private func revisionReply(_ saved: String, revision: String = "", error: String? = nil) -> String {
    let lists = saved.isEmpty ? #"{"titles":[],"title_unless":[],"companies":[],"locations":[],"only_locations":[],"descriptions":[]}"# : saved
    let tail = error.map { #","error":"\#($0)","code":"stale""# } ?? ""
    return #"{"exclude":\#(lists),"revision":"\#(revision)","max_entries":128,"max_descriptions":32,"max_entry_bytes":100\#(tail)}"#
}

/// A stand-in for `vani config exclude` with revisions: the config is the
/// `saved` file and its revision the `rev` file ("r0", "r1", ...). `--set
/// --revision R` is refused with the stale error unless R is the current one
/// (or a file `alwaysstale` sits beside it); a read waits when `slowread` does.
private func revisionVani(_ t: XCTestCase, saved: String? = nil) throws -> URL {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("vani")
    let empty = #"{"titles":[],"title_unless":[],"companies":[],"locations":[],"only_locations":[],"descriptions":[]}"#
    try Data((saved ?? empty).utf8).write(to: dir.appendingPathComponent("saved"))
    try Data("r0".utf8).write(to: dir.appendingPathComponent("rev"))
    try Data().write(to: dir.appendingPathComponent("calls"))
    let script = """
    #!/bin/sh
    d=$(dirname "$0")
    printf '%s\\n' "$*" >> "$d/calls"
    tail='"max_entries":128,"max_descriptions":32,"max_entry_bytes":100'
    if [ "$3" = "--set" ]; then
      cat > "$d/stdin"
      if [ -f "$d/alwaysstale" ] || { [ "$4" = "--revision" ] && [ "$5" != "$(cat "$d/rev")" ]; }; then
        printf '{"exclude":%s,"revision":"",%s,"error":"\(staleText)","code":"stale"}\\n' '\(empty)' "$tail"
        exit 1
      fi
      cp "$d/stdin" "$d/saved"
      n=$(cat "$d/rev"); printf 'r%s' "$(( ${n#r} + 1 ))" > "$d/rev"
    elif [ -f "$d/slowread" ]; then
      reply=$(printf '{"exclude":%s,"revision":"%s",%s}' "$(cat "$d/saved")" "$(cat "$d/rev")" "$tail")
      sleep 0.5
      printf '%s\\n' "$reply"
      exit 0
    fi
    printf '{"exclude":%s,"revision":"%s",%s}\\n' "$(cat "$d/saved")" "$(cat "$d/rev")" "$tail"
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return url
}

/// Another writer saved: the config now holds `saved`, at `revision`.
private func elsewhere(_ vani: URL, saved: String, revision: String) throws {
    let dir = vani.deletingLastPathComponent()
    try Data(saved.utf8).write(to: dir.appendingPathComponent("saved"))
    try Data(revision.utf8).write(to: dir.appendingPathComponent("rev"))
}
