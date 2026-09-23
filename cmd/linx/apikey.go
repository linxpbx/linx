package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// controlPlaneContainer and controlPlaneBinary are where `linx api-key`
// runs its server-side half (deploy/compose/compose.yaml,
// deploy/docker/go-service.Dockerfile).
const (
	controlPlaneContainer = "linx-control-plane"
	controlPlaneBinary    = "/usr/local/bin/service"
)

// apiKeyEnv is everything api-key touches on the host, so tests can fake it.
type apiKeyEnv struct {
	isRoot bool
	// run runs a command with the given output streams and returns its exit code.
	run func(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) (int, error)
}

func realAPIKeyEnv() apiKeyEnv {
	return apiKeyEnv{
		isRoot: os.Geteuid() == 0,
		run: func(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) (int, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdout, cmd.Stderr = stdout, stderr
			err := cmd.Run()
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return exitErr.ExitCode(), nil
			}
			return 0, err
		},
	}
}

// runAPIKey creates, lists and revokes API keys (docs/API.md §3 "First
// key") by running the control plane's own api-key command inside its
// container, so it uses the database credentials already there and nothing
// secret touches the host except the one-time key it prints.
func runAPIKey(ctx context.Context, args []string, stdout, stderr io.Writer, env apiKeyEnv) int {
	if !env.isRoot {
		fmt.Fprintln(stderr, "linx api-key manages who can use the Linx API and must run as root. Try: sudo linx api-key create --name \"My laptop\" --role admin")
		return 1
	}
	dockerArgs := append([]string{"exec", controlPlaneContainer, controlPlaneBinary, "api-key"}, args...)
	code, err := env.run(ctx, stdout, stderr, "docker", dockerArgs...)
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't run docker: %v\n", err)
		return 1
	}
	// 125-127: docker itself failed (container missing, not running, ...).
	if code >= 125 {
		fmt.Fprintln(stderr, "\nLinx doesn't seem to be running. Check with: sudo linx doctor")
		return 1
	}
	return code
}
