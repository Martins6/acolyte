package service

import (
	"fmt"
	"path/filepath"
	"strings"
)

func renderSystemd(opts Options) (Plan, error) {
	var path string
	if opts.Scope == ScopeSystem {
		path = filepath.Join("/etc/systemd/system", UnitName)
	} else {
		path = filepath.Join(opts.UserHome, ".config", "systemd", "user", UnitName)
	}

	prefix := []string{"systemctl"}
	sudo := ""
	if opts.Scope == ScopeUser {
		prefix = append(prefix, "--user")
	} else {
		sudo = "sudo "
	}

	enable := append(append([]string{}, prefix...), "enable", UnitName)
	restart := append(append([]string{}, prefix...), "restart", UnitName)
	reload := append(append([]string{}, prefix...), "daemon-reload")

	logsHint := "journalctl -u " + UnitName + " -f"
	statusHint := "systemctl status " + UnitName
	stopHint := sudo + "systemctl stop " + UnitName
	notes := []string{"Starts at boot."}
	if opts.Scope == ScopeUser {
		logsHint = "journalctl --user -u " + UnitName + " -f"
		statusHint = "systemctl --user status " + UnitName
		stopHint = "systemctl --user stop " + UnitName
		notes = []string{"Starts at login. For boot without login, run: loginctl enable-linger " + opts.UserName}
	}

	return Plan{
		Platform: platformName(opts.GOOS),
		Scope:    opts.Scope,
		Path:     path,
		Content:  systemdUnit(opts),
		Commands: []Command{
			{Args: reload},
			{Args: enable},
			{Args: restart},
		},
		LogsHint:   logsHint,
		StatusHint: statusHint,
		StopHint:   stopHint,
		Notes:      notes,
	}, nil
}

func systemdUnit(opts Options) string {
	var b strings.Builder

	b.WriteString("[Unit]\n")
	b.WriteString("Description=Acolyte orchestrator\n")
	b.WriteString("StartLimitIntervalSec=0\n")
	if opts.Scope == ScopeSystem {
		b.WriteString("After=network-online.target\n")
		b.WriteString("Wants=network-online.target\n")
	}
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")

	writeSystemdEnv(&b, "HOME", opts.UserHome)
	writeSystemdEnv(&b, "ACOLYTE_HOME", opts.Home)
	writeSystemdEnv(&b, "PATH", opts.PATH)

	fmt.Fprintf(&b, "WorkingDirectory=%s\n", systemdQuote(opts.Home, false))
	fmt.Fprintf(&b, "ExecStart=%s %s\n", systemdQuote(opts.Binary, true), systemdQuote("start", true))
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=5\n")
	b.WriteString("SyslogIdentifier=" + ServiceID + "\n")
	b.WriteString("StandardOutput=journal\n")
	b.WriteString("StandardError=journal\n")
	if opts.Scope == ScopeSystem {
		fmt.Fprintf(&b, "User=%s\n", opts.UserName)
	}

	b.WriteString("\n[Install]\n")
	if opts.Scope == ScopeSystem {
		b.WriteString("WantedBy=multi-user.target\n")
	} else {
		b.WriteString("WantedBy=default.target\n")
	}
	return b.String()
}

func writeSystemdEnv(b *strings.Builder, key, value string) {
	fmt.Fprintf(b, "Environment=%s\n", systemdQuote(key+"="+value, false))
}

// systemdQuote wraps a value in double quotes and escapes systemd's expansion
// characters. Dollar signs are only expanded in ExecStart/WorkingDirectory.
func systemdQuote(value string, escapeDollar bool) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, `%`, `%%`)
	if escapeDollar {
		value = strings.ReplaceAll(value, `$`, `$$`)
	}
	return `"` + value + `"`
}
