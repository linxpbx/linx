package main

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// capNetAdmin is CAP_NET_ADMIN (linux/capability.h).
const capNetAdmin = 12

// dropPrivileges runs this binary again as runAsUID with NET_ADMIN as its
// one (ambient) capability, passes SIGTERM and SIGINT on, and returns the
// child's exit code. The child can't gain anything back: the container has
// no-new-privileges and its bounding set is NET_ADMIN alone.
func dropPrivileges(log *slog.Logger) int {
	self, err := os.Executable()
	if err != nil {
		log.Error("finding this program", "err", err)
		return 1
	}
	cmd := exec.Command(self, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential:  &syscall.Credential{Uid: runAsUID, Gid: runAsUID, Groups: []uint32{}},
		AmbientCaps: []uintptr{capNetAdmin},
	}
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	if err := cmd.Start(); err != nil {
		log.Error("starting as a non-root user with NET_ADMIN", "err", err)
		return 1
	}
	go func() {
		for s := range sigs {
			cmd.Process.Signal(s)
		}
	}()
	err = cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		return 1
	}
	return 0
}
