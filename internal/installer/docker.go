package installer

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"linxpbx.com/linx/internal/hostinfo"
)

// Minimum Docker versions Linx supports.
var (
	MinEngine  = [3]int{27, 0, 0}
	MinCompose = [3]int{2, 24, 0}
)

// dockerKey is Docker's apt signing key, pinned in the binary rather than
// downloaded at install time. Fingerprint 9DC8 5822 9FC7 DD38 854A E2D8 8D81
// 803C 0EBF CD88 (checked by the tests). Same key for Ubuntu and Debian.
//
//go:embed docker.asc
var dockerKey []byte

// helloWorldImage verifies the install. Pinned by digest (multi-arch index).
const helloWorldImage = "hello-world:latest@sha256:5e23090353324d887c48ad5e5c56d294eab81588df9605b07d1afe895f9cc8f8"

// ServiceUser is the system account that owns Linx files and may use Docker.
// It has no login shell.
const ServiceUser = "linx"

// Where Docker came from.
const (
	SourceOfficial = "official" // docker-ce from download.docker.com
	SourceDistro   = "distro"   // docker.io from the OS repository
	SourceSnap     = "snap"
	SourceUnknown  = "unknown"
)

// conflictingPackages must be removed before installing Docker's packages
// (list from Docker's install guide).
var conflictingPackages = []string{"docker.io", "docker-doc", "docker-compose", "docker-compose-v2", "podman-docker", "containerd", "runc"}

var dockerPackages = []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"}

// DockerState is what's currently on the host.
type DockerState struct {
	Installed bool
	Source    string
	Engine    string // server version; empty if the daemon isn't reachable
	Compose   string // compose plugin version; empty if missing
}

// DockerAction is what setup needs to do about Docker.
type DockerAction int

const (
	DockerReady   DockerAction = iota // nothing to do
	DockerInstall                     // not installed
	DockerUpgrade                     // installed but too old, not running, or missing Compose
	DockerBlocked                     // needs manual action (snap)
)

// DetectDocker inspects the installed Docker, if any.
func DetectDocker(ctx context.Context, r Runner) DockerState {
	var s DockerState
	engine, err := output(ctx, r, "docker", "version", "--format", "{{.Server.Version}}")
	if errors.Is(err, exec.ErrNotFound) {
		return s
	}
	s.Installed = true
	if err == nil {
		s.Engine = engine
	}
	if v, err := output(ctx, r, "docker", "compose", "version", "--short"); err == nil {
		s.Compose = v
	}
	s.Source = SourceUnknown
	switch {
	case pkgInstalled(ctx, r, "docker-ce"):
		s.Source = SourceOfficial
	case pkgInstalled(ctx, r, "docker.io"):
		s.Source = SourceDistro
	default:
		if _, err := output(ctx, r, "snap", "list", "docker"); err == nil {
			s.Source = SourceSnap
		}
	}
	return s
}

// Assess decides what to do and explains it in plain language.
func (s DockerState) Assess() (DockerAction, string) {
	switch {
	case !s.Installed:
		return DockerInstall, "Docker isn't installed. Linx runs in Docker containers."
	case s.Source == SourceSnap:
		return DockerBlocked, "Docker is installed as a Snap package, which Linx doesn't support. " +
			"Remove it (sudo snap remove docker) and run linx setup again. " +
			"Removing it deletes the containers and data it holds, so back up anything you need first."
	case s.Engine == "":
		return DockerUpgrade, "Docker is installed but isn't running."
	case versionLess(s.Engine, MinEngine):
		return DockerUpgrade, fmt.Sprintf("Docker %s is too old. Linx needs %s or newer.", s.Engine, fmtVersion(MinEngine))
	case s.Compose == "":
		return DockerUpgrade, "Docker Compose isn't installed."
	case versionLess(s.Compose, MinCompose):
		return DockerUpgrade, fmt.Sprintf("Docker Compose %s is too old. Linx needs %s or newer.", s.Compose, fmtVersion(MinCompose))
	}
	return DockerReady, fmt.Sprintf("Docker %s with Compose %s is ready.", s.Engine, s.Compose)
}

