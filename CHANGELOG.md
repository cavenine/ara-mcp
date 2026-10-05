# Changelog

Notable changes are recorded here. Released versions will use Semantic Versioning
and dated headings. No release has been published.

## Unreleased

### Added

- Runnable Cobra/Viper CLI and official MCP Go SDK v1.8.0 stdio server with read-only
  Ara context/sequence tools, structured tool signals, and paced process diagnostics.
- Resty-backed Ara HTTP gateway with bounded request/response handling, explicit
  accepted/unknown mutation outcomes, GET-only retries, and OpenTelemetry signals.
- Go 1.27.x module and initial project documentation.
- Tool-neutral agent instructions and development workflows under `.agents/`.
- Commit-pinned JetBrains modern Go and samber Go skills, with upstream licenses
  and a skills lock file.
- GitHub CI, dependency updates, and issue and pull request templates.
- Agent instruction index, development guide, and a task-by-task implementation
  plan with dependencies, deliverables, and acceptance criteria.
- Logging and observability requirements, with feature-level instrumentation and
  verification responsibilities in the implementation plan.
- Task-based routing for the full bundled skill catalog, Cobra/Viper application
  choices, safety/generics and `lo`/`mo` guidance, and red-green-refactor requirements.
- Chi v5/middleware/render HTTP stack requirements, with structured request/recovery
  logging and streaming-aware implementation and verification criteria.
- Raspberry Pi 3/4/5-class small-SBC deployment intent, bounded resource requirements,
  and CPU/memory/headroom validation tasks.
- Process resource sampling, self-hosted SSE dashboard, and CSV/JSONL statistic-export
  requirements, with bounded history and a dedicated T12 implementation task.
- Plan-review resolutions: cooperative UI/MCP control, command arbitration/replay/
  completion contracts, preflight/context, degraded recovery, access, Datastar,
  provisional resource budgets, and optional bounded postmortem JSONL archival.
- Early T13 diagnostics HTTP foundation and revised dependencies so the dashboard/
  exports and platform/integration checks do not wait for all equipment features.
- Owner-assigned outstanding upstream, client/version, and measurement verification.
