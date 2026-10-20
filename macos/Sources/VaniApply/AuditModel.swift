import Foundation
import Observation
import VaniApplyKit

/// State for the Audit tab: the week's blind draw, one card at a time. Every
/// read and write is a `vani` call off the main thread. The session advances
/// only after `vani audit answer` succeeded, so a failed write leaves the
/// card where it was and nothing is counted twice.
@MainActor
@Observable
final class AuditModel {
    private(set) var session: AuditSession?
    private(set) var seed: String?
    private(set) var card: AuditCard?
    /// Why the draw could not be read; shown in place of the cards.
    private(set) var loadError: String?
    /// Why the card on screen could not be read; it cannot be judged unseen.
    private(set) var cardError: String?
    private(set) var loading = false
    private(set) var saving = false
    /// The last failed answer, shown as an alert.
    var answerError: String?

    private let client: () -> AuditClient
    private var cardGeneration = 0

    init(client: @escaping () -> AuditClient) {
        self.client = client
    }

    /// Fetches this week's draw (labelled postings are never drawn again) and
    /// shows its first card.
    func start() async {
        guard !loading, !saving else { return }
        loading = true
        loadError = nil
        let c = client()
        let result = await Task.detached(priority: .userInitiated) { Result { try c.draw() } }.value
        loading = false
        switch result {
        case let .success(d):
            seed = d.seed
            session = AuditSession(picks: d.picks)
            await loadCard()
        case let .failure(err):
            session = nil
            card = nil
            loadError = String(describing: err)
        }
    }

    /// Reads the card for the pick on screen.
    func loadCard() async {
        cardGeneration += 1
        let gen = cardGeneration
        card = nil
        cardError = nil
        guard let pick = session?.current else { return }
        let c = client()
        let result = await Task.detached(priority: .userInitiated) { Result { try c.card(pick) } }.value
        guard gen == cardGeneration else { return } // a newer card superseded this one
        switch result {
        case let .success(card):
            self.card = card
            await reportImpression(of: pick)
        case let .failure(err): cardError = String(describing: err)
        }
    }

    /// Why the card on screen could not be reported to `vani impressions log`;
    /// nil when it was. Measurement only: it never blocks the card.
    private(set) var impressionsError: String?

    /// Tells vani this card is on screen (key and position only), one slate per
    /// draw, so the audit's cards count as seen. A card shown again is not
    /// logged twice.
    private func reportImpression(of pick: AuditPick) async {
        guard let seed, let index = session?.index else { return }
        let jobs = client().jobs, section = pick.stratum
        let result = await Task.detached(priority: .utility) {
            Result { try jobs.logImpressions(surface: .audit, slate: seed, section: section, keys: [pick.key], firstPosition: index + 1) }
        }.value
        switch result {
        case .success: impressionsError = nil
        case let .failure(err): impressionsError = String(describing: err)
        }
    }

    /// Answers the card on screen. Ignored while another answer is being
    /// written and while there is no card to judge.
    func answer(_ verdict: AuditVerdict) async {
        guard !saving, card != nil, let pick = session?.current, card?.key == pick.key else { return }
        saving = true
        let c = client()
        let result = await Task.detached(priority: .userInitiated) { Result { try c.answer(pick, verdict) } }.value
        saving = false
        switch result {
        case .success:
            session?.record(verdict)
            await loadCard()
        case let .failure(err):
            answerError = String(describing: err)
        }
    }
}
