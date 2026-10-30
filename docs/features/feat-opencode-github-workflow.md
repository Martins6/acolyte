# Overview

Adds GitHub Actions automation around the OpenCode `build` agent using the official `anomalyco/opencode/github` action: one automatic PR review workflow (owner-only, verdict-tagged) plus a guard workflow that restricts `main` to `dev` -> `main` pull requests.

# Details

- Adds `.github/workflows/opencode-review.yml` triggered by `pull_request` types `[opened, synchronize, reopened, ready_for_review]`; runs only for non-draft PRs authored by the repository owner (`github.event.pull_request.user.login == github.repository_owner`) so a public repo never runs the agent on outside PRs
- Every new commit (`synchronize`) re-runs the review on the latest diff; drafts are skipped and reviewed once when marked ready
- The review posts exactly one PR review whose body starts with a `## Verdict` heading whose value is `LGTM` or `NEEDS_REVIEW`, swaps a matching `LGTM`/`NEEDS_REVIEW` label on the PR (created on demand via `gh label create`), and splits findings into `Necessary` (must fix) and `Non-necessary` (optional improvements); a `concurrency` group with `cancel-in-progress` makes rapid pushes cancel the in-flight review so only the latest diff is reviewed and label swaps cannot race
- Review scope: security issues, loopholes/abuse paths, edge cases, concurrency issues (data races, goroutine leaks, channel/lock misuse), unaccounted logic (unhandled errors, missing cases, untested behavior), and intent mismatches against the PR title/body; the PR title, body, commits, and diff are treated as untrusted input
- Verdict rules: `NEEDS_REVIEW` if any Necessary finding exists, otherwise `LGTM`
- Auth uses `use_github_token: true` with the workflow `GITHUB_TOKEN` (no OpenCode GitHub App or OIDC needed); the model key comes from the `OPENCODE_GO_API_KEY` environment secret, mapped to `OPENCODE_API_KEY` (the env var the `opencode-go` provider reads per models.dev) and exposed to the job via `environment: OPENCODE_GO_API_KEY`; job permissions are `contents: read`, `pull-requests: write`, `issues: write` (PR labels go through the issues API)
- `OPENCODE_PERMISSION` restricts the agent's shell access to an allowlist of `gh` subcommands (`gh pr view/diff/review/edit`, `gh label`); `gh auth`, `gh api`, `gh secret`, and `gh repo` are denied, any command containing `$` is denied so no environment variable can be expanded into `gh` output, and `webfetch`/`websearch`/`task` are denied so there is no direct network exfiltration channel; residual risk: `gh pr edit`/`gh pr review` can act on any PR and `gh label` on any label repo-wide, and the review body itself remains a channel the agent could be steered into
- A `Restore trusted agent runtime config` step removes `opencode.json`, `opencode.jsonc`, `AGENTS.md`, `CLAUDE.md`, and `.opencode/` unconditionally (so PR-added files under `.opencode/` cannot survive) and then restores the PR base branch's version of each path it has, before the agent runs, so the diff under review cannot configure its own runtime (provider base-URL redirection, auto-loaded instructions, plugin code); the checkout uses the PR head SHA so the action's own head-branch checkout is a no-op and the restored files persist
- Both actions are pinned by full commit SHA (`actions/checkout` `d23441a` # v6.1.0, `anomalyco/opencode/github` `77fc88c` # latest); residual supply-chain trust remains in the pinned action vendor and the opencode.ai installer the action runs internally
- Only code pushes (`synchronize`) re-trigger the review; title/body edits do not, so the verdict is judged against the description as of the last code change
- Adds `.github/workflows/pr-guard.yml`: pull requests into `main` fail unless the head branch is `dev` in this repository (not a fork) and the PR author is the repository owner, enforcing `dev` -> `main` as the only path onto `main`; event values are passed via `env:` and compared in shell (never interpolated into `run:` scripts, which would allow script injection via crafted ref names)
- A `Remove action comment` cleanup step (`if: always()`, paginated API scan) deletes the automatic comment the action posts for the agent's final reply, so the PR carries only the review
- No project-level `opencode.json` is committed (this is a public repo): the model is pinned via the action's `model` input (`opencode-go/mimo-v2.6-pro`), and the runtime-config restore step strips any PR-added agent config before the agent runs
- README.md gains a "GitHub Workflow" subsection covering the secret name, review behavior, and the `dev` -> `main` merge flow
- Repo-side (configured through the GitHub API, not part of this diff): branch protection on `main` requires a pull request, the `pr-guard` status check, and restricts merging to the repository owner; `dev` requires pull requests

# File Paths

- .github/workflows/opencode-review.yml
- .github/workflows/pr-guard.yml
- README.md
