package service

import (
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"
)

func renderLaunchd(opts Options) (Plan, error) {
	var (
		path    string
		logBase = filepath.Join(opts.Home, LogDir)
	)
	if opts.Scope == ScopeSystem {
		path = filepath.Join("/Library/LaunchDaemons", Label+".plist")
	} else {
		path = filepath.Join(opts.UserHome, "Library", "LaunchAgents", Label+".plist")
	}

	target := domainTarget(opts) + "/" + Label
	sudo := ""
	notes := []string{"Starts at login. Use --system to start at boot before login."}
	if opts.Scope == ScopeSystem {
		sudo = "sudo "
		notes = []string{"Starts at boot."}
	}

	return Plan{
		Platform: platformName(opts.GOOS),
		Scope:    opts.Scope,
		Path:     path,
		Content:  launchdPlist(opts, filepath.Join(logBase, "daemon.log"), filepath.Join(logBase, "daemon.err.log")),
		Commands: []Command{
			{Args: []string{"launchctl", "bootout", target}, IgnoreError: true},
			{Args: []string{"launchctl", "enable", target}},
			{Args: []string{"launchctl", "bootstrap", domainTarget(opts), path}},
		},
		LogPath:    filepath.Join(logBase, "daemon.log"),
		LogsHint:   "tail -f " + filepath.Join(logBase, "daemon.log"),
		StatusHint: sudo + "launchctl print " + target,
		StopHint:   sudo + "launchctl bootout " + target,
		Notes:      notes,
	}, nil
}

func launchdPlist(opts Options, logPath, errPath string) string {
	var b strings.Builder

	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")

	writePlistString(&b, "  ", "Label", Label)

	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range []string{opts.Binary, "start", "--log-file", logPath} {
		writePlistString(&b, "    ", "", arg)
	}
	b.WriteString("  </array>\n")

	writePlistString(&b, "  ", "WorkingDirectory", opts.Home)
	b.WriteString("  <key>RunAtLoad</key><true/>\n")
	b.WriteString("  <key>KeepAlive</key><true/>\n")
	b.WriteString("  <key>ThrottleInterval</key><integer>10</integer>\n")

	b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
	writePlistString(&b, "    ", "HOME", opts.UserHome)
	writePlistString(&b, "    ", "ACOLYTE_HOME", opts.Home)
	writePlistString(&b, "    ", "PATH", opts.PATH)
	if opts.Shell != "" {
		writePlistString(&b, "    ", "SHELL", opts.Shell)
	}
	b.WriteString("  </dict>\n")

	writePlistString(&b, "  ", "StandardOutPath", errPath)
	writePlistString(&b, "  ", "StandardErrorPath", errPath)

	if opts.Scope == ScopeSystem {
		writePlistString(&b, "  ", "UserName", opts.UserName)
	}

	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// writePlistString writes an optional key followed by one escaped string value.
func writePlistString(b *strings.Builder, indent, key, value string) {
	if key != "" {
		fmt.Fprintf(b, "%s<key>%s</key>\n", indent, key)
	}
	fmt.Fprintf(b, "%s<string>%s</string>\n", indent, xmlEscape(value))
}

func xmlEscape(value string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}
