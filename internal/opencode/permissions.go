package opencode

// Permission effects accepted by the OpenCode session permission API.
const (
	PermissionAllow = "allow"
	PermissionAsk   = "ask"
	PermissionDeny  = "deny"
)

// PermissionRule is one session-scoped permission rule (OpenCode
// Permission.Rule). Rules are evaluated after the agent's rules and the last
// matching rule wins, so a per-turn ruleset can deny tools the agent would
// otherwise allow.
type PermissionRule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}
