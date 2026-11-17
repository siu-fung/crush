// Command crush-search runs the full-text session search without going
// through the `crush` binary. It is a thin wrapper around `crush search`,
// so the two entry points stay in lockstep.
package main

import (
	"os"

	"github.com/charmbracelet/crush/internal/cmd"
)

func main() {
	cmd.ExecuteSearch(os.Args[1:])
}
