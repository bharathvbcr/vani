import AppKit
import Foundation
import VaniApplyKit

/// The open Resume Studio windows' editors, so quitting the app (⌘Q) can ask once about edits that were not
/// fitted. A window's own close button asks through `WindowCloseGuard`; quitting does not close windows one by
/// one, so without this the edits would go without a word. Editors are held weakly: a window that is gone is
/// not asked about.
@MainActor
final class StudioWindows {
    static let shared = StudioWindows()

    private final class Weak {
        weak var editor: ResumeStudioEditor?
        init(_ editor: ResumeStudioEditor) { self.editor = editor }
    }

    private var editors: [String: Weak] = [:]

    func register(_ editor: ResumeStudioEditor) { editors[editor.key] = Weak(editor) }

    func forget(key: String) { editors[key] = nil }

    /// The jobs whose Studio windows hold unsaved edits, in a stable order.
    var dirtyJobs: [String] {
        editors.values.compactMap(\.editor).filter(\.isDirty)
            .map { $0.jobName ?? "a job that is not in the Jobs list (\(ResumeWords.clipped($0.key, 40)))" }
            .sorted()
    }

    /// The one question for quitting, nil when nothing is unsaved.
    func quitPrompt() -> ResumeQuitPrompt? { ResumeQuit.prompt(jobs: dirtyJobs) }

    /// What `applicationShouldTerminate` answers: asks once, through `ask`, when any window has unsaved edits.
    /// `ask` returns true when the owner chose to quit and discard.
    func terminateReply(ask: (ResumeQuitPrompt) -> Bool) -> NSApplication.TerminateReply {
        guard let prompt = quitPrompt() else { return .terminateNow }
        return ask(prompt) ? .terminateNow : .terminateCancel
    }

    /// The real alert, for the app delegate.
    static func askWithAlert(_ prompt: ResumeQuitPrompt) -> Bool {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = prompt.title
        alert.informativeText = prompt.message
        alert.addButton(withTitle: prompt.stay) // the default: keep editing
        alert.addButton(withTitle: prompt.quit)
        return alert.runModal() == .alertSecondButtonReturn
    }
}
