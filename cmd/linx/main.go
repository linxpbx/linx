// Command linx is the Linx installer and operations CLI.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"linxpbx.com/linx/internal/version"
)

const usage = `linx — install and operate a Linx phone system

Usage:
  linx <command>

Commands:
  setup     Check this server and install what Linx needs (run with sudo)
  doctor    Check that everything is working (coming in Phase 0b)
  version   Show the Linx version
  help      Show this help
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version.String("linx"))
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	case "setup":
		return runSetup(context.Background(), args[1:], stdout, stderr, realSetupEnv())
	case "doctor":
		fmt.Fprintf(stderr, "linx %s: not available yet in this build.\n", args[0])
		return 1
	default:
		fmt.Fprintf(stderr, "linx: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
