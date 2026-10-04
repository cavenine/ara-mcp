# Ara and MCP integration rules

Read [AGENTS.md](../../AGENTS.md) and the [architecture notes](../../docs/architecture.md)
first. These rules apply to tool schemas, Ara requests, transports, and sessions.

## Tool and API contracts

- Identify the Ara route, request body, response, and backing service before adding
  a tool. Verify the relevant Ara version; a declared DTO field can be unused.
- Use explicit task-shaped tools. Avoid arbitrary URLs, arbitrary HTTP methods,
  shell execution, or direct Alpaca device control.
- Register the same handlers for both MCP transports. Transport selection must
  not change tool behavior or upstream validation.
- Use SDK schemas and validation, and describe units and defaults. Tool annotations
  such as read-only/idempotent hints describe behavior; they are not authorization.
- Keep planning separate from execution. Preserve the sequence tree and schema
  version; do not silently discard unsupported instructions.
- Do not equate Ara's limited sequence validation with full rig compatibility.
- Represent 202 responses as accepted operations and expose a way to inspect their
  outcome. Return upstream failures as tool errors, not success-shaped text.
- Never retry a mutation blindly after a timeout. Use a supported idempotency key
  or reconcile Ara state; report an uncertain outcome honestly.
- Apply [first-release arbitration/preflight/completion rules](../../docs/first-release-policy.md):
  one imaging run, normal mutation serialization, intent receipts, expected run IDs,
  reserved interrupts, verified instruction palette, and honest receipt-only tracking.

## Connections and lifecycle

- Ara control ownership is separate from an MCP connection. Respect the human
  client's handoff. Assume the user does not mutate through the UI while MCP controls
  the rig and only one adapter controls it; this is cooperative policy, not a new
  REST ownership lock. Begin/end control explicitly and never implicitly stop imaging.
- Reconnect by querying Ara's current state. Closing an MCP connection or cancelling
  a request must not implicitly abort an accepted Ara operation.
- Avoid counting automated reconnects as ongoing human attention. Upstream user
  activity hooks can affect unattended-shutdown behavior.
- Only bind/reconnect a valid adapter-owned WS session. On expiry/restart, invalidate
  control and fall back to read-only REST; require a new explicit begin-control call.
  Never open unbound monitor WS connections or borrow the UI's session.
- Stdio reserves stdout for protocol frames; diagnostics go to stderr.
- Streamable HTTP starts on loopback by default. Before network-facing deployment,
  implement appropriate authentication and MCP Origin validation, and document the
  transport's access model. Use SDK auth primitives rather than a custom protocol.

## HTTP routing and middleware

- Use `github.com/go-chi/chi/v5` and its `middleware` package for the MCP-facing
  router and optional diagnostics router. Keep them compatible with `net/http`.
  The outgoing Ara client remains an HTTP client, not a second server/router.
- Mount the official SDK's Streamable HTTP `http.Handler` at the documented MCP
  endpoint, preserving SDK methods, negotiation/session headers, JSON-RPC errors,
  and JSON/SSE responses. Share the same MCP tools with stdio.
- Compose request correlation/tracing and request logging/metrics outside recovery,
  with recovery around authentication/Origin checks and handlers. Denied requests
  and recoverable handler faults must both remain observable.
- Use Chi `middleware.RequestID`, ensuring untrusted incoming IDs are discarded
  before server assignment. Treat its HTTP request ID separately from the logical
  tool-call ID; correlate them without logging MCP/Ara session credentials.
- Use `middleware.RequestLogger` with a small native `log/slog` `LogFormatter`/
  `LogEntry` adapter. Place `middleware.Recoverer` beneath it so its panic hook
  uses that same structured entry. Emit one sanitized request/fault summary; do
  not stack another request logger or use the stdout/plain-text default logger.
- Log normalized routes after routing where available, with a fixed unmatched
  category otherwise. Preserve `http.Flusher`/response-controller functionality
  through wrappers. Metrics/logging must not buffer MCP requests or SSE responses.
- Scope timeout, throttling, compression, and content-type middleware to the routes
  where they are appropriate. No blanket short timeout/write deadline or response
  rewriting may terminate a valid long-lived MCP stream. Keep command deadlines
  distinct from transport lifetime and never replay a mutation to recover a stream.
- Use `github.com/go-chi/render` with typed ordinary HTTP response/error payloads
  and checked binder/renderer errors. Scope content negotiation to those endpoints;
  MCP framing belongs to the SDK; dashboard SSE, CSV/JSONL exports, and metrics/
  pprof keep their native formats.
  Do not replace global render functions or append a JSON error to a committed stream.
- Apply diagnostic auth/access policy before any profiler handler. Chi middleware
  helpers can be used for liveness/profiling; readiness retains its real Ara checks.
- Use RED/GREEN tests through the composed router, including auth denial, recovery,
  request ID handling, render status/content type, and actual SDK SSE flush/cancel
  behavior. A bare handler test alone does not establish middleware compatibility.
- Dashboard/live-statistics/export routes follow the diagnostics access policy,
  read one shared sampler/history, and never claim Ara control or trigger attention.
  Dashboard SSE is a separate endpoint/protocol from the MCP SDK's SSE; middleware
  must preserve both. Use locally served assets and bounded subscriptions/exports
  per [the dashboard contract](../../docs/resource-dashboard.md).

See [architecture](../../docs/architecture.md#http-stack) for the selected libraries
and source references, and [observability](../../docs/observability.md#http-middleware-and-streaming)
for required log fields, recovery semantics, and verification.

## Evidence

Use hardware-free HTTP contract tests and MCP SDK in-memory transports for checks
that do not require a rig. Test both deployment modes when transport behavior
changes. Live checks are opt-in and must state the Ara version, equipment, and
operations exercised. Never present stub responses as completed device actions.
