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
	Short: "Telegram bot gateway for OpenCode AI agent",
	Long: `A Telegram bot that acts as a gateway to an OpenCode server,
allowing users to interact with the OpenCode agent directly from Telegram.

The bot handles text, media (images, audio, files), and provides 
configuration via slash commands.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
