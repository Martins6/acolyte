package workspace

import "fmt"

const agentConfigContent = `# CONFIG

You are an **Acolyte** agent. Acolyte hosts you on top of **OpenCode**.

- **Engine:** OpenCode (https://opencode.ai) — your reasoning + tools come from here.
- **Acolyte modules** (high-level capabilities layered on OpenCode):
  - **Memory** — ` + "`memory-load` / `memory-access` / `memory-learn` / `memory-export`" + `.
  - **Communication** — channels to the outside world (e.g. Telegram).
  - **Orchestrator** — the ` + "`acolyte` daemon" + `: turn loop, cron, store, registry.

## Inspect your own capabilities
- ` + "`opencode.jsonc`" + ` — engine + capability wiring (instructions, skills, tools).
- ` + "`acolyte cron list`" + ` — scheduled jobs (heartbeats, dream/learn runs, tasks).
- ` + "`acolyte status`" + ` — your agent status.

## Learn more
Module docs: https://github.com/Martins6/acolyte/tree/main/docs/
Read a module's docs to understand or change how a capability works.
`

const agentSoulContent = `# Soul

Your core behaviour — fundamental and unbreakable:

- **Helpful**: do your best to assist the user.
- **Friendly**: be warm, approachable, and kind.
- **Concise but not boring**: get to the point without being cold.
- **Warm and caring**: show genuine care for the user's needs and wellbeing.
`

const agentToolsContent = `# Tools

Your tools come from OpenCode (built-in tools, MCP tools, and the ` + "`skill`" + ` tool).
Learned skills live in ` + "`.agents/skills/`" + ` and are loaded on demand by ID.

Acolyte adds CLI-level capabilities. Inspect the live wiring in ` + "`opencode.jsonc`" + `,
scheduled work with ` + "`acolyte cron list`" + `, and agent state with ` + "`acolyte status`" + `.

To send one image to the user, call the ` + "`send_image`" + ` tool with a workspace-relative path and
an optional caption. Only jpg, jpeg, png, and webp files are accepted; the file is copied under
` + "`media/outbound/images/`" + `.

Add a new learned skill as ` + "`.agents/skills/<skill-id>/SKILL.md`" + ` with frontmatter
` + "`name`" + ` and ` + "`description`" + `.
`

const agentSendImageTool = `import { tool } from "@opencode-ai/plugin"
import { execFile } from "node:child_process"
import { promisify } from "node:util"
import path from "node:path"

const execFileAsync = promisify(execFile)

export default tool({
  description:
    "Send exactly one image to the user on Telegram. The image is copied into media/outbound/images and delivered immediately as a photo; your final reply is never merged into it. Daemon- and inbox-routed turns deliver the reply as a separate Telegram message, while acolyte run prints it to stdout.",
  args: {
    path: tool.schema
      .string()
      .describe("Workspace-relative or absolute path to a jpg, jpeg, png, or webp image"),
    caption: tool.schema
      .string()
      .optional()
      .describe("Optional caption for the photo"),
  },
  async execute({ path: imagePath, caption }, context) {
    const workspace = context.directory || process.env.ACOLYTE_WORKSPACE_PATH || process.cwd()
    const absolutePath = path.isAbsolute(imagePath)
      ? imagePath
      : path.resolve(workspace, imagePath)
    const binary = process.env.ACOLYTE_BIN || "acolyte"
    const args = ["notify-user"]
    if (caption && caption.trim() !== "") {
      args.push(caption)
    }
    args.push("--workspace-folder", workspace)
    if (context.sessionID) {
      args.push("--session-id", context.sessionID)
    }
    args.push("--media", absolutePath)
    try {
      const { stdout } = await execFileAsync(binary, args)
      return {
        content: (stdout || "Photo queued.").trim(),
        metadata: { path: absolutePath, sessionID: context.sessionID },
      }
    } catch (error) {
      const detail =
        error && typeof error === "object" && "stderr" in error
          ? String(error.stderr || "").trim()
          : ""
      throw new Error(detail || "acolyte send_image failed")
    }
  },
})
`

const agentBoardReadTool = `import { tool } from "@opencode-ai/plugin"
import { execFile } from "node:child_process"
import { promisify } from "node:util"

const execFileAsync = promisify(execFile)

export default tool({
  description: "Read one internal board post and its ordered messages as JSON.",
  args: {
    post: tool.schema.string().describe("Board post id"),
  },
  async execute({ post }, context) {
    const workspace = context.directory || process.env.ACOLYTE_WORKSPACE_PATH || process.cwd()
    const binary = process.env.ACOLYTE_BIN || "acolyte"
    try {
      const { stdout } = await execFileAsync(binary, [
        "board",
        "show",
        post,
        "--workspace-folder",
        workspace,
        "--json",
      ])
      return { content: (stdout || "{}").trim(), metadata: { post, workspace } }
    } catch (error) {
      const detail =
        error && typeof error === "object" && "stderr" in error
          ? String(error.stderr || "").trim()
          : ""
      throw new Error(detail || "acolyte board_read failed")
    }
  },
})
`

