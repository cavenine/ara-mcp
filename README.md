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

**Implementation in progress.** T02's Resty-backed Ara HTTP client is complete.
T03 adds a runnable stdio MCP server with read-only Ara tools and local adapter
diagnostics. Streamable HTTP, mutations, and a release are not available yet.

The current tool set reads Ara server identity/version/state, rig/profile/device
context, saved sequence pages/details, and adapter/process diagnostics. No tool
changes equipment or sequence state. Ara connectivity is needed only when calling
Ara-backed tools; local diagnostics remain available when Ara is offline.

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

The stdio server uses the official
[MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk). Planned Streamable
HTTP will share the same tool handlers and Ara API integration:

| Mode | MCP transport | Connection to Ara | Lifecycle |
| --- | --- | --- | --- |
| On the agent's computer | stdio | Ara's LAN address | Launched by the agent |
| Beside Ara server | Streamable HTTP | Usually `localhost:5555` | Persistent service |

Running ara-mcp beside Ara does not require embedding it in Ara. Ara continues an
imaging session when the agent or adapter disconnects.

The executable uses Cobra and Viper. Configuration precedence is explicit flags >
`ARA_MCP_*` environment > optional config file > defaults; details and runnable
commands are in the [development guide](docs/development.md#application-commands-and-configuration).
HTTP serving will use Chi v5, its middleware, and go-chi/render, with structured
`slog` request logging and the MCP SDK's handler preserving protocol framing.
See [HTTP architecture](docs/architecture.md#http-stack).

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
- `list_sequences` and `get_sequence`: bounded saved-sequence listing and detail.
- `get_adapter_diagnostics`: Ara reachability and local process/runtime sample.

## Planned capabilities

- Create, inspect, validate, and update sequences, including template-based plans.
- Start, pause, resume, stop, and monitor sequences.
- Request manual actions through Ara, such as exposures, slews, and autofocus.
- Retrieve image previews and operation results.
- Monitor the application's own CPU/memory/goroutine and related usage on a
  self-hosted, automatically updating SSE dashboard.
- Download retained resource statistics as CSV or JSONL files.
- Optionally retain bounded rotating JSONL resource history across restarts for
  postmortem diagnostics; the persistent-service example will enable it.

These remain implementation goals. See the [architecture and API notes](docs/architecture.md)
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
