# Architecture and Ara API notes

## Responsibilities

ara-mcp is an MCP adapter to Ara server. The AI agent interprets the user's intent;
the adapter exposes explicit tools and translates their arguments to Ara API
requests. Ara owns equipment coordination, sequence persistence and execution,
image storage, and imaging-session state. AlpacaBridge supplies hardware drivers.

Both stdio and Streamable HTTP use the same tool handlers and Ara client. Select
one transport per process. Stdio is for agent-launched local use; HTTP is for a
persistent service, commonly on the telescope computer. HTTP deployment must
address authentication, Origin validation, and client ownership before accepting
network-facing control requests.

Use the official [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk).
Use Cobra for application commands and Viper for configuration, with typed,
validated configuration passed into runtime components. The Ara client uses
[Resty v2.17.2](https://github.com/go-resty/resty/tree/v2.17.2) behind
`internal/ara/http_gateway.go`. The Ara client exposes the request/outcome contract;
the gateway owns HTTP execution, status/body decoding, bounded reads, retry policy,
and request instrumentation.
Use Go's standard library for JSON and logging where it covers the requirement.
Use Chi and its middleware for HTTP serving, and Chi render for ordinary HTTP
payloads as described below. A WebSocket dependency can be selected when event
integration is implemented. The MCP server and transports are not implemented yet.

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
| HTTP routing and composition | `github.com/go-chi/chi/v5` |
| Request IDs, recovery, and route-scoped middleware | Chi's `middleware` package |
| Ordinary diagnostic/status/error payloads | `github.com/go-chi/render` with typed payloads |
| Resource dashboard | Locally served Datastar frontend, official Go SDK, and dedicated SSE stream |
| Resource downloads | Bounded CSV/JSONL snapshots encoded with their native Go encoders |
| MCP negotiation, JSON-RPC, and JSON/SSE framing | Official MCP Go SDK Streamable HTTP handler, mounted into Chi |
| Structured request/fault logs | Chi `RequestLogger` formatter hook backed by the shared native `log/slog` logger |

Both the MCP listener and optional separate diagnostics listener use Chi routers.
Chi render handles ordinary application payloads; it does not encode SDK MCP
responses, dashboard SSE, CSV/JSONL exports, metrics, or pprof data. Middleware
preserves streaming/flush behavior and distinguishes HTTP request lifetime from
logical tool-call latency.

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
| Centering and solving | `POST /platesolve/center`; `POST /platesolve/frames/{id}/solve` |
| Background jobs | `GET/DELETE /jobs/{id}` |
| Server state | `GET /server/info`, `/server/state` |
| Control session | `POST /server/connect`, `/server/disconnect`; `GET /server/session` |
| Events | WebSocket `/ws`; `GET /ws/catalog` |
| Images and settings | `/frames`, `/sessions`, `/profile`, `/profiles` |

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

Evidence: [validator](https://github.com/open-astro/openastro-ara/blob/34b59e6de/OpenAstroAra.Server/Services/SequenceSchemaValidator.cs),
[executor](https://github.com/open-astro/openastro-ara/blob/34b59e6de/OpenAstroAra.Server/Services/SequencerService.cs),
[client sessions](https://github.com/open-astro/openastro-ara/blob/34b59e6de/OpenAstroAra.Server/Services/ClientSessionService.cs),
and [WebSocket handling](https://github.com/open-astro/openastro-ara/blob/34b59e6de/OpenAstroAra.Server/Endpoints/WebSocketEndpoints.cs).
