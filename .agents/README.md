# Agent guidance and workflows

[AGENTS.md](../AGENTS.md) is the canonical shared instruction file. This directory
contains scoped guidance and reusable task workflows inspired by AlpacaBridge's
agent setup, adapted for Go and Ara integration.
Use [docs/agent-instructions.md](../docs/agent-instructions.md) to find the rules,
skills, workflow, and plan context for a task.

Project workflows are ordinary Markdown documents; bundled skills use the public
[Agent Skills format](https://agentskills.io/). Read files explicitly when a client
does not discover them. A workflow or skill does not grant permission to commit,
push, deploy, or control equipment.

## Public Go skills

The repository includes JetBrains' `use-modern-go` and the complete samber Go
skill collection, including referenced assets. The complete collection preserves
cross-skill references; only load the skills relevant to the current task.

1. Start a Go task with [golang-how-to](skills/golang-how-to/SKILL.md).
2. Read the task-specific skills it selects under `skills/<name>/SKILL.md`.
3. Before editing Go code, read [use-modern-go](skills/use-modern-go/SKILL.md) and
   run its wrapper using the [development guide](../docs/development.md#agent-assisted-development).

The JetBrains wrapper installs its versioned CLI into the user's cache on first
use. It does not add a runtime dependency to ara-mcp. Node/npm are needed only
for the optional skills installer, not to build or run the Go application.

The skills were installed as ordinary copies in the shared `.agents/skills/`
directory. The installer selected Codex only to target that shared directory;
the files are not Codex-specific, and there are no client-specific skill aliases.

## Third-party sources and licenses

| Material | Upstream snapshot | License |
| --- | --- | --- |
| `skills/use-modern-go/` | [JetBrains/go-modern-guidelines, 155dc7c](https://github.com/JetBrains/go-modern-guidelines/tree/155dc7ca10da5e1f6c841503086957b1b37f5815/plugin/skills/use-modern-go) | [Apache-2.0, JetBrains s.r.o.](licenses/JetBrains-go-modern-guidelines.txt) |
| `skills/golang-*/` | [samber/cc-skills-golang, 8e899e2](https://github.com/samber/cc-skills-golang/tree/8e899e20ff0cd4dc524af3993e4c62d8ee8c5717/skills) | [MIT, Samuel Berthe](licenses/samber-cc-skills-golang.txt) |

Upstream files are copied without changes and retain their original attribution.
They are not relicensed under ara-mcp's AGPL license. The root
[skills-lock.json](../skills-lock.json) records commit refs, source paths, and
content hashes. Preserve the full license texts when updating the skills.

To reinstall the reviewed snapshots, use the same project-local installer:

```sh
npx --yes skills@1.7.0 add https://github.com/JetBrains/go-modern-guidelines/tree/155dc7ca10da5e1f6c841503086957b1b37f5815/plugin/skills/use-modern-go --skill use-modern-go --agent codex --copy --yes
npx --yes skills@1.7.0 add https://github.com/samber/cc-skills-golang/tree/8e899e20ff0cd4dc524af3993e4c62d8ee8c5717 --skill '*' --agent codex --copy --yes
```

For an update, select and review a new upstream commit, install from that commit's
URL, and update this table and any changed license notices together with the lock
file. Keep repository-specific overrides in `AGENTS.md` and `rules/`, not in the
copied skill files.

## Project rules and workflows

Scoped rules live in `rules/`; workflows live in `commands/`. Their canonical
task-to-document index is [docs/agent-instructions.md](../docs/agent-instructions.md).
Tools that support custom commands can map workflow files into their own command
system. Do not assume `.agents/commands/` is automatically discovered.
