# First-release policies and outstanding verification

**Status:** resolved design choices for implementation, not implemented guarantees
or validated performance claims. This document records the plan-review resolutions.
Task owners implement them with RED/GREEN evidence; outstanding verification is
listed at the end.

## Table of contents

- [Cooperative control](#cooperative-control)
- [Command arbitration and replay handling](#command-arbitration-and-replay-handling)
- [Completion and cancellation contracts](#completion-and-cancellation-contracts)
- [Sequence context and preflight](#sequence-context-and-preflight)
- [Degraded startup and restart recovery](#degraded-startup-and-restart-recovery)
- [Access and deployment](#access-and-deployment)
- [Dashboard, history, and provisional limits](#dashboard-history-and-provisional-limits)
- [Early delivery and integration checks](#early-delivery-and-integration-checks)
- [Outstanding verification](#outstanding-verification)

## Cooperative control

While MCP controls the rig, **the user will not send mutating commands through
Ara's UI**. The first release assumes one controlling ara-mcp instance for that
rig and no competing external automation. Human inspection/review is read-only
where the Ara client supports it. This is an operator workflow, not a new UI lock
or server-enforced REST authorization mechanism.

Use explicit `begin_control` and `end_control` tools to mark the phase boundary.
Beginning control uses Ara's existing handoff/session contract and returns a
locally generated `control_id`. Normal mutations require that current ID. Ending
control releases the session and invalidates the ID; it does not stop Ara's run.
Process shutdown releases the slot best-effort without issuing equipment commands.
Authenticated HTTP MCP clients share this adapter identity; there are no per-agent
roles in the first release, but commands still pass through one arbitration policy.

WebSocket connections are made only with a valid **adapter-owned** Ara session.
Reconnect that session without a fresh claim. If it expires, is rejected, or Ara
restarts, invalidate control, stop mutation dispatch, and require a new explicit
`begin_control`; never auto-reclaim and replay a command.

Outside control, inspect Ara via bounded GET requests/shared polling, not an unbound
WebSocket. In the inspected Ara baseline, an unbound WS connection triggers user
activity and can cancel unattended shutdown. This REST fallback resolves the current
side effect without an upstream feature; a non-attention observer WS remains an
optional upstream enhancement. Never borrow the human UI's session ID.

## Command arbitration and replay handling

- Permit one active imaging run on this rig through the adapter. Serialize normal
  mutation dispatch, and reject conflicts explicitly instead of queueing them for
  surprising execution later. Account for runs/automatic calibration started by Ara.
- During an active run, including paused/awaiting-user states, permit lifecycle
  pause/resume/stop/abort/status operations. Reject independent manual exposure,
  slew, focus, filter, cooler, and guiding mutations until the run is terminal.
  Resume's verified recenter/refocus options cover the initial adjustment workflow.
- Stop/abort and a verified Ara emergency-stop tool have a reserved admission lane;
  ordinary reads, mutations, SSE subscribers, and downloads cannot consume it.
  Admission limits belong at the tool/route level, not a blanket HTTP throttle
  that blocks an interrupt before the SDK can dispatch it.
- Normal mutation calls carry a bounded caller `intent_id` and current `control_id`.
  Keep a bounded in-process receipt ledger for the control phase: identical intent/
  arguments reuse the recorded acceptance/result/uncertain outcome, never dispatch
  again; reused intent with different arguments is a conflict. Hash large bodies
  instead of retaining them in the ledger. Retain bounded receipt/outcome identifiers,
  not full sequence/image payloads; details can be read through the corresponding
  read tools. A replay response identifies itself and cannot invent a fresh dispatch.
- Do not evict seen mutation intents while a phase is active. At capacity, reject
  new normal intents and require an explicit end/begin phase; interrupts remain
  available. A new phase invalidates old control IDs, so old intents cannot replay.
  This is bounded process/phase protection, not durable exactly-once execution.
- No automatic mutation retry after HTTP/MCP disconnect, timeout, process restart,
  or a lost acceptance response. Return a structured `unknown` outcome with
  `retry_safe: false` and reconciliation identifiers when known. Only verified
  upstream deduplication is advertised as retry-safe (sequence creation has actual
  support in the inspected source; start/device actions cannot be assumed to).
- Targeted run-control calls include `expected_run_id`, checked against fresh Ara
  state under adapter arbitration. The cooperative single-controller assumption
  makes this meaningful within the adapter; the current API has no atomic server
  run-ID precondition, so no cross-controller guarantee is claimed.

## Completion and cancellation contracts

T01's tool table must include acceptance identity, completion authority, correlated
failure evidence, cancellation semantics, and retry classification for every tool.
Do not interpret a receipt-only `OperationId` as a job or subscribe to a presumed
universal operation event stream.

| Operation family | Initial tracking contract |
| --- | --- |
| Save/update/template instantiation | Synchronous returned sequence detail; read-back confirms persistence |
| Start/pause/resume/stop/abort | Sequence ID plus observed run ID/state and correlated sequence events; stale run IDs are rejected |
| Manual exposure | Returned frame ID and frame/capture evidence; missing frame before completion is not automatically failure |
| Autofocus/centering | Actual returned job ID and `/jobs/{id}` state where provided |
| Slew/park/focus/filter/guiding | Acceptance plus observed device state/events; claim causal completion only if the baseline supplies sufficient correlation |
| Emergency stop | Ara's actual result/rung fields; report partial failures, not blanket success |

An accepted result remains accepted until authoritative evidence changes it.
Receipt-only operations can expose state observations without claiming that a
specific receipt completed. Lost/unrecoverable evidence stays `unknown`; neither
HTTP 404 nor a quiet event stream proves success. Observation timeouts never
silently convert accepted work into completion.

Cancelling an MCP request ends local waiting/I/O where possible. It does not cancel
accepted Ara work; explicit operation-specific stop/abort/job-cancel tools do that.

## Sequence context and preflight

The first release assumes the rig is configured and connected through Ara before
MCP control. Device discovery/connect and profile-selection mutations are outside
the initial tool set. Add a read-only `get_rig_context` tool containing the active
profile/site, relevant imaging/filter defaults, connected device identities/status,
and capabilities needed to construct the verified sequence recipes.

Sequence authoring uses verified executable templates and a documented baseline-
specific instruction/container/condition/trigger palette. Preserve opaque metadata,
but reject unsupported executable types and malformed parameters rather than run
them or silently discard them. Initially support only verified bounded loop forms.
Plugin/script instructions are not enabled by a generic body passthrough.

T05's adapter palette is pinned to Ara commit
`6374eede73383851486e6fb498a3311a3be58d82`: `SequentialContainer`, at most one
`LoopCondition` (1–1,000 iterations) per container, `SwitchFilter`, `TakeExposure`,
and `Annotation`; nesting is capped at 64 levels and expanded work at 1,000 sequence
instructions. The static check preserves other metadata and reports unsupported execution
separately from Ara's structural `valid` result. It does not establish active-profile
filter membership, equipment capabilities, or readiness; T06 must refresh those
before start. These adapter ceilings bound authored plans, not an upstream Ara
guarantee.

Before start, refresh profile/equipment context and verify the supported recipe/
palette, required slots, filter references, available capabilities, known parameter
bounds, and finite execution constraints. Then use Ara's validation as an additional
check, clearly stating its limits. This is contract/preflight validation, not a
second sequencer or replacement for Ara's hardware guards.

## Degraded startup and restart recovery

Valid configuration starts the adapter/sampler/optional dashboard even when Ara
is offline. Local diagnostics remain usable; Ara-dependent tools return unavailable
errors, and readiness is degraded. Invalid configuration still fails startup.

Check server UUID, API/version/build identity, and reviewed feature contracts on
connection. Unknown/incompatible baselines disable mutation tools with an explicit
compatibility reason; local monitoring and safe identity reads remain available.
Use `/server/info`, `/server/versions`, and `/server/state` fields that the baseline
actually supplies. T01 fills the supported-baseline matrix from tested evidence.

Treat a changed server identity/version or invalidated WS resume epoch as recovery
requiring a fresh snapshot and control invalidation. A network reconnect is not
assumed to be a daemon restart, nor does reusing a server UUID prove continuity.
On Ara restart, expired run/job state is interrupted/unknown unless Ara supplies
correlated terminal evidence. On adapter restart, prior control IDs/receipts are
invalid. Never auto-resume/start or replay an interrupted/uncertain operation.

Acceptance cases include startup without Ara, network loss during accepted work,
Ara restart, incompatible upgrade, adapter restart before receipt delivery, and
UI/MCP handoff. Tests prove that no stale command is released after recovery.

## Access and deployment

First-release trust model: one trusted operator with authenticated agents sharing
control authority. Stdio inherits the launching process's local trust boundary.
HTTP MCP requires a configured bearer credential and uses supported SDK/net/http
auth integration; client examples explicitly supply the Authorization header.
SDK protocol authentication metadata is handled according to the tested client/
protocol baseline, not a custom OAuth server. Client compatibility remains a gate.

Diagnostics use separate read-only credentials and browser-compatible HTTP Basic
authentication when remotely exposed; these credentials grant no MCP tool access.
Loopback-only diagnostics may omit auth except protected profiling. Remote exposure
requires TLS, supplied by the deployment or an explicit trusted reverse proxy,
with configured Origin/Host handling. No tokens in URLs or logs. Environment/file
configuration provides secrets without command-line defaults or dumps.

Update/restart instructions require ending the control phase first. Ara continues
accepted work; upgrading the adapter is not an implicit imaging stop. Configuration
validation and a previous-binary/config rollback path are documented in T10/T11.

## Dashboard, history, and provisional limits

Use **Datastar with its official Go SDK** for the self-updating SSE dashboard.
Serve a pinned frontend asset locally; use Go templates and typed view models.
The framework choice is resolved; released frontend/SDK version pairing is verified
and pinned by T12. Browser signals and MCP framing remain separate protocols.

CPU percentage uses **one logical CPU core as 100%**; multi-core values may exceed
100%. Show logical CPU count/context separately. RSS and Go heap remain distinct.

The following are provisional implementation defaults, not validated hardware claims.
All applicable configuration values have range validation and SBC-aware tests.

| Setting | Initial default/limit |
| --- | --- |
| Resource sampling | Every 2 seconds, one shared sampler |
| Live history | At most 1 hour, 1,800 samples, and 2 MiB; earliest bound wins |
| Dashboard subscribers | 4, with one pending latest-state update each |
| Concurrent exports | 2; no sampler locks during writes |
| Export snapshots | At most 2 MiB in-memory for live data; archive exports stream bounded files |
| Normal read tool calls | 4 concurrent |
| Normal mutations | 1 dispatch at a time, explicit busy/conflict responses |
| Interrupt lane | 1 reserved dispatch, independent of normal admissions |
| MCP HTTP sessions | 4 where the selected SDK exposes session accounting |
| Upstream event backlog | 128 events / 1 MiB per active control phase, shared by consumers; overflow marks a gap and requires REST reconciliation |
| JSON bodies / preview data | 2 MiB / 1 MiB respectively; explicit size errors |
| Mutation intent ledger | At most 1,024 seen intents and 1 MiB of receipt state per control phase; no silent eviction |
| Read-only Ara polling | Shared, paced at 5 seconds while needed; no unbound WS |
| Optional disk archive | 4 JSONL segments of at most 8 MiB each (32 MiB total), with visible rotation/gap metadata |
| Archive writer | 64 samples / 128 KiB queue; periodic flush at most every 5 seconds, bounded shutdown flush |
| Engineering footprint targets | Release-build idle RSS <= 64 MiB, normal busy RSS <= 128 MiB; idle CPU <= 1%, representative normal CPU <= 5% of one core |

Release targets are measured on the intended low-resource board with representative
payloads/concurrency and imaging-service headroom. If unattainable, revise defaults
or claims explicitly before release; do not silently remove diagnostics/correctness.
These are working budgets; board availability and measurements remain outstanding.

Provide an opt-in bounded JSONL disk archive for postmortem resource history. General
CLI/stdio defaults remain memory-only unless an archive directory is configured.
The packaged persistent-service example enables archival in its dedicated data
directory. Rotation caps storage; restart keeps completed retained records, and
download results identify their instance/schema/range. It is best-effort sampling
history, not a zero-loss journal: a crash may lose the most recent buffered records.

Archive write errors/queue overflow produce rate-limited diagnostics and explicit
gaps while live sampling/control continue. Use confined, application-owned paths
and fixed segment names. CSV/JSONL exports support retained live data and, when
enabled, retained archive data; snapshot/byte/range/cancellation behavior is tested.
No database or mandatory external telemetry service is introduced.

## Early delivery and integration checks

T03 introduces runtime sampling and early cross-build checks for Linux amd64/arm64,
macOS amd64/arm64, and Windows amd64, with native platform tests where available.
Linux ARM64 process-resource support is required; other platforms report unsupported
measurements without breaking the build or fabricating values.

T13 delivers the basic Chi diagnostics listener/access/health layer after T03.
T12 then delivers the dashboard/exports without waiting for all equipment tools.
Full HTTP MCP in T09 also uses T13 but need not wait for T08. All tools still share
one implementation and acquire their own transport-parity checks as they land.

Add a reproducible, opt-in integration recipe against a real Ara daemon configured
with simulated equipment. Exercise read contracts early (T02/T03), control/validation/
execution with their tasks, and both transports when HTTP lands. Fixtures remain
useful but cannot substitute for testing Ara's actual service wiring. Physical-rig
and resource measurements remain separate T10 evidence.

## Outstanding verification

These items cannot be truthfully completed through documentation alone. They have
owners and explicit consequences rather than unspecified design choices.

| ID | Item and required evidence | Owner / consequence |
| --- | --- | --- |
| O1 | Ara-specific release tag is not yet published; master `6374eede` is the current tested target. On first Ara release, test newest Ara tag and master independently. SDK v1.8.0 protocol set is selected; no named MCP-host compatibility promise. | T01/T03/T09; unknown builds cannot enable mutations. The repo's inherited N.I.N.A. `v1.10.1` tag is not an Ara release target. |
| O2 | Receipt IDs do not correlate to run IDs; `sequence.failed` is emitted but missing from WS catalog; `ContinueOnError` can emit `instruction_failed` and still finish `completed`; the sequence list exposes run state only for returned items and ignores cursor continuation. Active-run emergency stop passed on OmniSim. T05/T06 reject `ContinueOnError: true`; T06 also refuses start when the bounded 100-item view cannot establish completeness and does not claim a global scan. Run-state alone still cannot reveal instruction failures in externally started runs; event observation belongs to T08. | T01/T04/T06–T08; use instruction events plus per-sequence state where known, never infer success from a receipt alone or claim a global active-run scan; keep conflicts fail-closed. |
| O3 | Finite `SequentialContainer`/`LoopCondition`/`SwitchFilter`/`TakeExposure` ran on OmniSim; T05 enforces that bounded palette and T06 now checks known camera limits and filter slots before start. Packaged LRGB frames were attributed to loop names, lacked filter metadata, and `frames_captured` lagged frame rows; execution metadata remains an Ara gap. | T01/T06; do not claim reliable target/filter/frame attribution until Ara or adapter execution semantics are fixed and tested. |
| O4 | Datastar JS v0.21.4 / Go SDK v1.2.2 stable pair, MIT asset license, local Chromium patch/event behavior, and loopback Basic-auth browser flow verified; remote/TLS browser access remains untested | T12; verify remote access before claiming the full dashboard acceptance |
| O5 | SBC measurements, footprint targets, histogram/default-limit adjustments, and cross-platform resource availability | T03/T12/T10; provisional budgets are not hardware support claims |
| O6 | On current master, restart changed server UUID, cleared the session, and removed in-memory run state while preserving the saved sequence. Resume-epoch, interrupted-run and job evidence remain to verify on future release builds. | T04/T08 integration checks; invalidate control and report unknown until authoritative recovery |
| O7 | Optional read-only Ara WS subscription that does not count as human attention | Upstream enhancement, non-blocking for first release; use REST outside valid owned sessions |

Refer to [the plan](plan.md) and [dashboard contract](resource-dashboard.md) for task
details. Durable exactly-once execution or server-enforced multi-controller locking
would require a separately evidenced upstream contract; the cooperative first
release does not claim either.
