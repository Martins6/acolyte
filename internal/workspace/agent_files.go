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
    "Send exactly one image to the user on Telegram. The image is copied into media/outbound/images and delivered as a photo.",
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

const agentUserContent = `# User Information

[Critical information about the user will be filled here]
`

const agentHeartbeatContent = `# Heartbeat

When the heartbeat routine runs, inspect the latest session and these notes.
Surface something only when it is genuinely interesting; otherwise stay silent.
Do not repeat what the user has already seen.
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
