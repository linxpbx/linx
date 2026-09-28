package ops

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
)

// The control plane's container and binary, as linx user and
// linx-backup-agent reach them.
const (
	ControlPlaneContainer = "linx-control-plane"
	ControlPlaneBinary    = "/usr/local/bin/service"
)

// RunCommand is Exec on this machine.
func RunCommand(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

// DialControlPlane is Agent.Dial on the host: it starts the control
// plane's ops-bridge through docker exec -i, whose input and output are
// the link. Its errors (the control plane not running yet, say) go to this
// process's standard error.
func DialControlPlane(ctx context.Context) (io.ReadWriteCloser, error) {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", ControlPlaneContainer, ControlPlaneBinary, "ops-bridge")
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &bridge{cmd: cmd, in: in, out: out}, nil
}

type bridge struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.ReadCloser
	once sync.Once
}

func (b *bridge) Read(p []byte) (int, error)  { return b.out.Read(p) }
func (b *bridge) Write(p []byte) (int, error) { return b.in.Write(p) }

// Close ends the bridge (safe to call more than once, from any goroutine).
func (b *bridge) Close() error {
	b.once.Do(func() {
		b.in.Close()
		_ = b.cmd.Process.Kill()
		_ = b.cmd.Wait()
	})
	return nil
}
