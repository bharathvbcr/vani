import AppKit
import SwiftUI
import VaniApplyKit

// The Pipeline tab's doctor (DoctorModel). Every string from a report but the counts came from a board,
// a posting or the model: it is shown verbatim, never as Markdown.

/// The doctor's panel: what it does, the newest report's headline, and the review's progress.
struct DoctorSection: View {
    @Environment(DoctorModel.self) private var doctor
    @State private var showingReport = false

    var body: some View {
        @Bindable var doctor = doctor
        Panel(title: "Pipeline doctor") {
            Text("Opus reads the last 30 runs' records and logs for boards that read nothing without failing, and a sample of 40 dropped postings for drops made for the wrong reason. It only reports: no posting, rule or board changes.")
                .font(.callout).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            phaseView
            if let err = doctor.lastError {
                Label {
                    Text(verbatim: doctor.reportIsStale
                         ? "The newest report could not be read, so the one below may be out of date: \(err)"
                         : "The newest report could not be read: \(err)")
                } icon: {
                    Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                }
                .font(.callout).textSelection(.enabled)
            }
            if let shown = doctor.report {
                DoctorHeadline(report: shown.report, path: shown.path) { showingReport = true }
            } else if doctor.lastError == nil, doctor.lastLoaded, doctor.phase == .idle {
                Text("No review has been run yet.").font(.callout).foregroundStyle(.secondary)
            }
        }
        .task { await doctor.loadLast() }
        .sheet(isPresented: Binding(get: { doctor.confirming != nil }, set: { if !$0 { doctor.dismissConfirmation() } })) {
            if let plan = doctor.confirming {
                DoctorConfirmSheet(plan: plan)
            }
        }
        .sheet(isPresented: $showingReport) {
            if let shown = doctor.report {
                DoctorReportSheet(report: shown.report, path: shown.path)
            }
        }
    }

    @ViewBuilder private var phaseView: some View {
        switch doctor.phase {
        case .idle, .confirming:
            HStack {
                Button("Check the Pipeline…") { doctor.estimate() }
                    .disabled(!doctor.isAvailable || doctor.isBusy)
                    .help("Price a review first; nothing is sent until you confirm a budget")
                    .accessibilityIdentifier("pipeline.doctor.check")
                Spacer()
            }
        case .estimating:
            HStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text("Reading the run history and pricing a review…")
                Spacer()
                Button("Cancel") { doctor.cancel() }
            }
        case let .running(cancelling):
            HStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text(cancelling ? "Stopping…" : "Opus is reading the boards and drops. This can take up to 20 minutes.")
                Spacer()
                Button("Stop", role: .destructive) { doctor.cancel() }.disabled(cancelling)
            }
        case let .finished(reply):
            DoctorNote(tone: reply.result.map { $0.complete ? .fine : .problem } ?? .fine,
                       title: reply.result == nil ? "Nothing to ask" : "Review finished",
                       message: reply.result == nil
                           ? "No board tripped a signal and no dropped posting was sampled, so nothing was sent or spent."
                           : "Written to \(reply.report ?? "the state folder").") { doctor.dismiss() }
        case let .failed(f):
            DoctorNote(tone: .problem, title: f.title, message: f.failure.message,
                       detail: f.mayHaveSpent ? "A request may have been sent and paid for; Spend today shows what was." : "Nothing was sent.",
                       action: f.wantsNewEstimate || f.stage == .estimate ? ("Price Again", { doctor.estimate() }) : nil) { doctor.dismiss() }
        case .stopped:
            DoctorNote(tone: .watch, title: "Review stopped",
                       message: "A request already sent was paid for; Spend today shows what was. Any report it wrote is shown below.") { doctor.dismiss() }
        }
    }
}

/// A result line under the doctor's controls, with Dismiss.
private struct DoctorNote: View {
    let tone: DoctorPresentation.Tone
    let title: String
    let message: String
    var detail: String?
    var action: (String, () -> Void)?
    let dismiss: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: tone == .problem ? "exclamationmark.octagon.fill" : tone == .watch ? "pause.circle.fill" : "checkmark.circle.fill")
                .foregroundStyle(doctorColor(tone))
            VStack(alignment: .leading, spacing: 4) {
                Text(title).fontWeight(.semibold)
                Text(verbatim: message).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                if let detail { Text(detail).font(.callout).foregroundStyle(.secondary) }
            }
            Spacer(minLength: 0)
            if let action { Button(action.0, action: action.1) }
            Button("Dismiss", action: dismiss).buttonStyle(.link)
        }
        .padding(10)
        .background(doctorColor(tone).opacity(0.10), in: RoundedRectangle(cornerRadius: 8))
    }
}

