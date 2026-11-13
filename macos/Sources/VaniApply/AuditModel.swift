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

    /// Picks from the "teacher wants" disagreement cell, added to the next draw.
    /// 0 (the default) draws none; the quota is the owner's call. Kept across
    /// launches.
    var disagree: Int {
        didSet { defaults.set(disagree, forKey: Self.disagreeKey) }
    }

    /// Picks nearest the owner's yes answers, added to the next draw; vani
    /// refuses the draw unless its taste column passes. 0 draws none. Kept
    /// across launches; with `disagree` at most AuditClient.maxDisagree.
    var neighbours: Int {
        didSet { defaults.set(neighbours, forKey: Self.neighboursKey) }
    }

    /// Random engineering-titled picks added to the next draw. 0 draws none.
    /// Kept across launches.
    var engineering: Int {
        didSet { defaults.set(engineering, forKey: Self.engineeringKey) }
    }

    static let disagreeKey = "audit.disagree"
    static let neighboursKey = "audit.neighbours"
    static let engineeringKey = "audit.engineering"

    /// A maybe or a no waiting for its reason: the card stays until the owner
    /// picks one, says none, or cancels.
    private(set) var pendingVerdict: AuditVerdict?

    private let client: () -> AuditClient
    private let defaults: UserDefaults
    private var cardGeneration = 0

    init(client: @escaping () -> AuditClient, defaults: UserDefaults = .standard) {
        self.client = client
        self.defaults = defaults
        let d = min(max(defaults.integer(forKey: Self.disagreeKey), 0), AuditClient.maxDisagree)
        disagree = d
        neighbours = min(max(defaults.integer(forKey: Self.neighboursKey), 0), AuditClient.maxDisagree - d)
        engineering = min(max(defaults.integer(forKey: Self.engineeringKey), 0), AuditClient.maxEngineering)
    }

    /// The most yes-neighbour picks the stepper allows with the disagreement
    /// picks as set (vani caps the two together).
    var maxNeighbours: Int { max(0, min(AuditClient.maxNeighbours, AuditClient.maxDisagree - disagree)) }
    /// The most disagreement picks the stepper allows with the neighbours as set.
    var maxDisagree: Int { max(0, AuditClient.maxDisagree - neighbours) }

    /// Fetches this week's draw (labelled postings are never drawn again) and
    /// shows its first card.
    func start() async {
        guard !loading, !saving else { return }
        loading = true
        loadError = nil
        let c = client()
        let o = AuditOverlays(disagree: disagree, neighbours: neighbours, engineering: engineering)
        pendingVerdict = nil
        let result = await BlockingWork.run { try c.draw(o) }
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
        let result = await BlockingWork.run { try c.card(pick) }
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
        let result = await BlockingWork.run {
            try jobs.logImpressions(surface: .audit, slate: seed, section: section, keys: [pick.key], firstPosition: index + 1)
        }
        switch result {
        case .success: impressionsError = nil
        case let .failure(err): impressionsError = String(describing: err)
        }
    }

    /// A verdict key or button: yes and can't-tell are written at once; a maybe
    /// or a no waits for its reason (`giveReason`), so the owner can say what
    /// kept it from a yes in one more key.
    func choose(_ verdict: AuditVerdict) async {
        guard !saving, card != nil, session?.current != nil else { return }
        if AuditReason.allowed(for: verdict) {
            pendingVerdict = verdict
        } else {
            pendingVerdict = nil
            await answer(verdict)
        }
    }

    /// Writes the pending maybe or no with reason (nil: no reason given).
    func giveReason(_ reason: AuditReason?) async {
        guard let v = pendingVerdict else { return }
        await answer(v, reason: reason)
    }

    /// Drops the pending verdict; the card stays unanswered.
    func cancelReason() { pendingVerdict = nil }

    /// Answers the card on screen. Ignored while another answer is being
    /// written and while there is no card to judge.
    func answer(_ verdict: AuditVerdict, reason: AuditReason? = nil) async {
        guard !saving, card != nil, let pick = session?.current, card?.key == pick.key else { return }
        saving = true
        let c = client()
        let result = await BlockingWork.run { try c.answer(pick, verdict, reason: reason) }
        saving = false
        switch result {
        case .success:
            pendingVerdict = nil
            session?.record(verdict)
            await loadCard()
        case let .failure(err):
            answerError = String(describing: err)
        }
    }
}
