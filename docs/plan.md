# ara-mcp implementation plan

**Status:** project setup is complete; runtime implementation has not
started. This is a proposed delivery plan, not an implemented capability list.
No release version or date is assigned.

## Table of contents

- [Goal and scope](#goal-and-scope)
- [Cross-cutting requirements](#cross-cutting-requirements)
- [Resolved policies and outstanding verification](#resolved-policies-and-outstanding-verification)
- [Task list](#task-list)
- [Task details](#task-details)
  - [T00 — Repository foundation](#t00-repository-foundation)
  - [T01 — Ara compatibility and tool contracts](#t01-ara-compatibility-and-tool-contracts)
  - [T02 — Ara HTTP client](#t02-ara-http-client)
  - [T03 — Executable, stdio, and read-only tools](#t03-executable-stdio-and-read-only-tools)
  - [T13 — Basic diagnostics HTTP foundation](#t13-basic-diagnostics-http-foundation)
  - [T04 — Ara control ownership and connection lifecycle](#t04-ara-control-ownership-and-connection-lifecycle)
  - [T05 — Sequence authoring](#t05-sequence-authoring)
  - [T06 — Sequence execution](#t06-sequence-execution)
  - [T07 — Manual equipment tools](#t07-manual-equipment-tools)
  - [T08 — Progress, events, and image previews](#t08-progress-events-and-image-previews)
  - [T09 — Streamable HTTP deployment](#t09-streamable-http-deployment)
  - [T12 — Resource dashboard and exports](#t12-resource-dashboard-and-exports)
  - [T10 — Deployment and end-to-end validation](#t10-deployment-and-end-to-end-validation)
  - [T11 — First release preparation](#t11-first-release-preparation)
- [Outstanding verification](#outstanding-verification)
- [Completion and maintenance](#completion-and-maintenance)

## Goal and scope

Deliver one Go 1.27.x MCP adapter using the official MCP Go SDK, with shared tools
for two modes: agent-launched local stdio and a persistent Streamable HTTP service
beside Ara server. Ara remains the authority for equipment and imaging sessions.
Intended telescope-side targets include Raspberry Pi 3/4/5-class Linux ARM64 SBCs
with limited CPU/RAM. Co-located deployment leaves headroom for the imaging stack;
adapter targets do not override Ara's independent hardware requirements.

The first release should let an agent inspect a rig, prepare and save a sequence,
let the user review it in Ara, start and monitor it through the agreed control
handoff, and request a focused set of manual actions. Disconnecting the agent or
adapter must leave Ara's accepted work under Ara's control.
Assume that while MCP controls the rig, the user will not send UI mutations and
one adapter is its controller. This cooperative workflow removes the need for a
new UI/REST ownership-lock feature.
The application also monitors its own resources on a self-hosted, self-updating
SSE dashboard and provides CSV/JSONL downloads from bounded retained statistics.

Start with Ara's existing structured sequence format and templates. Natural-language
interpretation belongs to the AI agent; this adapter does not need an embedded
LLM or a second planning/execution engine. Add specialized sequence builders only
when real authoring experience shows that raw JSON or templates are insufficient.

The [architecture notes](architecture.md) own the call flow, route inventory,
upstream evidence, and known limitations. The [development guide](development.md)
owns check commands. This plan owns delivery order and task acceptance criteria.

## Cross-cutting requirements

All runtime implementation tasks use
[test-driven red-green-refactor](development.md#test-driven-development), one
observable behavior at a time. Task work includes its RED/GREEN evidence and retained
regression tests. Assess and load applicable skills before work, including testing
and observability skills for their respective behavior. There is no later task that
retrofits all tests or instrumentation after feature implementation.

[Logging and observability requirements](observability.md) apply to every runtime
task. Implement structured logs, bounded metrics, correlated traces, and relevant
health/diagnostic behavior alongside each feature. Exporting telemetry is
configurable; instrumentation and its tests are part of acceptance in both modes.

T02 owns upstream request measurements. T03 establishes the process logger, tool
instrumentation, shared resource sampler, and read-only adapter diagnostics.
T13 delivers basic Chi diagnostics serving independently of equipment tools.
T04/T08 add connection and event signals. T09 adds full HTTP MCP transport;
T12 delivers the self-hosted resource page,
dashboard SSE, bounded history, and CSV/JSONL exports per
[resource-dashboard.md](resource-dashboard.md). T10 supplies profile access and
deployment/resource evidence. T05–T07 preserve correlation and distinguish saved-plan
changes, accepted commands, and observed results.

[Small-SBC resource constraints](architecture.md#deployment-targets-and-resource-constraints)
also apply to every runtime task: bound work, retained state, payload/preview sizes,
and telemetry; reuse connections; pace polling/reconnects; and test backpressure
and cleanup. T03 records an initial footprint, T08 checks event/preview growth,
and T10 validates target-board budgets and shared-service headroom.

## Resolved policies and outstanding verification

[first-release-policy.md](first-release-policy.md) is the canonical resolution of
the plan review: cooperative control, explicit control phases, command arbitration/
interrupts, bounded intent receipts, operation-specific tracking, preflight/context,
degraded/restart behavior, access, Datastar, history/archive, and provisional budgets.
Implement its contracts rather than re-opening those choices implicitly in each task.

The [outstanding table](first-release-policy.md#outstanding-verification) assigns
baseline/client compatibility, operation evidence/palette, restart fields, Datastar
version pairing, and board measurements to owner tasks. These require implementation
or integration evidence; documentation alone cannot complete them. Read-only WS
enhancement is optional because the first-release REST fallback covers monitoring.

## Task list

Status is recorded here once; the detailed sections explain the work. `Complete`
requires implemented deliverables and recorded verification, not merely a design.

| ID | Task | Status | Depends on |
| --- | --- | --- | --- |
| T00 | [Repository foundation](#t00-repository-foundation) | Complete | — |
| T01 | [Ara compatibility and tool contracts](#t01-ara-compatibility-and-tool-contracts) | Complete for master `6374eede`; first Ara release and host-specific transport checks remain gated to O1/T03/T09 | T00 |
| T02 | [Ara HTTP client](#t02-ara-http-client) | Pending | T01 |
| T03 | [Executable, stdio, and read-only tools](#t03-executable-stdio-and-read-only-tools) | Pending | T02 |
| T13 | [Basic diagnostics HTTP foundation](#t13-basic-diagnostics-http-foundation) | Pending | T03 |
| T04 | [Ara control ownership and connection lifecycle](#t04-ara-control-ownership-and-connection-lifecycle) | Pending | T03 |
| T05 | [Sequence authoring](#t05-sequence-authoring) | Pending | T04 |
| T06 | [Sequence execution](#t06-sequence-execution) | Pending | T05 |
| T07 | [Manual equipment tools](#t07-manual-equipment-tools) | Pending | T04, T06 |
| T08 | [Progress, events, and image previews](#t08-progress-events-and-image-previews) | Pending | T06, T07 |
| T09 | [Streamable HTTP deployment](#t09-streamable-http-deployment) | Pending | T03, T04, T13 |
| T12 | [Resource dashboard and exports](#t12-resource-dashboard-and-exports) | Pending | T03, T13 |
| T10 | [Deployment and end-to-end validation](#t10-deployment-and-end-to-end-validation) | Pending | T05–T09, T12, T13 |
| T11 | [First release preparation](#t11-first-release-preparation) | Pending | T10 |

First milestone: T01–T03, a real read-only stdio adapter. T13/T12 can then deliver
local HTTP diagnostics/dashboard/downloads before all equipment tools are complete.
T04–T06 add sequence control; T09 HTTP MCP can land once control and the HTTP
foundation exist. T07/T08 complete manual actions/monitoring; T10/T11 validate and
release the combined product. Existing task IDs remain stable.

## Task details

### T00 Repository foundation

**Deliverables:** public `cavenine/ara-mcp` repository, local checkout, Go module,
AGPL license, contribution templates, CI, project docs, and generic `.agents/`
guidance with commit-pinned public Go skills and preserved licenses.

**Acceptance:** setup checks pass with Go 1.27.x; the README distinguishes setup
from implemented features; project files are reviewed and committed.
No runtime or hardware validation is claimed.

### T01 Ara compatibility and tool contracts

**Goal:** establish exactly which Ara behavior the first tools can rely on.

**Work:**

- Recheck the Ara commit/version referenced in `architecture.md` and select the
  baseline to test against. Inspect backing services as well as endpoints and DTOs.
- Define the initial tool names, argument/result schemas, units, and expected errors.
  Map each to an upstream route; identify reads, saved-plan mutations, and rig actions.
- Include acceptance/correlation identity, completion/failure authority, cancellation,
  retry classification, intent/run-ID semantics, and receipt-only limitations per tool.
  Verify actual Ara behavior, including completed-run retries and missing state.
- Select a released official MCP Go SDK version and supported protocol versions.
- Define the Cobra command surface, Viper keys/precedence and environment bindings,
  and startup validation/output contracts. Pin released versions with T03 implementation.
- Define the Chi HTTP endpoint/middleware contract and diagnostic render payloads,
  including structured logging hooks and SDK stream-preservation requirements for T09.
- Apply the resolved provisional payload/concurrency/retention/footprint defaults
  from `first-release-policy.md`; verify feasibility instead of leaving limits unset.
- Define resource sample fields/units and supported-platform availability, sampling
  cadence/history bounds, and dashboard/export access contracts. Datastar/Go SDK,
  CPU denominator, and initial history/archive/access policies are selected; verify
  field/API/version pairing rather than choose another framework implicitly.
- Capture small, sanitized request/response fixtures from the verified contracts.
- Resolve the control/authoring policy questions needed by T04–T06. Record an
  upstream gap rather than exposing an option whose implementation is missing.
- Verify packaged template deserialization/execution subset, `get_rig_context` inputs,
  sequence-list run-state visibility and cursor-pagination behavior, restart/session
  invalidation, and emergency-stop results.
  Record target/filter metadata and event-catalog gaps, and keep unsupported guarantees
  fail-closed. Maintain O1–O3/O6 with source, test, and simulator evidence.

**Deliverables:** current-master compatibility notes, initial tool contract table,
and sanitized fixtures to drive T02 tests. Source and OmniSim evidence are recorded
in [api-contracts.md](api-contracts.md); Ara-specific release-tag checks wait for
Ara's first release, while full route/client tests continue with T03/T04/T07/T09.
Keep the architecture document as the overview.

**Acceptance:** every planned first tool has an evidenced request/result contract.
Unused start options, limited validation, and any required Ara changes are named.
No runtime dependency is chosen merely because a bundled skill recommends it.

### T02 Ara HTTP client

**Goal:** implement one reliable upstream client used by all tool handlers.

**Work:**

- Add a focused private Ara client and Resty-backed HTTP gateway with configured base
  URL, shared client, deadlines, context propagation, and bounded response reads.
- Validate configuration and route parameters. Decode Ara's wire format and
  problem responses, preserving useful upstream error context without credentials.
- Support required pagination and represent synchronous, accepted, and failed
  outcomes distinctly. Handle empty successful bodies correctly.
- Preserve opaque sequence bodies and upstream identifiers. Implement mutation
  idempotency/reconciliation only for the routes whose support T01 verifies.
- Separate GET retry/recovery from mutation dispatch; implement structured unknown/
  receipt-only outcomes and the per-operation tracking table without universal-job assumptions.
- Test request formation, status/body handling, malformed JSON, deadlines, and
  lost mutation responses using `httptest`.
- Instrument normalized request outcomes/latency and child spans; verify sanitized
  error context and request correlation without duplicate lower-layer error logs.

**Deliverables:** Ara client and focused HTTP contract tests, with no MCP-specific
transport logic in this package.

**Acceptance:** tests prove the observed upstream contract and uncertain outcomes
are not retried blindly or reported as completed. Default checks need no Ara server.
Request metrics and trace tests include decoding failures and deadlines.

### T03 Executable, stdio, and read-only tools

**Goal:** ship the first usable slice: an agent reading real Ara state over stdio.

**Work:**

- Add `cmd/ara-mcp/`, pin the official MCP SDK plus Cobra/Viper, and register a small
  shared tool set with typed generic SDK tool handlers.
  Keep executable wiring thin and implementation private.
- Implement Cobra commands/flags and Viper configuration for transport/Ara URL,
  help/version behavior, and supported diagnostics settings. Resolve explicit flags
  > environment > optional file > defaults into a validated typed configuration.
  Use isolated command/config instances, stderr diagnostics, signals, and bounded shutdown.
- Establish JSON `slog` records, configurable levels, tool metrics/spans, and a
  read-only adapter diagnostic tool per `observability.md`. External exporters
  remain optional and diagnostic listeners remain off unless configured.
- Introduce one paced process/runtime sampler for CPU, RSS/Go memory, goroutines,
  uptime, and available process statistics. Share it with diagnostics/metrics and
  test rate calculations, unavailable/warmup values, lifecycle, and low-cost collection.
- Start with server/equipment state and sequence list/detail tools. Validate schemas
  and map Ara errors to MCP tool errors with structured results.
- Add read-only `get_rig_context` for the selected profile/site/imaging/filter context
  and connected identities/capabilities. Initially require a rig set up in Ara's UI.
- Start local diagnostics/sampling with valid configuration even when Ara is offline.
  Gate mutations on reviewed API/build contracts; exercise unknown/incompatible versions.
- Add early platform cross-build checks and available native tests when executable/
  OS-specific code lands, rather than first discovering portability problems in T10.
- Test discovery and calls with the SDK's in-memory transport; add a process-level
  stdio smoke check that detects stray stdout logging.
- Add real build/run commands and one local-agent configuration example to the
  development guide.
- Record a release-build startup/idle and representative read-only footprint to
  establish the CPU/memory baseline before additional runtime features.

**Deliverables:** runnable stdio adapter, read-only tools, and working agent example.

**Acceptance:** an MCP client lists and calls the tools against a test Ara endpoint;
invalid arguments produce useful errors; stdout contains protocol frames only.
No control-slot claim is needed merely to inspect Ara.
Tests verify log level/redaction, request correlation, logical tool outcomes,
balanced in-flight gauges, and trace operation with export disabled.
RED/GREEN command tests prove precedence, false/zero values, missing/invalid config,
test isolation, useful help without an Ara connection, and no usage/banner pollution
of MCP stdout while serving.
Tests cover degraded startup/local monitoring, compatibility gating, and the first
real-Ara simulator integration recipe in addition to mock/SDK-in-memory checks.

### T13 Basic diagnostics HTTP foundation

**Goal:** make the application's local diagnostics useful without waiting for full
equipment control or HTTP MCP transport.

**Work:**

- Pin Chi v5/render and build the optional diagnostics listener, health/readiness/
  status/metrics routes, structured middleware logging, and shared request correlation.
- Implement loopback defaults and the selected separate Basic-auth/TLS remote access
  policy. Protect profiling when later enabled; invalid access configuration fails startup.
- Expose shared T03 sample/diagnostic data even while Ara is unavailable. Preserve
  proper liveness/degraded readiness and zero equipment/attention side effects.
- Establish streaming-safe wrappers/route policies for T12's SSE and T09's later SDK
  mount, with RED/GREEN router, denial/recovery, and cancellation checks.

**Deliverables:** diagnostics HTTP/router/access foundation, configuration/examples,
health/status/metrics, and integration tests. No equipment tool dependency is needed.

**Acceptance:** a stdio process can enable local HTTP diagnostics without polluting
stdout; valid configuration starts them while Ara is down. Probes/readers make no
mutations, structured logs/access work, and stream-capable response writers survive
the middleware chain. T12 and T09 extend this tested foundation.

### T04 Ara control ownership and connection lifecycle

**Goal:** implement the cooperative control phase and command arbitration before
introducing mutations, assuming no human UI writes during MCP control.

**Work:**

- Implement explicit begin/end control with a fresh local control ID, existing Ara
  handoff/session handling, and the configured-rig prerequisite. Saved-plan mutations
  and normal equipment/run actions require control; ending it does not stop a run.
- Implement the selected policy using Ara's connect/session/handoff contract and
  the session-bound WebSocket behavior required for holder liveness.
- Keep MCP connections separate from the single adapter identity. Trusted HTTP
  agents share authority but normal dispatch remains serialized; test collisions,
  active-run manual-action rejection, reserved interrupts, intent replay/conflicts,
  receipt-capacity behavior, and expected-run-ID checks.
- Handle denial, another controller, session expiry, reconnect, and shutdown.
  Do not silently exploit legacy REST access or continually signal human attention.
- Use only valid adapter-owned WS sessions. On expiry/restart, invalidate control
  and use read-only REST polling; require new explicit begin control. Never reconnect
  unbound, auto-reclaim a slot, release stale queued mutations, or auto-resume/start.
- Re-read identity/version/resume epoch and authoritative run/job state on recovery.
  Exercise server restart separately from network reconnect and preserve unknown/
  interrupted outcomes when evidence is lost.
- Expose connection/control state and confirmed heartbeat freshness; log loss and
  recovery without emitting a record for every heartbeat or changing user-attention rules.

**Deliverables:** documented ownership policy, the necessary connection/event
handling, and lifecycle tests.

**Acceptance:** a simulated human owner can retain control; a rejected handoff
cannot still result in a mutating request. Recovery and adapter shutdown leave
accepted Ara work intact, and connections/goroutines terminate cleanly.
No UI lock/server REST enforcement is claimed. Actual Ara integration verifies that
read-only monitoring/reconnects do not signal attention via unbound WS, that control
IDs expire correctly, and that overload cannot prevent supported stop/abort dispatch.
Connection metrics and diagnostic state distinguish denied ownership, a broken
WebSocket, and a healthy read-only adapter.

### T05 Sequence authoring

**Goal:** let an agent prepare a saved plan without starting equipment actions.

**Work:**

- Add create/update and validate tools, plus template listing/instantiation.
  Reuse list/detail tools from T03 for inspection and review.
- Accept the verified structured sequence body and preserve its `$type` tree,
  schema version, identifiers, conditions, triggers, and unknown data on round-trip.
- Preserve metadata but reject unsupported executable types/parameters against the
  verified palette; only supported bounded recipes/loops can be considered executable.
  Preflight required slots, filter references/capabilities, and fresh rig context
  before start. Ara's limited `/validate` remains an additional structural check.
- Exercise at least one real executable imaging template/body from the targeted
  Ara version; do not assume every advertised template is executable.
- Interpret validation's `valid`/`reason` result, not just HTTP 200. State the
  upstream check's limits and surface invalid or unsupported plans honestly.
- Provide an authoring recipe: inspect equipment/profile context, select a template
  or construct a body, validate, save, and read back for review in Ara.

**Deliverables:** sequence-authoring tools, verified example body/template,
round-trip tests, and an agent-facing recipe.

**Acceptance:** a plan can be saved and read back unchanged without calling start
or any equipment endpoint. Active-run update conflicts and invalid bodies return
useful errors. The recipe works with the ownership policy selected in T04.
Saved-plan mutation diagnostics contain correlation/sequence IDs, not sequence bodies.

### T06 Sequence execution

**Goal:** control the lifecycle of a saved sequence while Ara owns execution.

**Work:**

- Add explicit start, pause, resume, stop, abort, and run-state tools. Distinguish
  stop/abort semantics based on verified Ara behavior.
- Use sequence/run identifiers and return accepted results for asynchronous commands.
  Observe the authoritative state rather than assuming a requested transition occurred.
- Apply the control/intent/expected-run-ID rules, one-run policy, and per-tool status
  table. Test retries after terminal completion, lost responses, stale run-control,
  adapter/Ara restart, and unavailable correlation with structured unknown results.
- Expose recenter/refocus resume options only as supported by the chosen Ara baseline.
- Define behavior for repeated calls, missing sequences, existing runs, command
  timeouts, and cancellation after an operation has been accepted.
- Keep start separate from create/validate. Do not offer a hardware-free execution
  mode based on Ara's currently unused `DryRun` field.

**Deliverables:** execution tools and lifecycle/error tests against controlled Ara
responses, plus an end-to-end save/start/status recipe.

**Acceptance:** acceptance and completion are distinguishable; pause reports the
observed state; duplicate starts and uncertain responses do not create uncontrolled
replays. Agent disconnect does not trigger stop/abort.
Logs, counters, and traces keep acceptance, observed terminal results, and uncertain
mutation outcomes distinct; a returned tool-call span does not remain open all night.

### T07 Manual equipment tools

**Goal:** expose a focused set of manual actions through Ara, sharing its guards.

**Work:**

- Start with camera exposure/abort/cooling, mount slew/park/unpark, focuser move/
  autofocus, filter selection, and guider start/stop/dither where the API supports them.
- Define individual tool contracts with explicit units and capability requirements.
  Use existing device status to explain disconnected or unsupported operations.
- Route every action through the same Ara client and ownership policy. Preserve
  upstream busy/state errors when a sequence or another operation owns equipment.
- Reject conflicting manual mutations during active/paused runs; implement the
  verified mount-abort/emergency-stop interrupt tools outside normal saturation.
  Receipt-only device actions report accepted/observed state without invented causal completion.
- Return job identifiers for job-backed operations and accepted results where the
  upstream action completes asynchronously.
- Test important state conflicts and input boundaries with fake upstream responses.

**Deliverables:** the selected manual-action tools, API mappings, and behavior tests.

**Acceptance:** no direct AlpacaBridge route or arbitrary HTTP passthrough exists.
Units, capability errors, and sequence conflicts are visible to the agent; acceptance
is never presented as evidence that the mount/exposure/autofocus has finished.
Tool/upstream diagnostics correlate each action and represent capability/state
conflicts separately from adapter faults.

### T08 Progress, events, and image previews

**Goal:** let the agent inspect long-running work and captured results.

**Work:**

- Add job status and the frame list/detail/preview operations needed by the recipes.
  Return supported MCP image content with bounded payloads instead of embedding FITS files.
- Extend the T04 WebSocket connection to observe relevant sequence, equipment,
  job, and failure events. Follow Ara's version/heartbeat/resume contract.
- Outside a valid owned control session, use the shared read-only REST fallback.
  Do not create unbound subscriptions just to monitor; test their attention-free
  behavior against actual Ara and preserve explicit stale/unknown observations.
- Keep state queries useful without push-event support from the agent client.
  Use current Ara state after reconnect and distinguish stale observations.
- Expose MCP progress/resources only where SDK and client support make them useful;
  status tools remain the baseline. Do not build a local durable job database.
- Test event recovery, connection shutdown, failed jobs, missing previews, and
  response-size limits.
- Instrument event processing/freshness, reconnects, and any queues/dropped events.
  Deduplicate replayed terminal observations and keep identifiers out of metric labels.
- Bound preview bytes and per-consumer event/state retention; exercise slow clients
  and reconnects without unbounded memory growth or busy polling.

**Deliverables:** status/result tools, event integration, and monitoring examples.

**Acceptance:** an agent can identify pending, running, completed, and failed work,
retrieve a preview when available, and recover after an event gap without inventing
state or leaking background connections.
Heartbeat freshness is distinct from ordinary event activity; idle rigs do not
generate false outage reports or info-level frame/heartbeat noise.

### T09 Streamable HTTP deployment

**Goal:** serve the same adapter as a persistent MCP endpoint beside Ara.

**Work:**

- Extend T13's Chi foundation with the MCP listener, reusing its structured logging/
  access/streaming conventions and pinned Chi/render dependencies.
- Mount the official SDK's Streamable HTTP handler at the documented endpoint,
  preserving protocol methods/headers and JSON/SSE framing. Use the same MCP tool
  registration and handlers as stdio; render ordinary diagnostic payloads with typed
  Chi render responses, without re-encoding SDK messages or native metric/profile data.
- Compose Chi request-ID middleware, correlation/tracing, request logging/metrics,
  recovery, and access checks in the tested order. Bridge `RequestLogger`'s formatter
  hook to the shared JSON `slog` logger and recovery entry. Discard untrusted IDs,
  normalize routes, redact attributes, and emit one final request/fault summary.
- Implement selected bearer-auth/TLS MCP access with SDK primitives, configured
  Origin/Host behavior, and real supported-client examples. Diagnostics credentials
  are separate/read-only; all authenticated MCP agents share the operator authority.
- Apply the T04 control identity policy across concurrent MCP clients. An MCP session
  must not silently become an independent equipment controller.
- Handle HTTP disconnects, request cancellation, and service shutdown without
  changing accepted Ara operations.
- Preserve writer flushing and streaming through middleware. Scope short-route
  deadlines/content-type/compression policies appropriately; no blanket timeout
  or write deadline may kill a valid MCP stream or trigger a mutation retry.
- Add a remote-agent example and transport-parity/access tests.
- Instrument HTTP requests and enable configurable diagnostic `/healthz`, `/readyz`,
  and metrics serving on the separate loopback-default diagnostics listener.
  Test the access model and exporter failure/flush bounds.

**Deliverables:** Chi-based HTTP mode, render-backed diagnostic payloads, structured
middleware logs/metrics, documented access/configuration model, and parity tests.

**Acceptance:** both modes expose the same tool contracts and upstream behavior.
Network-facing control is configured with the selected access model; unauthorized
requests do not reach Ara. One client's disconnect does not break another's reads.
Diagnostic probes/scrapes never claim Ara control or perform mutations, and HTTP
protocol success is not counted as tool success when the tool result is an error.
RED/GREEN tests through the complete router prove middleware order, request ID
handling, 404/405/auth/fault diagnostics, render status/content type, and SDK SSE
flush/disconnect behavior beyond ordinary-route deadlines. Recovery after committed
headers produces a structured fault without appending a second error payload or
rewriting committed headers; the SDK client observes stream termination/failure.

### T12 Resource dashboard and exports

**Goal:** monitor the application's own resources through a self-hosted live page
and downloadable CSV/JSONL statistics, within the small-SBC budget.

**Work:**

- Use T03's shared sampler and T13's listener; add byte/count/age-bounded history with explicit
  retained range/instance metadata. Select the measured cadence/limits with Viper
  configuration and typed validation.
- Serve read-only HTML and pinned local Datastar assets/Go SDK on the diagnostics
  listener. Verify the frontend/SDK version pairing and licensing. No runtime CDN,
  Node, or external collector is required.
- Implement dedicated dashboard SSE for initial/current state and ongoing updates,
  with reconnect/freshness indicators and bounded subscriber lifecycle/backpressure.
  Keep it separate from Ara WebSocket and MCP SSE/protocol messages.
- Add finite CSV and JSONL download endpoints with stable schema/units, proper
  attachment headers, validated range filters, and bounded consistent snapshots.
  Stream rows without locking/stalling the sampler; handle unavailable values,
  empty history, evicted ranges, and cancellation explicitly.
- Add the opt-in bounded rotating JSONL archive and retained archive export path for
  postmortem samples. Test restart, rotation/quota, write failure, full disk/queue,
  bounded flushing, explicit gaps, and sampling/control continuing after archive failure.
- Apply diagnostics access policy to page/feed/downloads. Use browser-compatible
  remote auth without URL credentials, and retain structured middleware logging.
- Use RED/GREEN tests from `resource-dashboard.md`, plus a browser smoke check for
  automatic updates, offline/local assets, readable units, freshness, and downloads.

**Deliverables:** shared retained resource data, a self-hosted live dashboard,
CSV/JSONL downloads, configuration/access docs, and behavior/browser checks.

**Acceptance:** the application reports its own CPU, memory, live goroutine and
related usage with honest units/availability/freshness. The page self-updates;
both downloadable formats parse and match the retained ordered sample snapshot.
Multiple/slow viewers or exports do not multiply collectors, stall sampling,
grow unbounded buffers, or change Ara state. Stdio remains protocol-only.
All [dashboard acceptance criteria](resource-dashboard.md#acceptance-criteria) hold;
T10 measures the feature's sustained resource cost on the intended SBC target.

### T10 Deployment and end-to-end validation

**Goal:** demonstrate both intended deployments with reproducible evidence.

**Work:**

- Build the executable for selected local-agent platforms and Linux ARM64. Add
  platform CI coverage where it provides real build/test signal.
- Validate a representative lowest-resource SBC target, including a Pi 3-class
  deployment where feasible. Record CPU, RSS/heap/GC, connections/goroutines,
  backlog, latency, and cleanup under sustained traffic, large allowed previews,
  outages/reconnects, slow clients, and optional telemetry. Account for co-located
  imaging workloads and document measured budgets/defaults and compatibility limits.
- Add a minimal non-root systemd service example and deployment instructions for
  HTTP mode, including endpoint/access configuration and graceful restart behavior.
- Enable archival in the persistent-service example's dedicated data directory;
  document first-release credentials/TLS, end-control-before-update, interrupted/
  unknown recovery behavior, and previous-binary/config rollback.
- Run hardware-free save/read/start/status/failure flows through both transports.
- Define and run opt-in live checks on the chosen Ara version, including a local
  stdio client and an HTTP service beside Ara. Record human-UI handoff behavior.
- Record simulator versus physical-rig evidence, versions, equipment if used,
  adapter commit, operations, and results. Update supported claims accordingly.
- Add opt-in protected native profiling, runtime/process measurements, and a compact
  monitoring recipe. Verify log retrieval/retention, optional scrape/OTLP export,
  failure queries, clean telemetry shutdown, and overhead on Linux ARM64.
- Validate resource sampling, live dashboard SSE, and CSV/JSONL downloads with
  multiple/slow viewers, retained-history limits, simultaneous exports, and blocked
  external networking. Record sampler/UI/export overhead and cleanup on an SBC.

**Deliverables:** deployment guide, reproducible end-to-end checks, and a compact
compatibility/validation record. Add these documents when the workflows exist.

**Acceptance:** documented commands reproduce the tested deployments. Real Ara
validation covers sequence authoring/execution and representative manual actions;
disconnect/reconnect preserves authoritative run state. A cross-build alone does
not earn a hardware-supported claim.
Operators can distinguish process liveness, Ara reachability, control availability,
and actual run state without a mandatory telemetry backend.
Resource-limit tests and target measurements demonstrate bounded steady-state
behavior and headroom; neither a desktop benchmark nor cross-compilation alone
establishes Pi 3/4/5 suitability or a minimum-memory recommendation.

### T11 First release preparation

**Goal:** make the verified implementation installable and understandable.

**Work:**

- Reconcile the README, tool reference, configuration examples, plan status, and
  changelog with actual implemented behavior and recorded compatibility.
- Add binary archives/checksums for the validated targets and a source-build path.
  Start with standard Go builds; add packaging automation only when it reduces real work.
- Document licenses/third-party notices and how network users can obtain the
  corresponding source. Keep credentials and local configuration out of artifacts.
- Choose a release version and release procedure once the scope is verified.

**Deliverables:** release-ready docs, build/artifact procedure, and release notes.

**Acceptance:** a fresh user can install, connect an agent in either supported mode,
and follow the validated sequence recipe. Published capability claims match the
tests and live evidence. Creating tags/releases remains a separately requested action.

## Outstanding verification

The canonical owner/evidence table is
[O1–O7 in the first-release policy](first-release-policy.md#outstanding-verification).
Control, framework, access, retention, and provisional limit choices are resolved;
their implementation/integration evidence is not. Task owners close each item only
with recorded results and update supported claims accordingly.

In particular, Ara baseline/client compatibility, exact operation correlation and
global run/restart evidence, executable palette/preflight inputs, Datastar version
pairing, and SBC measurements remain open. Read-only observer WS enhancement is
non-blocking because REST polling outside control is the selected fallback.

Resolve a decision in the task that needs it. If it has lasting tradeoffs, add a
short decision record under `docs/` and link it here and from the owning guidance.
Do not create empty decision, failure, deployment, or API documents in advance.

## Completion and maintenance

- Keep task IDs stable. Use `Pending`, `In progress`, `Blocked`, or `Complete` in
  the task list, with a short reason for blocked work.
- Update status only after deliverables and acceptance criteria are satisfied.
  Link exact check results, the relevant commit/PR when available, and live evidence.
- If a task exposes an upstream Ara gap, name it and link an issue when one exists.
  Continue independent tasks rather than inventing unsupported behavior.
- Keep verification inside implementation tasks; T10 adds deployment evidence,
  not the first tests for everything written earlier.
- A runtime feature must satisfy the relevant [observability acceptance criteria](observability.md#acceptance-and-delivery)
  before its task is marked complete.
- When capabilities land, update the README and tool reference. This plan is the
  roadmap; it is not a substitute for accurate user-facing documentation.
