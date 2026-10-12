// Command livekit-entrypoint runs linx-livekit, Linx's video server for
// group calls (ADR-088): it finds the container's own address on
// linx-media (where coturn relays to it), renders LiveKit's configuration
// for that address alone (internal/livekit), hands LiveKit its key through
// the environment — LiveKit insists a key file be mode 0600, and Linx's
// Docker secrets are 0440 for the nonroot group — and becomes LiveKit.
//
// `livekit-entrypoint healthcheck` is the container's Docker health check.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"linxpbx.com/linx/internal/livekit"
	"linxpbx.com/linx/internal/version"
)

const service = "linx-livekit"

const livekitBin = "/usr/local/bin/livekit-server"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service)
	log.Info("starting", "version", version.String(service))

	secret, err := os.ReadFile(envOr("LINX_LIVEKIT_SECRET_FILE", "/run/secrets/linx_livekit_secret"))
	if err != nil || len(strings.TrimSpace(string(secret))) < 32 {
		log.Error("reading the video server's key", "err", err)
		os.Exit(2)
	}
	host := envOr("LINX_LIVEKIT_MEDIA_HOST", livekit.DefaultMediaHost)
	ip, err := ownAddress(host)
	if err != nil {
		log.Error("finding this container on linx-media", "host", host, "err", err)
		os.Exit(1)
	}
	conf := envOr("LINX_LIVEKIT_CONF", "/tmp/linx-livekit/livekit.yaml")
	if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
		log.Error("config folder", "err", err)
		os.Exit(2)
	}
	if err := os.WriteFile(conf, []byte(livekit.Config(ip)), 0o600); err != nil {
		log.Error("writing the configuration", "err", err)
		os.Exit(2)
	}
	log.Info("media on linx-media only", "address", ip, "port", livekit.MediaPort)
	env := append(os.Environ(), "LIVEKIT_KEYS="+livekit.Key+": "+strings.TrimSpace(string(secret)))
	if err := syscall.Exec(livekitBin, []string{livekitBin, "--config", conf}, env); err != nil {
		log.Error("starting LiveKit", "err", err)
		os.Exit(1)
	}
}

// ownAddress is this container's IPv4 address on the network where it is
// called host (an alias only that network knows), retried while Docker's
// DNS catches up with a container that has just started.
func ownAddress(host string) (string, error) {
	var last error
	for range 15 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		cancel()
		if err == nil && len(ips) > 0 {
			return ips[0].Unmap().String(), nil
		}
		last = err
		time.Sleep(time.Second)
	}
	return "", fmt.Errorf("no address: %v", last)
}

func healthcheck() int {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", livekit.SignalPort))
	if err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "unhealthy: status", resp.StatusCode)
		return 1
	}
	return 0
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
