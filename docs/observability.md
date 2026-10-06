# Logging and observability requirements

**Status:** T02/T03 implement structured Ara/MCP instrumentation and local process
sampling; T04 implements control-phase logs, heartbeat/reconnect metrics, bounded
mutation outcome instrumentation, and diagnostic connection state; T05 adds saved-
plan change logs with correlation/sequence IDs and no sequence bodies; T13 adds an
optional Chi diagnostics listener with health/status and Prometheus metrics. T06
records distinct terminal run-state observations from explicit state reads. T09 adds
Streamable HTTP request IDs, structured request logs, request counters/latency/in-flight
measurements, recovered-fault counting (including after committed headers), request
spans, and bearer/Origin enforcement on `/mcp`. Resource dashboard/exports and profiling
remain planned.

T08 counts processed Ara events by bounded category/outcome without identifier labels.
`get_adapter_diagnostics` exposes last event time/sequence, retained backlog, gap, and
dropped count; `get_recent_ara_events` returns the bounded buffer. Fresh heartbeat
state, not ordinary event activity, determines whether an idle owned socket is stale.

These requirements apply to both stdio and Streamable HTTP. They observe the
adapter and its interaction with Ara; Ara remains authoritative for imaging and
equipment state. Implement and test the relevant signals with each feature,
following [red-green-refactor](development.md#test-driven-development).

## Table of contents

- [Skills and implementation choices](#skills-and-implementation-choices)
- [Logging requirements](#logging-requirements)
- [HTTP middleware and streaming](#http-middleware-and-streaming)
- [Metrics requirements](#metrics-requirements)
- [Tracing requirements](#tracing-requirements)
- [Health and diagnostic status](#health-and-diagnostic-status)
- [Resource monitoring dashboard and downloads](#resource-monitoring-dashboard-and-downloads)
- [Profiling and operational access](#profiling-and-operational-access)
- [Acceptance and delivery](#acceptance-and-delivery)

## Skills and implementation choices

Use the task-based selection process in [AGENTS.md](../AGENTS.md). Applicable
bundled skills for these requirements are:

- [golang-observability](../.agents/skills/golang-observability/SKILL.md), including
  its logging, metrics, tracing, and profiling references.
- [golang-samber-slog](../.agents/skills/golang-samber-slog/SKILL.md) for `slog`
  handling, context, and sink lifecycle.
- [golang-error-handling](../.agents/skills/golang-error-handling/SKILL.md) for
  single-boundary error handling.
- [golang-testing](../.agents/skills/golang-testing/SKILL.md) for behavioral checks;
  [golang-stretchr-testify](../.agents/skills/golang-stretchr-testify/SKILL.md) when
  Testify is used in those tests.

Use standard-library `log/slog` handlers first. Prometheus-compatible metrics and
OpenTelemetry instrumentation cover aggregate measurements and request traces.
Chi's `RequestLogger` hooks connect HTTP request/fault records to that same logger.
Pin their dependencies when implementation needs them; do not write a custom
metrics exposition format or trace protocol.

The adapter must run without an external telemetry service. Collect local metrics
and create the required instrumentation in both modes; metric serving and OTLP
trace export are explicitly configurable. Trace export and profiling are disabled
by default. Failures contacting a collector must not alter Ara operations.
The adapter targets small SBCs such as Raspberry Pi 3/4/5. Keep telemetry CPU,
memory, network, and log-volume costs bounded, including during outages; follow
the [resource contract](architecture.md#deployment-targets-and-resource-constraints).

## Logging requirements

### Format, destination, and configuration

- **L1:** Emit structured JSON records using `log/slog`, with UTC timestamps and
  stable message/event names. JSON is the default in both deployment modes.
  A human-readable format may be selected for local debugging.
- **L2:** Send logs to stderr. In stdio mode, stdout contains MCP frames only,
  including during startup errors, reconnects, and shutdown. SDK/internal diagnostics
  must follow the same destination policy.
- **L3:** Provide explicit log-level configuration with `info` as the default.
  Use `debug` for diagnostics, `info` for normal lifecycle/action transitions,
  `warn` for recoverable degradation or contention, and `error` for failed work
  or unrecoverable adapter faults. Expected cancellation is not a server fault.
  Routine state polling and heartbeat frames must not produce info-level noise.

stderr works with the launching agent locally and with journald in service mode.
Deployment documentation must explain retention and log retrieval through the
process supervisor; the adapter does not need its own file-rotation subsystem.

### Correlation and event coverage

- **L4:** Include `service`, `version`, `component`, and `event` on runtime records.
  Add `transport`, a locally assigned `request_id`, `tool`, `outcome`, and
  `duration_seconds` to completed tool-call records where applicable. Unknown or
  rejected tool names must not become arbitrary event names.
- **L5:** Carry request correlation through the Ara client. Add normalized `method`
  and `route`, `http_status` when received, and a bounded `error_class` on relevant
  diagnostics. Include `sequence_id`, `run_id`, `job_id`, or `operation_id` when
  actually supplied by Ara; never fabricate an upstream identifier. Include
  `trace_id` and `span_id` only when a valid trace context exists.
- **L6:** Record startup/shutdown, connection loss/recovery, control acquisition/
  denial/loss, saved-plan changes, mutation acceptance, uncertain mutation outcomes,
  and observed terminal operation results. Keep `accepted` separate from `completed`.
  A later Ara event carries its operation IDs even if the initiating tool has returned.

Log each failure once at the owning tool, connection, or process boundary. Lower
layers return errors with context instead of logging and returning them repeatedly.
The boundary translates the failure into the appropriate MCP result and diagnostic.
Use context-aware logging without assuming that passing a context automatically
adds correlation fields; configure the required logger/handler attributes explicitly.

### Content and volume

- **L7:** Allowlist diagnostic attributes and sanitize error text before emission.
  Exclude credentials, authorization headers, Ara/MCP session tokens, URL userinfo
  or sensitive query strings, full sequence/profile bodies, and image/FITS payloads.
  This applies at debug level, to trace attributes, and to diagnostic status too.
  Bound text fields supplied by a client or upstream server.
- **L8:** Bound repeated noise during outages: preserve the first failure, changes
  in failure reason, and recovery; rate-limit duplicates and count suppressed
  records. Preserve mutation outcomes and distinct terminal failures. Do not log
  every WebSocket frame or duplicate an event when replayed after reconnect.

## HTTP middleware and streaming

- **HTTP1:** Use Chi `middleware.RequestLogger` with a native `slog` formatter/
  request entry and `middleware.Recoverer` beneath it. The default `middleware.Logger`
  is plain text on stdout, and an unbound recoverer prints a plain-text stack. The
  composed stack must route both through structured JSON on stderr, including
  when the diagnostics listener runs alongside stdio.
- **HTTP2:** Attach a locally assigned `http_request_id`, normalized `method`/`route`,
  actual `http_status`, `response_bytes`, and `duration_seconds` to the final HTTP
  record. Link logical tool `request_id` and valid trace context where available.
  Chi request-ID middleware accepts supplied headers; discard untrusted IDs before
  assignment rather than treating arbitrary caller text as local correlation.
- **HTTP3:** Collect the route pattern after dispatch, using a fixed unmatched value
  for unknown/early-rejected requests. Keep message/event names constant and
  apply the existing attribute allowlist/redaction before emission. Do not capture
  bodies, sensitive headers, full URLs/query strings, or raw panic values; emit only
  bounded, sanitized fault details.
- **HTTP4:** Recovery records fault metadata in the owning request entry and emits
  one structured failure summary at request termination. Classify panic/fault outcome
  independently of HTTP status: a stream can already have committed HTTP 200. Return
  500 only when still possible; do not append a rendered error to an active MCP/SSE
  response. Preserve deliberate `http.ErrAbortHandler`/cancellation behavior.
- **HTTP5:** Request wrappers preserve flushing and response-controller behavior;
  logging/tracing must not buffer, read ahead, or rewrite the SDK's stream. Record
  long-lived stream lifetime separately from tool latency, with active-request/stream
  state visible while open. Successful probes/scrapes are quiet; their faults still log.

Request correlation/tracing and logger/metrics wrap recovery and downstream access
checks so denied requests and recovered failures remain observable. Use Chi render
for typed ordinary diagnostic responses; the SDK owns MCP payloads/content types.
Only short ordinary routes receive generic deadline/content-type/compression
middleware; stream policies are explicitly tested against the SDK.

## Metrics requirements

### Required measurements

Use counters for occurrences, histograms for request latency, and gauges for
current state. Durations use seconds; sizes use bytes. Initial metric contracts
are defined with the relevant implementation task and documented beside declarations.

| Measurement | Required semantics | Delivery |
| --- | --- | --- |
| Ara HTTP requests and latency | Count requests by normalized method/route/status class; record network errors, timeouts, and response decoding failures distinctly | T02 |
| MCP tool calls, latency, and in-flight work | Count logical calls and bounded outcomes, including rejected arguments, accepted work, failures, cancellation, and uncertain outcomes | T03 |
| WebSocket/control health | Connection state, reconnect attempts, last confirmed heartbeat, and whether the adapter holds Ara control | T04 |
| Observed operation results | Count observed terminal transitions separately from command acceptance; deduplicate replayed results | T06, T08 |
| Event processing | `ara.events.processed` counts bounded categories (`sequence`, `equipment`, `job`, `frame`, `other`, `recovery`) and outcomes; diagnostics expose backlog, drops, last sequence, and gap | T08 |
| HTTP MCP requests | Normalized Chi route/method latency/status and transport faults; separate stream lifetime from tool latency and distinguish protocol success from tool errors | T09 |
| Runtime/process usage | Shared process CPU/RSS, Go memory/GC, goroutines, uptime, and available process statistics with explicit units/freshness | T03, T12 |
| Resource dashboard/history/exports | Sampling failures, retained window/bytes, live subscribers, export concurrency/failures, and slow-consumer handling | T12 |

T04 records `ara.control.claims` (counter by `outcome`), `ara.control.owned` and
`ara.websocket.active` (up/down counters), `ara.websocket.reconnects`,
`ara.websocket.heartbeats`, `ara.control.mutations` (bounded `kind`, `outcome`, and
`error.type`), and `ara.control.mutation.replays`. IDs and user text are never metric
labels. `/status` and `get_adapter_diagnostics` expose ownership, socket state,
last heartbeat, and last reconciliation; none include the Ara session capability.
Short-lived `ara.control.begin`, `ara.control.end`, and `ara.control.reconnect` spans
capture lifecycle work; reconnect spans link to the initiating begin span, and no
span remains open for the lifetime of an imaging session.

T06 records `ara.sequence.runs.observed` when an explicit state read first observes
a terminal `completed`, `stopped`, or `failed` state for a sequence/run ID pair.
The metric labels only the bounded terminal state; IDs are used only in structured
logs and the bounded in-process replay-deduplication set. Command acceptance remains
in the T04 mutation outcome metric and is not counted as a terminal result. This
initial delivery observes on tool reads/immediate post-command reads; it does not
consume Ara events (T08 owns event observation).

T09's `mcp.http.requests` counter labels normalized transport, route, method, status
class, and outcome; recovered handler faults use `outcome=error` even if the SDK has
already committed HTTP 200. `mcp.http.faults` separately counts recovered panics, and
the corresponding span is marked failed. Panic values are not recorded.

Choose histogram buckets that cover documented request timeouts. Tool-call latency
measures adapter request handling, not the duration of an exposure or an imaging
night. Observed equipment/run outcomes are labeled as observations, not proof that
the adapter received every event since startup.

### Cardinality and access

- **M1:** Keep labels bounded: registered tool name, transport, normalized route,
  method, outcome, status class, and known event/error categories. Map unknown
  values to an `unknown` category. Never label by request/session/sequence/run/job/
  frame IDs, raw URLs, target names, arbitrary error messages, or user-supplied text.
- **M2:** Metrics must have consistent behavior in both transports and be testable
  through an in-process registry without a live Prometheus server. No tool call
  opens a new telemetry connection merely to record a measurement.
- **M3:** Provide an opt-in Prometheus-compatible scrape endpoint on a diagnostics
  listener. Default to loopback, separate diagnostic access from rig-control access,
  and document authentication for any remotely exposed diagnostics. Stdio must
  start without opening an HTTP listener unless explicitly configured.

Document names, labels, units, reset behavior, and example queries when metrics
are implemented. Provide a small deployment monitoring recipe for upstream
unavailability, repeated reconnects, failed/uncertain commands, and resource growth.
Choose alert thresholds from test/deployment evidence; a quiet event stream alone
is not an outage or a failed imaging sequence.

## Tracing requirements

- **TR1:** Instrument logical MCP tool calls, outgoing Ara HTTP requests, meaningful
  connection/reconnect attempts, and HTTP transport handling with OpenTelemetry.
  Propagate context and record sanitized errors and span status. Correlate spans
  and logs with the same logical request and known upstream operation IDs.
- **TR2:** Inject supported W3C trace context into outgoing Ara HTTP requests.
  Trace extraction from MCP requests must follow the selected protocol/SDK's
  actual support. Ara-side tracing is not assumed; document the observed boundary.
- **TR3:** End a tool-call span when its result is returned. A 202 result represents
  acceptance, not completion. Later operation observations use separate events/
  spans and link to the originating context when still available. Do not hold a
  tool-call span open for an entire imaging night or every heartbeat.
- **TR4:** Provide optional OTLP export and configurable sampling. Without an exporter,
  the application remains fully functional. Export queues and shutdown flushing have
  explicit bounds; a failed exporter must not pause, repeat, cancel, or fail an Ara
  command that otherwise succeeded. Never print exported spans to MCP stdout.

Test instrumentation with in-memory exporters. Remote collectors are deployment
choices, not prerequisites for default tests or local-agent use.

## Health and diagnostic status

- **H1:** Expose a read-only, transport-independent adapter diagnostic tool with
  version, uptime, transport, Ara connectivity/last successful check, WebSocket state,
  last heartbeat/reconciliation time, control ownership status, and enabled telemetry
  outputs. Unknown state is explicit; diagnostics do not reveal credentials or tokens.
- **H2:** When the optional diagnostics listener is enabled, `/healthz` reports
  process liveness and `/readyz` reports initialization and Ara API reachability.
  Readiness may be degraded while liveness remains healthy. Use bounded read-only
  checks and cached timestamps; report freshness rather than hiding stale checks.
- **H3:** No connected equipment or another human control owner does not, by itself,
  make the adapter unready for read-only work. Report rig state and mutation/control
  availability separately. WebSocket availability is explicit and affects the
  operations that require it, rather than inventing a global "rig ready" claim.
- **H4:** Health checks, scrapes, and diagnostics never acquire the control slot,
  masquerade as human attention, issue equipment commands, or resume a sequence.
  Missing ordinary telemetry events do not imply failure if heartbeats are healthy.

## Resource monitoring dashboard and downloads

[resource-dashboard.md](resource-dashboard.md) owns the required sampling schema,
self-hosted live page, SSE behavior, retained history, and CSV/JSONL exports.
T03 collects the shared process/runtime samples; T13 serves diagnostic health and
metrics; T12 delivers the diagnostics UI and downloadable finite histories on that
listener, including
the opt-in rotating JSONL archive. T10 validates footprint and deployment behavior.
The page works without a hosted monitoring service or required external assets.

Share samples with metric/diagnostic adapters; do not run a collector per viewer,
scrape, or download. Keep process RSS separate from Go heap and show unsupported/
warmup/stale values honestly. Dashboard SSE and exports retain their own formats,
while all HTTP requests use the existing Chi access/logging policy.
Use the selected [CPU denominator, limits, and archive/access defaults](first-release-policy.md#dashboard-history-and-provisional-limits).
Archives survive restart for completed retained records, expose gaps/rotation, and
degrade independently on write failure; they never block control or sampling.

## Profiling and operational access

Use native `net/http/pprof` for explicitly enabled, on-demand profiling on the
diagnostics listener. Profiling is off by default, loopback-only by default, and
requires authenticated access when enabled. Keep it off the public MCP listener.
Document how to capture CPU/heap/goroutine evidence and disable profiling afterward.
Profiles and crash diagnostics need the same care as logs before sharing them.

Logging, metrics, and traces are required adapter instrumentation. Product analytics
and a mandatory hosted monitoring stack are not part of the initial adapter scope.
Use the deploying supervisor/collector for retention and aggregation.

## Acceptance and delivery

These are feature acceptance criteria, implemented incrementally with their owner
tasks (T02–T10 plus T12/T13):

1. Tests show valid structured logs on stderr and protocol-only stdout, including
   startup failure, normal calls, reconnects, and shutdown. Log level and redaction
   work; each lower-layer failure produces one boundary diagnostic.
2. Tests correlate a tool call and upstream request, keep accepted/terminal/uncertain
   results distinct, and avoid duplicate terminal logs/counters after replay.
3. Metrics tests cover success/failure/deadline/cancellation paths, balanced in-flight
   gauges, histogram observations, and bounded cardinality across many distinct IDs.
4. In-memory trace tests verify parent/child relationships, sanitization, error status,
   span termination, and continued operation with export disabled or unavailable.
5. Diagnostics distinguish live/degraded/stale/unknown state without making mutations.
   Listener/access tests cover disabled-by-default serving and protected profiling.
   Tests through the full Chi stack cover structured denial/recovery logs, bounded
   request IDs, render status/content types, normal unmatched routes, and SDK SSE
   flushing/cancellation beyond ordinary-route deadlines. A recovered fault after
   committed headers is not reported as a successful operation or a second response.
6. Repeated reconnect and collector-failure tests show bounded queues, clean shutdown,
   and no instrumentation-caused mutation retry or leaked background work.
7. Deployment evidence includes log retrieval/retention, a scrape/trace example when
   enabled, useful failure queries, and measured diagnostic overhead on Linux ARM64.
   Measure default and optional telemetry on a representative low-resource SBC,
   including collector outages, queue bounds, and shared imaging-service headroom.
8. The self-hosted page auto-updates from shared resource samples and serves locally
   hosted assets. CSV/JSONL downloads agree with retained data and preserve units,
   time/range/availability, snapshot consistency, and SBC memory limits; verify the
   [dashboard acceptance criteria](resource-dashboard.md#acceptance-criteria).

See [the implementation plan](plan.md#cross-cutting-requirements) for task ownership
and [the development guide](development.md#logging-and-observability-verification)
for verification practice. No task is complete solely because it emits a log line.
