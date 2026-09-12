// Package cli wires kraai's command-line surface. cmd/kraai stays a thin
// entrypoint; every Cobra command lives here per docs/BLUEPRINT.md D20.
package cli

import (
	"github.com/spf13/cobra"
)

// NewRootCommand builds the kraai root command. Subsequent workstreams add
// plan/apply/destroy/status/gc as children of this command.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "kraai",
		Short: "kraai is a multi-cloud devops control plane",
		Long: "kraai declares environments as manifests and applies them with\n" +
			"one command, across cloud providers. See https://github.com/evatt-labs/kraai.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newVersionCommand())

	return root
}

// Execute runs the root command with the given args (typically os.Args[1:])
// and returns the error the command produced, if any. cmd/kraai/main.go owns
// turning that error into process output and an exit code.
func Execute(args []string) error {
	root := NewRootCommand()
	root.SetArgs(args)
	return root.Execute()
}
