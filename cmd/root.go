package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var versionValue = "dev"

func SetVersion(v string) {
	versionValue = v
}

var rootCmd = &cobra.Command{
	Use:   "acolyte",
	Short: "Multi-agent orchestrator for OpenCode",
	Long: `Acolyte is a multi-agent orchestrator for OpenCode.

It registers agents, routes channel messages into their OpenCode sessions,
runs scheduled jobs, and provides durable communication delivery.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
