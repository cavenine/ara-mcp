# Implement an MCP tool

1. Read `AGENTS.md`, `.agents/rules/go.md`, `.agents/rules/ara-mcp.md`, and
   `docs/architecture.md` from the repository root. Evaluate the task and load
   applicable skills using `docs/agent-instructions.md`; include testing and
   relevant observability guidance. CLI/configuration changes use Cobra/Viper skills.
2. State the user's operation, the proposed tool input/output, and the matching Ara
   route. Inspect upstream handlers and services, and record the Ara version.
3. Trace existing handlers/helpers before adding code. Keep one implementation
   shared by stdio and Streamable HTTP.
4. Define explicit schemas, units, and error behavior using the official MCP SDK.
   Determine whether the action is read-only, a mutation, or an asynchronous job.
   Keep sequence creation distinct from starting a run.
5. For mutations, resolve the Ara session/ownership behavior and timeout/retry
   semantics first. Do not treat unused upstream `DryRun` fields as safeguards.
6. Use red-green-refactor: write and run one failing observable contract test,
   implement the smallest working change through the existing Ara client, rerun it,
   and refactor only while green. Repeat for remaining behaviors/failure paths.
   Include the feature's required logging, metrics, tracing, and diagnostic behavior
   from `docs/observability.md` in the corresponding slices.
7. Run the checks in `docs/development.md` and record exact RED/GREEN results.
   Update the README's actual capability list,
   integration notes, and changelog when behavior becomes available.
8. Report the changed behavior and exact verification. Leave changes uncommitted
   unless the user has explicitly requested a commit.
