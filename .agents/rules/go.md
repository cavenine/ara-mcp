# Go implementation rules

Read [AGENTS.md](../../AGENTS.md) first. General Go conventions come from public
skills rather than a parallel, locally maintained style guide:

- [golang-how-to](../skills/golang-how-to/SKILL.md) selects samber skills for the task.
- [use-modern-go](../skills/use-modern-go/SKILL.md) supplies version-aware modern Go
  guidance. Read its full `list` output before editing Go code.
- [Effective Go](https://go.dev/doc/effective_go) and the
  [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) remain official
  references.

## Project-specific choices

- Target Go 1.27.x; do not lower the module baseline to match an older local toolchain.
- The official MCP SDK owns protocol and transport handling. Use the standard
  library for Ara HTTP/JSON integration where it covers the need.
- Keep one tool implementation for both transports. Use explicit upstream JSON
  tags and preserve opaque sequence bodies where appropriate.
- Stdio logs go to stderr, overriding any generic stdout-logging advice.
- Default tests use `testing` and `httptest` without a live rig. Add dependencies,
  packages, or frameworks only for implemented requirements.
- Keep upstream skills unchanged. Make project-specific decisions here or in
  `AGENTS.md`, not by rewriting copied skill files.
- CI has explicit permissions and timeouts, SHA-pinned actions, and the same
  format/module/vet/test/build checks documented in
  [the development guide](../../docs/development.md#build-and-checks).

## Application commands and configuration

Use [Cobra](../skills/golang-spf13-cobra/SKILL.md) for commands and flags, with
[golang-cli](../skills/golang-cli/SKILL.md) for lifecycle/I/O conventions. Use
[Viper](../skills/golang-spf13-viper/SKILL.md) for configuration resolution.

- Construct command trees explicitly, use `RunE` and argument validators, and
  return failures to the process boundary. Preserve defers and bounded shutdown.
- Inject output/error writers; configure Cobra so usage, errors, and banners do
  not leak onto MCP stdout while serving. Fresh command trees isolate tests.
- Use a fresh `viper.New()` per application/test, bind supported keys and Cobra
  flags before resolution, and namespace environment variables with `ARA_MCP`.
- Required precedence is explicitly set flags > environment > optional config
  file > defaults. Use `SetDefault` for defaults; do not shadow user inputs with
  unconditional `Set` calls. Bind environment keys needed for typed unmarshaling.
- Resolve once into a typed configuration with explicit `mapstructure` tags and
  validate it before network/listener startup. Values permitted from env/file are
  validated after resolution, not incorrectly forced to be CLI-only required flags.
- Missing auto-discovered optional config is allowed. A missing explicitly selected
  file, malformed data, or unreadable file is an error. Keep errors/logs sanitized.
- Test precedence, explicit false/zero values, invalid inputs, and per-test isolation
  through the command/configuration interface using red-green-refactor.

CLI syntax, supported configuration keys/formats, and defaults are defined in T01/T03.
Remote KV storage and live reload require an actual feature requirement; Viper's
availability alone does not enable them.

## Safety and generics-first implementation

Load [golang-safety](../skills/golang-safety/SKILL.md) for type, nil, collection,
numeric, and resource invariants. Load the relevant data-structure/type/concurrency
skills from the index when those concerns are affected.

- Use concrete request/result/event models; use generics for operations over known
  type sets before falling back to `any`, reflection, or runtime assertions. Do not
  generalize a single concrete operation just to add type parameters.
- Validate external/dynamic values before conversion. Handle assertion failures;
  do not panic on malformed MCP/Ara responses or return typed-nil errors/interfaces.
- Preserve absent/null/zero distinctions and safe initialization. Guard map writes,
  indexes, numeric narrowing, non-finite equipment values, and resource lifetimes.
- Protect owned data from slice/map aliasing, including mutable nested elements.
  Document mutating operations and test ownership-sensitive boundaries.
- Prefer checked extraction and explicit error handling to `Must`/`MustGet` helpers
  in request paths. Safety does not justify silently defaulting a failed mutation
  into a success or inferring physical completion.

## Functional helpers when applicable

- Load [golang-samber-lo](../skills/golang-samber-lo/SKILL.md) for finite collection
  map/filter/group/chunk/deduplication work. Use its generic helpers when they make
  the real transformation clearer. Prefer equivalent modern `slices`/`maps` functions
  where they already cover the operation. New outer collections do not imply deep
  copies of pointer elements. Parallel/mutable variants require measured need.
- Load [golang-samber-mo](../skills/golang-samber-mo/SKILL.md) for meaningful typed
  absence (`Option`), success/failure composition (`Result`), or distinct alternatives
  (`Either`). Use these where they improve the model, preserving native `(T, error)`
  interoperability and the exact Ara JSON contract with boundary tests.
- `mo` futures/tasks are not Ara execution state. `lo` retry helpers must not repeat
  an uncertain equipment command. Reactive streams use `golang-samber-ro` when the
  event pipeline warrants them; finite collections do not require a stream layer.
- Pin `lo`/`mo` when their first real use is implemented, with dependency review and
  tests. Installed library skills are available guidance, not imported dependencies.
