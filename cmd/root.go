package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var versionValue = "dev"

// envFlags holds repeatable --env KEY=VALUE overrides for the current
// invocation.
var envFlags []string

func SetVersion(v string) {
	versionValue = v
}

var rootCmd = &cobra.Command{
	Use:   "acolyte",
	Short: "Multi-agent orchestrator for OpenCode",
	Long: `Acolyte is a multi-agent orchestrator for OpenCode.

It registers agents, routes channel messages into their OpenCode sessions,
runs scheduled jobs, and provides durable communication delivery.

Secrets resolve in this order (highest first): --env KEY=VALUE, process
environment, <workspace>/.env, $ACOLYTE_HOME/.env. Use
` + "`acolyte config <agent> env`" + ` to see where each value comes from.`,
}

func init() {
	rootCmd.PersistentFlags().StringArrayVar(&envFlags, "env", nil,
		"KEY=VALUE secret override for this run (repeatable; wins over process env and .env files)")
}

// secretOverrides parses the --env flags into a map.
func secretOverrides() (map[string]string, error) {
	overrides := make(map[string]string, len(envFlags))
	for _, raw := range envFlags {
		key, value, ok := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --env %q: expected KEY=VALUE", raw)
		}
		overrides[key] = value
	}
	return overrides, nil
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
