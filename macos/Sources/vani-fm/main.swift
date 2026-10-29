// vani-fm: the on-device (Apple Intelligence) decision helper. One JSON request on stdin, one JSON
// reply line on stdout, exit 0; the contract and the pure logic are OnDeviceDecider (VaniApplyKit)
// and macos/README.md. Vani's Go daemon runs it once per question (internal/decide/fmbackend).
//
// It sees a form field's label and type, never a value, a saved answer or a job description.
// Generation is constrained to the request's options, so the model cannot say anything else; the
// choice is validated again before it is reported. A model that is off, refuses or hangs yields a
// reading (`unavailable`, `refused`), never a stall: a watchdog answers and exits at the deadline.
import Dispatch
import Foundation
import FoundationModels
import VaniApplyKit

typealias Decider = OnDeviceDecider

/// Writes the one reply and exits. The first caller wins, so the watchdog and the model cannot both speak.
final class Emitter: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false

    func emit(_ reply: Decider.Reply) -> Never {
        lock.lock()
        if done {
            lock.unlock()
            // Another thread is already writing and will exit the process.
            while true { sleep(60) }
        }
        done = true
        lock.unlock()
        FileHandle.standardOutput.write(reply.encoded() + Data("\n".utf8))
        exit(0)
    }
}

let emitter = Emitter()

/// Reads stdin up to the request cap plus one byte, so an oversized request is seen, not truncated.
func readRequest() -> Data {
    var data = Data()
    let limit = Decider.maxRequestBytes + 1
    while data.count < limit {
        guard let chunk = try? FileHandle.standardInput.read(upToCount: limit - data.count), !chunk.isEmpty else { break }
        data.append(chunk)
    }
    return data
}

@available(macOS 26.0, *)
func classify(_ error: Error) -> Decider.Failure {
    if let e = error as? LanguageModelSession.GenerationError {
        switch e {
        case .refusal: return .refusal
        case .guardrailViolation: return .guardrail
        case .exceededContextWindowSize: return .contextSize
        case .unsupportedLanguageOrLocale: return .unsupportedLocale
        case .rateLimited, .concurrentRequests: return .rateLimited
        case .assetsUnavailable: return .assetsUnavailable
        case .decodingFailure, .unsupportedGuide: return .decoding
        @unknown default: return .other
        }
    }
    if #available(macOS 27.0, *), let e = error as? LanguageModelError {
        switch e {
        case .refusal: return .refusal
        case .guardrailViolation: return .guardrail
        case .contextSizeExceeded: return .contextSize
        case .unsupportedLanguageOrLocale: return .unsupportedLocale
        case .rateLimited: return .rateLimited
        case .timeout: return .timeout
        case .unsupportedCapability, .unsupportedTranscriptContent, .unsupportedGenerationGuide: return .decoding
        @unknown default: return .other
        }
    }
    return .other
}

@available(macOS 26.0, *)
func ask(_ req: Decider.Request) async -> Decider.Reply {
    let model = SystemLanguageModel.default
    if case .unavailable(let reason) = model.availability {
        switch reason {
        case .deviceNotEligible: return Decider.reply(unavailable: .deviceNotEligible)
        case .appleIntelligenceNotEnabled: return Decider.reply(unavailable: .appleIntelligenceNotEnabled)
        case .modelNotReady: return Decider.reply(unavailable: .modelNotReady)
        @unknown default: return Decider.reply(unavailable: .other)
        }
    }
    do {
        let choice = DynamicGenerationSchema(name: "Kind", anyOf: Decider.choices(for: req))
        let root = DynamicGenerationSchema(
            name: "Reading", properties: [DynamicGenerationSchema.Property(name: "kind", schema: choice)])
        let schema = try GenerationSchema(root: root, dependencies: [])
        let session = LanguageModelSession(model: model, instructions: Decider.sessionInstructions(for: req))
        let response = try await session.respond(
            to: Decider.prompt(for: req), schema: schema,
            options: GenerationOptions(samplingMode: .greedy, maximumResponseTokens: 48))
        let picked = try? response.content.value(String.self, forProperty: "kind")
        return Decider.reply(choice: picked, options: req.options)
    } catch {
        return Decider.reply(failure: classify(error))
    }
}

switch Decider.parse(readRequest()) {
case .failure(let reply):
    emitter.emit(reply)
case .success(let req):
    // The watchdog does not wait for the model: respond() may not honour cancellation.
    DispatchQueue.global().asyncAfter(deadline: .now() + Decider.modelTimeoutSeconds) {
        emitter.emit(Decider.reply(failure: .timeout))
    }
    if #available(macOS 26.0, *) {
        emitter.emit(await ask(req))
    } else {
        emitter.emit(Decider.reply(unavailable: .osTooOld))
    }
}
