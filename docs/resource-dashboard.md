# Resource monitoring, dashboard, and exports

**Status:** T12 implementation complete: bounded live/archive history, local Datastar
page/SSE feed, newest-first gauges/charts and Ara event table, and filtered live/archive
CSV/JSONL exports. Exports report retained/
exported ranges and bounded export/subscriber metrics. T10 verified the remote
Basic-auth/TLS browser path and recorded initial RPi4 measurements; representative
imaging-load and trusted-certificate/reverse-proxy validation remain open. RPi 3 is
untested and excluded from support claims.

The local Chromium smoke confirmed automatic sample advancement and parseable
live/archive CSV/JSONL responses without any external asset requests. T10's remote
Chromium run loaded the dashboard, local asset, and SSE directly from the RPi4 over
HTTPS with Basic auth. It used a temporary self-signed test certificate accepted in
the browser, not a production trust chain.

The page serves the MIT-licensed Datastar v0.21.4 browser bundle locally and emits
`datastar-merge-fragments` SSE patches through Datastar Go SDK v1.2.2's SSE writer.
This stable pair was smoke-tested in Chromium with local networking only. The old
wire event is intentional: the stable browser bundle handles it; the Go SDK's newer
`PatchElements` helper emits the different v1-beta event name. The remote test and
board measurements are recorded in [T10](plan.md#t10-deployment-and-end-to-end-validation);
they do not establish Pi 3 or physical-imaging suitability.
See [T12 evidence](plan.md#t12-resource-dashboard-and-exports).

The application monitors its own process/runtime usage and displays it on a
self-hosted, automatically updating page. Statistics are downloadable as **CSV**
and **JSONL**. The feature works without Prometheus, an OTLP collector, Grafana,
or an internet connection, within the [small-SBC budget](architecture.md#deployment-targets-and-resource-constraints).

## Table of contents

- [Resource sample contract](#resource-sample-contract)
- [Sampling and bounded history](#sampling-and-bounded-history)
- [Optional bounded disk archive](#optional-bounded-disk-archive)
- [Self-hosted live dashboard](#self-hosted-live-dashboard)
- [Ara server events](#ara-server-events)
- [CSV and JSONL downloads](#csv-and-jsonl-downloads)
- [HTTP and access boundaries](#http-and-access-boundaries)
- [Acceptance criteria](#acceptance-criteria)

## Resource sample contract

Use one typed sample model, with a schema version, application-instance identifier,
monotonic sample sequence, and UTC timestamp. T01 defines the exact field names,
units, supported-platform availability, and sampling/retention defaults.
Apply the selected [first-release defaults](first-release-policy.md#dashboard-history-and-provisional-limits).

| Required measurement | Meaning |
| --- | --- |
| Process CPU usage | CPU consumed over the sample interval; 100% means one logical core, with user/system counters and logical CPU context where available |
| Resident memory | Process RSS in bytes, kept distinct from Go heap/reserved memory |
| Go memory | Heap allocation/live usage and relevant runtime allocation/GC counters, in bytes or clearly identified cumulative units |
| Goroutines | Current live goroutine count, not an assertion that each is actively executing |
| Process uptime | Time since the current application instance started |
| Active work | Current in-flight tool/HTTP work, active streams/connections, and sampler/subscriber backlog where implemented |
| Additional platform statistics | Threads, file descriptors, and related process statistics where supported, with explicit availability |

Monitor the adapter, not the machine-wide CPU average or other rig processes.
Resource support is required on the Linux ARM64 SBC target; other platforms must
report unsupported measurements explicitly. Distinguish unavailable, first-sample
warmup, stale values, and a real numeric zero. The UI shows the last sample time
and freshness; unavailable values export as JSON `null` or empty CSV fields.

CPU rate uses elapsed time and counter deltas, not cumulative CPU time mislabeled
as a percentage. Account for startup, counter resets, and clock changes. The selected
denominator is one logical core: a multi-core process can exceed 100%.
Go heap statistics must not be presented as RSS.

Prefer native runtime/OS measurements where they cover the requirement. Evaluate
and pin a process-metrics package only if it simplifies needed platform support.
Use a controllable clock/source boundary for deterministic tests.

## Sampling and bounded history

- Run one application-owned sampler at a configurable paced interval, independent
  of dashboard viewers, metric scrapes, or export requests. It has explicit stop/
  wait behavior; each viewer must not create another collector or polling loop.
- Share the latest sample with MCP diagnostics, the dashboard, and metric adapters.
  Reuse values rather than repeatedly inspecting the process for each consumer.
- Retain a rolling history bounded by sample count, bytes, and age. Publish the
  configured interval, retained range, eviction behavior, and current instance.
  Initial defaults are a 2-second interval and at most 1 hour / 1,800 samples /
  2 MiB, whichever history bound is reached first; T10 validates SBC headroom.
- Resolve and validate sample interval, history count/age, subscriber count, and
  export concurrency through Viper. Samples are fixed-size values, so the validated
  1,800-sample ceiling also keeps their history within the 2 MiB budget.
- Bound subscribers and export concurrency. A slow browser may receive coalesced
  latest-state updates or be disconnected according to a documented policy; it
  cannot stall sampling or grow an unbounded queue. Retained export history stays
  independent of the samples that happened to reach a browser.
- A sampler failure records a bounded diagnostic and marks the affected values
  unavailable/stale. It neither claims Ara control nor stops/retries equipment work.

Live history covers the current application's retained in-process window. The
dashboard/downloads identify it; archived history is separately identified when
enabled. Neither source promises data before recording or after eviction/rotation.

## Optional bounded disk archive

When `resource-archive-dir` is configured, the sampler writes opt-in rotating JSONL
resource history for postmortem samples. General CLI/stdio starts memory-only unless
configured. Limits are four 8 MiB segments (32 MiB total), a 64-sample / 128 KiB
writer queue, periodic flush within 5 seconds, and bounded shutdown flushing.
Segment files are created with mode `0600` using fixed names; startup trims excess
segments and repairs a partial trailing record before appending. A write/flush error
disables only the archive and logs a bounded failure event; live sampling continues.

Retained completed records survive restart and carry instance/schema/sample
identity. Archive CSV/JSONL exports use a fixed-size file-handle snapshot and stream
rows without loading the 32 MiB retained ceiling into memory. Response headers expose
retained/exported ranges and archive gaps. `X-Resource-Archive-Gap-Count` counts both
malformed rows and missing sample-sequence spans; `X-Resource-Archive-Corrupt-Records`
counts malformed rows. Corrupt rows are skipped, and a partially written final row is
truncated at restart. Queue drops and write failures are logged with bounded categories.
A crash may lose recent buffered samples; this is bounded best-effort resource
history, not a zero-loss journal. Queue/full-disk/write failures degrade archival
without blocking sampling, dashboard reads, or equipment control.

Use fixed names, confined paths, native JSONL encoding, and no database. Bound
snapshots/file readers/export concurrency and preserve fixed completed-record
offsets during download; rotation/append cannot change the selected snapshot midway.
Test restart, quota/rotation, partial-record recovery, schema handling, slow disks,
writer failure, and sampling continuing while archival is unavailable.

## Self-hosted live dashboard

Serve a read-only page and its assets from the optional Chi diagnostics listener.
It can be enabled alongside either stdio or HTTP MCP mode; stdio still starts
without an HTTP listener unless explicitly configured. Show CPU, RSS/Go memory,
goroutines, uptime, active work, and freshness with clearly labeled units.
Use accessible HTML, readable numbers/tables, and bounded trend history; updates
must not steal focus or conceal a disconnected/stale state.

Use **SSE** for automatic updates. A new subscription receives current state and
continues with new samples. Reconnect from the latest available state, identify
restarts/gaps explicitly, and release subscriptions when the browser disconnects.
Each patch carries an SSE ID of `instance_id:sample_sequence`; after reconnect, a
restart or skipped/coalesced sequence updates the freshness message to identify that
the latest sample was reconciled rather than replaying fabricated intermediate data.
The dashboard SSE stream is separate from both Ara's WebSocket and MCP's SSE.

### Ara server events

The event table shows up to 50 events, newest first, from the adapter's existing
owned-session socket. Rows show Ara's event time/type, equipment type/ID/name, state,
fault/action/progress context, exposure timing, guider measurements/session markers, and
autofocus probe/fit/results where supplied. It does not open an observer WebSocket.
Events appear while a control session is active; the last rows remain visible after
release. Reconnect or retention gaps and the dropped-event count are shown. Treat event
rows as evidence of what Ara reported, not proof an operation completed; use Ara's
sequence/job/frame state tools for current state.

The diagnostics SSE carries a bounded initial event snapshot and then only newly
received records. It checks the existing in-memory event buffer at the shared sampler
cadence (2 seconds by default); it adds no background poller or Ara connection. Event
raw payloads and image data are not exposed; only the adapter's bounded context
projection is included.

[Datastar](https://data-star.dev/) and its
[Go SDK](https://github.com/starfederation/datastar-go) are selected. T12 verifies
and pins a released frontend/SDK version pair and its footprint. The SDK owns
dashboard patch events; those events never enter the MCP endpoint.

Pin and serve browser assets locally, retaining licenses; no required CDN or
Node runtime on the SBC. Render/escape initial HTML with Go templates, update only
the needed UI state/fragments, and keep both browser/server buffers bounded.
The chosen framework/event format is documented before implementation.

## CSV and JSONL downloads

Provide format-specific download links for retained live samples or retained archive
samples when enabled, filtered to a validated time range/source/instance. Downloading returns
a finite, consistent snapshot ordered by sample sequence, not an endless SSE feed.
Both formats use the same sample model as the dashboard. Sequence ordering applies
within an instance; archival recording order and instance IDs prevent mixed-restart
data from being presented as one continuous sequence.

- **CSV:** UTF-8, stable header/column order, explicit units in the schema, and proper
  escaping using Go's CSV encoder. Include timestamp, schema/instance/sample identity,
  and the resource values/availability needed to interpret the series.
- **JSONL:** one valid JSON object per line, with the same typed field names/units,
  sample identity, and UTC timestamps. Encode unavailable values as `null`, never
  `NaN`/infinity or a fabricated zero.
- Set the correct media type, `.csv`/`.jsonl` filename, and attachment disposition.
  Use fixed/sanitized filenames and fields, not arbitrary paths or user text.
- Serialize rows incrementally from a bounded snapshot, with bounded concurrent
  export memory and cancellation. Do not hold sampler locks during network writes
  or allocate an unbounded archive; slow downloads cannot stop sampling.
- State the retained/exported range and any requested range that is unavailable.
  Empty history produces a documented valid empty result. Reject invalid formats/
  ranges before streaming, and keep credentials, raw logs, and payloads out of exports.
- Include `X-Resource-Instance-ID`, retained start/end and sample count, and exported
  start/end and sample count headers. Return 416 before attachment headers when the
  requested interval lies wholly outside retained history; mark a partially truncated
  starting interval with `X-Resource-Range-Truncated: start`.

Planned routes on the diagnostics listener are `GET /` (page),
`GET /resources/stream` (Datastar SSE), and `GET /resources.csv` /
`GET /resources.jsonl` (downloads). Downloads default to retained live data;
`source=archive` is supported when enabled, with validated `instance_id`, `from`,
and `to` filters. Empty CSV has its header; empty JSONL has no rows. Unknown formats,
instances, or invalid/unavailable ranges produce explicit errors before streaming.
T01/T12 pin exact field/filter/schema examples; implementation docs add actual URLs.

## HTTP and access boundaries

Use the [Chi middleware rules](../.agents/rules/ara-mcp.md#http-routing-and-middleware)
and [structured HTTP diagnostics](observability.md#http-middleware-and-streaming).
Apply the diagnostics access policy to the page, assets as appropriate, live feed,
and downloads. Use browser-compatible authentication for remote access without
putting bearer/session tokens in URLs. Default serving remains loopback-only.
Use the selected [separate diagnostics access policy](first-release-policy.md#access-and-deployment).

Dashboard requests and downloads are read-only, use the shared sampler/history,
and do not count as human equipment attention. Preserve SSE flushing/cancellation;
short-route deadlines, blanket compression, or response buffering must not break
the live feed. Serve CSV/JSONL with their native encoders, not MCP/HTML patch framing.
Expose subscriber/export counts and failures without logging every resource tick.

## Acceptance criteria

1. RED/GREEN tests verify CPU deltas/units, RSS versus heap, goroutine counts,
   warmup/unavailable/stale handling, pacing, and collector shutdown using controlled data.
2. History tests prove byte/count/age limits, eviction/range reporting, snapshot
   consistency, and continued sampling during slow/cancelled exports.
3. Tests through the full Chi stack cover initial/live SSE updates, reconnect/restart,
   slow consumers, access denial, flushing, cancellation, and subscriber cleanup.
4. A browser smoke test demonstrates automatic updates, units, stale/recovery states,
   accessible download controls, and locally served assets with external networking blocked.
5. CSV and JSONL tests parse the files and compare values/order/units to the same
   retained samples, including missing values, empty history, filters, and invalid inputs.
6. Dashboard/export traffic never causes Ara mutations or attention events. A stdio
   process with diagnostics enabled still emits protocol-only stdout.
7. T10 measures sampler, history, page/stream, and export CPU/RAM overhead on a
   representative small SBC under multiple/slow viewers and simultaneous downloads.
8. Archive tests verify completed-record survival/recovery, rotation/quota, explicit
   gaps on queue/disk failures, instance/schema-aware exports, and bounded shutdown
   without an archival failure affecting Ara operations.

Apply observability, context/concurrency, safety, security, and testing skills as
appropriate, plus benchmark/performance guidance for footprint claims. The bundled
catalog has no Datastar-specific skill; consult its official
[SSE reference](https://data-star.dev/reference/sse_events) and Go SDK API when selected.
