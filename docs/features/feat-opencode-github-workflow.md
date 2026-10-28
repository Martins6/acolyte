# Overview

Adds GitHub Actions automation around the OpenCode `build` agent using the official `anomalyco/opencode/github` action: one automatic PR review workflow (owner-only, verdict-tagged) plus a guard workflow that restricts `main` to `dev` -> `main` pull requests.

# Details

- Adds `.github/workflows/opencode-review.yml` triggered by `pull_request` types `[opened, synchronize, reopened, ready_for_review]`; runs only for non-draft PRs authored by the repository owner (`github.event.pull_request.user.login == github.repository_owner`) so a public repo never runs the agent on outside PRs
- Every new commit (`synchronize`) re-runs the review on the latest diff; drafts are skipped and reviewed once when marked ready
- The review posts exactly one PR review whose body starts with `Verdict: LGTM | NEEDS_REVIEW` and swaps a matching `LGTM`/`NEEDS_REVIEW` label on the PR; findings are split into `Necessary` (must fix) and `Non-necessary` (optional improvements)
- Review scope: security issues, loopholes/abuse paths, edge cases, concurrency issues (data races, goroutine leaks, channel/lock misuse), unaccounted logic (unhandled errors, missing cases, untested behavior), and intent mismatches against the PR title/body; the PR title, body, commits, and diff are treated as untrusted input
- Verdict rules: `NEEDS_REVIEW` if any Necessary finding exists, otherwise `LGTM`
- Auth uses `use_github_token: true` with the workflow `GITHUB_TOKEN` (no OpenCode GitHub App or OIDC needed); the model key comes from `${{ secrets.OPENCODE_GO_API_KEY }}`; job permissions are `contents: read`, `pull-requests: write`, `issues: write` (PR labels go through the issues API)
- `OPENCODE_PERMISSION` restricts the agent's shell access to `gh` commands only
- Adds `.github/workflows/pr-guard.yml`: pull requests into `main` fail unless the head branch is `dev` in this repository (not a fork) and the PR author is the repository owner, enforcing `dev` -> `main` as the only path onto `main`
- Project-level `opencode.json` at the repo root pins the `opencode-go` provider and `mimo-v2.6-pro` model so the opencode server started by the action resolves the model reliably in the runner (the same pattern that resolved [opencode#7958](https://github.com/anomalyco/opencode/issues/7958))
- README.md gains a "GitHub Workflow" subsection covering the secret name, review behavior, and the `dev` -> `main` merge flow
- Repo-side (configured through the GitHub API, not part of this diff): branch protection on `main` requires a pull request, the `pr-guard` status check, and restricts merging to the repository owner; `dev` requires pull requests

# File Paths

- .github/workflows/opencode-review.yml
- .github/workflows/pr-guard.yml
- opencode.json
- README.md
