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
    /// Every overlay is on by default: until the owner sets a stepper it shows
    /// vani's default and sends no flag, so vani's own default applies. A value
    /// the owner sets, 0 included (which turns it off), is kept across launches
    /// and sent.
    var disagree: Int {
        didSet { remember(disagree, Self.disagreeKey) }
    }

    /// Picks nearest the owner's yes answers, added to the next draw; vani
    /// skips them, and says why, until its taste column passes. With
    /// `disagree` at most AuditClient.maxDisagree.
    var neighbours: Int {
        didSet { remember(neighbours, Self.neighboursKey) }
    }

    /// Random engineering-titled picks added to the next draw.
    var engineering: Int {
        didSet { remember(engineering, Self.engineeringKey) }
    }

    /// The steppers the owner has set (their keys); the rest follow vani's
    /// defaults.
    private(set) var chosen: Set<String> = []

    /// The overlays the last draw could not make, and why, as vani said.
    private(set) var skipped: [String] = []

    static let disagreeKey = "audit.disagree"
    static let neighboursKey = "audit.neighbours"
    static let engineeringKey = "audit.engineering"

    private var restoring = false

    private func remember(_ value: Int, _ key: String) {
        guard !restoring else { return }
        defaults.set(value, forKey: key)
        chosen.insert(key)
    }

    /// Back to vani's defaults: the stored values are forgotten and no overlay
    /// flag is sent until a stepper is set again.
    func useVaniDefaults() {
        restoring = true
        defer { restoring = false }
        disagree = AuditClient.defaultDisagree
        neighbours = AuditClient.defaultNeighbours
        engineering = AuditClient.defaultEngineering
        for key in [Self.disagreeKey, Self.neighboursKey, Self.engineeringKey] {
            defaults.removeObject(forKey: key)
        }
        chosen = []
    }

    /// What the next draw asks for: a stepper the owner set, or one clamped
    /// away from vani's default, is sent; any other is left to vani.
    var overlays: AuditOverlays {
        func send(_ value: Int, _ key: String, _ vani: Int) -> Int? {
            chosen.contains(key) || value != vani ? value : nil
        }
        return AuditOverlays(disagree: send(disagree, Self.disagreeKey, AuditClient.defaultDisagree),
                             neighbours: send(neighbours, Self.neighboursKey, AuditClient.defaultNeighbours),
                             engineering: send(engineering, Self.engineeringKey, AuditClient.defaultEngineering))
    }

    /// A maybe or a no waiting for its reason: the card stays until the owner
    /// picks one, says none, or cancels.
    private(set) var pendingVerdict: AuditVerdict?

    private let client: () -> AuditClient
    private let defaults: UserDefaults
    private var cardGeneration = 0

    init(client: @escaping () -> AuditClient, defaults: UserDefaults = .standard) {
        self.client = client
        self.defaults = defaults
        // A key never written is vani's default, not 0: integer(forKey:) cannot tell them apart.
        func stored(_ key: String) -> Int? { defaults.object(forKey: key) == nil ? nil : defaults.integer(forKey: key) }
        let sd = stored(Self.disagreeKey), sn = stored(Self.neighboursKey), se = stored(Self.engineeringKey)
        let d = min(max(sd ?? AuditClient.defaultDisagree, 0), AuditClient.maxDisagree)
        disagree = d
        neighbours = min(max(sn ?? AuditClient.defaultNeighbours, 0), AuditClient.maxDisagree - d)
        engineering = min(max(se ?? AuditClient.defaultEngineering, 0), AuditClient.maxEngineering)
        chosen = Set([(sd, Self.disagreeKey), (sn, Self.neighboursKey), (se, Self.engineeringKey)].compactMap { $0.0 == nil ? nil : $0.1 })
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
        let o = overlays
        pendingVerdict = nil
        let result = await BlockingWork.run { try c.draw(o) }
        loading = false
        switch result {
        case let .success(d):
            seed = d.seed
            skipped = d.skipped
            session = AuditSession(picks: d.picks)
            await loadCard()
        case let .failure(err):
            session = nil
            card = nil
            skipped = []
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
