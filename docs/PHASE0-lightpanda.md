# Phase 0: Lightpanda behind the egress proxy

Date: 2026-10-18. Binary: `/opt/homebrew/bin/lightpanda` `1.1.0-nightly.10455+44e378d61`.
Lightpanda is AGPL; Vani only ever executes it as a separate process.

Method: a scratch logging CONNECT proxy (`build/spike/main.go`, gitignored) that prints the method,
target and `Proxy-Authorization` scheme of every request, then tunnels. Telemetry was disabled with
`LIGHTPANDA_DISABLE_TELEMETRY=1` (see finding 5). Afterwards the real [`egress.Proxy`](file:///Users/bharath/Code/apps/Vani/internal/egress) and
[`page.Source`](file:///Users/bharath/Code/apps/Vani/internal/sources/page) were run end to end (`build/spike2`).

```mermaid
sequenceDiagram
    autonumber
    participant Vani as Vani Runner (page.Source)
    participant Proxy as egress.Proxy (127.0.0.1:port)
    participant LP as Lightpanda Subprocess (AGPL)
    participant Target as External Job Board (Port 443)

    Note over Vani,Proxy: 1. Ephemeral Proxy Setup
    Vani->>Proxy: Start loopback proxy with per-run random auth token
    Note over Proxy: Enforces: port 443 only, allowlisted hosts,<br/>private IP block, pinned dialing

    Note over Vani,LP: 2. Process Spawn
    Vani->>LP: Exec lightpanda fetch --dump html<br/>--http-proxy http://vani:TOKEN@127.0.0.1:port<br/>--proxy-bearer-token TOKEN<br/>--block-private-networks --block-cidrs -127.0.0.1/32<br/>(LIGHTPANDA_DISABLE_TELEMETRY=1)

    Note over LP,Proxy: 3. Egress-Controlled Tunnel
    LP->>Proxy: CONNECT target.com:443 (Proxy-Authorization: Basic)
    alt Host not in allowlist or resolves to private IP
        Proxy-->>LP: 403 Forbidden / Connection Refused
        LP-->>Vani: Navigation failed (exit != 0)
    else Host verified & allowed
        Proxy->>Target: Pinned TLS Handshake & TCP Tunnel
        Target-->>Proxy: TLS Stream
        Proxy-->>LP: 200 Connection Established (Tunnel)
        LP->>Target: GET /jobs/123 (Encrypted via tunnel)
        Target-->>LP: HTML Response
    end

    Note over LP,Vani: 4. Data Extraction
    LP-->>Vani: stdout: Full HTML Dump (exit 0)
    Note over Vani: Extract <script type="application/ld+json"><br/>Parse schema.org/JobPosting
```

## Findings

| Question | Answer |
|---|---|
| Does every HTTPS request and subresource arrive as CONNECT at the proxy? | **Yes.** Every HTTPS host contacted arrived as `CONNECT host:443`; nothing bypassed the proxy. |
| Is `--proxy-bearer-token` sent as `Proxy-Authorization: Bearer`? | **Not on CONNECT.** Only on plain-`http://` proxied requests. HTTPS never carries it. |
| Does `--block-private-networks` refuse `http://127.0.0.1` and `http://169.254.169.254` without a proxy? | **Yes, both** (`CouldntConnect`). |

### 1. All HTTPS traffic arrives as CONNECT

```
$ lightpanda fetch --dump html --obey-robots --http-proxy http://vani:TESTTOKEN123@127.0.0.1:59343 \
    --http-timeout 20000 --load-resources stylesheet --load-resources image https://getbootstrap.com/
exit=0 (46529 bytes)
REQ method=CONNECT target=getbootstrap.com:443        auth_scheme="Basic d<redacted>" auth_matches=true
REQ method=CONNECT target=cdn.carbonads.com:443       ...
REQ method=CONNECT target=fonts.googleapis.com:443    ...
REQ method=CONNECT target=cdn.usefathom.com:443       ...
REQ method=CONNECT target=d1m1uiy1540nk9.cloudfront.net:443 ...
```

Third-party scripts and (with `--load-resources`) stylesheets and images each opened a CONNECT. Without
credentials the proxy's 407 makes the whole fetch fail (`err=CouldntConnect`), so there is no
fall-back to a direct connection that I could provoke. Requests are multiplexed, so the count of
CONNECTs is the count of host connections, not of requests. Plain `http://` URLs are sent to the proxy as
ordinary `GET http://host/` requests (the egress proxy answers 405 and never forwards them).

### 2. The bearer flag does not reach CONNECT; Basic credentials in the proxy URL do

```
$ lightpanda fetch ... --http-proxy http://127.0.0.1:59056 --proxy-bearer-token TESTTOKEN123 https://example.com/
REQ method=CONNECT target=example.com:443 auth_scheme="" auth_matches=false headers=[Proxy-Connection]
   (the same with the proxy answering 407 + "Proxy-Authenticate: Bearer": no retry with credentials)

$ lightpanda fetch ... --http-proxy http://127.0.0.1:59056 --proxy-bearer-token TESTTOKEN123 http://example.com/
REQ method=GET target=example.com auth_scheme="Bearer <redacted>" auth_matches=true      <- plain http only

$ lightpanda fetch ... --http-proxy http://127.0.0.1:59056 --http-header "Proxy-Authorization: Bearer TESTTOKEN123" https://example.com/
REQ method=CONNECT target=example.com:443 auth_scheme="" ...                              <- also not sent

$ lightpanda fetch ... --http-proxy http://u:TESTTOKEN123@127.0.0.1:59056 https://example.com/
REQ method=CONNECT target=example.com:443 auth_scheme="Basic d<redacted>" headers=[Proxy-Authorization Proxy-Connection]
```

**Design change:** a bearer-only proxy would reject every HTTPS fetch. `egress.Proxy` therefore accepts
the per-Start token either as `Proxy-Authorization: Bearer <token>` or as the password of
`Proxy-Authorization: Basic base64(user:token)` (user ignored; both constant-time). `page.Source`
passes `--http-proxy http://vani:<token>@127.0.0.1:<port>` and also `--proxy-bearer-token <token>`.
The token is in argv either way (visible to same-user `ps`); it is random per Start and grants only
what the proxy's rules allow.

### 3. `--block-private-networks`

```
$ lightpanda fetch --dump html --block-private-networks http://127.0.0.1:59343/        -> Navigation failed: CouldntConnect
$ lightpanda fetch --dump html --block-private-networks http://169.254.169.254/         -> Navigation failed: CouldntConnect
$ lightpanda fetch --dump html http://127.0.0.1:59343/                                   -> page body served (flag off)
$ lightpanda fetch --dump html --block-private-networks --http-proxy http://u:T@127.0.0.1:59343 https://example.com/
                                                                                         -> CouldntConnect, no CONNECT logged
$ ... add --block-cidrs -127.0.0.1/32                                                    -> works; CONNECT arrives
```

Two consequences. (a) With a loopback proxy the flag also blocks **the proxy itself**, so the proxy's
address must be exempted with `--block-cidrs -<ip>/32` (the source does this). (b) With a proxy
configured the check is on the proxy's address, not on the target: `http://127.0.0.1:59343/` was sent
through the exempted proxy as a `GET`. The browser-side flag is defence in depth only; the egress proxy
(port 443 only, allowlist, DNS vetting, pinned dial) is the control.

### 4. `--dump structured_data` does not exist in this build

`lightpanda help fetch` lists `--dump` modes `html`, `markdown`, `pdf`, `png`, `semantic_tree`,
`semantic_tree_text`. `--dump structured_data` makes `structured_data` parse as a second URL
(`multiple URLs require --json`). JSON-LD is instead read from the **`--dump html`** output, which keeps
`<script type="application/ld+json">` bodies verbatim. Real shape (Lever, Palantir posting page):

```
<script type="application/ld+json">{"@context" : "http://schema.org","@type" : "JobPosting","title" : "Administrative Business Partner",
"hiringOrganization" : {"@type" : "Organization","name": "Palantir Technologies","logo": "..."},
"jobLocation":{"@type" : "Place","address" : {"@type" : "PostalAddress","addressLocality" : "London, United Kingdom",
"addressRegion" : null,"addressCountry" : null,"postalCode" : null}},"employmentType" : "Full-time",
"datePosted" : "2024-03-25","description" : "..."}</script>
```

Note the bare-date `datePosted`, the explicit `null`s, and the country inside the locality string; the
parser handles all three. Greenhouse's board pages carry no JSON-LD at all (title-only fallback applies).

### 5. Other observations

- Without `LIGHTPANDA_DISABLE_TELEMETRY=1` each run also sent `CONNECT telemetry.lightpanda.io:443` through
  the proxy (which the egress allowlist would refuse). The runner sets the variable.
- Navigation failure exits with status 1 but still prints a "Navigation failed" HTML page on stdout, so exit
  status, never stdout, decides success. `--fail-on-http-error` makes HTTP >= 400 exit 22; the source passes it.
- `--proxy-bearer-token=VALUE` (with `=`) is rejected as an unknown argument; flags take a separate value.
- End to end through the real proxy: `page.Source` on `https://jobs.lever.co/palantir` with a one-posting
  pattern returned the posting (`pg:palantir:8975b1...`, 4587-byte description); the same source on a host
  missing from the allowlist failed the board (`CouldntConnect`), as designed.
