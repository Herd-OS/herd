package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	showAll bool
)

// SetVersion sets the version string displayed by --version.
func SetVersion(v string) {
	version = v
}

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "herd",
		Short: "GitHub-native orchestration for agentic development systems",
		Long:  "Herd turns GitHub into your orchestration layer for AI coding agents.\nWork is tracked as Issues, executed by Actions on self-hosted runners,\nand landed as a single reviewed PR.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.SetVersionTemplate(fmt.Sprintf("herd %s\n", version))
	root.Version = version

	root.PersistentFlags().BoolVar(&showAll, "help-all", false, "Show all commands including internal ones")

	root.AddCommand(newConfigCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newPlanCmd())
	root.AddCommand(newDispatchCmd())

	// Override the help function to support --help-all
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if showAll {
			// Temporarily unhide all commands
			for _, c := range cmd.Commands() {
				c.Hidden = false
			}
		}
		defaultHelp(cmd, args)
	})

	return root
}

// Execute runs the root command.
func Execute() {
	root := NewRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
