# Agent instructions

This file owns the shared agent rules for ara-mcp. Read it in full before planning,
editing, or reviewing. Then read the [agent instruction index](docs/agent-instructions.md)
and the scoped guidance relevant to the task. `.agents/` is tool-neutral; do not
assume a client automatically loads its workflows or rules.

## Task-based skill selection

Before starting work, evaluate the actual task: its intent, affected code/docs,
interfaces, failure modes, and verification needs. Inspect the skills available
in the current harness and the bundled `.agents/skills/` inventory. Choose and
load the primary skill plus relevant supporting skills before making changes.
Use descriptions and applicability, not just matching keywords or a skill's
presence. Follow necessary references and reassess when the scope changes.

For Go tasks, use `golang-how-to` to route that assessment. Testing tasks require
`golang-testing`; use `golang-stretchr-testify` when writing, reviewing, or choosing
Testify assertions/mocks. Application commands use `golang-cli` and
`golang-spf13-cobra`; configuration uses `golang-spf13-viper`. HTTP routing and
middleware use the Chi project rules plus relevant context, logging, security,
and testing skills. Type/nil/resource safety uses `golang-safety`; collection
transforms and optional/result models
use `golang-samber-lo` and `golang-samber-mo` when applicable. Logging/observability
tasks require the signal skills listed below. The instruction index routes all
bundled skills by task, including debugging, security, CI, and conditional libraries.
If the harness supplies a TDD skill, load it for runtime features and bug fixes.
Otherwise follow the repository's documented red-green workflow.
Read local `SKILL.md` files when installed skills are not exposed by the harness.
Do not load unrelated library/framework skills or the entire collection by default.

## Public Go skills

Before any Go coding, review, debugging, troubleshooting, or setup task, load the
`samber/cc-skills-golang@golang-how-to` skill first — it routes to whichever other
Go skills the task needs. Its local entrypoint is
[.agents/skills/golang-how-to/SKILL.md](.agents/skills/golang-how-to/SKILL.md).
Load the relevant skills it selects, following cross-references as needed.

