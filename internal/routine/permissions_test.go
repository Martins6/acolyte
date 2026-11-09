package routine

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/martins6/acolyte/internal/opencode"
)

// permissionEffect mirrors OpenCode's evaluation: wildcard action/resource
// matching with the last matching rule winning, defaulting to ask when no rule
// matches.
func permissionEffect(rules []opencode.PermissionRule, action, resource string) string {
	effect := opencode.PermissionAsk
	for _, rule := range rules {
		if wildcardMatch(action, rule.Action) && wildcardMatch(resource, rule.Resource) {
			effect = rule.Effect
		}
	}
	return effect
}

func wildcardMatch(input, pattern string) bool {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	escaped := b.String()
	// OpenCode treats a trailing " *" as optional extra arguments, so
	// "acolyte inbox *" also matches the bare command.
	if strings.HasSuffix(escaped, " .*") {
		escaped = strings.TrimSuffix(escaped, " .*") + "( .*)?"
	}
	return regexp.MustCompile(escaped + "$").MatchString(input)
}

func TestDreamAnalysisPermissionsAreReadOnly(t *testing.T) {
	ws := t.TempDir()
	base := filepath.ToSlash(filepath.Clean(ws))
	rules := dreamAnalysisPermissions(ws)

	tests := []struct {
		action   string
		resource string
		want     string
	}{
		{"read", base + "/.acolyte/SOUL.md", opencode.PermissionAllow},
		{"read", base + "/.agents/skills/good/SKILL.md", opencode.PermissionAllow},
		{"glob", "**/*.md", opencode.PermissionAllow},
		{"list", base + "/.agents/skills", opencode.PermissionAllow},
		{"read", base + "/.env", opencode.PermissionDeny},
		{"read", "/etc/passwd", opencode.PermissionDeny},
		{"grep", "password", opencode.PermissionDeny},
		{"edit", base + "/.acolyte/SOUL.md", opencode.PermissionDeny},
		{"edit", base + "/main.go", opencode.PermissionDeny},
		{"shell", "rm -rf " + base, opencode.PermissionDeny},
		{"subagent", "*", opencode.PermissionDeny},
		{"webfetch", "https://example.com", opencode.PermissionDeny},
	}
	for _, tt := range tests {
		if got := permissionEffect(rules, tt.action, tt.resource); got != tt.want {
			t.Errorf("%s %s = %s, want %s", tt.action, tt.resource, got, tt.want)
		}
	}
}

func TestDreamWriterPermissionsAllowOnlyMemoryPaths(t *testing.T) {
	ws := t.TempDir()
	base := filepath.ToSlash(filepath.Clean(ws))
	rules := dreamWriterPermissions(ws)

	tests := []struct {
		action   string
		resource string
		want     string
	}{
		{"read", base + "/.acolyte/USER.md", opencode.PermissionAllow},
		{"read", base + "/.env", opencode.PermissionDeny},
		{"edit", base + "/.acolyte/USER.md", opencode.PermissionAllow},
		{"edit", base + "/.acolyte/HEARTBEAT.md", opencode.PermissionDeny},
		{"edit", base + "/.acolyte", opencode.PermissionDeny},
		{"edit", base + "/.agents", opencode.PermissionDeny},
		{"edit", base + "/.agents/skills", opencode.PermissionDeny},
		{"edit", base + "/.agents/skills/good/SKILL.md", opencode.PermissionAllow},
		{"edit", base + "/.agents/skills/good/run.sh", opencode.PermissionDeny},
		{"edit", base + "/main.go", opencode.PermissionDeny},
		{"edit", "/tmp/outside.md", opencode.PermissionDeny},
		{"shell", "touch " + base + "/pwned", opencode.PermissionDeny},
		{"subagent", "*", opencode.PermissionDeny},
	}
	for _, tt := range tests {
		if got := permissionEffect(rules, tt.action, tt.resource); got != tt.want {
			t.Errorf("%s %s = %s, want %s", tt.action, tt.resource, got, tt.want)
		}
	}
}

func TestHeartbeatPermissionsAllowOnlyInboxCommand(t *testing.T) {
	ws := t.TempDir()
	base := filepath.ToSlash(filepath.Clean(ws))
	rules := heartbeatPermissions(ws)

	tests := []struct {
		action   string
		resource string
		want     string
	}{
		{"shell", `acolyte inbox --agent-name valet --session-id ses_user "hi"`, opencode.PermissionAllow},
		{"shell", `/usr/local/bin/acolyte inbox --agent-name valet "hi"`, opencode.PermissionAllow},
		{"shell", "rm -rf " + base, opencode.PermissionDeny},
		{"shell", "acolyte inbox", opencode.PermissionAllow},
		{"shell", "acolyte inboxx", opencode.PermissionDeny},
		{"read", base + "/.acolyte/HEARTBEAT.md", opencode.PermissionAllow},
		{"edit", base + "/.acolyte/HEARTBEAT.md", opencode.PermissionDeny},
		{"edit", base + "/main.go", opencode.PermissionDeny},
	}
	for _, tt := range tests {
		if got := permissionEffect(rules, tt.action, tt.resource); got != tt.want {
			t.Errorf("%s %s = %s, want %s", tt.action, tt.resource, got, tt.want)
		}
	}

	// OpenCode scans compound shell commands into separate resources, so a
	// chained write does not inherit the acolyte inbox allow.
	if got := permissionEffect(rules, "shell", "rm -rf "+base); got != opencode.PermissionDeny {
		t.Errorf("chained write = %s, want deny", got)
	}
}
