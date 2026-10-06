# Development guide

This guide owns setup and verification commands for ara-mcp. Read
[CONTRIBUTING.md](../CONTRIBUTING.md) for contribution policy and
[AGENTS.md](../AGENTS.md) for coding-agent constraints.

## Table of contents

- [Current state](#current-state)
- [Prerequisites](#prerequisites)
- [Set up the checkout](#set-up-the-checkout)
- [Repository layout](#repository-layout)
- [Application commands and configuration](#application-commands-and-configuration)
- [HTTP routing and middleware](#http-routing-and-middleware)
- [Build and checks](#build-and-checks)
- [Test-driven development](#test-driven-development)
- [Testing Ara integration](#testing-ara-integration)
- [Logging and observability verification](#logging-and-observability-verification)
- [Agent-assisted development](#agent-assisted-development)
- [CI and portability](#ci-and-portability)
- [Small-SBC validation](#small-sbc-validation)
- [Working from the plan](#working-from-the-plan)

## Current state

T02's Resty-backed Ara HTTP client, T03's executable stdio MCP server, T04's explicit
`begin_control`/`end_control` phase tools, T05's sequence-authoring tools, T06's
sequence-execution tools, T07 manual equipment actions, T09's authenticated
Streamable HTTP endpoint, T13's optional diagnostics HTTP listener, T08's job/frame
readers, previews, and owned-session event buffer, and T12's initial live/archive
resource dashboard and exports are implemented. Remote/TLS deployment validation and
T10 board measurements remain; no release is available yet.

Implementation order and acceptance criteria are in [plan.md](plan.md).
Resolved choices and outstanding evidence are in
[first-release-policy.md](first-release-policy.md).
The runnable command and agent configuration example are below. Ara is contacted
only when an Ara-backed tool is called, so local diagnostics remain available while
Ara is offline.

## Prerequisites

- **Go 1.27.x**, using the latest patch release in that line.
- **Git** for source control.
- **GitHub CLI (`gh`)** for issue/PR work, when needed.
- A C toolchain for `go test -race`; on Linux, GCC or Clang is sufficient.

`go.mod` declares Go 1.27.0 and selects Go 1.27.1 as its default toolchain. Older
local installations can fetch that toolchain when `GOTOOLCHAIN=auto` is enabled.
Use `go version` in the repository to confirm the selected version.

Node/npm are optional and used only to install or update the bundled agent skills.
They are not build or runtime dependencies. No Ara server or physical equipment
is required for the default checks.

## Set up the checkout

```sh
git clone https://github.com/cavenine/ara-mcp.git
cd ara-mcp
go version
go env GOTOOLCHAIN
go build ./...
```

For an existing checkout, run the commands from its root. Create a descriptive
feature branch before implementation; see [contribution conventions](../CONTRIBUTING.md).

`go mod download` restores the pinned runtime and test dependencies. Review module
and checksum changes when adding or updating a dependency.

## Repository layout

| Path | Responsibility |
| --- | --- |
| [go.mod](../go.mod) | Module path, language baseline, and default toolchain |
| [doc.go](../doc.go) | Initial package documentation |
| [cmd/ara-mcp/](../cmd/ara-mcp/main.go) | Executable, signal handling, and process boundary |
| [internal/app/](../internal/app/) | Cobra/Viper command, validated configuration, and stdio runtime |
| [internal/mcpserver/](../internal/mcpserver/) | Shared typed MCP tools and tool instrumentation |
| [internal/monitor/](../internal/monitor/) | Paced process/runtime sample |
| [internal/ara/](../internal/ara/) | Resty-backed Ara HTTP client |
| [docs/](.) | Architecture, development, observability, agent routing, and implementation plan |
| [AGENTS.md](../AGENTS.md) | Shared coding-agent constraints |
| [.agents/](../.agents/README.md) | Scoped rules, workflows, bundled skills, and their licenses |
| [skills-lock.json](../skills-lock.json) | Upstream skill commits and hashes |
| [.github/](../.github/) | CI, dependency updates, and contribution templates |

Keep the executable in `cmd/ara-mcp/` and private code in focused `internal/`
packages. Keep Ara communication separate from MCP tool/transport handling without
building a plugin framework or duplicating logic between transports.

## Application commands and configuration

The application uses **Cobra** for command/flag handling, **Viper** for configuration,
and the official MCP Go SDK v1.8.0. The current command surface is `serve` (stdio)
and `version`.

Use explicit flags > environment > optional configuration file > defaults.
Environment variables use the `ARA_MCP` prefix. Supported settings:

| Setting | Flag | Environment | Default |
| --- | --- | --- | --- |
| Ara base URL | `--ara-url` | `ARA_MCP_ARA_URL` | `http://127.0.0.1:5555` |
| Transport | `--transport` | `ARA_MCP_TRANSPORT` | `stdio` |
| Log level | `--log-level` | `ARA_MCP_LOG_LEVEL` | `info` |
| Ara timeout | `--timeout` | `ARA_MCP_TIMEOUT` | `10s` |
| GET retries | `--read-retries` | `ARA_MCP_READ_RETRIES` | `0` (maximum 2) |
| HTTP MCP listen address | `--http-listen` | `ARA_MCP_HTTP_LISTEN` | `127.0.0.1:8080` |
| HTTP MCP bearer token | config file only | `ARA_MCP_HTTP_BEARER_TOKEN` | unset; required for HTTP |
| Additional trusted HTTP Origins | `--http-origins` | `ARA_MCP_HTTP_ORIGINS` | same-origin only |
| HTTP MCP TLS certificate/key | `--http-tls-cert` / `--http-tls-key` | `ARA_MCP_HTTP_TLS_CERT` / `ARA_MCP_HTTP_TLS_KEY` | unset; required for non-loopback |
| Diagnostics listener | `--diagnostics-listen` | `ARA_MCP_DIAGNOSTICS_LISTEN` | disabled |
| Diagnostics Basic-auth username/password | not exposed as CLI flags | `ARA_MCP_DIAGNOSTICS_USERNAME` / `ARA_MCP_DIAGNOSTICS_PASSWORD` | unset |
| Diagnostics TLS certificate/key | `--diagnostics-tls-cert` / `--diagnostics-tls-key` | `ARA_MCP_DIAGNOSTICS_TLS_CERT` / `ARA_MCP_DIAGNOSTICS_TLS_KEY` | unset |
| Resource sample interval | `--resource-sample-interval` | `ARA_MCP_RESOURCE_SAMPLE_INTERVAL` | `2s` (250ms–1m) |
| Resource history samples | `--resource-history-samples` | `ARA_MCP_RESOURCE_HISTORY_SAMPLES` | `1800` (maximum) |
| Resource history age | `--resource-history-age` | `ARA_MCP_RESOURCE_HISTORY_AGE` | `1h` (maximum) |
| Dashboard subscribers | `--dashboard-subscriber-limit` | `ARA_MCP_DASHBOARD_SUBSCRIBER_LIMIT` | `4` (maximum) |
| Concurrent resource exports | `--resource-export-limit` | `ARA_MCP_RESOURCE_EXPORT_LIMIT` | `2` (maximum) |
| Resource archive directory | `--resource-archive-dir` | `ARA_MCP_RESOURCE_ARCHIVE_DIR` | unset; memory-only |

HTTP mode serves the SDK Streamable HTTP endpoint at `/mcp`. The bearer token is
intentionally not exposed as a CLI flag, avoiding process-list disclosure. Use a
config file or environment variable. Browser cross-origin requests are rejected by
Go's `CrossOriginProtection` unless the exact Origin is configured in `http-origins`;
the MCP SDK also retains its localhost Host protection. Non-loopback listeners need
TLS. A trusted reverse proxy may terminate external TLS and connect to a loopback
listener. Authenticated HTTP agents share the adapter's single Ara control identity.
The optional diagnostics listener is independent of the MCP transport and starts
alongside stdio when configured. Bind it to loopback for local access. Non-loopback
addresses require Basic-auth credentials and a valid TLS certificate/key pair;
diagnostics credentials are separate from MCP credentials. Endpoints are
`/healthz` (process liveness), `/readyz` (Ara API reachability), `/status` (adapter
and sampler state), and `/metrics` (Prometheus exposition). TLS is terminated by
ara-mcp for configured certificates; do not expose plaintext diagnostics remotely.
Explicitly selected config files must exist and parse; no file is required. YAML:

```yaml
ara-url: http://127.0.0.1:5555
transport: stdio
log-level: info
timeout: 10s
read-retries: 0
diagnostics-listen: 127.0.0.1:9090
resource-sample-interval: 2s
resource-history-samples: 1800
resource-history-age: 1h
dashboard-subscriber-limit: 4
resource-export-limit: 2
# resource-archive-dir: /var/lib/ara-mcp/resources
```

Resolve keys once into a typed, validated configuration before constructing the
client or transport. Runtime handlers do not read global Viper state.

Command/configuration work loads `golang-cli`, `golang-spf13-cobra`, and
`golang-spf13-viper`, then applicable testing/safety guidance. The
[project rules](../.agents/rules/go.md#application-commands-and-configuration)
cover binding, missing-file behavior, output, and test isolation. TDD checks must
prove precedence, explicit false/zero inputs, validation failures, independent
command/config instances, useful help/version without Ara, and protocol-only stdout
when serving stdio.

### Running and connecting over stdio

```sh
go run ./cmd/ara-mcp version
ARA_MCP_ARA_URL=http://127.0.0.1:5555 go run ./cmd/ara-mcp serve
go run ./cmd/ara-mcp --config ./ara-mcp.yaml serve
```

An agent configuration uses its supported MCP config format. Generic server entry:

```json
{
  "mcpServers": {
    "ara-mcp": {
      "command": "/absolute/path/to/ara-mcp",
      "args": ["--ara-url", "http://127.0.0.1:5555", "serve"]
    }
  }
}
```

### Running and connecting over HTTP

Set a random bearer token of at least 32 characters, then start the persistent
service:

```sh
ARA_MCP_TRANSPORT=http \
ARA_MCP_HTTP_BEARER_TOKEN='replace-with-a-long-random-secret' \
ARA_MCP_HTTP_LISTEN=127.0.0.1:8080 \
go run ./cmd/ara-mcp serve
```

Configure the MCP client for `http://127.0.0.1:8080/mcp` and provide
`Authorization: Bearer <token>` using the client's supported HTTP-header setting.
Do not put credentials in URLs or logs. Additional browser client Origins can be
listed with `ARA_MCP_HTTP_ORIGINS` or `http-origins` in the config file. Remote
listeners require `ARA_MCP_HTTP_TLS_CERT` and `ARA_MCP_HTTP_TLS_KEY`. Diagnostics
remain a separate optional listener with independent credentials.

The process writes JSON logs to stderr and MCP frames to stdout only. Available
tools include `get_server_context`, `get_rig_context`, `list_sequences`,
`get_sequence`, `get_sequence_state`, `list_sequence_templates`,
`validate_sequence`, `get_adapter_diagnostics`, `get_job_status`, `list_frames`,
`get_frame`, and `get_frame_preview`. With T04 control configured, it also exposes
`begin_control`, `end_control`, `get_recent_ara_events`, `create_sequence`, `update_sequence`, and
`instantiate_sequence_template`, plus T06's `start_sequence`, `pause_sequence`,
`resume_sequence`, `stop_sequence`, and `abort_sequence`. Starts verify saved-body
validity, camera-reported exposure/gain/offset/binning limits, connected required
devices and profile/physical filter slots; Ara's own execution guards remain
authoritative. Starts reject `ContinueOnError` plans. Commands report acceptance
separately from the immediate state observation. No named MCP host compatibility is
claimed yet.

T07 adds `capture_exposure`, `abort_exposure`, `set_camera_cooler`,
`slew_telescope`, `park_telescope`, `unpark_telescope`, `abort_telescope_slew`,
`move_focuser`, `run_autofocus`, `select_filter`, `start_guiding`, `stop_guiding`,
`dither_guiding`, and `emergency_stop`. Normal
actions require T04 control, connected device/capability preflight, and a fresh run
snapshot; active/paused runs reject manual actions. Ara's accepted response remains
distinct from frame/job identifiers and immediate device state. `get_job_status`,
`list_frames`, `get_frame`, and `get_frame_preview` expose the corresponding Ara
records; previews are capped at 1 MiB. `get_recent_ara_events` reads only the existing
adapter-owned socket, reports resume/retention gaps, and never opens an unbound monitor
connection.

The stdio tool set includes `begin_control` and `end_control` in addition to the
read tools below. Begin requires an active Ara profile and binds the session WebSocket;
ending or shutting down ara-mcp releases the slot without stopping Ara work. See the
[T04 policy and remaining verification](first-release-policy.md#cooperative-control).

The full [skill index](agent-instructions.md#public-go-skills) also routes type/
generic safety and appropriate `lo`/`mo` use. Those helpers are selected for real
transformations or absence/result models, with Ara wire behavior preserved by tests.

## HTTP routing and middleware

HTTP serving uses **Chi v5**, Chi's middleware, and **go-chi/render**. T13 implements
the diagnostics router; T09 mounts Streamable HTTP MCP at `/mcp`; T12 adds
Datastar/dashboard streams and exports.
The [architecture](architecture.md#http-stack) records the framework/protocol boundary,
and the [HTTP rules](../.agents/rules/ara-mcp.md#http-routing-and-middleware) own composition.

Mount the MCP SDK's Streamable HTTP handler directly in the Chi router. Use render
for ordinary diagnostic payloads and let the SDK retain MCP JSON-RPC/SSE framing.
Connect Chi `RequestLogger` and recovery to the shared JSON `slog` logger on stderr.
Scope ordinary-route middleware so it does not buffer or time out a valid stream.

HTTP work loads applicable context, observability/slog, error-handling, security,
and testing skills, using the official Chi/render documentation for their APIs.
RED/GREEN checks go through the entire router: IDs/correlation, structured access
denial and panic logs, normalized unmatched routes, rendered status/content type,
writer flushing, and stream cancellation. Use an actual SDK HTTP client/stream
smoke test alongside `httptest`; a JSON-only recorder cannot prove SSE compatibility.

The [HTTP observability requirements](observability.md#http-middleware-and-streaming)
define final request fields and distinguish stream lifetime from tool latency.

## Build and checks

Run from the repository root:

```sh
gofmt -l .
go mod tidy -diff
go vet ./...
go test -race -shuffle=on ./...
go build ./...
```

- `gofmt -l .` must print no paths. Use `gofmt -w .` to format first-party Go code.
- `go mod tidy -diff` must produce no diff. For an intentional dependency change,
  run `go mod tidy`, review the result, then repeat the check.
- Vet, tests, and build must exit successfully. `[no test files]` is expected for
  the current setup; it is not evidence of tested runtime behavior.

To check one implemented package, replace `./...` with its path. To reproduce a
particular test, use `go test -race -count=1 -run 'TestName' ./path/to/package` once
that package and test exist.

For CI configuration changes, the workflow can also be checked locally:

```sh
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/ci.yml
```

This runs a development tool without adding it to the application's dependencies.
Inspect `git status`, working/staged diffs, and new files before submitting work.
Leave unrelated user changes intact.

## Test-driven development

Runtime features and bug fixes use **red → green → refactor**, in small vertical
slices through the real adapter boundary. This includes request handling,
configuration parsing, logging/redaction, metrics, health, and connection recovery.

Before code changes, define the next observable behavior from the selected plan
task and assess the applicable skills. Load
[golang-testing](../.agents/skills/golang-testing/SKILL.md) and the harness's TDD
skill when available. The latter is a workflow aid, not a required installed
runtime dependency; this section is the portable repository process.

For each behavior:

1. **RED:** write one test against the client/tool/transport contract and run it.
   It must fail for the intended missing or incorrect behavior, not a broken
   fixture, unavailable dependency, or unrelated environment problem. For a bug,
   reproduce it before applying the fix. Do not count an unrun test as RED.
2. **GREEN:** implement the smallest working change and rerun that same test.
   Confirm that it passes and that the relevant existing tests remain green.
3. **REFACTOR:** simplify code or remove duplication while preserving behavior.
   Rerun affected tests after refactoring. Never refactor while RED.
4. Repeat for the next behavior. Add important failure/boundary cases incrementally,
   then run the normal vet, shuffled race-test, and build checks.

For a real package/test, the focused command is:

```sh
go test -count=1 -run '^TestName$' ./path/to/package
```

Replace the placeholders with the actual package/test. The T02 client tests run with
`go test -race -count=1 ./internal/ara`.

Tests must survive internal refactoring: exercise observable interfaces with
`httptest` upstreams or MCP SDK clients, not duplicated implementation code or
expectations about private helper calls. Use deterministic synchronization and
Go's supported testing/time tools for lifecycle tests; avoid sleeps as proof that
an asynchronous operation completed. Hardware-free contract integration belongs
in default checks; live Ara/rig tests remain explicitly opt-in.

### Testify applicability

Use Go's `testing` package as the entrypoint. Testify is an optional assertion/mock
helper, not a replacement for `testing` or a requirement to introduce suites.
When selected or already present, load
[golang-stretchr-testify](../.agents/skills/golang-stretchr-testify/SKILL.md) as well.
Use `require` for prerequisites and `assert` for remaining checks, and bind helpers
to each subtest's own `*testing.T`. Add a dependency only when the tests need it.

### Evidence and completion

Record the exact focused command, expected RED failure, GREEN result, and final
checks in the PR or task verification notes. Retain the test in the repository.
Refactoring an existing tested path needs a green baseline and final green checks;
new or changed behavior needs a new RED/GREEN cycle. Explain an existing failing
baseline rather than representing it as the new behavior's RED result.

Do not write all tests for a task up front and then all implementation. Coverage
percentages and passing empty suites are not evidence that the specified behavior
works. Documentation-only changes use link/anchor, consistency, and command review
instead of artificial runtime tests. T10 deployment checks extend these tests;
they do not replace earlier RED/GREEN evidence.

## Testing Ara integration

Default tests should use Go's `testing` package, `httptest` servers for Ara HTTP
contracts, and MCP SDK in-memory transports where appropriate. Keep tests next
to the implementation and fixtures under the owning package's `testdata/`.

Cover observable behavior: request fields/units, malformed responses, upstream
problem details, pagination, deadlines, asynchronous acceptance, and uncertain
mutation outcomes. Test a shared handler once at its boundary, then use transport
checks to establish that stdio and HTTP expose the same behavior.

Ara's API declarations are not enough to establish support. Recheck backing
services against the targeted Ara version. The current evidence and known gaps
are in [architecture.md](architecture.md#contract-details-to-preserve).

Live integration tests are opt-in. Record the Ara version, adapter commit,
transport, OS/architecture, equipment if used, and operations exercised. Run
mutating tests only on an explicitly selected test setup. Keep simulator results,
cross-build results, and physical-rig results distinct.

## Logging and observability verification

[observability.md](observability.md) owns the required log schema, metrics, traces,
health behavior, and diagnostic access model. T03 implements stderr JSON logs,
tool-call metrics/traces, Ara request correlation, and local process diagnostics for
stdio. T13 adds optional Prometheus exposition and HTTP diagnostic probes.
The [resource dashboard contract](resource-dashboard.md) covers the self-hosted
SSE/HTML page, bounded history, and CSV/JSONL exports (T12).

Use captured stderr/parsed log records, in-process metric registries, and in-memory
trace exporters. Assert correlation, redaction, bounded labels, accepted versus
terminal results, clean shutdown, and collector-failure behavior. A stdio smoke
test must still parse every stdout message as MCP traffic. Avoid assertions on
exact timestamps, random IDs, or unrelated record ordering.

Use controlled resource counters/clocks to test CPU deltas, warmup/availability,
history eviction, and snapshots. Parse CSV/JSONL exports and compare them to the
retained samples. Through Chi, test live updates, slow/cancelled subscribers and
downloads, auth boundaries, and no sampler locks held during writes. Add a browser
smoke check showing automatic updates with locally served assets and no external
network dependency. T10 measures overhead rather than relying on flaky RSS assertions.

Live checks must show how to retrieve stderr/journald logs and distinguish an
adapter fault from an Ara operation failure. T13's diagnostics listener and
Prometheus scrape endpoint are optional and disabled by default; OTLP exporting and
profiling remain unimplemented. Do not require a remote collector for normal tests
or to start a local stdio adapter.

Add a reproducible opt-in integration recipe against an actual Ara daemon with
simulated equipment as the client/control/authoring tasks land, not only after
T10. Capture server build/API, profile/fixture setup, endpoint/WS results, and cleanup.
Include restart/upgrade and completed-run replay cases from the
[resolved recovery/operation policies](first-release-policy.md). Default tests
remain isolated; physical-rig results are separate evidence.

### Read-only Ara simulator recipe (opt-in)

Use the Ara development build and OmniSim versions recorded in
[api-contracts.md](api-contracts.md#pinned-alpaca-simulator-check-2026-10-04), with
Ara's API bound to loopback and no route from the simulator to physical equipment.
Connect simulated devices in Ara, then launch ara-mcp against that daemon:

```sh
ARA_MCP_ARA_URL=http://127.0.0.1:15555 go run ./cmd/ara-mcp serve
```

From an MCP client, list tools and call `get_server_context`, `get_rig_context`,
`list_sequences`, and `get_sequence` for a saved test sequence. The rig context
must show selected simulator devices as available and unselected devices as
explicitly unavailable. `get_adapter_diagnostics` reports local process data and
Ara reachability. This recipe uses read-only operations only. Record the daemon
commit, OmniSim version, adapter commit, OS/architecture, and observations. The
T01 simulator results are source evidence; this T03 MCP flow has not yet been run
against the live daemon.

### T04 Ara control-session check (opt-in)

The integration-tagged Ara test exercises the real session claim, session-bound
WebSocket/version and resume headers, server heartbeat/pong, same-session re-claim,
takeover rejection, and release. It sends no equipment command. The test skips when
Ara already reports a control owner; a fresh claim is an Ara user-activity event.
For a remote daemon, forward its loopback listener over SSH:

```sh
ssh -N -L 15555:127.0.0.1:5555 user@ara-host
ARA_MCP_LIVE_ARA_URL=http://127.0.0.1:15555 go test -tags=integration -count=1 -run '^TestLiveAraControlSessionAndHeartbeat$' ./internal/ara
ARA_MCP_LIVE_ARA_URL=http://127.0.0.1:15555 go test -tags=integration -count=1 -run '^TestLiveAraBeginControlRequiresProfileWithoutClaimingSlot$' ./internal/mcpserver
ARA_MCP_LIVE_ARA_URL=http://127.0.0.1:15555 go test -tags=integration -count=1 -run '^TestLiveAraBeginControlUsesProfileRepositorySelection$' ./internal/mcpserver
ARA_MCP_LIVE_ARA_URL=http://127.0.0.1:15555 go test -tags=integration -count=1 -run '^TestLiveAraSequenceStartAndStateWithPinnedOmniSim$' ./internal/mcpserver
```

The manager test only attempts begin when Ara reports no active profile and confirms
the control slot remains free. Record Ara build/API identity, platform, adapter commit,
and test result; do not infer physical-equipment behavior from these session checks.
The profile-selection test claims/releases control only when `/profiles.active_id` is
set and the Ara control session is free; it sends no equipment command.
The T06 test requires the `ara-mcp-t06-omnisim` profile and pinned simulator camera
ID. It runs a short simulated capture, then exercises pause/resume/stop and abort on a
bounded loop, and removes its test sequences on cleanup. It sends no telescope action.

## Agent-assisted development

Start with [agent-instructions.md](agent-instructions.md) to select rules and skills.
Evaluate the task and available skill descriptions before editing, then load only
applicable guidance. The bundled samber `golang-how-to` routes Go tasks to relevant
skills; testing and instrumentation add the supporting skills listed above. Before
editing Go code, JetBrains' `use-modern-go` supplies version-aware idioms:

```sh
sh .agents/skills/use-modern-go/scripts/run-tool.sh list --file-path doc.go
```

Replace `doc.go` with the file being edited. Read the full list. Request `explain`
only for applicable guideline IDs, following the skill's instructions. On Windows,
use the bundled PowerShell wrapper instead.

The wrapper installs its versioned CLI into the user's cache on first use. Skill
installation/update commands and preserved upstream licenses are documented in
[.agents/README.md](../.agents/README.md#third-party-sources-and-licenses).

## CI and portability

[CI](../.github/workflows/ci.yml) runs formatting, module metadata, vet, shuffled
race tests, builds, and cross-builds on Linux with Go 1.27.x. It does not run native
macOS/Windows tests or a physical ARM64 rig. Actions are SHA-pinned and updated by
[Dependabot](../.github/dependabot.yml).

Local-agent targets are Linux, macOS, and Windows. Telescope-side deployment
targets Linux ARM64 SBCs, including Raspberry Pi 3/4/5-class boards with a 64-bit OS
and limited CPU/RAM. Go 1.27 requires macOS 13 or newer on Darwin. CI cross-builds
Linux amd64/arm64, macOS amd64/arm64, and Windows amd64; native platform tests,
release binaries, and a service installation example remain in
[T10](plan.md#t10-deployment-and-end-to-end-validation). Cross-builds do not
establish runtime or hardware support.

## Small-SBC validation

[The resource contract](architecture.md#deployment-targets-and-resource-constraints)
applies throughout implementation, not just packaging. Respect Ara's independent
requirements and leave measured headroom for camera downloads, guiding, plate
solving, and the OS when services are co-located.

Record an initial release-build baseline in T03, then validate representative
low-resource hardware in T10. Record board/model, total and available RAM,
OS/architecture, Go version, adapter commit/configuration, co-located workloads,
payload/client counts, and thermal/throttling conditions. Compare the same toolchain
and workload; a race build or active profiler is not the normal production footprint.

T03 local baseline (2026-10-05): stripped `go build -trimpath -ldflags '-s -w'`
binary on Linux/amd64 with Go 1.27.1, no Ara calls, idle for 2 seconds: 3 ms to
startup log, 14,204 KiB RSS, 0.0% CPU rounded by `ps`, and 7 process threads.
With one SDK `get_server_context` call against a local test HTTP endpoint, the
process returned the result 18 ms from launch and used 16,980 KiB RSS, 0.0% rounded
CPU, and 17 threads. These are container-host observations, not Pi/SBC or
co-located-workload claims.

Measure startup/idle and sustained normal/busy behavior: CPU, resident memory,
live heap/allocations/GC, goroutine/connection counts, queue/backlog sizes, and tool
latency. Exercise reconnect storms, slow consumers, maximum accepted payloads and
preview sizes, cleanup, and default versus optional telemetry export. Run benchmark
comparisons serially and use the benchmark/performance skills for relevant analysis.

Use deterministic RED/GREEN tests for limit/backpressure and cleanup behavior.
Measure footprint separately rather than asserting unstable OS memory figures in
ordinary unit tests. A constrained test environment is useful evidence, but board-
specific support claims require results on that hardware. Publish recommended
defaults/minimum resources only after measurements and co-located headroom checks.

Keep Go runtime defaults initially. Deployment tuning may use `GOMEMLIMIT` and
`GOMAXPROCS` based on measurements; document the chosen values when used.
`GOMEMLIMIT` is a soft Go-managed-memory target, not a hard process/RSS limit, and
does not replace payload/concurrency limits. Do not tune for throughput at the
expense of rig-service responsiveness or correct operation outcomes.

## Working from the plan

Choose a ready task from [the task list](plan.md#task-list), inspect its dependencies,
and work to its acceptance criteria. Add tests with non-trivial behavior rather
than leaving all verification to the last task. Record results and update the
task's status only after those criteria hold.

The first useful implementation milestone is a local stdio agent reading real
Ara state. Sequence authoring, ownership-aware execution, equipment commands,
and the HTTP service build on that same adapter.
