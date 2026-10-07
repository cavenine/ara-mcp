# Architecture and Ara API notes

## Responsibilities

ara-mcp is an MCP adapter to Ara server. The AI agent interprets the user's intent;
the adapter exposes explicit tools and translates their arguments to Ara API
requests. Ara owns equipment coordination, sequence persistence and execution,
image storage, and imaging-session state. AlpacaBridge supplies hardware drivers.

Both stdio and Streamable HTTP use the same tool handlers and Ara client. Select
one transport per process. Stdio is for agent-launched local use; HTTP serves the
SDK endpoint at `/mcp` for a persistent service, commonly on the telescope computer.
HTTP requires a configured bearer token, applies same-origin/allowed-Origin checks
and the SDK's localhost Host protection, and gives authenticated clients the same
shared Ara control identity.

Use the official [MCP Go SDK v1.8.0](https://github.com/modelcontextprotocol/go-sdk)
for the stdio and Streamable HTTP transports, read tools, T04 control phase, and T05
saved-plan authoring. Use Cobra for application commands and Viper for configuration,
with typed, validated configuration passed into runtime components. The Ara client
uses [Resty v2.17.2](https://github.com/go-resty/resty/tree/v2.17.2) behind
`internal/ara/http_gateway.go`. `internal/ara.Client` owns endpoint paths and Ara
request/response contracts; MCP tools call its operation-specific methods. The
generic gateway owns HTTP execution, status/body decoding, bounded reads, retry
policy, and request instrumentation without knowing Ara endpoint semantics.
Use Go's standard library for JSON and logging where it covers the requirement.
Use Chi and its middleware for HTTP serving, and Chi render for ordinary HTTP
payloads as described below. T04 uses `github.com/coder/websocket` v1.8.12 for
Ara's session-bound control socket; it is not an unbound monitoring connection.

See the [implementation plan](plan.md) for delivery tasks and the
[development guide](development.md) for setup and verification commands.
The [observability requirements](observability.md) define how logs, metrics,
traces, and diagnostics make adapter behavior visible in both transports.
T01's source-reviewed initial schemas and evidence gaps are tracked in
[api-contracts.md](api-contracts.md); they are not release compatibility claims.
Resolved operator/control, operation/recovery, authentication, dashboard, and initial
resource-limit choices are in [first-release-policy.md](first-release-policy.md).

## Deployment targets and resource constraints

ara-mcp is intended for **small SBCs such as Raspberry Pi 3, 4, and 5**, including
lower-memory configurations. Telescope-side builds target Linux ARM64 with a
64-bit OS; local-agent builds also target desktop platforms. Board targets concern
this adapter, not a guarantee that Ara and every other rig service can run on the
same board. Co-located deployment must account for their combined CPU/RAM demand.

The resource contract is:

- Keep the adapter thin: coordinate through Ara rather than duplicating image
  processing, plate solving, sequence execution, or local AI inference.
- Bound concurrent calls/streams, retained operation/session state, JSON payloads,
  preview bytes, event queues, and telemetry buffers. Byte limits matter as well
  as item counts. Reject or backpressure excess work explicitly; never silently
  truncate a plan, lose a terminal failure, or retry an uncertain mutation.
- Reuse the Ara HTTP transport and shared event connection. Pace status polling
  and reconnect attempts; outages and slow consumers must not cause tight loops,
  per-client polling fan-out, or unbounded goroutine/connection growth.
- Avoid retaining whole FITS files or unnecessary image/JSON copies. Release
  completed work and expired state, and test payload/queue limits and cleanup.
- Keep logs/metric labels bounded, trace export optional, and profiling off by
  default. Instrumentation must fit the SBC workload while preserving diagnostics
  and protocol/correctness requirements.
- Measure startup/idle and representative busy workloads, including reconnects,
  slow clients, previews, and optional telemetry. Set defaults/resource budgets
  from target-board evidence with co-located headroom, not desktop throughput.

No minimum RAM or CPU budget is validated yet. T03 records an initial executable
baseline; T10 validates the lowest-resource intended deployment and documents
measured recommendations. See [the validation guide](development.md#small-sbc-validation).
Use `golang-benchmark` for measurement and `golang-performance` for evidenced
optimizations; add only the caching/pooling/parallelism the measurements justify.

## HTTP stack

| Responsibility | Selected implementation |
| --- | --- |
| Outgoing Ara REST calls | Resty v2.17.2 in the private Ara HTTP gateway |
| HTTP routing and composition | `github.com/go-chi/chi/v5` v5.3.2 |
| Request IDs, recovery, and route-scoped middleware | Chi's `middleware` package |
| Ordinary diagnostic/status/error payloads | `github.com/go-chi/render` v1.0.3 with typed payloads |
| Prometheus diagnostics exposition | OpenTelemetry Prometheus exporter v0.69.0 with a private registry |
| Resource dashboard | Locally served Datastar frontend, official Go SDK, and dedicated SSE stream |
| Resource downloads | Bounded CSV/JSONL snapshots encoded with their native Go encoders |
| MCP negotiation, JSON-RPC, and JSON/SSE framing | Official MCP Go SDK Streamable HTTP handler, mounted into Chi |
| Structured request/fault logs | Chi `RequestLogger` formatter hook backed by the shared native `log/slog` logger |
| MCP HTTP access | SDK bearer-token middleware plus Go `CrossOriginProtection`; SDK localhost Host protection remains enabled |

Both the MCP listener and optional separate diagnostics listener use Chi routers.
Chi render handles ordinary application payloads; it does not encode SDK MCP
responses, dashboard SSE, CSV/JSONL exports, metrics, or pprof data. Middleware
preserves streaming/flush behavior and distinguishes HTTP request lifetime from
logical tool-call latency.

T13's diagnostics listener is disabled by default and independent of MCP transport.
It exposes read-only `/healthz`, `/readyz`, `/status`, and Prometheus `/metrics`
routes, sharing T03's sampler and telemetry instruments. Loopback is the default
access boundary. Remote binds require separate Basic-auth credentials and direct
TLS certificate/key configuration; Ara control credentials are never reused.

Chi's default request logger writes plain text to stdout. The selected integration
uses its existing `LogFormatter`/`LogEntry` extension point for structured JSON
on stderr, with `Recoverer` using the same entry's panic hook. Request/fault
correlation, redaction, route normalization, and middleware order are covered by
the [HTTP project rules](../.agents/rules/ara-mcp.md#http-routing-and-middleware) and
[observability requirements](observability.md#http-middleware-and-streaming).

Sources: [Chi router/middleware](https://github.com/go-chi/chi),
[RequestLogger hooks](https://github.com/go-chi/chi/blob/master/middleware/logger.go),
[recovery behavior](https://github.com/go-chi/chi/blob/master/middleware/recoverer.go),
[Chi render](https://github.com/go-chi/render), and the
[SDK handler/middleware example](https://github.com/modelcontextprotocol/go-sdk/blob/main/mcp/streamable_example_test.go).
Pin released versions with T09 implementation and verify against those versions;
upstream examples are API references, not a middleware stack to copy wholesale.

## Resource dashboard and exports

One application-owned sampler monitors ara-mcp's process CPU, RSS and Go memory,
goroutines, uptime, and relevant active work. The optional diagnostics listener
serves a self-updating SSE page and downloads the same retained series as CSV or
JSONL. Datastar with its Go SDK is the selected dashboard framework. A bounded
in-process history, optional rotating JSONL archive, and bounded subscriptions/export snapshots
keep it suitable for Pi 3/4/5-class boards; it works without a telemetry backend.
See [the resource dashboard contract](resource-dashboard.md) for units, availability,
retention, framework/asset choices, access boundaries, and verification.

## Ara operations available to wrap

The following routes were inspected in
[openastro-ara at commit 6374eede73383851486e6fb498a3311a3be58d82](https://github.com/open-astro/openastro-ara/tree/6374eede73383851486e6fb498a3311a3be58d82).
These notes are source inspection, not broad release validation. The checkout
reports scaffold tier; in-memory built-in templates are placeholders, while
packaged sequence templates and selected camera/telescope/filter-wheel routes were
exercised only with simulated devices. See the
[T01 evidence notes](api-contracts.md#evidence-baseline). Recheck handlers and
service wiring against the Ara version being targeted.

All paths below start with `/api/v1`:

| Area | Existing routes |
| --- | --- |
| Sequence library | `GET/POST /sequences`; `GET/PATCH/DELETE /sequences/{id}` |
| Validation | `POST /sequences/validate` |
| Templates | `GET /sequences/templates`; `POST /sequences/templates/{name}/instantiate` |
| Execution | `POST /sequences/{id}/start`, `/pause`, `/resume`, `/stop`, `/abort`, `/skip-current` |
| Run state | `GET /sequences/{id}/state` |
| Live edits | `POST/DELETE /sequences/{id}/run/items`; `POST /sequences/{id}/run/items/move` |
| Device status and connections | `/equipment/{type}` and the type's discovery/connect/disconnect routes |
| Manual imaging | `POST /equipment/camera/exposure`, `/equipment/camera/exposure/abort`, `/equipment/camera/cooler` |
| Mount actions | `POST /equipment/telescope/slew`, `/park`, `/unpark`, `/home`, `/abort`, `/tracking` |
| Focus and filters | `POST /equipment/focuser/move`, `/equipment/focuser/autofocus`, `/equipment/filterwheel/change` |
| Guider actions | `POST /equipment/guider/start`, `/stop`, `/dither?pixels=` |
| Centering and solving | `POST /platesolve/center`; `POST /platesolve/frames/{id}/solve` |
| Background jobs | `GET/DELETE /jobs/{id}` |
| Server state | `GET /server/info`, `/server/state` |
| Control session | `POST /server/connect`, `/server/disconnect`; `GET /server/session` |
| Events | WebSocket `/ws`; `GET /ws/catalog` |
| Images and settings | `/frames`, `/sessions`, `/profile`, `/profiles` |

T08 wraps the verified `GET /jobs/{id}`, cursor-paged `GET /frames`,
`GET /frames/{id}`, and `GET /frames/{id}/thumbnail` routes in `internal/ara.Client`.
Frame details omit Ara's server-local FITS path.
Job state is in-memory and disappears on Ara restart. Thumbnail content is JPEG and
is capped at 1 MiB before it reaches MCP; Ara may return its placeholder JPEG if the
catalog entry's FITS file is missing. The implementation uses the real thumbnail
route, which reads/writes Ara's bounded sidecar cache when available.

The event reader extends only T04's session-bound WebSocket. It retains at most 128
events and 1 MiB, deduplicates by Ara's monotonically increasing `seq`, resumes from
the last observed sequence after reconnect, and reports an explicit gap after an
expired resume cursor, sequence discontinuity, or buffer eviction. Retained records
contain bounded IDs, sequence state/progress, failure summaries, and selected bounded
event-family contexts; raw event JSON and FITS/image bytes are not retained. An idle stream remains
healthy when the Ara heartbeat is fresh. Without owned control, ara-mcp makes no
WebSocket connection; job/frame/sequence tools read current state over REST.
`get_recent_ara_events` reports socket availability/freshness and retained gap/drop
state; callers reconcile gaps with current Ara REST state rather than treating
retained events as a durable journal. The bounded projection includes camera exposure
lifecycle timing, guider step measurements/session markers, and autofocus probe/fit/run
details where Ara supplies them; unsupported/raw payload fields and image bytes are
discarded. The event contracts were additionally source-reviewed against
[openastro-ara commit `29f72ea2246a1343a60724361d271912610b3503`](https://github.com/open-astro/openastro-ara/tree/29f72ea2246a1343a60724361d271912610b3503), specifically its
[WebSocket event catalog](https://github.com/open-astro/openastro-ara/blob/29f72ea2246a1343a60724361d271912610b3503/OpenAstroAra.Server/Contracts/WsEvents/WsEventCatalog.cs)
and event publishers. Decoder/retention tests are contract evidence, not a claim that
each physical event source was exercised on hardware.

The newer source-reviewed Ara surface at that commit also supplies the T15–T18
integrations: autofocus state/frame/cancel/calibration at `/autofocus/*`, retained
fault list/detail at `/faults`, guide-camera focus lease endpoints at
`/equipment/guider/focus*`, and saved-frame solve/coordinate center at
`/platesolve/*`. The centering route returns an asynchronous `center` job; the existing
`/jobs/{id}` reader supplies its outcome. See the task-specific entries in
[`plan.md`](plan.md#task-list) for adapter limits and verification.

Ara also exposes guider, rotator, dome, switch, flat-device, calibration, mosaic,
polar-alignment, diagnostics, and other operations. Select a useful tool surface
instead of turning every REST route into an MCP tool automatically.

Sources: [sequence endpoints](https://github.com/open-astro/openastro-ara/blob/34b59e6de/OpenAstroAra.Server/Endpoints/SequenceEndpoints.cs),
[equipment endpoints](https://github.com/open-astro/openastro-ara/blob/34b59e6de/OpenAstroAra.Server/Endpoints/EquipmentEndpoints.cs),
and [OpenAPI snapshot](https://github.com/open-astro/openastro-ara/blob/34b59e6de/OpenAstroAra.Server/openapi.yaml).

## Contract details to preserve

- **Sequence bodies:** the executable JSON is a NINA-style `$type` tree with
  `schemaVersion: "openastroara-sequence-v1"`. Ara does not expose a natural-language
  sequence-planning endpoint. Keep upstream wire field names and units intact.
- **Validation:** `/sequences/validate` checks only object shape, schema version,
  and an instruction-count/reachability condition. Equipment resolution, filter
  lookup, infinite-loop detection, and capability matching are explicitly deferred.
  Passing this check does not establish that a plan will run on the rig.
- **Start options:** the inspected start DTO includes `DryRun`,
  `StartFromInstructionIndex`, and `ContinueOnRecoverableErrors`, but the real
  executor does not use them. Never advertise an equipment-free dry run based on
  this DTO alone.
- **Asynchronous work:** HTTP 202 means accepted, not completed. Completion comes
  from the applicable run state, job, or WebSocket event. Cancellation of an MCP
  request does not itself mean Ara cancelled an accepted operation.
- **Editing:** ordinary sequence updates/deletion are refused during an active
  run. Dedicated live-edit routes act on pending items only.
- **Control ownership:** Ara's single-client handoff uses `/server/connect` and
  a session-bound WebSocket. Legacy REST access remains possible without claiming
  the slot. The operator workflow assumes no UI mutations during MCP control and
  one controlling adapter; use Ara's existing handoff without claiming a new
  server-enforced REST lock. See the [resolved control policy](first-release-policy.md#cooperative-control).
- **Attention and disconnects:** Ara counts certain connects and commands as user
  activity. Automated reconnects must not continually impersonate human attention.
  Adapter/agent shutdown must not automatically stop an imaging sequence. On
  reconnect, read Ara's current state rather than trust stale local state.
  Use REST polling outside a valid owned session, because unbound WS upgrades count
  as user activity. Do not automatically reclaim an expired session.
- **Retries:** use upstream idempotency support where actually implemented. A lost
  response to a mutation is an uncertain outcome; do not blindly repeat it.
- **Control WebSocket:** the current Ara client binds `/api/v1/ws` with the
  adapter-owned `X-Ara-Session` capability and `X-Ara-WS-Version: 1`. Ara's JSON
  `ping` requires a JSON `pong` message (separate from RFC WebSocket control frames);
  takeover requests are rejected to preserve the current control phase. On network
  loss, the adapter re-reads server identity, profile, session liveness, resume cursor,
  and the bounded sequence page before reclaiming the same session ID. A changed
  daemon identity/build/profile or expired/rejected session invalidates the local
  control ID; a new claim then requires an explicit tool call. T08 consumes sequenced
  events only on this owned socket and marks expired replay/buffer loss for REST
  reconciliation. Ara O2 still prevents a guaranteed global active-run scan.

Evidence: [job endpoint](https://github.com/open-astro/openastro-ara/blob/6374eede73383851486e6fb498a3311a3be58d82/OpenAstroAra.Server/Endpoints/JobsEndpoints.cs),
[image endpoints](https://github.com/open-astro/openastro-ara/blob/6374eede73383851486e6fb498a3311a3be58d82/OpenAstroAra.Server/Endpoints/ImageEndpoints.cs),
[frame repository](https://github.com/open-astro/openastro-ara/blob/6374eede73383851486e6fb498a3311a3be58d82/OpenAstroAra.Server/Services/SqliteFrameRepository.cs),
[event catalog](https://github.com/open-astro/openastro-ara/blob/6374eede73383851486e6fb498a3311a3be58d82/OpenAstroAra.Server/Contracts/WsEvents/WsEventCatalog.cs),
[validator](https://github.com/open-astro/openastro-ara/blob/34b59e6de/OpenAstroAra.Server/Services/SequenceSchemaValidator.cs),
[executor](https://github.com/open-astro/openastro-ara/blob/34b59e6de/OpenAstroAra.Server/Services/SequencerService.cs),
[client sessions](https://github.com/open-astro/openastro-ara/blob/34b59e6de/OpenAstroAra.Server/Services/ClientSessionService.cs),
and [WebSocket handling](https://github.com/open-astro/openastro-ara/blob/34b59e6de/OpenAstroAra.Server/Endpoints/WebSocketEndpoints.cs).
