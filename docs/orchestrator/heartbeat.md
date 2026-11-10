# Orchestrator / heartbeat

**Intent.** A recurring, self-directed check that decides whether anything is worth surfacing to the
user — and, most of the time, decides *not* to. It is the **`heartbeat` routine** (a `routine`-kind
cron job) that runs every `schedule.heartbeat_minutes` minutes.

## How it works
1. The `heartbeat` routine (owner `heartbeat`, `kind: routine`) fires every X minutes.
2. It runs in **its own session** (separate from the user's conversations).
3. It reads the **`HEARTBEAT.md`** persona file and the latest **non-cron** session.
4. It runs a **prompt-guided turn**. The default prompt instructs the agent:
   - if something is genuinely interesting, reply `NOTIFY: <one concise message for the user>`;
   - otherwise reply exactly `NO_ACTION` and do not notify anyone.
   The turn runs under a session-scoped permission ruleset that is fully read-only: memory
   discovery is allowed, while `edit`, every shell command, network tools, subagents, and
   `external_directory` are denied. Injected session text cannot modify the workspace or run
   commands.
5. The routine parses the `NOTIFY:` reply and delivers it to the latest user session through the
   communication sink. The model never runs a shell command, and the routine returns empty so cron
   does not send a second notification.

## Err on silence
The heartbeat **always errs on the side of not sending anything**: "didn't find anything particularly
interesting… not sending anything to the user." When in doubt, stay silent. A quiet, attentive
presence — not a chatty one.

## `HEARTBEAT.md`
The persona file holding standing instructions/notes — "what to look for and do." The heartbeat reads
it each run and treats its notes as cues for what counts as "interesting."

## Message primitives (manual)
There is **no `acolyte heartbeat` command** — the routine runs on schedule. To move a message by hand:
- `acolyte inbox "<text>"` — deliver an external event to the agent on a session (agent reacts).
- `acolyte notify-user "<text>"` — notify the user only (never agent, never session).

## Config
- `schedule.heartbeat_minutes` — interval; `0` disables.
- `schedule.timezone` — schedule timezone.

## Artifacts
- A `routine`-kind cron job (`owner=heartbeat`), visible in `acolyte cron list`.
- Its own session; surfaces findings to the user's latest session when warranted.
