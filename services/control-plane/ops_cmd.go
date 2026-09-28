package main

import (
	"fmt"
	"io"
	"net"

	"linxpbx.com/linx/internal/ops"
)

// runOpsBridge is `ops-bridge`: linx-ops-agent runs it through docker exec
// -i (the same trust as linx user) and it joins its input and output to
// the server's helper socket (internal/ops.Hub), for as long as either end
// stays open. Nothing else calls it.
func runOpsBridge(socket string, stdin io.Reader, stdout, stderr io.Writer) int {
	c, err := net.Dial("unix", socket)
	if err != nil {
		fmt.Fprintf(stderr, "The control plane isn't ready for the server helper yet: %v\n", err)
		return 1
	}
	defer c.Close()
	go func() {
		_, _ = io.Copy(c, stdin)
		// The agent closed its end: tell the hub, which then closes too.
		if uc, ok := c.(*net.UnixConn); ok {
			_ = uc.CloseWrite()
		}
	}()
	_, _ = io.Copy(stdout, c)
	return 0
}

func opsSocket(getenv func(string) string) string {
	return envOr(getenv, "LINX_OPS_SOCKET", ops.DefaultSocket)
}
