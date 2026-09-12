// Command kraai is the CLI entrypoint. It stays thin by design (see
// docs/BLUEPRINT.md D20): all command wiring lives in internal/cli, and all
// business logic lives deeper under internal/. The centralized error
// handling and exit-code mapping described in D18/D19 lands with the
// errors-package workstream; this minimal fallback exists only until then.
package main

import (
	"fmt"
	"os"

	"github.com/evatt-labs/kraai/internal/cli"
)

func main() {
	if err := cli.Execute(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
