// Package ops is System → Status's link to the server itself
// (docs/ADMIN.md §9 "Service health, logs and Restart", ADR-056): which
// Linx containers are running, their recent log lines, and restarting one.
//
// A container can never do any of that (no Docker socket for any Linx
// container, docs/THREAT_MODEL.md): linx-ops-agent, a host service, does.
// It keeps one `docker exec -i linx-control-plane service ops-bridge`
// open, which connects to a private socket the control plane listens on
// inside its own container (Hub), and answers the control plane's requests
// over it. The host side (Agent) only ever acts on the fixed list in
// Services, checked there again whatever the control plane asks: an admin
// account, or a control plane someone broke into, can read these logs and
// restart these services, and nothing else on the host.
package ops

import "slices"

// Service is one Linx container the helper may report on.
type Service struct {
	// Name is the compose service name, what the API and the browser use.
	Name string `json:"service"`
	// Container is its fixed container name (deploy/compose/compose.yaml).
	Container string `json:"-"`
	// Label is its plain-language name on System → Status.
	Label string `json:"label"`
	// Restart: an admin may restart it from the browser. The database and
	// the internal certificate authority may not: everything depends on
	// them, and restarting either fixes nothing a restart of what uses them
	// wouldn't (sudo linx doctor on the server for those).
	Restart bool `json:"can_restart"`
	// Optional: only some installs run it (the front door on 443, WireGuard
	// connections), so its absence isn't a problem.
	Optional bool `json:"optional"`
}

// Services is every container the helper knows, in the order System →
// Status lists them.
var Services = []Service{
	{Name: "control-plane", Container: "linx-control-plane", Label: "Web and API", Restart: true},
	{Name: "asterisk", Container: "linx-asterisk", Label: "Phone system", Restart: true},
	{Name: "coturn", Container: "linx-coturn", Label: "Calls-from-outside relay", Restart: true},
	{Name: "postgres", Container: "linx-postgres", Label: "Database"},
	{Name: "certd", Container: "linx-certd", Label: "Certificates", Restart: true},
	{Name: "step-ca", Container: "linx-step-ca", Label: "Internal certificates"},
	{Name: "wireguard", Container: "linx-wireguard", Label: "WireGuard connections", Restart: true, Optional: true},
	{Name: "sni", Container: "linx-sni", Label: "Front door on port 443", Restart: true, Optional: true},
}

// Lookup finds a service by name.
func Lookup(name string) (Service, bool) {
	i := slices.IndexFunc(Services, func(s Service) bool { return s.Name == name })
	if i < 0 {
		return Service{}, false
	}
	return Services[i], true
}

// Log lines: how many a request may ask for, and how long one may be.
const (
	DefaultLogLines = 200
	MaxLogLines     = 500
	maxLineRunes    = 1000
)

// ClampLines keeps a requested line count within 1..MaxLogLines (0: the
// default).
func ClampLines(n int) int {
	switch {
	case n <= 0:
		return DefaultLogLines
	case n > MaxLogLines:
		return MaxLogLines
	}
	return n
}