// DockerPlan installs (or upgrades to) Docker Engine and the Compose plugin
// from Docker's official repository, adds the service user to the docker
// group and verifies with a test container. It's safe to re-run.
func DockerPlan(ctx context.Context, r Runner, h hostinfo.Info) (Plan, error) {
	repo, err := dockerRepo(h)
	if err != nil {
		return nil, err
	}
	arch, err := output(ctx, r, "dpkg", "--print-architecture")
	if err != nil {
		return nil, fmt.Errorf("reading the package architecture: %w", err)
	}
	if arch != "amd64" && arch != "arm64" {
		return nil, fmt.Errorf("the system's packages are %s; Linx needs a 64-bit (amd64 or arm64) system", arch)
	}

	var p Plan
	var remove []string
	for _, pkg := range conflictingPackages {
		if pkgInstalled(ctx, r, pkg) {
			remove = append(remove, pkg)
		}
	}
	if len(remove) > 0 {
		p = append(p, aptStep("Remove the operating system's Docker packages (containers, images and volumes are kept)",
			append([]string{"remove", "-y"}, remove...)...))
	}
	sources := fmt.Sprintf("Types: deb\nURIs: %s\nSuites: %s\nComponents: stable\nArchitectures: %s\nSigned-By: /etc/apt/keyrings/docker.asc\n",
		repo, h.OSCodename, arch)
	p = append(p,
		aptStep("Refresh the package list", "update"),
		aptStep("Install certificate authorities", "install", "-y", "ca-certificates"),
		fileStep("Add Docker's signing key", "/etc/apt/keyrings/docker.asc", dockerKey, 0o644, 0o755),
		fileStep("Add Docker's official package repository", "/etc/apt/sources.list.d/docker.sources", []byte(sources), 0o644, 0o755),
		aptStep("Refresh the package list", "update"),
		aptStep("Install Docker Engine and Docker Compose", append([]string{"install", "-y"}, dockerPackages...)...),
		cmdStep("Start Docker now and on every boot", "systemctl", "enable", "--now", "docker"),
	)
	if _, err := output(ctx, r, "id", "-u", ServiceUser); err != nil {
		p = append(p, cmdStep("Create the linx service account", "useradd", "--system", "--user-group",
			"--home-dir", "/var/lib/linx", "--no-create-home", "--shell", "/usr/sbin/nologin", ServiceUser))
	}
	p = append(p,
		cmdStep("Allow the linx service account to use Docker", "usermod", "--append", "--groups", "docker", ServiceUser),
		cmdStep("Check Docker works by running a test container", "docker", "run", "--rm", "--pull", "missing", helloWorldImage),
		cmdStep("Check Docker Compose works", "docker", "compose", "version"),
	)
	return p, nil
}

func dockerRepo(h hostinfo.Info) (string, error) {
	if h.OSCodename == "" {
		return "", errors.New("couldn't read the operating system's release name from /etc/os-release")
	}
	switch h.OSID {
	case "ubuntu":
		return "https://download.docker.com/linux/ubuntu", nil
	case "debian": // includes Raspberry Pi OS 64-bit
		return "https://download.docker.com/linux/debian", nil
	}
	return "", fmt.Errorf("no Docker repository for %q", h.OSID)
}

func pkgInstalled(ctx context.Context, r Runner, pkg string) bool {
	out, err := output(ctx, r, "dpkg-query", "--show", "--showformat=${db:Status-Status}", pkg)
	return err == nil && out == "installed"
}

// versionLess compares the leading numeric parts of a version such as
// "27.3.1", "v2.29.7" or "24.0.7-0ubuntu4" against min.
func versionLess(v string, min [3]int) bool {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexFunc(v, func(r rune) bool { return r != '.' && (r < '0' || r > '9') }); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	for i := range 3 {
		n := 0
		if i < len(parts) {
			n, _ = strconv.Atoi(parts[i])
		}
		if n != min[i] {
			return n < min[i]
		}
	}
	return false
}

func fmtVersion(v [3]int) string { return fmt.Sprintf("%d.%d", v[0], v[1]) }
