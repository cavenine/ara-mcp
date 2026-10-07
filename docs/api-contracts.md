# Initial Ara and MCP contracts

**Status:** development-build and simulator evidence for T01 is recorded below.
This is not a release support claim or complete runtime contract. Broader
request/response fixtures, edge-case integration checks, and a supported Ara
release/build matrix remain outstanding.

**Compatibility policy:** until Ara publishes its first Ara-specific release,
`master` is the support target. After releases begin, test the newest Ara release
tag and current `master` independently. The checkout's Git tag `v1.10.1` is
inherited N.I.N.A. code dated 2020-10-18, not an Ara release. Current master
evidence below is pinned to its exact commit.

The MCP contract targets the official Go SDK's supported protocol revisions. T03
tests SDK tool discovery/calls in memory and process-level stdio framing; no named
host application (Claude Desktop, Cursor, VS Code, etc.) is promised or verified.
Streamable HTTP checks belong with T09. Ara's Flutter WILMA client speaks Ara
REST/WebSocket and is not an MCP host.

## Evidence baseline

The source inspected and daemon exercised are `openastro-ara` commit
[`6374eede73383851486e6fb498a3311a3be58d82`](https://github.com/open-astro/openastro-ara/tree/6374eede73383851486e6fb498a3311a3be58d82),
with `OpenAstroAra.Server` targeting .NET 10. `/api/v1/server/info` identifies API
`v1` but reports tier `scaffold`; `/api/v1/server/versions` reports REST and
WebSocket surface versions `1.0.0`. The server UUID is process-generated in the
inspected source, so it is not a reliable installation identity across restarts.
Those fields do not establish a production/release baseline.

Several endpoint files still have stale comments saying routes are stubs. The
registered implementation matters: `Program.cs` wires a filesystem-backed sequence
store, the real `SequencerService`, and Alpaca-backed equipment services. In-memory
built-in templates are placeholders; packaged templates are structured sequence
trees. Simulator checks below exercise a subset of camera, mount, sequence, session,
and event behavior. Other routes remain source-reviewed only. Do not infer hardware
behavior from DTOs, comments, or HTTP status declarations.

The actual Ara endpoint prefix is `/api/v1`. Path parameters below are UUID strings.
JSON wire names use the daemon's configured lower-snake-case serializer; confirm
captured bodies before freezing schemas.

## First tool surface

The table is the MCP tool contract for the initial T03–T07 surface. T08's job/frame
readers and T14's event projection are implemented; the source-reviewed T15–T18
surfaces are recorded below. Every mutation also requires the selected
first-release `control_id` and `intent_id` fields, except `end_control`, which
requires the current control ID. The adapter checks these locally; Ara does not
enforce this contract for legacy REST mutations. Operation acceptance is never
reported as physical completion.

| MCP tool | Ara route(s) | Input and result contract | Classification / evidence |
| --- | --- | --- | --- |
| `get_server_context` | `GET /server/info`, `/server/versions`, `/server/state` | No arguments. Return identity, version/API surface, and state snapshot; keep version and server identity distinct. | Read-only. `/server/versions` is a service implementation; compatibility fields and identity stability need live verification. |
| `get_rig_context` | `GET /server/state`, `/profiles`, `/profile/site`, `/profile/imaging-defaults`, `/profile/filter-wheel/labels`, `/profile/filter-set`, `/equipment/{camera,telescope,focuser,filterwheel}` | No arguments. Return nullable active profile ID, profile/site/imaging/filter defaults and selected device status/capabilities. A disconnected/unselected device route returns 404 and becomes an explicit unavailable status, not a fabricated connected record. | Read-only. On the RPi4 Ara build, `/profiles.active_id` reports the selected profile while `/server/state.current_profile_id` remains null; the adapter uses the profile-library selection as fallback. Disconnected device routes return 404; after OmniSim camera/filter-wheel connection their status/capability DTOs are available. |
| `list_sequences` | `GET /sequences?limit=` | Optional limit (client default 50, maximum 100); return `items`, `next_cursor`, `has_more`. Each returned item includes `current_run_state` when Ara has retained run state. | Read-only. On pinned master, `FileSequenceService.ListAsync` applies the limit but ignores `cursor` and always returns `next_cursor: null`, `has_more: false`. There is no sequence-list continuation/global scan; this is an upstream gap, not a client pagination guarantee. |
| `get_sequence` | `GET /sequences/{id}` | Required sequence UUID; return metadata and opaque JSON `body`; unknown ID is not-found. | Read-only. Preserve `$type` and unknown metadata. |
| `list_sequence_templates` | `GET /sequences/templates` | No arguments; return names, category, description, built-in flag, and opaque body. | Implemented T05, read-only. The three in-memory built-ins are placeholder bodies; packaged templates are structured sequence trees. `lrgb-dso` deserialized and executed on OmniSim below; other templates and full profile/equipment preflight remain unverified. |
| `instantiate_sequence_template` | `POST /sequences/templates/{name}/instantiate` | Template name plus `new_sequence_name`; optional object `parameters`; return newly saved sequence detail. | Implemented T05, saved-plan mutation. Ara substitutes supported `{{token}}` values, then creates the sequence. No verified idempotency key on this route; reconcile uncertain outcomes before a new intent. |
| `validate_sequence` | `POST /sequences/validate` | Opaque JSON sequence body; return Ara `valid`/optional `reason` and adapter `executable_supported`/optional `support_reason`. | Implemented T05, read-only. Ara checks object shape, `schemaVersion == "openastroara-sequence-v1"`, and reachable capturable-instruction count only. Adapter checks the pinned finite execution palette; neither establishes equipment, filter membership, camera capabilities, or start readiness. |
| `create_sequence` | `POST /sequences` | Required `name`, `body`; optional `description`, `template_origin`; return saved sequence detail and adapter mutation receipt. | Implemented T05, saved-plan mutation. On tested master, Ara uses `Idempotency-Key` with persisted create replay mapping while the created file exists. The adapter uses `intent_id` as this key. |
| `update_sequence` | `PATCH /sequences/{id}` | Sequence UUID plus one or more optional name, description, and/or body fields; return updated detail and adapter mutation receipt. Active-run edits are rejected with conflict; missing ID is not-found. | Implemented T05, saved-plan mutation. Not idempotent; no automatic retry after an uncertain response. |
| `begin_control` | `GET /server/info`, `/server/versions`, `/server/state`, `/profiles`; `POST /server/connect`; bind `/ws` with `X-Ara-Session` | Adapter display name; return adapter-local control ID and sanitized control status. Keep Ara session capability internal. Reclaim with the same session only for recovery within the running adapter. | Control mutation. Another live holder can reject or time out; fresh claims can count as user activity. The selected profile ID falls back to `/profiles.active_id` when this build's server-state placeholder is null. Do not auto-claim or retry a lost claim response. |
| `end_control` | `POST /server/disconnect` | Current control ID; release Ara session. Does not stop a run. | Control mutation. A stale session can return not-found; invalidate local control either way. |
| `start_sequence` | `POST /sequences/{id}/start`, then `GET /sequences/{id}/state` | Sequence UUID plus intent/control IDs. Ara's required body is `dry_run: false`, `start_from_instruction_index: null`, `continue_on_recoverable_errors: false`; do not present these ignored fields as supported options. The adapter refreshes the saved body, Ara structural validation, profile, connected camera/filter wheel, camera exposure/gain/offset/binning limits, referenced wheel positions/profile labels, and bounded active-run list. `ContinueOnError: true` is rejected because this Ara build may emit `instruction_failed` and still report `completed`. | Implemented T06, equipment-changing and asynchronous. Ara returns 202 with `OperationAcceptedDto`; its `operation_id` is not a completion/job key and the route has no verified idempotency support. An unknown start outcome is returned with reconciliation state when available and is not retried. Ara remains the final execution authority. |
| `get_sequence_state` | `GET /sequences/{id}/state` | Sequence UUID; return Ara's run-state object unchanged, including run ID, state, progress, timestamps, estimated duration, and captured-frame count. States are `idle`, `starting`, `running`, `paused`, `aborting`, `stopped`, `completed`, `failed`, `pausedawaitinguser`. | Implemented T06, read-only. Explicit reads and immediate post-command reads count a distinct terminal sequence/run pair once. Absent state is not evidence of completion. Run records are in memory and bounded; after Ara restart, saved sequence detail remains but run state is 404/unknown. `GET /server/state.active_sequence_run` is currently an empty placeholder; start preflight inspects `current_run_state` for the bounded sequence list and fails closed at the 100-item limit. |
| `pause_sequence`, `resume_sequence`, `stop_sequence`, `abort_sequence` | `POST /sequences/{id}/{pause,resume,stop,abort}`, then `GET /sequences/{id}/state` | Sequence UUID, expected run ID, control and intent IDs. Pause/stop/abort have no body; resume sends explicit `recenter: false` and `refocus: false` to avoid implicit telescope/focuser actions (the RPi4 Ara build returns 415 without a JSON body). | Implemented T06, equipment-changing and asynchronous. Each must return 202; a different response is treated as unknown. Fresh state and expected-run-ID checks happen under adapter arbitration. Stop/abort use the reserved interrupt lane. Immediate state is only an observation; state-read failure does not erase acceptance. No mutation is automatically retried. Live simulator checks exercised pause→resume→stop and a separate abort. |
| `emergency_stop` | `POST /server/emergency-stop` | No Ara body; adapter requires local control/intent IDs. Return `already_in_progress`, `runs_aborted`, `exposure_aborted`, `guiding_stopped`, `park_requested`, `flat_panel_light_off`, and `failed_rungs`. | Implemented T07 as a reserved interrupt with Ara's synchronous per-rung result. Existing simulator evidence exercised an active-run abort and mount-park request. The `exposure_aborted` flag currently means the abort command succeeded on a connected camera, not proof an exposure had actually been active. |

### Initial manual-action tools

These names form the initial implemented T07 tool surface. They use the matching Ara
DTO fields in lower-snake-case JSON. Exposure is seconds; RA is hours; declination
and angles are degrees; temperature is Celsius; timestamps are UTC. Device
connection/profile-selection routes are not exposed as agent tools.

| MCP tool | Ara input | Result and completion authority |
| --- | --- | --- |
| `capture_exposure` | Required `exposure_sec` (>0 and within device caps), nullable `gain`; `bin_x`/`bin_y` default 1; optional `offset_x`, `offset_y`, `width`, `height`, `filter_name`, `camera_offset`. | Ara returns 202 with `frame_id`, `preview_url`, `exposure_sec`, `captured_at`; this acknowledges background capture. Poll `GET /frames/{id}` for persisted metadata; preview is a separate endpoint. A server-generated operation receipt is not a job ID. |
| `abort_exposure` | No Ara body. | Ara returns 202 without a result body. This is an abort request, not evidence that a frame was interrupted; reconcile camera/frame state. |
| `set_camera_cooler` | `enabled`; optional `target_temperature_c` (ignored when disabling; omitted means leave setpoint unchanged). | Ara returns 202 without a result body. Unsupported cooler/setpoint or device errors map to conflict; observe camera status for effect. |
| `slew_telescope` | `right_ascension_hours`, `declination_degrees`, optional `sync` (default false). | Ara returns 202 with `OperationAcceptedDto`; observe telescope runtime/target state. No causal completion from `operation_id`. |
| `park_telescope` | Optional `reason`. | Ara returns 202 with receipt; observe `parked` state. |
| `unpark_telescope` | No Ara body. | Ara returns 202 with receipt; observe `parked`/state fields. |
| `abort_telescope_slew` | No Ara body. | Ara returns 202 without a result body; it also pauses active sequences in the endpoint handler. Observe telescope and sequence states. |
| `move_focuser` | `target_position`; optional `use_temp_comp` (default false). | Ara returns 202 with receipt; observe focuser position/state. |
| `run_autofocus` | No Ara body. | Ara returns 202 with `BatchJobDto`; track `job_id` via `GET /jobs/{id}`. `DELETE /jobs/{id}` requests cancellation. |
| `select_filter` | `position` (slot index from filter-wheel status). | Ara returns 202 with receipt; observe current slot. |
| `start_guiding`, `stop_guiding` | No Ara body. | Ara schedules the PHD2 action and returns 202 with an operation receipt; observe guider status. |
| `dither_guiding` | Required `pixels` query parameter, finite positive amplitude in pixels. | Ara schedules dither and returns 202 with an operation receipt; observe guider status. |

`abort_telescope_slew` also uses the reserved interrupt lane because Ara's route
unconditionally pauses active sequences after asking the mount to abort. Exposure
abort and `emergency_stop` likewise bypass ordinary mutation saturation. Normal
manual actions preflight connected-device capabilities/status and the bounded sequence
list; active/paused runs are rejected before dispatch. Immediate device reads after
acceptance are labeled observations, not causal completion. T08 adds Ara's frame/job
readers. Ara's pinned master source registers guider
`/start`, `/stop`, and `/dither` routes, backed by `GuiderService` methods that require
a connected PHD2 guider and schedule the operation before returning a 202 receipt.
T07 checks guider connection and the bounded active-run list before dispatch.
The route and service wiring were verified in Ara commit
[`6374eede73383851486e6fb498a3311a3be58d82`](https://github.com/open-astro/openastro-ara/tree/6374eede73383851486e6fb498a3311a3be58d82),
`OpenAstroAra.Server/Endpoints/EquipmentEndpoints.cs` and
`OpenAstroAra.Server/Services/GuiderService.cs`. These are source/fake-Ara contract
checks only; T07 has no live guider or physical-rig validation.

### Ara API follow-up tools (T15–T18)

The following routes and backing services were rechecked in Ara commit
[`29f72ea2246a1343a60724361d271912610b3503`](https://github.com/open-astro/openastro-ara/tree/29f72ea2246a1343a60724361d271912610b3503).
They are source-verified against that development build; per-tool evidence and the
RPi4/OmniSim exposure-event check are recorded in [T14–T18](plan.md#task-list).

| MCP tool family | Ara routes | Contract and limits |
| --- | --- | --- |
| Autofocus state/frame/calibration | `GET /autofocus/state`, `/frame`, `/calibration` | State carries the current/recent run, bounded probes, fit curve and `frame_seq`; frame is JPEG, `X-Frame-Seq`, `Cache-Control: no-store`, 204 until rendered. Calibration 404 means uncalibrated. |
| Autofocus cancel/recalibrate | `POST /autofocus/cancel`, `/recalibrate` | Cancel returns 202 or 409 when no run is active and can cancel a sequence-started sweep. Recalibrate synchronously clears stored calibration; next successful Classic sweep rebuilds it. Ara cancel acceptance is not terminal completion. |
| Fault history | `GET /faults?limit=&cursor=&equipmentType=&sessionId=&unresolvedOnly=&faultType=`, `GET /faults/{id}` | Ara caps the page at 200, returns a numeric offset cursor, and keeps retained rows in SQLite with configured pruning. History is not a complete event journal or current state; invalid cursors fall back to offset zero upstream, so ara-mcp validates cursors before dispatch. |
| Guide-camera focus | `GET/POST /equipment/guider/focus`, `/focus/frame`, `POST /focus/start`, `/focus/stop`; `GET /equipment/polaralign/status` | Start is 202 (0.05–30 s exposure, optional binning); 409 for disconnected/busy/guide or polar-alignment lease conflicts. Stop is 204 after the in-flight frame drains. Status is a current snapshot; frame is JPEG or 204, capped by ara-mcp at 1 MiB. |
| Saved-frame solve | `POST /platesolve/frames/{id}/solve` | Optional coordinate hints are an RA-hour/Dec-degree pair. Returns solution metadata only; 404 is a missing frame and 422 reports solver/profile configuration errors. No FITS bytes or daemon-local paths are exposed. |
| Coordinate centering | `POST /platesolve/center`, `GET/DELETE /jobs/{id}` | RA hours `[0,24)`, declination degrees `[-90,90]`; 202 returns an asynchronous `center` job. Same-target requests join; a different active target returns 409. Job status is authoritative; delete requests cancellation but does not itself prove the mount is stationary. |

The simulator confirmed one supported sequence palette: typed sequential containers,
bounded `LoopCondition`, `SwitchFilter`, and `TakeExposure`. The packaged
`lrgb-dso` tree ran four one-iteration filter/exposure blocks and wrote four FITS
frames. It also exposed data-contract limits that must remain visible: the frame
`target_name` was the immediate loop name (`L x30`, `R x15`, etc.), not the root
template target; `filter_name` was null; and the finished run reported
`frames_captured: 3` while the frame list contained four records. Do not promise
correct target/filter metadata or use these templates for unattended production
until Ara resolves those semantics. T05's adapter palette check constrains the
instruction tree; it does not correct or guarantee execution metadata.

Ara's `Idempotency-Key` header is not proof of deduplication. Only use it as a
retry guarantee where the route's service has verified replay semantics (sequence
create does); all other mutation retries stay disabled after uncertain outcomes.

## Outcomes, retry, and cancellation

- Map Ara problem responses to structured MCP errors with status/type/detail where
  safe; redact connection/session IDs and credentials. Distinguish invalid input,
  not-found, active-run/control conflict, unavailable/incompatible server, and
  uncertain mutation outcomes.
- A `202` means accepted only. Sequence state/event evidence determines run outcome;
  `/jobs/{id}` is usable only when a route actually returns a job ID. Never query a
  receipt-only `operation_id` as if it were a job.
- No automatic mutation retry after timeout, cancellation, disconnect, or lost
  response. `create_sequence` may use Ara's verified idempotency key. Other actions
  need route-specific proof before being marked retry-safe.
- Cancelling an MCP wait ends local waiting/I/O where possible. It does not undo an
  accepted Ara operation. Explicit stop/abort is a separate command.
- Sequence state/event payloads carry `sequence_id` and `run_id`, but they do not
  provide a universal operation-to-event correlation key. Keep causal completion
  unknown where the tool receipt cannot be joined to authoritative state.
- The adapter's `expected_run_id` is a local stale-call guard, not an Ara atomic
  precondition. The inspected executor reserves by sequence ID; starting a sequence
  after a terminal run can create another run. `GET /sequences` exposes current run
  state only for its limited result; the inspected `FileSequenceService` ignores
  cursor and returns no continuation. There is no global active-run scan or atomic
  compare-and-start across sequences/adapters. The one-run rule remains adapter-local
  and assumes the cooperative single-controller policy.
- Ara's handoff controls a single WS-bound client, not REST authorization. An
  unbound WS connection may trigger attention behavior. Outside valid adapter-owned
  control, use bounded GET polling only; never claim server-enforced exclusion of
  other REST clients.

## MCP SDK and initial runtime configuration

Select official [`github.com/modelcontextprotocol/go-sdk` v1.8.0](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk@v1.8.0)
for T03/T09 implementation. It is a tagged stable release, declares Go 1.25.0,
and therefore fits this module's Go 1.27 baseline. The release notes list support
for MCP protocol versions `2026-07-28`, `2025-11-25`, `2025-06-18`, `2025-03-26`,
and `2024-11-05`; server configuration can narrow the advertised set. Use the
SDK's default negotiated set initially. This is the protocol target, not a claim
that every named host client has been tested.

Initial CLI shape: `ara-mcp [--config FILE] [--log-level LEVEL] serve
--transport stdio|http`; retain `--help` and `version`. Configuration precedence
is explicitly-set flags > `ARA_MCP_*` environment > optional config file > defaults.
Resolve once to typed validated settings. Stdio is the default transport, logs go
to stderr, and stdout is protocol-only. HTTP and diagnostics listeners remain
loopback by default; their credentials/TLS settings follow
[`first-release-policy.md`](first-release-policy.md#access-and-deployment).

## Local development-daemon check (2026-10-04)

Built and ran this checkout in the cached .NET 10 SDK container because the host
has no `dotnet` executable. The checkout was mounted read-only; the daemon used
Development mode and a disposable `/tmp` profile inside the container, bound only
to host loopback port `15555`. Build outputs and profile data stayed in the
container. This is a local development build check, not a released Ara baseline.

Observed read responses:

```json
GET /api/v1/server/info
{"server_uuid":"<per-process UUID>","nickname":"<container hostname>","version":"1.0.0.0","api":"v1","mdns_service":"_openastroara._tcp.local","tier":"scaffold"}

GET /api/v1/server/versions
{"daemon_version":"1.0.0.0","daemon_git_sha":"6374eede73383851486e6fb498a3311a3be58d82","dotnet_version":"10.0.12","api_surfaces":[{"name":"rest","version":"1.0.0"},{"name":"websocket","version":"1.0.0"}]}

GET /api/v1/sequences?limit=10
{"items":[],"next_cursor":null,"has_more":false}

POST /api/v1/sequences/validate (body is the packaged lrgb-dso template body)
{"valid":true,"reason":null}

POST /api/v1/sequences/validate (unsupported schema version)
{"valid":false,"reason":"unrecognized schema version 'not-supported'; expected 'openastroara-sequence-v1'"}
```

`GET /sequences/templates` returned six rows: three in-memory built-ins with
placeholder bodies, and the packaged `lrgb-dso`, `narrowband-shoo`, and `comet`
templates with NINA-style `$type` sequence trees. Each packaged template returned
`{"valid":true,"reason":null}` from Ara's structural validator. Instantiating
`lrgb-dso` with `{"new_sequence_name":"T01 contract probe","parameters":{"target_name":"M31"}}`
returned 201 and a saved sequence named `LRGB - M31`; GET read-back returned the
same ID and substituted name. A repeated POST `/sequences` with the same
`Idempotency-Key` returned the first created ID and original name. A missing
sequence detail returned 404.

The rig-context GETs returned active-profile settings, but `current_profile_id` in
server state was null; camera/telescope/focuser/filter-wheel status routes returned
404 because no equipment was connected. The pinned simulator directory was absent.
No exposure or telescope motion was sent during this initial response-shape check.
The build logged missing optional/operational CFITSIO, ASTAP, astrometry natives,
and guider dependencies; later capture testing installed CFITSIO only in the
disposable container.

This confirms selected response shapes, template listing/instantiation, structural
validation, sequence read-back, and create-key replay on one development checkout.
Later sections cover a supported execution subset and restart/session behavior; WS
recovery/attention, broad operation behavior, and release compatibility remain open.

## Pinned Alpaca simulator check (2026-10-04)

Downloaded the repository-pinned ASCOM OmniSim **v0.4.0**, SHA
`012a5778b4335b17332b9bffd8f3a0c561c727d8`, using Ara's
`scripts/get-alpaca-simulators.sh` (the artifact SHA-256 was verified by that
script). The simulator and daemon shared an isolated Docker network namespace;
the daemon retained its disposable profile and the simulator mount had no route to
physical equipment.

- `GET /api/v1/equipment/discover/camera?forceRefresh=true` returned 200 with an
  Alpaca device record: `unique_id`, `name`, `type`, `host_name`, `ip_address`,
  `ip_port`, `alpaca_device_number`, `use_https`.
- Telescope discovery returned the same shape. Both reported `type` in lower case,
  `ip_address: "127.0.0.1"`, `ip_port: 32323`, and Alpaca device number `0`.
- `POST /api/v1/equipment/{camera,telescope}/connect` with
  `{"device": <discovered device>}` and an `Idempotency-Key` returned 202 with an
  `OperationAcceptedDto` receipt. Follow-up GETs returned state `connected`; POST
  disconnect returned 202 and subsequent GETs returned `disconnected`.
- `POST /api/v1/server/connect` with
  `{"hostname":"ara-mcp-t01-test","session_id":null}` returned 200 and a
  session capability; GET `/server/session` reported connected; POST disconnect
  returned 204 and the subsequent session GET reported disconnected. The capability
  is omitted here.
- The focused Ara test command selected `SequenceBodyDeserializerTest`,
  `TakeExposureTest`, `SequenceTemplateInstantiateTest`, and
  `SequenceSchemaValidatorTest`: **22 passed, 0 failed**. This includes proof that
  the NINA `TakeExposure` `$type` remaps to Ara's executable instruction type.

No exposure, telescope motion, or sequence start was requested during this initial
discovery/connect check. The container
reported that CFITSIO, ASTAP, astrometry natives, and guider were unavailable; the
mount/camera connection path was exercised, not capture or plate solving. This
confirms live discovery and connect/status/disconnect contracts with the pinned
simulator, not production compatibility or end-to-end imaging.

## Sequence, event, restart, and emergency-stop checks (2026-10-04–05)

Installed Ubuntu's `libcfitsio10` package only inside the disposable Ara container,
changed only that container's profile storage path to
`/tmp/ara-t01-profile/captures`, then connected the simulator camera and opened the
session-bound `/ws` endpoint with `X-Ara-Session` and `X-Ara-WS-Version: 1`.
Created a disposable one-instruction sequence from the packaged LRGB tree, keeping
its `TakeExposure` type and setting one 0.1-second light exposure with
`AbortOnError`. No mount motion was issued.

Ara accepted `POST /sequences/{id}/start` with 202 and an operation receipt. The
WebSocket delivered `sequence.started`, progress, and `sequence.complete`; the
terminal payload contained `sequence_id` and `run_id`, not the receipt's
`operation_id`. `GET /sequences/{id}/state` agreed on `completed`. The frame catalog
then returned the captured frame: 0.1 seconds, `light`, 800×600, 16-bit, and a
964,800-byte FITS file. Camera and Ara control session were disconnected afterward.
This proves one successful simulated capture and the start/state/event/frame flow
on this development build, not a physical-rig result or a release baseline.

The first disposable attempt intentionally exposed another important distinction:
with the default profile's `/media/openastroara` store unmounted, Ara emitted
`sequence.instruction_failed` for a blocked capture. The item was configured with
NINA's `ContinueOnError`, so Ara then emitted `sequence.complete` and reported the
run `completed`, despite no frame being written. The successful run used
`AbortOnError`; clients must inspect instruction-failure events/results as well as
the terminal run state before claiming all requested work succeeded.

There is also a catalog mismatch in this source revision: `sequence.instruction_failed`
is advertised and was observed; the sequencer emits `sequence.failed` in its
top-level failure path, but `/api/v1/ws/catalog` does **not** advertise that event.
Do not rely on `sequence.failed` without an upstream correction or a tested
compatibility gate. This is a concrete Ara follow-up; no upstream source was changed
in this task.

The four-block LRGB check used one exposure per filter and verified all four
simulated filter-wheel position changes/captures. The packaged 30/15/15/15 loop
counts were reduced only in the disposable test sequence. The four resulting rows
were under subcontainer names (`L x30`, `R x15`, `G x15`, `B x15`) rather than the
root sequence target, all with `filter_name: null`; the immediate-parent fallback
in `TakeExposure` explains this. The run-state frame count was also 3 while the
frame list contained 4. Treat target/filter/frame-count reporting as an unresolved
Ara contract issue, not as verified template behavior.

A separate active-run check started a 30-second simulated exposure, then called
emergency stop; Ara returned 200 with `runs_aborted: 1`, `exposure_aborted: true`,
`park_requested: true`, and no `failed_rungs`, and the run state became `stopped`.
The mount was subsequently observed parked. A no-active-run stop also returned
`exposure_aborted: true` for a connected but idle camera, confirming that field
means the abort call succeeded, not proof an exposure was in flight. The
`safety.emergency_stop` and `safety.action_taken` events were observed.

While a wait-only sequence was active, `GET /sequences` returned its
`current_run_state` as `starting`. The same server snapshot returned
`active_sequence_run: {}`. The pinned `FileSequenceService.ListAsync` applies the
requested limit, ignores its cursor argument, and returns `next_cursor: null` and
`has_more: false`; only returned items can be inspected. Do not assume a complete
run list or atomic cross-sequence snapshot.

Restarting the disposable daemon changed `server_uuid`, cleared `/server/session`,
made the old session's disconnect return 404, preserved saved sequence details,
and made their in-memory run-state endpoint return 404. This matches the adapter
policy: discard local control/run assumptions after Ara restart and reconcile from
saved/readable state.

## Remaining release and implementation gates

1. Ara's first Ara-specific tag does not exist yet. Keep master `6374eede` as the
   current target; test its first release tag independently when published. The
   inherited N.I.N.A. `v1.10.1` tag is not an Ara target (O1).
2. Simulator edge cases still needed before shipping mutations: equipment errors,
   start races/retries, and WS attention/recovery; emergency stop and one active-run
   abort have passed the local OmniSim path (O1/O2).
3. The supported sample tree (`SequentialContainer`, finite `LoopCondition`,
   `SwitchFilter`, `TakeExposure`) deserializes and ran against OmniSim. Correct
   target/filter attribution and matching all profile/equipment preflight cases
   remain required before claiming reliable template execution (O3).
4. SDK v1.8.0 and its protocol set are selected. No named MCP host is promised;
   stdio/Streamable HTTP smoke and any host-specific matrix belong to T03/T09.

Until their evidence exists, adapters must not advertise untested tool behavior,
compatibility with a released Ara version, or physical-rig success. Claims about the
current master/OmniSim checks must stay limited to the exact behaviors above.
