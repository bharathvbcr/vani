# decide

Typed multiple-choice questions to decision backends, with typed readings. One owner for the
decision clients shared by Vani, DevCouncil and n8n-email-engine (Vani plan §4, §8).

- `decide`: `Question`, `Slot`, `Answer`, `SlotReading` (answered, abstained, refused, failed,
  unavailable, not asked), per-backend calibration (`LappiCalib`, `JevCalib`; there is no shared
  confidence number), `ValidateQuestion`/`CheckValue`, the `Router` (ordered backends, a shadow
  backend that never decides, a total deadline), and `Spend`/`DailyCap`.
- `decide/jev`: the Decisions API client for typesafe/jev, on OpenRouter or any Jev-compatible
  endpoint (multi-slot requests, one whole-call deadline, bounded and configurable retries on
  429/5xx, spend charged before it is trusted, a caller-shaped `state`). Plain http only on
  loopback; a key read from the environment is only ever sent to openrouter.ai, and only
  `Config.Key` travels to another host. `Config.Name` (default `jev`) names the backend in every
  answer, so one process can run a second client for another systemone-shaped server (Vani's
  `local`: llama.cpp `/v1/systemone`); `Config.Instructions` overrides one slot's question text.
  `Config.Wire` picks the dialect: the default speaks OpenRouter and every `/v1/systemone` server
  (SubQ's `subq-snap-1.2`), `WireGemini` speaks Google's `/v1beta/decisions`
  (`gemini-decision-1`: `input` string, `x-goog-api-key`, documented key limits checked before the
  network, an input safety block read as `Refused`). `Config.InputUSDPerMTok` prices a reply that
  reports input tokens but no cost (SubQ and Gemini), so the daily cap binds; a reported cost wins.
- `decide/lappi`: the Lappi agent client (Unix socket, one JSON line each way under one deadline,
  strict reply parsing) and `Backend`, its `decide.Backend` adapter.

Rule 7 holds by construction: an answered value is always one of the caller's options, and
nothing here means "allow". Decisions annotate; callers decide.

Stdlib only. No third-party dependency without the owner's approval.

```bash
GOWORK=off go -C decide vet ./...        # from the Vani repo root
GOWORK=off go -C decide test -race ./...
```

It lives in the Vani repository as a nested module (`decide/`, its own `go.mod`, module path
unchanged) so a fresh clone of Vani builds without a private checkout. Vani's `go.mod` replaces
it with `./decide`. `scripts/test.sh` tests it on its own, because the root module's `./...`
does not reach a nested module.

Consumers today: Vani. DevCouncil's `devcouncil/lappi`
and n8n-email-engine's inline Jev client are to become thin users of this module; each is a
separate, approved change (DevCouncil's touches the Manvi and Jarvis pins).
