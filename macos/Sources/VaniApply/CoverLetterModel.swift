import Foundation
import Observation
import VaniApplyKit

/// Build and Use for one posting's cover letter, on the Resume card. Build saves a version.
/// Use is what the extension may fetch. The log records an event and a count, never the text.
@MainActor
@Observable
final class CoverLetterModel {
    private let client: (() -> CoverLetterClient)?
    private let bankStore: () -> BankStore?
    var status = ""
    var busy = false
    var available: Bool { client != nil }

    init(client: (() -> CoverLetterClient)?, bankStore: @escaping () -> BankStore? = { nil }) {
        self.client = client
        self.bankStore = bankStore
    }

    func build(_ job: Job) {
        guard !busy, let make = client else { return }
        busy = true
        status = "Building the letter…"
        let key = job.key
        let letter = make()
        Task {
            let result = await BlockingWork.value { letter.build(key) }
            busy = false
            switch result {
            case let .success(saved):
                status = saved.pdf ? "Saved v\(saved.version)." : "Saved the text of v\(saved.version). The PDF could not be drawn."
            case let .failure(_, message):
                status = BoundedText.sentence(message)
            }
        }
    }

    /// The child run, the file reads, the letter store write and the activity log all take locks or
    /// block on IO, so they run together off the main actor; only the status is set here.
    func use(_ job: Job) {
        guard !busy, let make = client else { return }
        busy = true
        status = "Using the letter…"
        let key = job.key
        let letter = make()
        let store = bankStore()
        Task {
            let message = await BlockingWork.value { Self.useLetter(key: key, letter: letter, store: store) }
            busy = false
            status = message
        }
    }

    /// Runs off the main actor. Returns the status line.
    private nonisolated static func useLetter(key: String, letter: CoverLetterClient, store: BankStore?) -> String {
        switch letter.use(key) {
        case let .failure(_, message):
            return BoundedText.sentence(message)
        case let .success(used):
            let text = letter.readAllowed(used.textPath, max: CoverStore.maxTextBytes)
            let pdf = used.pdfPath.flatMap { letter.readAllowed($0, max: CoverStore.maxBytes) }
            guard let store else {
                return "The letter was marked used, but this app cannot hand it to the browser."
            }
            guard let text, let body = String(data: text, encoding: .utf8), !body.isEmpty else {
                return "The used letter could not be read."
            }
            let letters = CoverStore(directory: store.directory.appendingPathComponent("letters", isDirectory: true))
            do {
                _ = try letters.put(key: key, name: "Cover_Letter.pdf", pdf: pdf, body: body)
                try? store.log(ActivityEntry(source: .app, event: .cover, counts: ["chosen": 1]))
                return "v\(used.version) is the letter the browser may attach."
            } catch {
                return BoundedText.sentence(error.localizedDescription)
            }
        }
    }
}
