// Package hostinfo detects the server's operating system and hardware. The
// installer uses it for its prerequisites step; `linx doctor --capacity` and
// the admin capacity page will reuse it.
package hostinfo

import (
	"bufio"
	"bytes"
	"io/fs"
	"runtime"
	"strconv"
	"strings"
)

// Info describes the host.
type Info struct {
	GOOS        string // "linux", "darwin", ...
	OSID        string // os-release ID, e.g. "ubuntu", "debian", "raspbian"
	OSVersionID string // os-release VERSION_ID, e.g. "24.04", "12"
	OSCodename  string // e.g. "noble", "bookworm"
	OSName      string // os-release PRETTY_NAME, for display
	Arch        string // Go architecture name: "amd64", "arm64"
	CPUs        int
	MemBytes    uint64
	DiskFree    uint64 // free bytes where Docker keeps its data (/var/lib)
	DiskTotal   uint64
	Model       string // board model from the device tree, e.g. "Raspberry Pi 5 Model B Rev 1.0"
	RootDevice  string // block device holding "/", e.g. "/dev/mmcblk0p2"
}

// IsRaspberryPi reports whether the board is a Raspberry Pi.
func (i Info) IsRaspberryPi() bool { return strings.Contains(i.Model, "Raspberry Pi") }

// RootOnSDCard reports whether "/" lives on an SD card (or eMMC), which wears
// out quickly under database and log writes.
func (i Info) RootOnSDCard() bool { return strings.HasPrefix(i.RootDevice, "/dev/mmcblk") }

// Detect reads host details from the real system.
func Detect() Info { return detect(rootFS, diskUsage) }

// detect reads host details from root (the filesystem at "/") so tests can
// supply fixtures. Missing files leave fields empty rather than failing.
func detect(root fs.FS, usage func(path string) (free, total uint64, err error)) Info {
	info := Info{GOOS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU()}

	if b, err := fs.ReadFile(root, "etc/os-release"); err == nil {
		osr := parseOSRelease(b)
		info.OSID = osr["ID"]
		info.OSVersionID = osr["VERSION_ID"]
		info.OSName = osr["PRETTY_NAME"]
		info.OSCodename = osr["VERSION_CODENAME"]
		if c := osr["UBUNTU_CODENAME"]; c != "" {
			info.OSCodename = c
		}
	}
	if b, err := fs.ReadFile(root, "proc/meminfo"); err == nil {
		info.MemBytes = parseMemTotal(b)
	}
	if b, err := fs.ReadFile(root, "proc/device-tree/model"); err == nil {
		info.Model = strings.TrimSpace(strings.TrimRight(string(b), "\x00"))
	}
	if b, err := fs.ReadFile(root, "proc/self/mountinfo"); err == nil {
		info.RootDevice = parseRootDevice(b)
	}
	if free, total, err := usage("/var/lib"); err == nil {
		info.DiskFree, info.DiskTotal = free, total
	}
	return info
}

func parseOSRelease(b []byte) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		} else {
			v = strings.Trim(v, `'"`)
		}
		m[k] = v
	}
	return m
}

func parseMemTotal(b []byte) uint64 {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "MemTotal:" {
			kb, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				return 0
			}
			return kb * 1024
		}
	}
	return 0
}

// parseRootDevice finds the mount source for "/" in /proc/self/mountinfo.
// Line format: id parent maj:min root mountpoint opts [optional...] - fstype source superopts
func parseRootDevice(b []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		pre, post, ok := strings.Cut(sc.Text(), " - ")
		if !ok {
			continue
		}
		f := strings.Fields(pre)
		if len(f) < 5 || f[4] != "/" {
			continue
		}
		if p := strings.Fields(post); len(p) >= 2 {
			return p[1]
		}
	}
	return ""
}
