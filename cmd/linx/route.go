package main

import (
	"context"
	"fmt"
	"io"
)

// runRoute shows what Linx would do with an outgoing call (docs/TRUNKS.md
// §5) by running the control plane's own route command inside its
// container, exactly like runUser. It never places a call.
func runRoute(ctx context.Context, args []string, stdout, stderr io.Writer, env apiKeyEnv) int {
	if !env.isRoot {
		fmt.Fprintln(stderr, "linx route reads the phone system's settings and must run as root. Try: sudo linx route test 0501234567 --from 101")
		return 1
	}
	dockerArgs := append([]string{"exec", controlPlaneContainer, controlPlaneBinary, "route"}, args...)
	code, err := env.run(ctx, stdout, stderr, "docker", dockerArgs...)
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
