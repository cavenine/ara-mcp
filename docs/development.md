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

The repository has an in-progress Resty-backed Ara HTTP client and its hardware-free
contract tests, but no CLI or MCP listener yet. `go build ./...` does not produce a
server executable.

Implementation order and acceptance criteria are in [plan.md](plan.md).
Resolved choices and outstanding evidence are in
[first-release-policy.md](first-release-policy.md).
Add verified run commands and agent connection examples here when the executable
and transports are implemented; the plan's proposed interface is not runnable today.

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
| [docs/](.) | Architecture, development, observability, agent routing, and implementation plan |
| [AGENTS.md](../AGENTS.md) | Shared coding-agent constraints |
| [.agents/](../.agents/README.md) | Scoped rules, workflows, bundled skills, and their licenses |
| [skills-lock.json](../skills-lock.json) | Upstream skill commits and hashes |
| [.github/](../.github/) | CI, dependency updates, and contribution templates |

When executable code is added, put the entrypoint in `cmd/ara-mcp/` and private
code in focused `internal/` packages. These directories do not exist yet. Keep
Ara communication separate from MCP tool/transport handling without building
a plugin framework or duplicating logic between transports.

## Application commands and configuration

The selected application stack is **Cobra** for command/flag handling and **Viper**
for configuration. These dependencies and the executable will be added in T03;
no commands or configuration keys are implemented today.

Use explicit flags > environment > optional configuration file > defaults.
Environment variables use the `ARA_MCP` prefix. Resolve known keys into a typed,
validated configuration before opening connections; pass that configuration to
runtime components instead of reading global Viper state from handlers.

Command/configuration work loads `golang-cli`, `golang-spf13-cobra`, and
`golang-spf13-viper`, then applicable testing/safety guidance. The
[project rules](../.agents/rules/go.md#application-commands-and-configuration)
cover binding, missing-file behavior, output, and test isolation. TDD checks must
prove precedence, explicit false/zero inputs, validation failures, independent
command/config instances, and protocol-only stdout when serving stdio.

The full [skill index](agent-instructions.md#public-go-skills) also routes type/
generic safety and appropriate `lo`/`mo` use. Those helpers are selected for real
transformations or absence/result models, with Ara wire behavior preserved by tests.

## HTTP routing and middleware

HTTP serving uses **Chi v5**, Chi's middleware, and **go-chi/render**. T13 introduces
the basic diagnostics router; T09 adds HTTP MCP and T12 adds Datastar/dashboard
streams and exports. No HTTP listener is implemented today.
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

Live integration tests will be opt-in. Their connection configuration and commands
must be documented when implemented. Record the Ara version, adapter commit,
transport, OS/architecture, equipment if used, and operations exercised. Run
mutating tests only on an explicitly selected test setup. Keep simulator results,
cross-build results, and physical-rig results distinct.

## Logging and observability verification

[observability.md](observability.md) owns the required log schema, metrics, traces,
health behavior, and diagnostic access model. They are requirements today, not
implemented runtime features. Add their behavioral tests in the same red-green
slice as the operation being introduced.
The [resource dashboard contract](resource-dashboard.md) covers shared process
sampling, self-hosted SSE/HTML, bounded history, and CSV/JSONL exports (T03/T12).

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
adapter fault from an Ara operation failure. Once diagnostic serving is implemented,
document the actual listener, access configuration, scrape example, OTLP settings,
and profiling commands here or in the deployment guide. Do not require a remote
collector for normal tests or to start a local stdio adapter.

Add a reproducible opt-in integration recipe against an actual Ara daemon with
simulated equipment as the client/control/authoring tasks land, not only after
T10. Capture server build/API, profile/fixture setup, endpoint/WS results, and cleanup.
Include restart/upgrade and completed-run replay cases from the
[resolved recovery/operation policies](first-release-policy.md). Default tests
remain isolated; physical-rig results are separate evidence.

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
race tests, and build checks on Linux with Go 1.27.x. It does not currently test
macOS, Windows, or a physical ARM64 rig. Actions are SHA-pinned and updated by
[Dependabot](../.github/dependabot.yml).

Local-agent targets are Linux, macOS, and Windows. Telescope-side deployment
targets Linux ARM64 SBCs, including Raspberry Pi 3/4/5-class boards with a 64-bit OS
and limited CPU/RAM. Go 1.27 requires macOS 13 or newer on Darwin. Platform build
checks, binaries, and a service installation example are planned in
[T10](plan.md#t10-deployment-and-end-to-end-validation).
T03 introduces Linux amd64/arm64, macOS amd64/arm64, and Windows amd64 cross-build
checks when executable/OS-specific code lands, with native tests where available.
Current CI is still the setup-only Linux job; early platform checks are planned,
not a claim that those platforms are already tested.

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
