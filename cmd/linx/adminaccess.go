package main

import (
	"context"
	"fmt"
	"io"
)

// runAdminAccess shows or reopens "where admins may sign in from"
// (docs/ADMIN.md §3) by running the control plane's own admin-access
// command inside its container, exactly like runUser.
func runAdminAccess(ctx context.Context, args []string, stdout, stderr io.Writer, env apiKeyEnv) int {
	if !env.isRoot {
		fmt.Fprintln(stderr, "linx admin-access changes who can manage Linx and must run as root. Try: sudo linx admin-access open")
		return 1
	}
	dockerArgs := append([]string{"exec", controlPlaneContainer, controlPlaneBinary, "admin-access"}, args...)
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
