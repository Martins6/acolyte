package service

import (
	"strings"
	"testing"
)

func testOptions(goos string, scope Scope) Options {
	home := "/Users/alice"
	if goos == "linux" {
		home = "/home/alice"
	}
	return Options{
		GOOS:     goos,
		Scope:    scope,
		Binary:   "/usr/local/bin/acolyte",
		Home:     home + "/.acolyte",
		UserHome: home,
		UserName: "alice",
		UID:      501,
		PATH:     DefaultPath(home),
	}
}

func TestRenderLaunchdUser(t *testing.T) {
	plan, err := Render(testOptions("darwin", ScopeUser))
	if err != nil {
		t.Fatal(err)
	}
	if want := "/Users/alice/Library/LaunchAgents/ai.acolyte.daemon.plist"; plan.Path != want {
		t.Fatalf("path = %q, want %q", plan.Path, want)
	}
	for _, want := range []string{
		"<key>Label</key>\n  <string>ai.acolyte.daemon</string>",
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key><true/>",
		"<key>ThrottleInterval</key><integer>10</integer>",
		"<string>--log-file</string>",
		"<string>/Users/alice/.acolyte/logs/daemon.log</string>",
		"<key>ACOLYTE_HOME</key>",
		"<key>PATH</key>",
		"<key>StandardErrorPath</key>",
	} {
		if !strings.Contains(plan.Content, want) {
			t.Errorf("plist missing %q:\n%s", want, plan.Content)
		}
	}
	if strings.Contains(plan.Content, "<key>UserName</key>") {
		t.Errorf("user LaunchAgent must not set UserName:\n%s", plan.Content)
	}
	if len(plan.Commands) != 3 {
		t.Fatalf("commands = %d, want 3", len(plan.Commands))
	}
	if got := strings.Join(plan.Commands[0].Args, " "); got != "launchctl bootout gui/501/ai.acolyte.daemon" {
		t.Errorf("bootout command = %q", got)
	}
	if got := strings.Join(plan.Commands[2].Args, " "); got != "launchctl bootstrap gui/501 /Users/alice/Library/LaunchAgents/ai.acolyte.daemon.plist" {
		t.Errorf("bootstrap command = %q", got)
	}
	if !strings.Contains(strings.Join(plan.Notes, " "), "Use --system") {
		t.Errorf("user plan should mention --system for boot start: %v", plan.Notes)
	}
}

func TestRenderLaunchdSystem(t *testing.T) {
	plan, err := Render(testOptions("darwin", ScopeSystem))
	if err != nil {
		t.Fatal(err)
	}
	if want := "/Library/LaunchDaemons/ai.acolyte.daemon.plist"; plan.Path != want {
		t.Fatalf("path = %q, want %q", plan.Path, want)
	}
	if !strings.Contains(plan.Content, "<key>UserName</key>\n  <string>alice</string>") {
		t.Errorf("system LaunchDaemon missing UserName:\n%s", plan.Content)
	}
	for _, cmd := range plan.Commands {
		joined := strings.Join(cmd.Args, " ")
		if strings.Contains(joined, "gui/") {
			t.Errorf("system command uses gui domain: %s", joined)
		}
	}
	if got := strings.Join(plan.Commands[2].Args, " "); got != "launchctl bootstrap system /Library/LaunchDaemons/ai.acolyte.daemon.plist" {
		t.Errorf("bootstrap command = %q", got)
	}
}

func TestRenderSystemdUser(t *testing.T) {
	opts := testOptions("linux", ScopeUser)
	plan, err := Render(opts)
	if err != nil {
		t.Fatal(err)
	}
	if want := "/home/alice/.config/systemd/user/acolyte.service"; plan.Path != want {
		t.Fatalf("path = %q, want %q", plan.Path, want)
	}
	for _, want := range []string{
		"Description=Acolyte orchestrator",
		"StartLimitIntervalSec=0",
		"Restart=always",
		"RestartSec=5",
		"Environment=\"HOME=/home/alice\"",
		"Environment=\"ACOLYTE_HOME=/home/alice/.acolyte\"",
		`ExecStart="/usr/local/bin/acolyte" "start"`,
		"StandardOutput=journal",
		"WantedBy=default.target",
	} {
		if !strings.Contains(plan.Content, want) {
			t.Errorf("unit missing %q:\n%s", want, plan.Content)
		}
	}
	if strings.Contains(plan.Content, "User=") {
		t.Errorf("user unit must not set User=:\n%s", plan.Content)
	}
	if strings.Contains(plan.Content, "network-online.target") {
		t.Errorf("user unit should not require the system network target:\n%s", plan.Content)
	}
	if got := strings.Join(plan.Commands[0].Args, " "); got != "systemctl --user daemon-reload" {
		t.Errorf("reload command = %q", got)
	}
	if got := strings.Join(plan.Commands[1].Args, " "); got != "systemctl --user enable acolyte.service" {
		t.Errorf("enable command = %q", got)
	}
	if got := strings.Join(plan.Commands[2].Args, " "); got != "systemctl --user restart acolyte.service" {
		t.Errorf("restart command = %q", got)
	}
	if !strings.Contains(strings.Join(plan.Notes, " "), "loginctl enable-linger alice") {
		t.Errorf("user plan should mention linger: %v", plan.Notes)
	}
}

