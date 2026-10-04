# Commit changes

Run this workflow only after the user explicitly requests a commit.

1. Read `AGENTS.md`. Inspect `git status`, `git diff`, `git diff --cached`, and
   `git log --oneline -10` when history exists. Read untracked files too.
2. Review the intended change and relevant check results. Exclude unrelated user
   work, generated files, credentials, and local configuration.
3. Check documentation and changelog entries against current behavior.
4. Stage only the intended paths and review the staged diff.
5. Use a concise imperative commit subject. Do not amend, skip hooks, or change
   Git configuration unless explicitly requested.
6. If hooks fail, fix the failure and retry the commit normally. Report the commit
   and remaining working-tree changes. Push only when explicitly requested.
