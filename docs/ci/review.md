# Review check failure on PR #85 — investigation

**Status:** resolved (infrastructure flake; re-run required) · **Investigated:** 2026-11-14

The `review` check on PR #85 failed in 9 seconds without posting a review. The cause is a transient
network failure while the third-party composite action downloaded its installer — not the model,
the diff, or the review prompt. This note records the evidence and the remedy so the same red check
is not misdiagnosed later.

## Symptom and impact

- The PR head showed a red **`review`** check and **no automated review body** was posted (the
  PR had zero reviews and no action comments).
- **`unit`** and **`telegram`** passed on the same commit; **`e2e`** was skipped because that job
  only runs on `push`/`workflow_dispatch`. Tests and the diff were fine.
- The check is **advisory**: `dev` has no required status checks, so PR #85 merged with `review`
  red. Nothing was blocked and no human review was lost.
- Impact is limited to "no automated review on that revision". The failure is not sticky: it comes
  from an external download at run time, not from repository state.

## Evidence

| Item | Value |
| ---- | ----- |
| PR | [#85](https://github.com/Martins6/acolyte/pull/85) `feat(media): add single-image outbound delivery` |
| Head commit | `796cdeee4256e00111eb13a930ca26ae298dadcb` (branch `task/6-outbound-image`) |
| Workflow run | [`43314452833`](https://github.com/Martins6/acolyte/actions/runs/43314452833) (`opencode-review`, attempt 1) |
| Job | [`131518680584`](https://github.com/Martins6/acolyte/actions/runs/43314452833/job/131518680584) (`review`, 21:52:13Z → 21:52:22Z) |
| Failing workflow step | `Run OpenCode review` |
| Failing action step | `anomalyco/opencode/github@77fc88c8ade8e5a620ebbe1197f3a572d29ae91a` → internal step **`Install opencode`** |
| Failing command | `curl -fsSL https://opencode.ai/install \| bash` |
| Error | `curl: (56) Recv failure: Connection reset by peer` → `##[error]Process completed with exit code 56` |

Captured log excerpt (logs expire; this is the authoritative record):

```
Cache not found for input keys: opencode-Linux-X64-v1.19.5
##[start-action display=Install opencode;id=__anomalyco_opencode.__run]
##[group]Run curl -fsSL https://opencode.ai/install | bash
curl -fsSL https://opencode.ai/install | bash
shell: /usr/bin/bash --noprofile --norc -e -o pipefail {0}
Installing opencode version: 1.19.5
#=#=#
curl: (56) Recv failure: Connection reset by peer
##[error]Process completed with exit code 56.
##[end-action id=__anomalyco_opencode.__run;outcome=failure;conclusion=failure;duration_ms=552]
##[start-action display=Add opencode to PATH;id=__anomalyco_opencode.__run_2]
##[end-action id=__anomalyco_opencode.__run_2;outcome=skipped;conclusion=skipped;duration_ms=0]
##[start-action display=Run opencode;id=__anomalyco_opencode.run_opencode]
##[end-action id=__anomalyco_opencode.run_opencode;outcome=skipped;conclusion=skipped;duration_ms=0]
```

Context from the same run:

- The action's cache key `opencode-Linux-X64-v1.19.5` missed, so the install step had to run.
- `curl: (56)` means "failure in receiving network data": the TCP connection was reset while
  downloading the script. `bash` never received or executed the script — the pipe died first.
- `Add opencode to PATH` and `Run opencode` were both **skipped**, so `opencode github run` never
  started: no model request, no `gh pr diff`, no review body.
- `Checkout repository`, `Restore trusted agent runtime config`, `Remove action comment`, and the
  post-job cleanup steps all **succeeded**.
- Same commit, independent workflows: `unit` success (21:52:12Z–21:53:22Z), `telegram` success
  (21:52:12Z–21:52:35Z); `e2e` skipped by its `push`-only condition.

The action is pinned to a commit, but its install step is a plain
`curl -fsSL https://opencode.ai/install | bash` with **no retry**. Its preceding version lookup
(`curl -sf https://api.github.com/.../releases/latest`, also unretried) happened to succeed and
reported `1.19.5`; only the opencode.ai download was reset.

## Classification

**Infrastructure flake** — a transient outbound-network failure in the third-party composite
action's installer download, before anything project-specific ran.

It is not reproducible from repository state, and it would clear on a plain re-run of the job.

## What it is NOT

- **Not the model.** No model call was made: the action's `Run opencode` step was skipped after the
  install failed. Model choice, provider key, and model behavior were never exercised.
- **Not the diff.** `gh pr diff` never ran. `unit` and `telegram` passed on the exact same head
  `796cdee`, so the change compiled and its tests were green.
- **Not the review prompt, permissions, or workflow logic.** Checkout, trusted-runtime restore,
  the `OPENCODE_PERMISSION` env, and cleanup all completed successfully; those inputs are only
  consumed by `opencode github run`, which never started. The failure was a curl exit 56 while
  fetching `https://opencode.ai/install`.
- **Not runner exhaustion or a job timeout.** The job used 9 s of its 30-minute budget.

## Remediation

Re-run only the failed job, then confirm a review appears:

```bash
gh run rerun 43314452833 --failed --repo Martins6/acolyte
gh run watch 43314452833 --repo Martins6/acolyte
gh pr view 85 --repo Martins6/acolyte --json reviews   # expect a review from the action
```

If GitHub refuses to re-run a workflow whose PR is already merged (PR #85 merged while the check
was red), the equivalent confirmation is a `review` check that installs and posts on any later PR:
a cache miss plus a successful `Install opencode` step proves the path recovered.

For this failure mode, **do not** change the workflow or the review prompt. The check is advisory —
`dev` has no required status checks (`required_status_checks` is unset) — so a red `review` is
informational and does not block merging.

## Hardening (future work, not in this PR)

The action's installer is the weak point: an unretried `curl | bash` from a CDN, and an unretried
`releases/latest` lookup before it. Options, to be tracked separately:

1. **Replace the composite action's installer** with a pinned, checksummed download of the release
   artifact (not `| bash`), using retries, e.g.
   `curl -fsSL --retry 5 --retry-all-errors --retry-delay 2 ...`, then extract to
   `~/.opencode/bin`. Pinning the exact asset also removes the mutable `releases/latest` lookup.
2. **Vendor the action** into `.github/actions/opencode-review/` and own the install step, so a
   retry policy can be added and tested in-repo without waiting on upstream.

Either way, keep the advisory semantics: an install flake should cost a re-run, not a merge.
