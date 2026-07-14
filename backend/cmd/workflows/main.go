// Command workflows is the entry point for the workflows sidecar backend.
package main

import (
	"fmt"
	"os"

	"github.com/LukasHirt/ocis-workflows/pkg/command"
)

func main() {
	if err := command.Execute(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
