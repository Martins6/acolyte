package routine

import (
	"path/filepath"

	"github.com/martins6/acolyte/internal/opencode"
)

// readOnlyPermissions is the base ruleset for routine turns: deny every tool,
// then re-allow only the read-only discovery tools the prompts need. Rules are
// session-scoped, appended after the agent's rules, and the last matching rule
// wins. Reads are confined to the memory roots so a prompt-injected turn
// cannot exfiltrate workspace secrets through the model reply.
func readOnlyPermissions(workspace string) []opencode.PermissionRule {
	base := filepath.ToSlash(filepath.Clean(workspace))
	return []opencode.PermissionRule{
		{Action: "*", Resource: "*", Effect: opencode.PermissionDeny},
		{Action: "read", Resource: base + "/.acolyte/*", Effect: opencode.PermissionAllow},
		{Action: "read", Resource: base + "/.agents/skills/*", Effect: opencode.PermissionAllow},
		{Action: "glob", Resource: "*", Effect: opencode.PermissionAllow},
		{Action: "list", Resource: "*", Effect: opencode.PermissionAllow},
		{Action: "skill", Resource: "*", Effect: opencode.PermissionAllow},
	}
}

// dreamAnalysisPermissions makes the analysis turn read-only. It never gets
// edit or shell, even if a prompt injection asks it to write.
func dreamAnalysisPermissions(workspace string) []opencode.PermissionRule {
	return readOnlyPermissions(workspace)
}

// dreamWriterPermissions confines the writer turn to the memory allowlist:
// `.acolyte/*.md` (except HEARTBEAT.md) and `.agents/skills/<id>/SKILL.md`.
// Shell stays denied so the model cannot create symlinks, replace memory
// roots, or write elsewhere through commands. The post-turn snapshot remains
// as a second, policy-level check.
func dreamWriterPermissions(workspace string) []opencode.PermissionRule {
	base := filepath.ToSlash(filepath.Clean(workspace))
	rules := readOnlyPermissions(workspace)
	rules = append(rules,
		opencode.PermissionRule{Action: "edit", Resource: base + "/.acolyte/*.md", Effect: opencode.PermissionAllow},
		opencode.PermissionRule{Action: "edit", Resource: base + "/.agents/skills/*/SKILL.md", Effect: opencode.PermissionAllow},
		// The allow glob also matches HEARTBEAT.md; deny it after the allow so
		// the last matching rule wins.
		opencode.PermissionRule{Action: "edit", Resource: base + "/.acolyte/HEARTBEAT.md", Effect: opencode.PermissionDeny},
	)
	return rules
}

// heartbeatPermissions keeps the heartbeat turn read-only. Notification
// delivery is performed by the routine after the turn, not by a
// model-controlled shell command.
func heartbeatPermissions(workspace string) []opencode.PermissionRule {
	return readOnlyPermissions(workspace)
}
