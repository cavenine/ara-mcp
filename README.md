# ara-mcp

[![License: AGPL-3.0-or-later](https://img.shields.io/badge/License-AGPL--3.0--or--later-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27.x-00ADD8.svg)](go.mod)

**An AI-agent interface to OpenAstro Ara.**

ara-mcp is a Go project for a [Model Context Protocol (MCP)](https://modelcontextprotocol.io/)
server that lets AI agents prepare imaging sequences, request equipment actions,
and monitor sessions through [OpenAstro Ara](https://github.com/open-astro/openastro-ara).
Ara owns sequence execution and equipment coordination; ara-mcp provides another
interface alongside Ara's human-facing client.

## Status

**Implementation in progress.** T02's Resty-backed Ara HTTP client, T03's stdio
MCP server/read tools, T04's begin/end control-phase tools, T05's sequence-authoring
tools, T06's sequence-execution tools, T07's manual camera/mount/focuser/filter-wheel
actions, T09's authenticated Streamable HTTP MCP endpoint, and T13's optional
diagnostics HTTP listener are implemented. HTTP protocol and concurrent-session
behavior are tested with the official MCP Go SDK v1.8.0 client; no third-party host
compatibility is claimed. T08 job/frame readers, bounded image thumbnails, and
owned-session event monitoring are implemented. With the optional diagnostics
listener enabled, the resource page, Datastar SSE updates, live/archive CSV/JSONL
exports, and bounded rotating archive are available. The page serves Datastar JS
v0.21.4 with Go SDK v1.2.2. T10 still owns remote-deployment validation and SBC
measurements. No release is available yet.

The current tool set reads Ara server identity/version/state, rig/profile/device
context, saved sequence pages/details, sequence templates, validation results, and
adapter/process diagnostics. `begin_control`/`end_control` manage Ara's adapter
session. T05 can save, update, or instantiate plans through Ara; it does not start
them. Ara connectivity is needed only when calling Ara-backed tools; process
diagnostics remain available when Ara is offline. Optional HTTP diagnostics expose
`/healthz`, `/readyz`, `/status`, and `/metrics` separately.

## Architecture

```text
AI agent
   | MCP: stdio or Streamable HTTP
   v
ara-mcp
   | Ara REST API / WebSocket events
   v
Ara server
   | ASCOM Alpaca
   v
AlpacaBridge (or another compatible Alpaca device server)
   |
   v
Astrophotography equipment
```

Both transports use the official
[MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk) and the same tool
handlers and Ara API integration:

| Mode | MCP transport | Connection to Ara | Lifecycle |
| --- | --- | --- | --- |
| On the agent's computer | stdio | Ara's LAN address | Launched by the agent |
| Beside Ara server | Streamable HTTP | Usually `localhost:5555` | Persistent service |

Running ara-mcp beside Ara does not require embedding it in Ara. Ara continues an
imaging session when the agent or adapter disconnects.

The executable uses Cobra and Viper. Configuration precedence is explicit flags >
`ARA_MCP_*` environment > optional config file > defaults; details and runnable
commands are in the [development guide](docs/development.md#application-commands-and-configuration).
HTTP serving uses Chi v5 and its middleware, with structured `slog` request logging
and the MCP SDK's handler preserving protocol framing.
See [HTTP architecture](docs/architecture.md#http-stack).

HTTP mode serves `/mcp` on `127.0.0.1:8080` by default and requires a bearer token
of at least 32 characters from `ARA_MCP_HTTP_BEARER_TOKEN` or the config file.
Cross-origin browser requests are denied unless their Origin matches the request
Host or is listed in `http-origins`. Non-loopback binds require configured TLS.
See the [HTTP configuration guide](docs/development.md#running-and-connecting-over-http).

The first-release workflow is cooperative: while MCP controls the rig, the user
does not mutate equipment through Ara's UI. The rig is initially configured and
connected in Ara. See [resolved policies and outstanding verification](docs/first-release-policy.md)
for control phases, operation handling, degraded startup, and deployment defaults.

## Deployment targets

ara-mcp is intended to run on small, resource-constrained single-board computers
(SBCs), including **Raspberry Pi 3, 4, and 5** with a 64-bit Linux OS. Limited CPU
and RAM, including lower-memory board configurations, are design constraints.
The adapter should leave headroom for Ara, AlpacaBridge, and other rig services
when they share the same computer.

These are intended ara-mcp targets, not validated hardware/minimum-memory claims.
Ara's own hardware requirements remain separate; a Pi 3 adapter target does not
establish that the complete imaging stack fits on a Pi 3. See
[resource constraints](docs/architecture.md#deployment-targets-and-resource-constraints)
and [SBC validation](docs/development.md#small-sbc-validation).

## Available read-only tools

- `get_server_context`: Ara identity, API versions, and state.
- `get_rig_context`: profile, site/imaging defaults, filters, and available device status.
- `list_sequences`, `get_sequence`, `list_sequence_templates`, and
  `validate_sequence`: inspect saved plans/templates and run structural/palette
  validation. Neither validation confirms rig compatibility or readiness.
- `get_sequence_state`: read Ara's current run state; a missing in-memory state is not
  evidence of completion.
- `get_job_status`, `list_frames`, `get_frame`, and `get_frame_preview`: inspect
  ephemeral Ara jobs and the saved frame catalog; previews return JPEG image content
  capped at 1 MiB. Ara may return a placeholder when its catalogued FITS file is absent.
- `get_recent_ara_events`: inspect up to 128 / 1 MiB of events from the adapter-owned
  Ara session socket. Expired replay or buffer overflow is reported as a gap; read
  current sequence/job/frame state through the REST tools to reconcile. Without an
  owned control session, event streaming is unavailable and the tools use REST.
- `get_adapter_diagnostics`: Ara reachability and local process/runtime sample.

## Sequence-authoring tools

- `list_sequence_templates`: inspect Ara templates and opaque bodies.
- `validate_sequence`: return Ara's structural result plus the adapter's bounded
  executable-palette check. Neither is rig/equipment preflight.
- `create_sequence` and `update_sequence`: save or edit Ara plans through the T04
  control/intent dispatcher.
- `instantiate_sequence_template`: ask Ara to substitute template parameters and
  save a plan. No authoring tool starts a sequence.

See the [sequence-authoring recipe](docs/sequence-authoring.md) for the pinned
`lrgb-dso` example, validation limits, and review steps.

## Control-phase tools

- `begin_control`: require an active Ara profile, claim Ara's single-client session,
  and bind its session WebSocket before returning a local `control_id`.
- `end_control`: release the adapter's Ara session and invalidate that `control_id`;
  Ara runs continue. Process shutdown releases a held session best-effort.
- A transient WebSocket drop reclaims the same session after checking Ara's identity
  and liveness. Expiry or a server restart invalidates control and requires a new
  explicit `begin_control` call.
- Mutations built on this phase must use the serialized normal lane and bounded
  intent receipt ledger; lifecycle preflights fail closed on unknown run state. A
  reserved interrupt lane remains available when normal admission is saturated.

## Sequence-execution tools

- `start_sequence` re-reads the saved plan, checks Ara structural validation and the
  supported palette, rejects `ContinueOnError` plans, requires an active profile and
  connected required equipment, checks camera exposure/gain/offset/binning limits,
  and verifies each filter reference against profile labels and an available wheel slot.
- `pause_sequence`, `resume_sequence`, `stop_sequence`, and `abort_sequence` require
  the current `expected_run_id`; stop/abort use the reserved interrupt lane. Resume
  sends `recenter: false` and `refocus: false` to avoid implicit equipment actions.
- Execution commands return Ara's `accepted` receipt plus one immediate state
  observation. `get_sequence_state` is the authoritative read; accepted is not
  completion, and agent disconnect does not stop Ara's run.
- Ara remains the final execution authority. The RPi4 simulator lifecycle was tested
  on Ara build `34b59e6`; current-master/release compatibility and physical-rig support
  are separate claims.

See [T04](docs/plan.md#t04-ara-control-ownership-and-connection-lifecycle),
[T05](docs/plan.md#t05-sequence-authoring), and
[T06](docs/plan.md#t06-sequence-execution) for lifecycle contracts and compatibility
limits.

## Manual equipment tools

- `capture_exposure`, `abort_exposure`, and `set_camera_cooler` use Ara's camera API.
- `slew_telescope`, `park_telescope`, `unpark_telescope`, and
  `abort_telescope_slew` use Ara's telescope API. Telescope abort also pauses active
  sequences according to Ara's endpoint behavior.
- `move_focuser` and `run_autofocus` use Ara's focuser API; autofocus returns Ara's
  actual background job ID. `select_filter` selects an Ara-reported wheel slot.
- `start_guiding`, `stop_guiding`, and `dither_guiding` use Ara's PHD2 guider API;
  dither amplitude is in pixels. These return operation acceptance plus guider state,
  not proof that the guider has started, stopped, or finished dithering.
- `emergency_stop` returns Ara's synchronous per-rung best-effort result.
- Normal actions require the active control phase, pass the shared mutation ledger,
  refresh connected-device capabilities/status, and reject manual actions while an
  active or paused sequence is reported. Mount abort, exposure abort, and emergency
  stop use the reserved interrupt lane.
- Accepted operations remain accepted, not completed. Exposures return Ara's frame
  ID and autofocus returns its job ID; `get_job_status` and frame readers expose
  Ara's corresponding ephemeral job/frame results.
The T07 routes are source-verified and covered by fake-Ara/MCP contract tests against
the pinned Ara master commit `6374eede73383851486e6fb498a3311a3be58d82`. They have
not been exercised against a live daemon or physical equipment.

## Resource dashboard and exports

- Retrieve image previews and operation results.
- When the optional diagnostics listener is enabled, monitor process CPU, RSS/Go
  memory, goroutines, uptime, and freshness on the self-hosted live page at `/`.
- Download retained live resource statistics at `/resources.csv` or
  `/resources.jsonl`; both use the same ordered snapshot and support time/instance
  filters. Add `source=archive` to export retained archive records. Responses identify
  retained and exported ranges/counts and reject wholly unavailable ranges before streaming.
- Sampling cadence, retained history, dashboard subscriber, and concurrent export
  limits are configurable through Viper and validated before startup.
- Set `resource-archive-dir` to enable the bounded rotating JSONL archive and archive
  CSV/JSONL downloads; otherwise sampling remains in-memory only.

The Datastar frontend pair and optional archive are implemented. Remote/TLS browser
validation and board measurement remain for T10/O4/O5. See the [resource dashboard contract](docs/resource-dashboard.md)
and [architecture and API notes](docs/architecture.md)
for the existing Ara API and integration constraints, and the
[implementation plan](docs/plan.md) for delivery order and acceptance criteria.

## Development

Use Go **1.27.x**, preferably its latest patch release, and Git. The module declares
Go 1.27.0 and selects Go 1.27.1 as its default toolchain. With automatic toolchain
switching enabled, Go can download that toolchain when needed.

Follow the [development guide](docs/development.md) for setup, run/build/check
commands, testing, and agent workflows. Default checks are hardware-free; read-only
Ara simulator integration is opt-in.

Development and agent-local deployment should remain portable across Linux,
macOS, and Windows. Telescope-side deployment targets Linux ARM64 SBCs as described
above. These are intended targets, not a hardware-validation claim.

## Documentation

- [Architecture and Ara API](docs/architecture.md): responsibilities and upstream evidence.
- [Initial Ara/MCP contracts](docs/api-contracts.md): T01 tool surface, source evidence,
  and compatibility checks still required before implementation.
- [Development guide](docs/development.md): setup, checks, and integration testing.
- [Logging and observability](docs/observability.md): required logs, metrics, traces,
  health diagnostics, and their acceptance criteria.
- [Resource dashboard and exports](docs/resource-dashboard.md): process sampling,
  self-updating page, bounded statistics history, and CSV/JSONL download contracts.
- [Implementation plan](docs/plan.md): milestones, task dependencies, and task details.
- [First-release policies](docs/first-release-policy.md): resolved decisions,
  provisional resource budgets, and outstanding evidence with task owners.
- [Agent instruction index](docs/agent-instructions.md): which guidance to read for a task.

## Contributing and agent workflows

- [CONTRIBUTING.md](CONTRIBUTING.md): development, checks, and pull requests.
- [AGENTS.md](AGENTS.md): shared instructions for coding agents.
- [.agents/README.md](.agents/README.md): scoped guidance and reusable workflows.
- [.agents/skills/](.agents/skills/): public Go skills from JetBrains and samber,
  with upstream commits and content hashes recorded in [skills-lock.json](skills-lock.json).
- [GitHub Issues](https://github.com/cavenine/ara-mcp/issues): bugs and feature requests.

The `.agents/` setup is tool-neutral. Skills use the public Agent Skills format;
discovery depends on the client. Workflow documents in `.agents/commands/` are
read explicitly rather than assumed to be portable slash commands.

## Acknowledgements

Repository organization and agent workflows are inspired by
[AlpacaBridge](https://github.com/open-astro/AlpacaBridge). ara-mcp is an independent
project under the cavenine organization, not an official OpenAstro component.

## License

ara-mcp is licensed under the [GNU Affero General Public License v3 or later](LICENSE)
(`AGPL-3.0-or-later`). Contributions use the same license. No vendor-SDK linking
exception applies to this project. Bundled third-party skills retain their
[upstream licenses and attribution](.agents/README.md#third-party-sources-and-licenses).
