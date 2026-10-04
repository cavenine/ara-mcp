# Contributing to ara-mcp

Thank you for helping build an MCP interface to OpenAstro Ara. Keep changes small,
Go-idiomatic, and grounded in the Ara API that actually exists.

## Before starting

1. Read the [README](README.md), [architecture notes](docs/architecture.md), and
   relevant [implementation task](docs/plan.md#task-list), including the
   [first-release policies](docs/first-release-policy.md) it implements.
2. Search [issues](https://github.com/cavenine/ara-mcp/issues) and
   [pull requests](https://github.com/cavenine/ara-mcp/pulls) for related work.
3. For a substantial change, describe the problem and proposed behavior in an issue.
   Small fixes can describe the problem directly in the pull request.
4. Agents must also read [AGENTS.md](AGENTS.md) and the
   [agent instruction index](docs/agent-instructions.md).

## Set up

Install Go 1.27.x and Git. Use the latest 1.27 patch release. Clone
the repository and create a descriptive branch such as `feat/sequence-tools`,
`fix/request-timeout`, or `docs/local-setup`.

Follow the [development guide](docs/development.md) for checkout setup, toolchain
selection, and the current build/run status.

## Go conventions

- Language guidance comes from the bundled [JetBrains and samber skills](.agents/README.md).
  Load `golang-how-to` for task-specific routing and `use-modern-go` before editing
  Go code. Project constraints in `AGENTS.md` take priority over generic templates.
- Prefer the standard library, small packages, and concrete types.
- Use Cobra for application commands and Viper for configuration; follow their
  [project rules](.agents/rules/go.md#application-commands-and-configuration).
- Use Chi v5, its middleware, and go-chi/render for HTTP serving, following the
  [HTTP rules](.agents/rules/ara-mcp.md#http-routing-and-middleware). Verify structured
  logging and SDK stream behavior through the composed router.
- Prefer compile-time generics over `any`/reflection for known type sets. Apply
  [safety and functional-helper guidance](.agents/rules/go.md#safety-and-generics-first-implementation)
  when type, resource, collection, or optional/result behavior changes.
- Use the official `github.com/modelcontextprotocol/go-sdk` for MCP protocol and
  transport handling instead of implementing JSON-RPC yourself.
- Put the executable in `cmd/ara-mcp/` and private implementation in `internal/`
  when those components are introduced. Add packages for actual responsibilities,
  not for a speculative framework.
- Keep one set of tool handlers for stdio and Streamable HTTP.
- Pass `context.Context` through network operations. Bound requests and close
  response bodies. Return errors with useful context, without credentials.
- Send logs to stderr; stdout belongs to the MCP protocol in stdio mode.
- Keep dependencies pinned in `go.mod` and checksums in `go.sum` once needed.
- Use `// SPDX-License-Identifier: AGPL-3.0-or-later` and a copyright notice in
  first-party Go source files. Preserve attribution in any reused material.

## Verify changes

Run the [documented local checks](docs/development.md#build-and-checks) before
submitting a change. Runtime features and fixes use
[red-green-refactor](docs/development.md#test-driven-development), with a failing
behavior test run before the change and retained regression coverage. Include the
relevant [observability checks](docs/observability.md#acceptance-and-delivery).
See [integration testing](docs/development.md#testing-ara-integration) for default
hardware-free tests and opt-in live evidence.

Record exact commands and results in the PR. Update the relevant plan task only
after its acceptance criteria are satisfied; linking a PR is not proof of completion.

## Ara integration

All equipment and sequence operations go through Ara server. Keep creating a plan
separate from starting it. Report accepted operations as accepted, and obtain
completion from Ara's state, jobs, or events. Read the
[integration rules](.agents/rules/ara-mcp.md) before changing API or transport behavior.

## Pull requests

- Use a short, imperative commit subject, for example `Add sequence status tool`.
- Follow the [pull request template](.github/PULL_REQUEST_TEMPLATE.md).
- Explain the problem, changed behavior, and exact checks run. Name any checks
  that were not run and why.
- Update user-facing documentation and [CHANGELOG.md](CHANGELOG.md) for meaningful
  behavior changes. Keep unreleased changes under `Unreleased`.
- Call out API, tool-schema, configuration, and control-ownership changes.
- If AI assisted the change, identify the agent/model used; human review is still
  responsible for the result.

## License

By contributing, you agree to license your contribution under
[AGPL-3.0-or-later](LICENSE). Do not include secrets, generated binaries, or material
whose license is incompatible with the project. Bundled skills keep their upstream
MIT or Apache-2.0 licenses; preserve their notices when updating them. Skill update
and attribution details are in [.agents/README.md](.agents/README.md).
