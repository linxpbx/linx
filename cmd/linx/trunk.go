package main

import (
	"context"
	"fmt"
	"io"
)

// runTrunk adds, lists, tests and removes phone lines (docs/TRUNKS.md §10)
// by running the control plane's own trunk command inside its container,
// like runUser. Its input is passed through (docker exec -i), with a
// terminal when there is one, so `linx trunk add` can ask its questions
// and read the password without showing it.
func runTrunk(ctx context.Context, args []string, stdout, stderr io.Writer, env apiKeyEnv) int {
	if !env.isRoot {
		fmt.Fprintln(stderr, "linx trunk changes the phone system's lines and must run as root. Try: sudo linx trunk list")
		return 1
	}
	dockerArgs := []string{"exec", "-i"}
	if env.terminal {
		dockerArgs = append(dockerArgs, "-t")
	}
	dockerArgs = append(dockerArgs, controlPlaneContainer, controlPlaneBinary, "trunk")
	code, err := env.run(ctx, stdout, stderr, "docker", append(dockerArgs, args...)...)
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't run docker: %v\n", err)
		return 1
	}
	if code >= 125 {
		fmt.Fprintln(stderr, "\nLinx doesn't seem to be running. Check with: sudo linx doctor")
		return 1
	}
	return code
}
