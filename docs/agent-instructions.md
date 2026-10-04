# Finding the instructions for a task

Read [AGENTS.md](../AGENTS.md) in full before planning, editing, or reviewing.
It owns shared project constraints. This document routes tasks to the relevant
rules, skills, workflows, and implementation context; it does not duplicate them.

## Table of contents

- [Required context](#required-context)
- [Evaluate the task and select skills](#evaluate-the-task-and-select-skills)
- [Scoped guidance](#scoped-guidance)
- [Public Go skills](#public-go-skills)
- [Task workflows](#task-workflows)
- [Keeping instructions current](#keeping-instructions-current)

## Required context

- [README](../README.md): current capability and implementation status.
- [Architecture](architecture.md): responsibilities, upstream API evidence, and
  integration constraints.
- [Development guide](development.md): setup, checks, and testing practices.
- [Implementation plan](plan.md): task scope, dependencies, and acceptance criteria.
- [First-release policy](first-release-policy.md): resolved review choices,
  provisional defaults, and owner-assigned outstanding verification.
- [Logging and observability](observability.md): required signals and their verification,
  when the task affects runtime behavior or diagnostics.

Before starting a planned task, read its details and dependencies. Select rules by
affected behavior as well as file path: a shared client change can affect every
tool and both transports. Revisit this index when the scope expands.

## Evaluate the task and select skills

1. Identify the user's goal and the behaviors/interfaces the task changes. Include
   verification, error handling, lifecycle, logging, and observability where affected.
2. Check the current harness's skill descriptions and the bundled skill inventory.
   Start Go work with `golang-how-to`; use its routing plus the task's actual needs.
3. Load the primary skill and applicable supporting skills before editing. For a
   behavior change, include TDD/testing guidance. Add signal skills when instrumentation
   or diagnostic contracts are involved; load library-specific skills when that
   library is used or is being evaluated.
4. Follow relevant references, keep repository constraints authoritative, and
   reassess if the task expands. Read local skill files if the harness cannot load them.

The following tables route the bundled catalog, not an instruction to load every
skill. Available skills must be evaluated for each task; do not infer
relevance solely from a name, keyword, or previous task's choices.

## Scoped guidance

| Task or affected behavior | Required guidance |
| --- | --- |
| Go code, dependencies, tests, or CI | [Go rules](../.agents/rules/go.md) |
| Application commands or configuration | [Cobra/Viper project rules](../.agents/rules/go.md#application-commands-and-configuration) and their [skill routes](#public-go-skills) |
| Type/generic design, nil/numeric/resource safety, or functional helpers | [Safety/generics rules](../.agents/rules/go.md#safety-and-generics-first-implementation) and [lo/mo guidance](../.agents/rules/go.md#functional-helpers-when-applicable) |
| Ara requests, tool schemas, sequence JSON, MCP transports, or sessions | [Ara/MCP rules](../.agents/rules/ara-mcp.md) and [architecture notes](architecture.md) |
| Control phases, conflicting actions, retries, completion, preflight, or recovery | [First-release control/operation policies](first-release-policy.md) and [outstanding evidence](first-release-policy.md#outstanding-verification) |
| HTTP routers, middleware, diagnostic payloads, or MCP streaming | [Chi HTTP rules](../.agents/rules/ara-mcp.md#http-routing-and-middleware), [HTTP architecture](architecture.md#http-stack), and [HTTP observability](observability.md#http-middleware-and-streaming) |
| Runtime logging, metrics, tracing, health, or diagnostic endpoints | [Observability requirements](observability.md) and the relevant [signal skills](#public-go-skills) |
| Resource sampling, live dashboard, or CSV/JSONL exports | [Dashboard contract](resource-dashboard.md), [T12](plan.md#t12-resource-dashboard-and-exports), and affected Chi/observability/concurrency/testing rules |
| SBC deployment, resource limits, footprint, or performance | [Resource contract](architecture.md#deployment-targets-and-resource-constraints), [SBC validation](development.md#small-sbc-validation), and benchmark/performance skills as applicable |
| Runtime features, fixes, and behavior tests | [Red-green-refactor process](development.md#test-driven-development), testing skills, and the affected API/transport rules |
| Documentation or roadmap | [Documentation skills](#public-go-skills), the implemented behavior, and the relevant [plan task](plan.md#task-list) |
| GitHub issues or pull requests | [Contributing](../CONTRIBUTING.md) and the applicable [workflow](#task-workflows); use `gh` on `cavenine/ara-mcp` |
| Bundled skill updates | [Sources, licenses, and update procedure](../.agents/README.md#third-party-sources-and-licenses) and [skills-lock.json](../skills-lock.json) |

API/tool changes normally require both rule files. Do not copy generic Go guidance
into a new local style guide or overwrite the upstream skills.

## Public Go skills

For Go-related work, start with
[samber's golang-how-to](../.agents/skills/golang-how-to/SKILL.md). It selects the
relevant skills; load those and the references needed for the task. Do not load
the complete collection into every session.

Before editing Go code, also read
[JetBrains' use-modern-go](../.agents/skills/use-modern-go/SKILL.md) and run its
version-aware guidance command as documented in the
[development guide](development.md#agent-assisted-development).

All 47 bundled skills (46 samber skills and JetBrains' modern Go skill) are routed
here. Assess the task before loading a route's supporting skills; a route does not
require every listed skill if its corresponding concern is absent.

### Core task routes

| Work | Skills to start with |
| --- | --- |
| Go code style, names, and review | [golang-code-style](../.agents/skills/golang-code-style/SKILL.md), [golang-naming](../.agents/skills/golang-naming/SKILL.md); JetBrains guidance before Go edits |
| Types, generics, nil/numeric/resource safety, or collection ownership | [golang-safety](../.agents/skills/golang-safety/SKILL.md), [golang-structs-interfaces](../.agents/skills/golang-structs-interfaces/SKILL.md), [golang-data-structures](../.agents/skills/golang-data-structures/SKILL.md) |
| Application commands, flags, help, output, or exit behavior | [golang-cli](../.agents/skills/golang-cli/SKILL.md), [golang-spf13-cobra](../.agents/skills/golang-spf13-cobra/SKILL.md) |
| Configuration keys, precedence, decoding, or validation | [golang-spf13-viper](../.agents/skills/golang-spf13-viper/SKILL.md), `golang-cli`, and affected safety/testing guidance |
| Finite collection transforms | [golang-samber-lo](../.agents/skills/golang-samber-lo/SKILL.md), with `golang-data-structures` for ownership/layout choices |
| Typed optional values, results, or alternatives | [golang-samber-mo](../.agents/skills/golang-samber-mo/SKILL.md), with safety/error-handling guidance for the boundary |
| Ara error mapping | [golang-error-handling](../.agents/skills/golang-error-handling/SKILL.md) |
| Logging and operational signals | [golang-observability](../.agents/skills/golang-observability/SKILL.md), [golang-samber-slog](../.agents/skills/golang-samber-slog/SKILL.md), [golang-error-handling](../.agents/skills/golang-error-handling/SKILL.md) |
| Chi routing/middleware/render and SDK stream compatibility | Applicable context, logging/error, security, and testing skills above; official [Chi](https://github.com/go-chi/chi) and [render](https://github.com/go-chi/render) API references. The bundled catalog has no dedicated Chi skill |
| Deadlines and background connections | [golang-context](../.agents/skills/golang-context/SKILL.md), [golang-concurrency](../.agents/skills/golang-concurrency/SKILL.md) |
| Self-hosted resource page/SSE, shared sampler, or statistic downloads | Observability, context/concurrency, safety/security, testing, and relevant benchmark skills; official [Datastar SSE](https://data-star.dev/reference/sse_events) and [Go SDK](https://github.com/starfederation/datastar-go) when selected. There is no bundled Datastar skill |
| Test-driven development and contract tests | Harness TDD skill when available; [golang-testing](../.agents/skills/golang-testing/SKILL.md) and [the repository's red-green process](development.md#test-driven-development) |
| Testify assertions, mocks, or suites | [golang-stretchr-testify](../.agents/skills/golang-stretchr-testify/SKILL.md), alongside `golang-testing` |
| Go documentation and examples | [golang-documentation](../.agents/skills/golang-documentation/SKILL.md), naming/testing guidance when examples or API names are involved |
| Package structure, lifecycle patterns, or constructor wiring | [golang-project-layout](../.agents/skills/golang-project-layout/SKILL.md), [golang-design-patterns](../.agents/skills/golang-design-patterns/SKILL.md), [golang-dependency-injection](../.agents/skills/golang-dependency-injection/SKILL.md) |
| Behavior-preserving refactors or package moves | [golang-refactoring](../.agents/skills/golang-refactoring/SKILL.md), target-style skill, and [golang-gopls](../.agents/skills/golang-gopls/SKILL.md) |
| Local definitions, callers, diagnostics, or safe renames | `golang-gopls` for the resolved build; use available compiler/search tools if it is unavailable |
| Bugs, crashes, deadlocks, races, or unexpected results | [golang-troubleshooting](../.agents/skills/golang-troubleshooting/SKILL.md), then safety/concurrency/error/testing skills matching the cause |
| Benchmarks, profile capture, or measurement interpretation | [golang-benchmark](../.agents/skills/golang-benchmark/SKILL.md) |
| Optimizing an evidenced bottleneck | [golang-performance](../.agents/skills/golang-performance/SKILL.md), with measurement guidance |
| Trust-boundary validation, auth, secrets, or security review | [golang-security](../.agents/skills/golang-security/SKILL.md); safety/concurrency as affected |
| Lint configuration or finding interpretation | [golang-lint](../.agents/skills/golang-lint/SKILL.md) |
| CI, dependency-update workflows, or release automation | [golang-continuous-integration](../.agents/skills/golang-continuous-integration/SKILL.md), with the tool-specific skill for analysis |
| Module changes, version resolution, or dependency/vulnerability audits | [golang-dependency-management](../.agents/skills/golang-dependency-management/SKILL.md) |
| Candidate libraries or published package docs/versions/licenses | [golang-popular-libraries](../.agents/skills/golang-popular-libraries/SKILL.md), [golang-pkg-go-dev](../.agents/skills/golang-pkg-go-dev/SKILL.md) |
| Modernizing old Go idioms or changing the Go baseline | [golang-modernize](../.agents/skills/golang-modernize/SKILL.md), with JetBrains' version-aware guidance |
| Go release news, learning resources, or ecosystem discovery | [golang-stay-updated](../.agents/skills/golang-stay-updated/SKILL.md) |

### Conditional library and protocol routes

These skills are installed for when the task actually adopts or changes the named
technology. Their presence does not add a requirement for that technology to ara-mcp.

| Work, when actually applicable | Skill |
| --- | --- |
| Reactive event pipelines, multicast, stream composition, or `ro` usage | [golang-samber-ro](../.agents/skills/golang-samber-ro/SKILL.md); context/concurrency guidance for lifetime |
| A justified cache or `samber/hot` usage | [golang-samber-hot](../.agents/skills/golang-samber-hot/SKILL.md); preserve authoritative Ara state/freshness |
| Structured error extensions using `samber/oops` | [golang-samber-oops](../.agents/skills/golang-samber-oops/SKILL.md), alongside native error-handling guidance |
| A chosen `samber/do` container | [golang-samber-do](../.agents/skills/golang-samber-do/SKILL.md) |
| A chosen Google Wire injector | [golang-google-wire](../.agents/skills/golang-google-wire/SKILL.md) |
| A chosen Uber Dig container | [golang-uber-dig](../.agents/skills/golang-uber-dig/SKILL.md) |
| A chosen Uber Fx application/lifecycle | [golang-uber-fx](../.agents/skills/golang-uber-fx/SKILL.md) |
| Database access, transactions, or nullable columns | [golang-database](../.agents/skills/golang-database/SKILL.md) |
| gRPC/protobuf endpoints or client integration | [golang-grpc](../.agents/skills/golang-grpc/SKILL.md) |
| GraphQL schemas/resolvers/subscriptions | [golang-graphql](../.agents/skills/golang-graphql/SKILL.md) |
| Swagger/swaggo-generated OpenAPI documentation | [golang-swagger](../.agents/skills/golang-swagger/SKILL.md) |

Repository constraints in `AGENTS.md` take precedence over generic templates.
Cobra, Viper, Chi, Chi render, and Datastar/Go SDK are selected project choices. `lo` and `mo` are
preferred candidates where their typed transformations/models fit, subject to boundary behavior and
dependency review. Other library routes remain conditional. Before copying an
example, check it against Go 1.27 and the pinned library API; skill examples may
target earlier language or library versions.

## Task workflows

| Task | Read |
| --- | --- |
| Add or change an MCP tool | [Implement a tool](../.agents/commands/implement-tool.md) |
| Review changes | [Review](../.agents/commands/review.md) |
| Commit when explicitly requested | [Commit](../.agents/commands/commit.md) |
| Submit a PR when explicitly requested | [Submit PR](../.agents/commands/submit-pr.md) and [PR template](../.github/PULL_REQUEST_TEMPLATE.md) |

These are portable workflow documents, not universally discovered slash commands.
Use the client's skill loader where supported; otherwise read the local files.
Complete reads matter even when automatic attachment is truncated.

## Keeping instructions current

- Shared constraints belong in `AGENTS.md`; scoped rules belong in `.agents/rules/`.
- This index owns task routing. `.agents/README.md` owns skill provenance and updates.
- The development guide owns operational commands; `CONTRIBUTING.md` owns contribution
  policy. Link to them rather than maintaining duplicate checklists.
- The plan records work and acceptance criteria, not proof that features exist.
  Update the README only when implementation and verification support the claim.
- Add decision or failure records under `docs/` when there is a real decision or
  incident to preserve. Link them from the relevant rule and task when created.