/// The shown report's one-line summary, its gaps, and the ways to read it.
private struct DoctorHeadline: View {
    let report: DoctorReport
    let path: String?
    let show: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                Image(systemName: report.complete ? "stethoscope" : "exclamationmark.triangle.fill")
                    .foregroundStyle(report.complete ? Color.secondary : Color.orange)
                VStack(alignment: .leading, spacing: 2) {
                    Text(DoctorPresentation.headline(report)).fontWeight(.medium)
                    Text("Report of \(report.at.formatted(date: .abbreviated, time: .shortened)) by \(report.model)")
                        .font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                Button("Show Report…", action: show).accessibilityIdentifier("pipeline.doctor.show")
                if let path {
                    Menu {
                        Button("Open as Markdown") { NSWorkspace.shared.open(URL(fileURLWithPath: path)) }
                        Button("Show in Finder") { NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path)]) }
                    } label: { Image(systemName: "ellipsis.circle") }
                        .menuStyle(.borderlessButton).fixedSize()
                }
            }
            ForEach(DoctorPresentation.gaps(report), id: \.self) { gap in
                Text(verbatim: gap).font(.callout).foregroundStyle(.red).textSelection(.enabled)
            }
        }
    }
}

/// The estimate and the budget: nothing is sent until Run Review.
struct DoctorConfirmSheet: View {
    @Environment(DoctorModel.self) private var doctor
    let plan: DoctorReply

    var body: some View {
        @Bindable var doctor = doctor
        let blocker = doctor.runBlocker(plan)
        VStack(alignment: .leading, spacing: 14) {
            Text("Review the pipeline with \(plan.model)?").font(.title3).fontWeight(.semibold)
            ForEach(DoctorPresentation.warnings(plan), id: \.self) { w in
                Label { Text(w).fixedSize(horizontal: false, vertical: true) } icon: {
                    Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                }
            }
            Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 6) {
                GridRow { Text("Run records read").foregroundStyle(.secondary); Text("\(plan.runsRead)") }
                if plan.recoveredFromLogs > 0 {
                    GridRow { Text("Board reads from logs").foregroundStyle(.secondary); Text("\(plan.recoveredFromLogs) the records left unnamed") }
                }
                GridRow { Text("Boards to judge").foregroundStyle(.secondary); Text("\(plan.boardsSignaled) tripped a signal") }
                GridRow {
                    Text("Drops to judge").foregroundStyle(.secondary)
                    Text("\(plan.dropsSampled) sampled from \(plan.strata.count) drop classes" + (plan.seed.map { " (seed \($0))" } ?? ""))
                }
                ForEach(plan.requests.keys.sorted(), id: \.self) { part in
                    GridRow { Text("\(part.capitalized) request").foregroundStyle(.secondary); Text(verbatim: plan.requests[part] ?? "") }
                }
                GridRow {
                    Text("Worst case").foregroundStyle(.secondary)
                    Text(DoctorPresentation.usd(plan.worstCaseUSD)).fontWeight(.semibold).monospacedDigit()
                }
            }
            if !plan.nothingToAsk, DoctorPresentation.suggestedBudget(worstUSD: plan.worstCaseUSD) != nil {
                HStack {
                    Text("Spend at most")
                    TextField("Budget", value: $doctor.budget, format: .currency(code: "USD").locale(Locale(identifier: "en_US")))
                        .frame(width: 100).monospacedDigit()
                        .accessibilityIdentifier("pipeline.doctor.budget")
                    Stepper("", value: $doctor.budget, in: 0.25...DoctorClient.maxBudgetUSD, step: 0.25).labelsHidden()
                }
                Text("Vani refuses before sending anything when the worst case is over this or over what is left of today's LLM cap. It sends board ids, counts and log lines with secrets, your home folder and identifying words removed, and the sampled postings' titles and descriptions. Nothing about sponsorship is sent.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            if let blocker {
                Text(blocker).foregroundStyle(plan.nothingToAsk ? Color.secondary : Color.red).fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                Spacer()
                Button("Cancel", role: .cancel) { doctor.cancel() }.keyboardShortcut(.cancelAction)
                Button("Run Review") { doctor.confirm() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(blocker != nil)
                    .accessibilityIdentifier("pipeline.doctor.run")
            }
        }
        .padding(20)
        .frame(width: 520)
    }
}

/// The whole report: boards, drops, patterns, and what the logs held back.
struct DoctorReportSheet: View {
    @Environment(\.dismiss) private var dismiss
    let report: DoctorReport
    let path: String?

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Pipeline doctor").font(.title3).fontWeight(.semibold)
                    Text("\(report.at.formatted(date: .abbreviated, time: .shortened)) · \(report.model)" + (report.seed.map { " · seed \($0)" } ?? ""))
                        .font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                if let path {
                    Button("Open as Markdown") { NSWorkspace.shared.open(URL(fileURLWithPath: path)) }
                }
                Button("Done") { dismiss() }.keyboardShortcut(.defaultAction)
            }
            .padding(16)
            Divider()
            ScrollView { DoctorReportBody(report: report) }
        }
        .frame(minWidth: 720, idealWidth: 860, minHeight: 520, idealHeight: 720)
    }
}

