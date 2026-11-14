# Orchestrator / merge policy

**Intent.** Record how autopilot merges PRs, why the current "merge even if red" latitude is safe
today, and the exact trigger that must revoke it. This is a policy document: it changes no branch
protection settings and no workflows.

## Current protection state

Verified with the GitHub API on **2026-11-14**
(`gh api repos/Martins6/acolyte/branches/<branch>/protection`):

| Branch | Required status checks | Required approvals | `enforce_admins` |
| ------ | ---------------------- | ------------------ | ---------------- |
| `main` | `pr-guard` (commit status) | `0` | `true` |
| `dev` | **none** | `0` | `false` |

Consequences:

- `dev` accepts any PR merge regardless of check results. Merging a red PR needs no bypass at all,
  and `--admin` is unnecessary.
- `main` only accepts merges whose head SHA carries the `pr-guard` commit status, with admins
  subject to the same rule. That status is produced by `.github/workflows/pr-guard.yml` from the
  base branch (`pull_request_target`), and it verifies that the PR targets `main` from `dev` — it is
  not a test result.
- The `review` check (`opencode-review.yml`) is an **LLM review**, not a deterministic gate. It is
  advisory in this policy: it is not a required check on either branch, and it must not be treated
  as one unless it is explicitly added as a required status check.

## Decision

The "merge even if `unit`/`telegram` is red; use `--admin` only if branch protection blocks the
merge" latitude in [`TASK.md`](../../TASK.md) is scoped, not general:

- It exists **only** for an autopilot run whose instructions explicitly ask for that behavior.
  It is not a default for human or agent merges.
- It is safe today **only** because `dev` has no required status checks: the merge is a normal
  merge, the red result is recorded in the PR, and no protection rule is bypassed.
- `--admin` is **never** needed and **never** allowed under this policy. If a merge is blocked, the
  run stops and records the blocker instead of escalating privileges.

## Policy: the stop-on-red trigger

Required checks and stop-on-red must be enabled in this order — the instructions change **before**
the gate does, so no run can merge red through a required check:

1. **Switch autopilot to stop-on-red first.** Update the run instructions (`TASK.md`) to require
   `unit` and `telegram` to pass before merging, and to stop and report on red.
2. **Then enable required status checks on `dev`.** From that moment a red or missing check blocks
   the merge at the platform level.
3. **Then require approvals.** Only after the checks gate is live should review approvals be made
   required (`required_approving_review_count >= 1`).

Rules that hold at every step:

- **Never use `--admin` to bypass a required check.** A required check is a gate; bypassing it
  defeats the purpose of enabling it. A blocked merge is a stop-and-report condition.
- **Keep the review advisory.** The `review` job is an LLM review; it may inform a merge, but it
  does not gate one unless it is explicitly made a required status check. If it is ever required,
  the stop-on-red trigger applies to it too.
- **Red is never silently merged.** A merge with a known-red required check is a process failure,
  even if the platform would allow it.

## Checklist: enabling required checks safely

- [ ] Confirm autopilot instructions (`TASK.md` step 4) already say **stop on red**; merge that
      change first.
- [ ] Confirm no in-flight autopilot run still relies on the merge-red latitude.
- [ ] Enable `unit` and `telegram` as required status checks on `dev` (Settings → Branches →
      branch protection rule).
- [ ] Keep `enforce_admins` as intended; if admins are exempt, they must still follow stop-on-red
      by policy.
- [ ] Verify a deliberately failing PR is blocked, then close it without merging.
- [ ] Only then set `required_approving_review_count >= 1`.
- [ ] Update this document (state table + date) in the same change that alters protection.
- [ ] Do not add `review` as a required check without a separate decision: it is an LLM review,
      and it already failed once with no review body (PR #85).

## Where the latitude came from

PR #79's review flagged the merge-red instructions. The policy was intentional for the #77–#88
run, where every deterministic check passed; it must not survive silently once required checks or
approvals exist. This document is the recording of that decision, per issue #89 bullet 7.
