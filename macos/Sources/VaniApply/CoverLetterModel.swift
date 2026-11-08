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
            let result = await Task.detached(priority: .userInitiated) { letter.build(key) }.value
            busy = false
            switch result {
            case let .success(saved):
                status = saved.pdf ? "Saved v\(saved.version)." : "Saved the text of v\(saved.version). The PDF could not be drawn."
            case let .failure(_, message):
                status = BoundedText.sentence(message)
            }
        }
    }

    func use(_ job: Job) {
        guard !busy, let make = client else { return }
        busy = true
        status = "Using the letter…"
        let key = job.key
        let letter = make()
        Task {
            let result = await Task.detached(priority: .userInitiated) { () -> (CoverLetterClient.Outcome<CoverLetterClient.Used>, Data?, Data?) in
                switch letter.use(key) {
                case let .success(used):
                    let text = letter.readAllowed(used.textPath, max: CoverStore.maxTextBytes)
                    let pdf = used.pdfPath.flatMap { letter.readAllowed($0, max: CoverStore.maxBytes) }
                    return (.success(used), text, pdf)
                case let .failure(code, message):
                    return (.failure(code: code, message: message), nil, nil)
                }
            }.value
            busy = false
            switch result.0 {
            case let .failure(_, message):
                status = BoundedText.sentence(message)
            case let .success(used):
                guard let store = bankStore() else {
                    status = "The letter was marked used, but this app cannot hand it to the browser."
                    return
                }
                guard let text = result.1, let body = String(data: text, encoding: .utf8), !body.isEmpty else {
                    status = "The used letter could not be read."
                    return
                }
                let letters = CoverStore(directory: store.directory.appendingPathComponent("letters", isDirectory: true))
                do {
                    _ = try letters.put(key: key, name: "Cover_Letter.pdf", pdf: result.2, body: body)
                    try? store.log(ActivityEntry(source: .app, event: .cover, counts: ["chosen": 1]))
                    status = "v\(used.version) is the letter the browser may attach."
                } catch {
                    status = BoundedText.sentence(error.localizedDescription)
                }
            }
        }
    }
}
