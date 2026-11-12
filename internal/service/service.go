// Package service renders and writes the OS service definitions that keep
// `acolyte start` running in the background.
package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"
)

const (
	// Label is the macOS launchd job label.
	Label = "ai.acolyte.daemon"
	// UnitName is the Linux systemd unit name.
	UnitName = "acolyte.service"
	// ServiceID is the short name used by systemd/logging hints.
	ServiceID = "acolyte"
	// LogDir is the per-home directory that holds daemon log files.
	LogDir = "logs"
)

// Scope selects whether the service runs for one user or for the whole machine.
type Scope string

const (
	// ScopeUser starts at login and runs as the installing user.
	ScopeUser Scope = "user"
	// ScopeSystem starts at boot and runs as the target user (needs root).
	ScopeSystem Scope = "system"
)

// Options describes the service to render.
type Options struct {
	// GOOS overrides the target platform (defaults to runtime.GOOS).
	GOOS string
	// Scope is ScopeUser or ScopeSystem (defaults to ScopeUser).
	Scope Scope
	// Binary is the absolute path to the acolyte executable.
	Binary string
	// Home is the ACOLYTE_HOME directory.
	Home string
	// UserHome is the target user's home directory.
	UserHome string
	// UserName is the target account name.
	UserName string
	// UID is the target user's numeric ID (used for the launchd gui domain).
	UID int
	// PATH is the PATH the service should run with.
	PATH string
}

// Command is one service-manager command in the install sequence.
type Command struct {
	Args        []string
	IgnoreError bool
}

// Plan is a rendered service definition plus the commands that install it.
type Plan struct {
	Platform   string
	Scope      Scope
	Path       string
	Content    string
	Commands   []Command
	LogPath    string
	LogsHint   string
	StatusHint string
	StopHint   string
	Notes      []string
}

// Render returns the service definition for opts.GOOS.
func Render(opts Options) (Plan, error) {
	if err := applyDefaults(&opts); err != nil {
		return Plan{}, err
	}

	switch opts.GOOS {
	case "darwin":
		return renderLaunchd(opts)
	case "linux":
		return renderSystemd(opts)
	default:
		return Plan{}, fmt.Errorf("service: unsupported platform %q (supported: linux, darwin)", opts.GOOS)
	}
}

// Manual renders the plan as copy-paste instructions.
func (p Plan) Manual() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s, %s scope\n", p.Platform, p.Scope)
	if p.Scope == ScopeSystem {
		b.WriteString("# Run the write and the commands below as root (sudo).\n")
	}
	for _, note := range p.Notes {
		fmt.Fprintf(&b, "# Note: %s\n", note)
	}
	fmt.Fprintf(&b, "# 1. Write this file to %s\n\n", p.Path)
	b.WriteString(p.Content)
	b.WriteString("\n# 2. Reload and start the service\n")
	for _, cmd := range p.Commands {
		fmt.Fprintf(&b, "%s\n", ShellJoin(cmd.Args))
	}
	fmt.Fprintf(&b, "\n# Logs:   %s\n", p.LogsHint)
	fmt.Fprintf(&b, "# Status: %s\n", p.StatusHint)
	fmt.Fprintf(&b, "# Stop:   %s\n", p.StopHint)
	return b.String()
}

// Summary renders a short post-install report.
func (p Plan) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Installed acolyte service.\n\n")
	fmt.Fprintf(&b, "  Platform: %s\n", p.Platform)
	fmt.Fprintf(&b, "  Scope:    %s\n", p.Scope)
	fmt.Fprintf(&b, "  Unit:     %s\n", p.Path)
	if p.LogPath != "" {
		fmt.Fprintf(&b, "  Log:      %s\n", p.LogPath)
	}
	fmt.Fprintf(&b, "  Logs:     %s\n", p.LogsHint)
	fmt.Fprintf(&b, "  Status:   %s\n", p.StatusHint)
	fmt.Fprintf(&b, "  Stop:     %s\n", p.StopHint)
	for _, note := range p.Notes {
		fmt.Fprintf(&b, "  Note:     %s\n", note)
	}
	return b.String()
}