func TestRenderSystemdSystem(t *testing.T) {
	plan, err := Render(testOptions("linux", ScopeSystem))
	if err != nil {
		t.Fatal(err)
	}
	if want := "/etc/systemd/system/acolyte.service"; plan.Path != want {
		t.Fatalf("path = %q, want %q", plan.Path, want)
	}
	for _, want := range []string{
		"After=network-online.target",
		"Wants=network-online.target",
		"User=alice",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(plan.Content, want) {
			t.Errorf("unit missing %q:\n%s", want, plan.Content)
		}
	}
	if got := strings.Join(plan.Commands[0].Args, " "); got != "systemctl daemon-reload" {
		t.Errorf("reload command = %q", got)
	}
}

func TestRenderRejectsControlCharacters(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{"home newline", func(o *Options) { o.Home = "/home/alice/.acolyte\nExecStartPre=/bin/sh -c evil" }},
		{"path carriage return", func(o *Options) { o.PATH = "/usr/bin\rExecStartPre=/bin/sh -c evil" }},
		{"binary tab", func(o *Options) { o.Binary = "/usr/local/bin/acolyte\t" }},
		{"user newline", func(o *Options) { o.UserName = "alice\nUser=root" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := testOptions("linux", ScopeSystem)
			tt.mutate(&opts)
			if _, err := Render(opts); err == nil {
				t.Fatal("expected control character error")
			}
		})
	}
}

func TestRenderRequiresUserNameForSystemScope(t *testing.T) {
	t.Setenv("USER", "")
	opts := testOptions("linux", ScopeSystem)
	opts.UserName = ""

	if _, err := Render(opts); err == nil {
		t.Fatal("expected error for missing system user name")
	}
}

func TestRenderUnsupportedPlatform(t *testing.T) {
	opts := testOptions("linux", ScopeUser)
	opts.GOOS = "windows"
	if _, err := Render(opts); err == nil {
		t.Fatal("expected error for unsupported platform")
	}
}

func TestRenderRequiresAbsoluteBinary(t *testing.T) {
	opts := testOptions("linux", ScopeUser)
	opts.Binary = "acolyte"
	if _, err := Render(opts); err == nil {
		t.Fatal("expected error for relative binary")
	}
}

func TestManualContainsCommands(t *testing.T) {
	plan, err := Render(testOptions("darwin", ScopeUser))
	if err != nil {
		t.Fatal(err)
	}
	manual := plan.Manual()
	for _, want := range []string{
		"# macOS (launchd), user scope",
		"# 1. Write this file to /Users/alice/Library/LaunchAgents/ai.acolyte.daemon.plist",
		"launchctl bootstrap gui/501",
		"# Logs:   tail -f",
	} {
		if !strings.Contains(manual, want) {
			t.Errorf("manual missing %q:\n%s", want, manual)
		}
	}
}

func TestManualBothMentionsBothPlatforms(t *testing.T) {
	manual := ManualBoth()
	for _, want := range []string{"macOS (launchd)", "Linux (systemd)", "launchctl bootstrap", "systemctl --user enable"} {
		if !strings.Contains(manual, want) {
			t.Errorf("manual missing %q:\n%s", want, manual)
		}
	}
}

func TestShellJoinQuotesUnsafePaths(t *testing.T) {
	if got := ShellJoin([]string{"launchctl", "bootstrap", "gui/501", "/tmp/a b.plist"}); got != "launchctl bootstrap gui/501 '/tmp/a b.plist'" {
		t.Fatalf("ShellJoin = %q", got)
	}
	if got := ShellJoin([]string{"/usr/local/bin/acolyte", "start"}); got != "/usr/local/bin/acolyte start" {
		t.Fatalf("ShellJoin = %q", got)
	}
}

func TestDefaultPathIncludesOpenCode(t *testing.T) {
	path := DefaultPath("/home/alice")
	if !strings.Contains(path, "/home/alice/.opencode/bin") {
		t.Fatalf("PATH missing opencode dir: %s", path)
	}
	if !strings.Contains(path, "/usr/bin") {
		t.Fatalf("PATH missing /usr/bin: %s", path)
	}
}

func TestLaunchdEscapesXML(t *testing.T) {
	opts := testOptions("darwin", ScopeUser)
	opts.Home = "/Users/alice/A & B/.acolyte"
	opts.UserHome = "/Users/alice/A & B"
	plan, err := Render(opts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Content, "A & B") {
		t.Fatalf("plist contains unescaped ampersand:\n%s", plan.Content)
	}
	if !strings.Contains(plan.Content, "A &amp; B") {
		t.Fatalf("plist missing escaped ampersand:\n%s", plan.Content)
	}
}

func TestSystemdEscapesSpecifiers(t *testing.T) {
	opts := testOptions("linux", ScopeUser)
	opts.Home = "/home/alice/100%/.acolyte"
	plan, err := Render(opts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Content, `100%/`) {
		t.Fatalf("unit contains unescaped percent:\n%s", plan.Content)
	}
	if !strings.Contains(plan.Content, "100%%/") {
		t.Fatalf("unit missing escaped percent:\n%s", plan.Content)
	}
}
