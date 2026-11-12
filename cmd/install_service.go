package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/service"
	"github.com/spf13/cobra"
)

var (
	installServiceUser   bool
	installServiceSystem bool
	installServicePrint  bool
)

var installServiceCmd = &cobra.Command{
	Use:   "install-service",
	Short: "Install acolyte as a crash-restarting background service",
	Long: `Install acolyte as a background service.

The service runs 'acolyte start' for every registered agent, restarts it when
it crashes, and starts it automatically when you log in (macOS LaunchAgent or
systemd user unit). With --system it starts at boot and requires root.

Logs go to journald on Linux and to a size-rotated file under
$ACOLYTE_HOME/logs on macOS.

Use --print to inspect the exact unit file and commands without changing
anything. On an unsupported platform the command prints manual instructions
for both Linux and macOS; --system without root prints the exact sudo command
and the generated unit instead of writing anything.`,
	Example: `  acolyte install-service              # current user, starts at login
  acolyte install-service --system     # starts at boot (run with sudo)
  acolyte install-service --print      # show the unit and commands`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		scope, err := resolveInstallScope()
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()

		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			fmt.Fprint(out, service.ManualBoth())
			if installServicePrint {
				return nil
			}
			return fmt.Errorf("automatic service installation is not supported on %s", runtime.GOOS)
		}

		opts, err := buildServiceOptions(scope)
		if err != nil {
			return err
		}
		if !pathHasExecutable(opts.PATH, "opencode") {
			fmt.Fprintln(out, "warning: 'opencode' was not found in the service PATH; install the OpenCode CLI before starting the service.")
			fmt.Fprintln(out)
		}
		plan, err := service.Render(opts)
		if err != nil {
			return err
		}

		if installServicePrint {
			fmt.Fprint(out, plan.Manual())
			return nil
		}

		if plan.Scope == service.ScopeSystem && os.Geteuid() != 0 {
			fmt.Fprintf(out, "System scope needs root. Re-run:\n  sudo %s install-service --system\n\n", opts.Binary)
			fmt.Fprint(out, plan.Manual())
			return fmt.Errorf("system scope requires root")
		}

		if err := prepareServiceDirs(opts); err != nil {
			return err
		}
		if err := service.WriteUnit(plan.Path, plan.Content); err != nil {
			return err
		}
		if err := runServiceCommands(plan); err != nil {
			return err
		}
		if opts.GOOS == "linux" && plan.Scope == service.ScopeUser {
			warnLinger(out, opts.UserName)
		}

		fmt.Fprint(out, plan.Summary())
		return nil
	},
}

func init() {
	installServiceCmd.Flags().BoolVar(&installServiceUser, "user", false, "Install for the current user (starts at login; default)")
	installServiceCmd.Flags().BoolVar(&installServiceSystem, "system", false, "Install system-wide (starts at boot; requires root)")
	installServiceCmd.Flags().BoolVar(&installServicePrint, "print", false, "Print the unit and commands without installing")
	rootCmd.AddCommand(installServiceCmd)
}

func resolveInstallScope() (service.Scope, error) {
	if installServiceUser && installServiceSystem {
		return "", fmt.Errorf("use --user or --system, not both")
	}
	if installServiceSystem {
		return service.ScopeSystem, nil
	}
	return service.ScopeUser, nil
}

func buildServiceOptions(scope service.Scope) (service.Options, error) {
	binary, err := os.Executable()
	if err != nil {
		return service.Options{}, fmt.Errorf("resolve acolyte executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		binary = resolved
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return service.Options{}, fmt.Errorf("resolve acolyte executable: %w", err)
	}

	username, userHome, uid, err := serviceAccount()
	if err != nil {
		return service.Options{}, err
	}
	userHome, err = paths.Canonical(userHome)
	if err != nil {
		return service.Options{}, err
	}

	home := strings.TrimSpace(os.Getenv(paths.HomeEnv))
	if home == "" {
		home = filepath.Join(userHome, ".acolyte")
	}
	home, err = paths.Canonical(home)
	if err != nil {
		return service.Options{}, err
	}

	return service.Options{
		GOOS:     runtime.GOOS,
		Scope:    scope,
		Binary:   binary,
		Home:     home,
		UserHome: userHome,
		UserName: username,
		UID:      uid,
		PATH:     servicePath(userHome),
	}, nil
}

// serviceAccount returns the account the service should run as. Under sudo,
// that is the invoking user, not root.
func serviceAccount() (name, home string, uid int, err error) {
	if os.Geteuid() == 0 {
		if sudoUser := strings.TrimSpace(os.Getenv("SUDO_USER")); sudoUser != "" {
			u, lookupErr := user.Lookup(sudoUser)
			if lookupErr != nil {
				return "", "", 0, fmt.Errorf("resolve sudo user %q: %w", sudoUser, lookupErr)
			}
			uid, parseErr := strconv.Atoi(u.Uid)
			if parseErr != nil {
				return "", "", 0, fmt.Errorf("parse uid for %q: %w", sudoUser, parseErr)
			}
			return u.Username, u.HomeDir, uid, nil
		}
	}

	if u, currentErr := user.Current(); currentErr == nil {
		uid, parseErr := strconv.Atoi(u.Uid)
		if parseErr == nil && strings.TrimSpace(u.HomeDir) != "" {
			return u.Username, u.HomeDir, uid, nil
		}
	}

	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		return "", "", 0, fmt.Errorf("resolve user home: %w", homeErr)
	}
	return os.Getenv("USER"), home, os.Getuid(), nil
}