/// The report's sections, outside any scroll view.
struct DoctorReportBody: View {
    let report: DoctorReport

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            ForEach(DoctorPresentation.gaps(report), id: \.self) { gap in
                Label { Text(verbatim: gap).textSelection(.enabled) } icon: {
                    Image(systemName: "exclamationmark.octagon.fill").foregroundStyle(.red)
                }
            }
            boards
            drops
            logs
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder private var boards: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Boards").font(.headline)
            Text("\(report.boards.answered) of \(report.boards.asked) boards that tripped a signal were judged.")
                .font(.callout).foregroundStyle(.secondary)
            ForEach(DoctorPresentation.orderedBoards(report)) { f in
                DoctorBoardRow(finding: f, signal: report.signal(for: f.board))
            }
            if !report.boardsNotExamined.isEmpty {
                Text(verbatim: "Not examined: \(report.boardsNotExamined.joined(separator: ", "))")
                    .font(.callout).foregroundStyle(.red).textSelection(.enabled)
            }
            DoctorPatterns(patterns: report.boards.patterns)
            if let raw = report.boards.rawReply { DoctorRaw(raw: raw) }
        }
    }

    @ViewBuilder private var drops: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Dropped postings").font(.headline)
            Text("\(report.drops.answered) of \(report.drops.asked) sampled drops were judged.")
                .font(.callout).foregroundStyle(.secondary)
            ForEach(DoctorPresentation.orderedDrops(report)) { f in
                DoctorDropRow(finding: f, sampled: report.sampled(f.key))
            }
            if !report.dropsNotExamined.isEmpty {
                Text("Not examined: \(count(report.dropsNotExamined.count, "sampled drop")).").font(.callout).foregroundStyle(.red)
            }
            let missing = report.dropsSent.filter(\.bodyMissing).count
            if missing > 0 {
                Text("\(count(missing, "sampled drop")) \(missing == 1 ? "was" : "were") sent without their description; their verdicts rest on the title and readings.")
                    .font(.callout).foregroundStyle(.secondary)
            }
            DoctorPatterns(patterns: report.drops.patterns)
            if let raw = report.drops.rawReply { DoctorRaw(raw: raw) }
        }
    }

    @ViewBuilder private var logs: some View {
        let l = report.logs
        VStack(alignment: .leading, spacing: 4) {
            Text("What was read").font(.headline)
            Text("\(l.logsRead) run logs; \(l.linesSeen) trouble lines, \(l.distinct) distinct, \(l.lines.count) sent" + (l.leftOut > 0 ? ", \(l.leftOut) left out past the cap" : "") + ".")
            if report.recoveredFromLogs > 0 {
                Text("\(count(report.recoveredFromLogs, "board read")) the run records left unnamed \(report.recoveredFromLogs == 1 ? "was" : "were") read back from the logs.")
            }
            if l.withheld > 0 { Text("\(count(l.withheld, "trouble line")) \(l.withheld == 1 ? "was" : "were") withheld for an identifying word (not sent).") }
            if l.tooLong > 0 { Text("\(count(l.tooLong, "log line")) \(l.tooLong == 1 ? "was" : "were") too long to read.") }
            if !l.unreadable.isEmpty { Text(verbatim: "Unreadable logs: \(l.unreadable.joined(separator: ", "))").foregroundStyle(.orange) }
            if !l.tailOnlyFor.isEmpty { Text("\(count(l.tailOnlyFor.count, "log")) \(l.tailOnlyFor.count == 1 ? "was" : "were") read from their end only.") }
            if !report.runProblems.isEmpty {
                Text(verbatim: "Unreadable run records: \(report.runProblems.joined(separator: "; "))").foregroundStyle(.orange)
            }
        }
        .font(.callout).foregroundStyle(.secondary).textSelection(.enabled)
    }
}

