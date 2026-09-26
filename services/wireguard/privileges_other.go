//go:build !linux

package main

import "log/slog"

// dropPrivileges: linx-wireguard only runs on Linux.
func dropPrivileges(log *slog.Logger) int {
	log.Error("linx-wireguard runs on Linux only")
	return 1
}