func servicePath(userHome string) string {
	seen := make(map[string]bool)
	var parts []string
	add := func(entry string) {
		entry = strings.TrimSpace(entry)
		if entry == "" || seen[entry] {
			return
		}
		seen[entry] = true
		parts = append(parts, entry)
	}

	if opencode, err := exec.LookPath("opencode"); err == nil {
		add(filepath.Dir(opencode))
	}
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if isTransientPath(entry) {
			continue
		}
		add(entry)
	}
	for _, entry := range filepath.SplitList(service.DefaultPath(userHome)) {
		add(entry)
	}
	return strings.Join(parts, ":")
}

func pathHasExecutable(pathEnv, name string) bool {
	for _, dir := range filepath.SplitList(pathEnv) {
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

// isTransientPath drops PATH entries that point into a temporary directory.
// Baking those into a service definition would leave dead entries after the
// next reboot (the OpenCode tooling often exports one).
func isTransientPath(entry string) bool {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return false
	}
	for _, prefix := range []string{os.TempDir(), "/tmp/", "/private/tmp/", "/var/folders/", "/private/var/folders/"} {
		if prefix != "" && strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func prepareServiceDirs(opts service.Options) error {
	userHome, err := paths.Canonical(opts.UserHome)
	if err != nil {
		return err
	}
	home, err := paths.Canonical(opts.Home)
	if err != nil {
		return err
	}

	asRoot := os.Geteuid() == 0 && opts.UID != 0
	if asRoot && !isStrictSubdir(userHome, home) {
		return fmt.Errorf("refusing to prepare ACOLYTE_HOME %q as root: it must be a dedicated directory inside %q; move it under your home or run without --system", opts.Home, opts.UserHome)
	}

	dirs := []string{home}
	if opts.GOOS == "darwin" {
		dirs = append(dirs, filepath.Join(home, service.LogDir))
	}
	for _, dir := range dirs {
		info, statErr := os.Lstat(dir)
		existed := statErr == nil
		if statErr != nil && !os.IsNotExist(statErr) {
			return fmt.Errorf("stat %s: %w", dir, statErr)
		}
		if existed && !info.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", dir)
		}
		if !existed {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", dir, err)
			}
		}
		if !asRoot {
			continue
		}
		if existed && !ownedByUID(info, opts.UID) {
			// Never change ownership of a pre-existing directory the target
			// user does not own: that is a destructive sudo-only side effect.
			return fmt.Errorf("refusing to change ownership of existing directory %q; run: sudo chown %d %q", dir, opts.UID, dir)
		}
		if err := os.Chown(dir, opts.UID, -1); err != nil {
			return fmt.Errorf("chown %s: %w", dir, err)
		}
	}
	return nil
}

// isStrictSubdir reports whether path is a dedicated directory below base.
func isStrictSubdir(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func ownedByUID(info os.FileInfo, uid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == uid
}

func runServiceCommands(plan service.Plan) error {
	for _, command := range plan.Commands {
		if len(command.Args) == 0 {
			continue
		}
		output, err := exec.Command(command.Args[0], command.Args[1:]...).CombinedOutput()
		if err == nil || command.IgnoreError {
			continue
		}

		detail := strings.TrimSpace(string(output))
		if detail != "" {
			detail = "\n" + detail
		}
		manual := make([]string, 0, len(plan.Commands))
		for _, step := range plan.Commands {
			manual = append(manual, "  "+service.ShellJoin(step.Args))
		}
		return fmt.Errorf("run %s: %w%s\nservice file kept at %s; start it manually:\n%s",
			service.ShellJoin(command.Args), err, detail, plan.Path, strings.Join(manual, "\n"))
	}
	return nil
}

func warnLinger(out io.Writer, username string) {
	if username == "" {
		return
	}
	if err := exec.Command("loginctl", "enable-linger", username).Run(); err != nil {
		fmt.Fprintf(out, "\nwarning: could not enable linger for %s; the service starts at login but not at boot.\nRun: sudo loginctl enable-linger %s\n", username, username)
	}
}
