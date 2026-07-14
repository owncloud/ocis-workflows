// Package command implements the workflows CLI: a small "server"/"health" dispatch,
// deliberately not built on a CLI framework — this backend has no other commands and no
// need for one.
package command

import (
	"fmt"

	"github.com/LukasHirt/ocis-workflows/pkg/config"
)

// Execute dispatches the given CLI arguments to the matching subcommand.
func Execute(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: workflows <server|health>")
	}

	cfg, err := config.FromEnv()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	switch args[0] {
	case "server":
		return RunServer(cfg)
	case "health":
		return RunHealth(cfg)
	default:
		return fmt.Errorf("unknown command %q: usage: workflows <server|health>", args[0])
	}
}