Before editing Go code, also load
[JetBrains' use-modern-go skill](.agents/skills/use-modern-go/SKILL.md) and run its
version-aware guidance command. Use the Go version from `go.mod` (1.27.x).

Read local `SKILL.md` files directly if the harness cannot invoke installed skills.
Bundled skills are unchanged upstream material. Repository-specific constraints
here take precedence over generic templates, especially stdio logging, minimal
dependencies, and Ara ownership. A library/framework skill is guidance for when
that library is actually used, not a reason to add the dependency.

## Project and boundaries

ara-mcp is a Go MCP adapter to OpenAstro Ara, using the official
`github.com/modelcontextprotocol/go-sdk`. Read [README.md](README.md) for current
status and [docs/architecture.md](docs/architecture.md) for the call flow and API
evidence. T02's Ara HTTP client and T03's stdio server/read-only tools are implemented;
HTTP transport and mutation tools remain planned.

The call flow is always:

```text
AI agent -> ara-mcp -> Ara server -> AlpacaBridge/Alpaca server -> equipment
```

- Ara owns equipment operations, sequences, persistence, and run state.
- Reuse Ara's REST/WebSocket surface; do not add a second sequencer or hardware
  driver path here.
- Keep the same tool implementation for stdio and Streamable HTTP.
- Verify endpoint handlers and actual service behavior, not just DTOs or OpenAPI
  declarations. Cite the Ara version/commit for compatibility claims.
- Keep planned features, mock-tested behavior, and live rig validation distinct.
- Follow [first-release policies](docs/first-release-policy.md) for resolved control,
  command/preflight, access, recovery, resource/default, and delivery decisions.
  Its outstanding table records evidence still needed; do not turn it into a
  claim that runtime behavior or hardware has already been validated.

## Go and repository structure

- The module is `github.com/cavenine/ara-mcp`; use Go 1.27.x. `go.mod` declares the
  minimum language version and default toolchain; CI follows the 1.27 patch line.
- Prefer Go's standard library and existing helpers before adding dependencies.
- Keep packages small. Introduce `cmd/ara-mcp/` and `internal/` packages when there
  is executable/private implementation to put in them, not empty scaffolding.
- Use concrete types; add interfaces only at a real boundary that needs them.
- Use **Cobra** for application commands/flags and **Viper** for configuration.
  Their resolved configuration is typed, validated, and passed to the application;
  runtime handlers do not depend on global command/configuration state.
- Use **Chi v5** for HTTP routing, its middleware for HTTP cross-cutting concerns,
  and **go-chi/render** for ordinary HTTP payloads. Mount the MCP SDK's HTTP handler
  unchanged at the protocol boundary. Follow the
  [HTTP rules](.agents/rules/ara-mcp.md#http-routing-and-middleware).
- Prefer concrete models and compile-time generics over `any`, reflection, or
  unchecked type assertions when the type set is known. Use typed MCP SDK tools.
  Opaque upstream sequence JSON remains an explicit dynamic boundary.
- Apply `golang-safety` to nil/zero values, collection ownership, numeric bounds,
  and resource cleanup. Use `lo` for suitable finite transforms and `mo` for useful
  typed absence/result composition, following [.agents/rules/go.md](.agents/rules/go.md).
- Keep generated binaries under ignored `bin/` or `dist/`, and coverage under
  ignored `coverage/`. Never commit local configuration or credentials.
- First-party Go files carry a copyright notice and the SPDX identifier
  `AGPL-3.0-or-later`. Bundled `.agents/skills/` material retains its upstream
  licenses; do not relabel it as first-party code. Keep the upstream copies
  unmodified, with provenance in `skills-lock.json` and `.agents/README.md`.
- Logs go to stderr. Stdout is reserved for MCP in stdio mode.

## Small-SBC resource constraints

The adapter is intended for Raspberry Pi 3/4/5-class SBCs on 64-bit Linux, including
CPU- and memory-limited configurations. It may share resources with Ara,
AlpacaBridge, guiding, and plate solving. Preserve headroom for those workloads.
Follow [the resource contract](docs/architecture.md#deployment-targets-and-resource-constraints):
bound concurrency, sessions, payloads/previews, event retention, and telemetry
queues; reuse connections and avoid busy polling/reconnect loops. Limits must
produce explicit backpressure/errors without dropping critical results or
repeating equipment commands. Measure before adding caches, pools, or parallelism.
Use benchmark/performance skills for relevant work and report target-board evidence;
an ARM64 cross-build or a desktop benchmark does not prove SBC suitability.

## Logging and observability

Follow [docs/observability.md](docs/observability.md) for the required logging,
metrics, tracing, health, and diagnostic contracts. Load `golang-observability`,
`golang-samber-slog`, and `golang-error-handling` when the task affects these areas.
Use native `log/slog` first and context-correlated, sanitized structured records.
Chi HTTP request logging uses `middleware.RequestLogger` with a `slog` formatter;
recovery shares that structured path. Preserve SSE flushing and SDK response framing.
Implement and test a feature's signals with its behavior, not as release-time cleanup.
Telemetry must distinguish accepted work from observed completion and never change
Ara control ownership or repeat an equipment command.
The application must monitor its own CPU/memory/goroutine and related usage, with
a self-hosted, self-updating SSE dashboard and CSV/JSONL downloads. Follow
[docs/resource-dashboard.md](docs/resource-dashboard.md): one shared paced sampler,
bounded retained history/subscribers/exports, explicit units/freshness, and locally
served lightweight assets. Dashboard SSE and file encodings are separate from MCP
framing. No collector or browser viewer is required to collect local samples.

## Integration and units

- Creating/saving a sequence and starting it are separate operations.
- Cooperative control is assumed: while MCP controls the rig, the user does not
  mutate via Ara's UI. One adapter controls the rig; other MCP clients share its
  command arbitration. Do not introduce a new UI lock or claim server-enforced
  REST ownership. Explicit begin/end control never implicitly stops a run.
- Read-only Ara monitoring uses GETs/shared polling outside a valid adapter-owned
  session; do not auto-open unbound WS connections or auto-reclaim expired control.
- Serialize normal mutations, reject conflicting active-run manual actions, retain
  a reserved stop/abort lane, and never auto-retry an uncertain mutation. Apply the
  control/intent/run-ID and completion contracts in the first-release policy.
- HTTP 202 is acceptance, never proof of completion.
- Preserve Ara's validation and ownership rules. Do not bypass them by calling
  AlpacaBridge directly or silently taking over the human client's session.
- A client or adapter disconnect does not stop an Ara run. Reconcile current
  state on reconnect; never manufacture a completed or stopped result.
- Document units in tool schemas: exposure in seconds, RA in hours, declination
  and other angles in degrees, temperature in degrees Celsius, timestamps in UTC.
  Confirm each field against the upstream contract before implementing it.
- Never rely on Ara's advertised `DryRun` option without verifying execution.

## Checks and review

Runtime features and bug fixes must use **red → green → refactor**, one observable
behavior at a time. Write and run the failing test before the implementation/fix;
confirm its failure is caused by the intended missing behavior. Make the smallest
change to pass, then refactor only while green. Keep the test as regression coverage.
Do not write a batch of speculative tests and then a batch of implementation.
The canonical process and RED/GREEN evidence requirements are in
[docs/development.md](docs/development.md#test-driven-development).

Use the commands in [the development guide](docs/development.md#build-and-checks):
format, module metadata, vet, race-test, and build. Default tests must be
hardware-free. Use focused standard-library tests for non-trivial behavior and
`httptest` for upstream HTTP contracts.

Before fixing a shared function, trace its callers and sibling paths. Apply the
root-cause fix consistently across transports and tools. For network changes,
cover errors, deadlines, malformed responses, and ambiguous mutation outcomes
where relevant. Do not introduce a test framework just for convenience.

Report exact RED/GREEN checks and their results for behavior changes, plus required
final checks. Documentation-only changes need documentation verification, not
contrived runtime tests. A package with no tests is not behavior coverage; a
successful cross-build is not hardware validation.

## Git and GitHub

- Issues and pull requests live in `cavenine/ara-mcp`; use `gh` for GitHub tasks.
- Keep changes focused and inspect existing user work before overwriting files.
- Only commit, push, tag, release, or open a pull request when explicitly requested.
  Review of an uncommitted change is not permission to commit it.
- Use concise imperative commit subjects and the repository's PR template.
- Update docs and `CHANGELOG.md` when observable behavior changes. No release
  version or tag exists yet; do not invent one during ordinary development.
- Keep shared rules here; scoped details belong in `.agents/rules/`. Record new
  durable lessons in the owning document with evidence, rather than duplicating
  a rule in every workflow.
- [docs/plan.md](docs/plan.md) owns implementation task scope and acceptance criteria.
  Read the relevant task before work and update its status with verification evidence.
