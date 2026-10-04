# Submit a pull request

Run this workflow only after the user explicitly requests a pull request.

1. Read `AGENTS.md` and `CONTRIBUTING.md`. Inspect status, diff, branch tracking,
   recent commits, and the complete diff from the proposed base branch.
2. Review every commit that will be included. Confirm the branch contains only
   intended work and that documented checks pass.
3. Use `.github/PULL_REQUEST_TEMPLATE.md`. Include the problem, linked issue,
   concrete changes, verification, compatibility concerns, and AI assistance.
4. Use `gh` to create the PR on `cavenine/ara-mcp`. Never force-push or bypass hooks.
5. Inspect checks and actionable review comments. Fix root causes across sibling
   tools/transports when requested; do not apply a suggested patch without checking
   its behavior. Return the PR URL and check status.