// ManualBoth renders the user-scope instructions for Linux and macOS. It is
// used when the current platform cannot be configured directly.
func ManualBoth() string {
	var b strings.Builder
	b.WriteString("# Replace /usr/local/bin/acolyte and the placeholder home paths below\n")
	b.WriteString("# with the values for your machine.\n\n")
	for _, goos := range []string{"darwin", "linux"} {
		opts := placeholderOptions(goos)
		plan, err := Render(opts)
		if err != nil {
			continue
		}
		b.WriteString(plan.Manual())
		b.WriteString("\n")
	}
	return b.String()
}

// DefaultPath returns the PATH a service should run with so it can find
// acolyte's dependencies (notably opencode) without a login shell.
func DefaultPath(userHome string) string {
	return strings.Join([]string{
		filepath.Join(userHome, ".opencode", "bin"),
		filepath.Join(userHome, ".local", "bin"),
		filepath.Join(userHome, "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
		"/snap/bin",
	}, ":")
}

// ShellJoin quotes args for display in a POSIX shell.
func ShellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func applyDefaults(opts *Options) error {
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.Scope == "" {
		opts.Scope = ScopeUser
	}
	if opts.Scope != ScopeUser && opts.Scope != ScopeSystem {
		return fmt.Errorf("service: invalid scope %q", opts.Scope)
	}
	if strings.TrimSpace(opts.Binary) == "" {
		return fmt.Errorf("service: binary path is required")
	}
	if !filepath.IsAbs(opts.Binary) {
		return fmt.Errorf("service: binary path %q must be absolute", opts.Binary)
	}
	if strings.TrimSpace(opts.Home) == "" {
		if opts.UserHome == "" {
			return fmt.Errorf("service: home or user home is required")
		}
		opts.Home = filepath.Join(opts.UserHome, ".acolyte")
	}
	if !filepath.IsAbs(opts.Home) {
		return fmt.Errorf("service: acolyte home %q must be absolute", opts.Home)
	}
	if opts.UserHome == "" {
		opts.UserHome = filepath.Dir(opts.Home)
	}
	if !filepath.IsAbs(opts.UserHome) {
		return fmt.Errorf("service: user home %q must be absolute", opts.UserHome)
	}
	if strings.TrimSpace(opts.UserName) == "" {
		opts.UserName = os.Getenv("USER")
	}
	if opts.Scope == ScopeSystem && strings.TrimSpace(opts.UserName) == "" {
		return fmt.Errorf("service: user name is required for system scope")
	}
	if opts.UID < 0 {
		return fmt.Errorf("service: uid must not be negative")
	}
	if opts.UID == 0 {
		opts.UID = os.Getuid()
	}
	if strings.TrimSpace(opts.PATH) == "" {
		opts.PATH = DefaultPath(opts.UserHome)
	}

	// Unit files are line-oriented: a newline in any value would inject a
	// directive into systemd units, and control characters are invalid XML in
	// launchd plists. Reject them before rendering either format.
	for name, value := range map[string]string{
		"binary":    opts.Binary,
		"acolyte":   opts.Home,
		"user home": opts.UserHome,
		"user name": opts.UserName,
		"PATH":      opts.PATH,
	} {
		if err := validateNoControl(name, value); err != nil {
			return err
		}
	}
	return nil
}

func validateNoControl(field, value string) error {
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("service: %s contains a control character (U+%04X)", field, r)
		}
	}
	return nil
}

func placeholderOptions(goos string) Options {
	if goos == "darwin" {
		return Options{
			GOOS:     goos,
			Scope:    ScopeUser,
			Binary:   "/usr/local/bin/acolyte",
			Home:     "/Users/YOU/.acolyte",
			UserHome: "/Users/YOU",
			UserName: "YOU",
			UID:      501,
			PATH:     DefaultPath("/Users/YOU"),
		}
	}
	return Options{
		GOOS:     goos,
		Scope:    ScopeUser,
		Binary:   "/usr/local/bin/acolyte",
		Home:     "/home/you/.acolyte",
		UserHome: "/home/you",
		UserName: "you",
		UID:      1000,
		PATH:     DefaultPath("/home/you"),
	}
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') &&
			!(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') &&
			!strings.ContainsRune("_-./:@%+=", r)
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func platformName(goos string) string {
	switch goos {
	case "darwin":
		return "macOS (launchd)"
	case "linux":
		return "Linux (systemd)"
	default:
		return goos
	}
}

func domainTarget(opts Options) string {
	if opts.Scope == ScopeSystem {
		return "system"
	}
	return "gui/" + strconv.Itoa(opts.UID)
}