const agentBoardReplyTool = `import { tool } from "@opencode-ai/plugin"
import { execFile } from "node:child_process"
import { promisify } from "node:util"

const execFileAsync = promisify(execFile)

export default tool({
  description: "Reply to one internal board post.",
  args: {
    post: tool.schema.string().describe("Board post id"),
    text: tool.schema.string().describe("Message text"),
    reply_to: tool.schema
      .string()
      .optional()
      .describe("Optional entry id, prefix, or #seq to quote"),
  },
  async execute({ post, text, reply_to }, context) {
    const workspace = context.directory || process.env.ACOLYTE_WORKSPACE_PATH || process.cwd()
    const binary = process.env.ACOLYTE_BIN || "acolyte"
    const args = ["board", "message", post, text, "--workspace-folder", workspace]
    if (reply_to) {
      args.push("--reply-to", reply_to)
    }
    args.push("--json")
    try {
      const { stdout } = await execFileAsync(binary, args)
      return { content: (stdout || "{}").trim(), metadata: { post, workspace } }
    } catch (error) {
      const detail =
        error && typeof error === "object" && "stderr" in error
          ? String(error.stderr || "").trim()
          : ""
      throw new Error(detail || "acolyte board_reply failed")
    }
  },
})
`

const agentUserContent = `# User Information

[Critical information about the user will be filled here]
`

const agentHeartbeatContent = `# Heartbeat

When the heartbeat routine runs, inspect the latest session and these notes.
Surface something only when it is genuinely interesting; otherwise stay silent.
Do not repeat what the user has already seen.
`

const agentWorkspaceContent = `# Workspace

Every file you create or keep — scripts, notes, reports, generated assets, downloads, build
output — lives under ` + "`projects/`" + ` in your workspace. Never write loose files at the workspace
root.

## Project work

` + "`projects/{HH}-{MM}-{DD}-{MM}-{YYYY}-{brief-title}/`" + `

- ` + "`HH` `MM`" + ` = local 24-hour time the project was started, zero-padded; then
  ` + "`DD` `MM` `YYYY`" + ` = local date. Example:
  ` + "`projects/14-32-24-11-2026-aws-budget-report/`" + `.
- ` + "`brief-title`" + ` = lowercase kebab-case, English, 2–5 words.
- One folder per unit of work: put everything for that work in it (scripts, inputs, outputs,
  notes) and keep adding as the work continues. Do not create a new folder per file or per turn.
- Before starting, list ` + "`projects/`" + ` and resume the existing folder when the request continues
  earlier work; only create a new folder for genuinely new work.
- Keep the folder self-contained: relative paths inside a script should resolve within the
  project folder, so it can be moved or archived without breaking.

## Truly temporary work

` + "`projects/temp/`" + ` is for scratch scripts, one-off conversions, throwaway experiments, and
agent tool dumps — anything you would be happy to delete tomorrow. When unsure, use a project
folder instead.

## Workspace root

The root stays config-only: ` + "`AGENTS.md`" + `, ` + "`opencode.jsonc`" + `, ` + "`.acolyte/`" + `, ` + "`.opencode/`" + `,
` + "`.agents/`" + `, ` + "`media/`" + `, ` + "`downloads/`" + `, ` + "`.logs/`" + `, ` + "`projects/`" + `, ` + "`.gitignore`" + `, ` + "`.env`" + `, and
` + "`.acolyte.sqlite.db*`" + `. Do not create new files at the root or restructure config.

Files the user sends arrive under ` + "`media/inbound/<type>/`" + ` and stay there; copy what a project
needs into that project's folder.

When you finish a task that wrote files, say which project folder you used.
`

const agentGitignore = `# Agent-local secrets and state
.env
.acolyte.sqlite.db
.acolyte.sqlite.db-shm
.acolyte.sqlite.db-wal
.logs/
downloads/
media/
`

func agentAgentsContent(opts AgentOptions) string {
	name := opts.Name
	if name == "" {
		name = "Acolyte agent"
	}
	description := opts.Description
	if description == "" {
		description = "A personal agent powered by OpenCode."
	}
	return fmt.Sprintf(`# %s

%s

This workspace is an **OpenCode project** with an Acolyte overlay.

## Persona
- [CONFIG](.acolyte/CONFIG.md) — what Acolyte is and how to inspect your capabilities.
- [SOUL](.acolyte/SOUL.md) — core behaviour.
- [TOOLS](.acolyte/TOOLS.md) — tool behaviour and workflows.
- [USER](.acolyte/USER.md) — what you know about the user.
- [WORKSPACE](.acolyte/WORKSPACE.md) — where files go.
- [HEARTBEAT](.acolyte/HEARTBEAT.md) — what to look for on a heartbeat.

## Capabilities
- `+"`opencode.jsonc`"+` — engine + capability wiring.
- `+"`acolyte status`"+` — your agent status.
- `+"`acolyte cron list`"+` — scheduled jobs.

## Learned skills
Skills live in `+"`.agents/skills/`"+` and are loaded on demand.

## Docs
https://github.com/Martins6/acolyte/tree/main/docs/
`, name, description)
}
