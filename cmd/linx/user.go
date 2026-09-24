package main

import (
	"context"
	"fmt"
	"io"
)

// runUser creates, lists and issues set-password links for people
// (docs/WEB.md §4) by running the control plane's own user command inside
// its container, exactly like runAPIKey.
func runUser(ctx context.Context, args []string, stdout, stderr io.Writer, env apiKeyEnv) int {
	if !env.isRoot {
		fmt.Fprintln(stderr, "linx user manages who can sign in to Linx and must run as root. Try: sudo linx user create --email \"...\" --name \"...\" --role admin")
		return 1
	}
	dockerArgs := append([]string{"exec", controlPlaneContainer, controlPlaneBinary, "user"}, args...)
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
