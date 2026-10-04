# Review a change

Read `AGENTS.md` and the applicable scoped rules. Inspect `git status`, the working
and staged diffs, and new untracked files. Trace changed functions and their callers.
Evaluate the changed behavior and select applicable skills using the agent index:
testing, safety/generics, Cobra/Viper, Chi HTTP middleware/render, logging, or other
routes as actually affected.

Check the observable behavior: tool schemas, upstream routes/units, accepted versus
completed operations, retry outcomes, ownership, deadlines, and shutdown. Verify
the resolved `docs/first-release-policy.md` contracts: cooperative control, intent/
expected-run checks, conflict rejection/reserved interrupts, version/recovery gates,
verified preflight, separate access, and outstanding verification ownership. Verify
that both transports use the same handlers and that stdio logs cannot corrupt
protocol output. Check documentation claims against actual implementation.
Review RED/GREEN evidence and retained behavior tests. For runtime changes, verify
the relevant `docs/observability.md` criteria: correlation, redaction, bounded labels,
accepted/terminal outcomes, health semantics, and collector/shutdown behavior.
For Chi changes, review middleware order, structured request/recovery logs, render
boundaries, and SDK SSE tests. Check that wrappers preserve flushing and that
ordinary-route deadlines/content-type policies cannot rewrite or terminate streams.
Consider the small-SBC target: check bounded payload/preview bytes, concurrent work,
retained state/event queues, polling/reconnect pacing, and telemetry overhead.
Require relevant measurement evidence for resource/performance claims, preserving
headroom for co-located Ara/rig services and distinguishing desktop from board results.
For the resource dashboard, check one shared sampler, CPU/RSS/heap units and missing
values, local assets, independent dashboard SSE, history/subscriber/export bounds,
snapshot-consistent CSV/JSONL, browser updates, and diagnostics access/cleanup tests.
Include optional archive rotation/restart/gap/failed-write behavior and the provisional
budget defaults; never describe those defaults as measured hardware support.

Run the appropriate checks from `docs/development.md`. Report findings with file/line
references, impact, and a concrete correction. State exact check results and any
unverified live integration. Do not manufacture findings or claim no-test packages
provide behavior coverage. Review does not authorize a commit or push.