private struct DoctorBoardRow: View {
    let finding: DoctorBoardFinding
    let signal: DoctorSignal?

    var body: some View {
        let v = DoctorPresentation.boardVerdict(finding.verdict)
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline) {
                DoctorBadge(label: v.label, tone: v.tone)
                Text(verbatim: finding.board).fontWeight(.medium).textSelection(.enabled)
                if let company = signal?.company { Text(verbatim: company).foregroundStyle(.secondary) }
                Spacer()
                Text("\(finding.confidence) confidence").font(.caption).foregroundStyle(.secondary)
            }
            if let s = signal {
                Text(verbatim: signalLine(s)).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
            }
            Text(verbatim: finding.evidence).fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
            if !finding.nextStep.isEmpty {
                Text(verbatim: "Next: \(finding.nextStep)").font(.callout).fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 8))
    }

    private func signalLine(_ s: DoctorSignal) -> String {
        let series = s.listedSeries.map { $0 < 0 ? "–" : "\($0)" }.joined(separator: " ")
        var parts = [s.signals.map(DoctorPresentation.signal).joined(separator: ", ")]
        if !series.isEmpty { parts.append("listed, oldest first: \(series)") }
        if s.failures > 0 { parts.append("\(s.failures) failed runs") }
        return parts.filter { !$0.isEmpty }.joined(separator: " · ")
    }
}

private struct DoctorDropRow: View {
    let finding: DoctorDropFinding
    let sampled: DoctorSampledDrop?

    var body: some View {
        let v = DoctorPresentation.dropVerdict(finding.verdict)
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline) {
                DoctorBadge(label: v.label, tone: v.tone)
                if let s = sampled {
                    Text(verbatim: s.title).fontWeight(.medium)
                    Text(verbatim: s.company).foregroundStyle(.secondary)
                } else {
                    Text(verbatim: finding.key).fontWeight(.medium)
                }
                Spacer()
                if let cause = DoctorPresentation.cause(finding.cause) {
                    Text("Cause: \(cause)").font(.caption).foregroundStyle(.secondary)
                }
            }
            Text(verbatim: finding.why).fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
            Text(verbatim: [finding.key, sampled?.stratum].compactMap { $0 }.joined(separator: " · "))
                .font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 8))
    }
}

private struct DoctorPatterns: View {
    let patterns: [DoctorPattern]

    var body: some View {
        if !patterns.isEmpty {
            VStack(alignment: .leading, spacing: 6) {
                Text("Patterns").font(.subheadline).fontWeight(.semibold)
                ForEach(Array(patterns.enumerated()), id: \.offset) { _, p in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(verbatim: p.title).fontWeight(.medium)
                        if !p.detail.isEmpty { Text(verbatim: p.detail).fixedSize(horizontal: false, vertical: true) }
                        if !p.fix.isEmpty { Text(verbatim: "Fix: \(p.fix)").font(.callout).fixedSize(horizontal: false, vertical: true) }
                        if !p.refs.isEmpty { Text(verbatim: p.refs.joined(separator: ", ")).font(.caption).foregroundStyle(.secondary) }
                    }
                    .textSelection(.enabled)
                }
            }
        }
    }
}

/// A reply that was paid for but could not be read, kept as received.
private struct DoctorRaw: View {
    let raw: String

    var body: some View {
        DisclosureGroup("The reply as received (it could not be read)") {
            ScrollView { Text(verbatim: raw).font(.caption.monospaced()).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading) }
                .frame(maxHeight: 200)
        }
        .font(.callout)
    }
}

private struct DoctorBadge: View {
    let label: String
    let tone: DoctorPresentation.Tone

    var body: some View {
        Text(label).font(.caption).fontWeight(.semibold)
            .padding(.horizontal, 6).padding(.vertical, 2)
            .foregroundStyle(doctorColor(tone))
            .background(doctorColor(tone).opacity(0.15), in: Capsule())
    }
}

private func doctorColor(_ t: DoctorPresentation.Tone) -> Color {
    switch t {
    case .problem: .red
    case .watch: .orange
    case .fine: .green
    }
}

/// "1 log", "2 logs".
private func count(_ n: Int, _ noun: String) -> String { "\(n) \(noun)\(n == 1 ? "" : "s")" }
