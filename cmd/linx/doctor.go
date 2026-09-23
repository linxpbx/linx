package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"linxpbx.com/linx/internal/doctor"
	"linxpbx.com/linx/internal/installer"
)

// doctorEnv is everything doctor touches on the host, so tests can fake it.
type doctorEnv struct {
	isRoot      bool
	savedConfig func() ([]byte, error) // reads installer.ConfigPath
	checks      doctor.Env
}

func realDoctorEnv() doctorEnv {
	return doctorEnv{
		isRoot:      os.Geteuid() == 0,
		savedConfig: func() ([]byte, error) { return os.ReadFile(installer.ConfigPath) },
		checks: doctor.Env{
			Runner:       installer.ExecRunner{},
			Now:          time.Now,
			StagingRoots: doctor.LEStagingRoots(),
			Stat:         os.Stat,
		},
	}
}

const doctorUsage = `Usage: sudo linx doctor

Checks that Linx is working and says how to fix anything that isn't.
Today it checks the certificates; more checks arrive with each release.
`

func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer, env doctorEnv) int {
	if len(args) > 0 {
		code := 2
		if args[0] == "--help" || args[0] == "-h" {
			code = 0
		}
		fmt.Fprint(stderr, doctorUsage)
		return code
	}
	if !env.isRoot {
		fmt.Fprintln(stderr, "linx doctor reads Linx's settings and certificates and must run as root. Try: sudo linx doctor")
		return 1
	}
	b, err := env.savedConfig()
	if err != nil {
		fmt.Fprintln(stderr, "Linx isn't set up on this server yet. Run: sudo linx setup")
		return 1
	}
	cfg, err := installer.ParseConfig(strings.NewReader(string(b)))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	fmt.Fprintln(stdout, "Certificates")
	rs := doctor.Certificates(ctx, env.checks, cfg)
	var problems, warnings int
	for _, r := range rs {
		fmt.Fprintf(stdout, "  %-8s %s\n", r.Level, r.Message)
		if r.Fix != "" {
			fmt.Fprintf(stdout, "  %-8s Fix: %s\n", "", r.Fix)
		}
		switch r.Level {
		case installer.Fail:
			problems++
		case installer.Warn:
			warnings++
		}
	}

	fmt.Fprintln(stdout)
	switch {
	case problems == 0 && warnings == 0:
		fmt.Fprintln(stdout, "Everything checked is working.")
	case problems == 0:
		fmt.Fprintf(stdout, "Working, with %s to look at.\n", plural(warnings, "warning"))
	default:
		fmt.Fprintf(stdout, "Found %s and %s. Each one above says how to fix it.\n", plural(problems, "problem"), plural(warnings, "warning"))
		return 1
	}
	return 0
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
