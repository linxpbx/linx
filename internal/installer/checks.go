package installer

import (
	"fmt"
	"strconv"
	"strings"

	"linxpbx.com/linx/internal/hostinfo"
)

// Level is how serious a finding is.
type Level int

const (
	OK Level = iota
	Warn
	Fail
)

func (l Level) String() string {
	switch l {
	case OK:
		return "ok"
	case Warn:
		return "warning"
	default:
		return "problem"
	}
}

// Finding is one result of the host checks, written for a non-technical reader.
type Finding struct {
	Level   Level
	Message string
}

const gib = 1 << 30

// Hardware limits. A "4 GB" board reports a little under 4 GiB of MemTotal.
const (
	minMemBytes  = 3500 << 20
	minDiskFree  = 10 * gib
	goodDiskFree = 32 * gib
)

// CheckHost returns findings for the prerequisites step. Any Fail means setup
// cannot continue.
func CheckHost(h hostinfo.Info) []Finding {
	var fs []Finding
	add := func(l Level, format string, a ...any) { fs = append(fs, Finding{l, fmt.Sprintf(format, a...)}) }

	if h.GOOS != "linux" {
		add(Fail, "Linx runs on a Linux server. This computer runs %s.", h.GOOS)
		return fs
	}

	name := h.OSName
	if name == "" {
		name = h.OSID + " " + h.OSVersionID
	}
	if supportedOS(h.OSID, h.OSVersionID) {
		add(OK, "Operating system: %s", name)
	} else {
		add(Fail, "Operating system %s isn't supported. Use Ubuntu 24.04 or newer, Debian 12 or newer, or Raspberry Pi OS 64-bit.", name)
	}

	switch h.Arch {
	case "amd64", "arm64":
		add(OK, "Processor type: %s", h.Arch)
	default:
		add(Fail, "Processor type %s isn't supported. Linx needs a 64-bit Intel/AMD (amd64) or ARM (arm64) system.", h.Arch)
	}

	switch {
	case h.MemBytes == 0:
		add(Warn, "Couldn't read how much memory this server has.")
	case h.MemBytes < minMemBytes:
		add(Fail, "Memory: %s. Linx needs at least 4 GB.", humanBytes(h.MemBytes))
	default:
		add(OK, "Memory: %s", humanBytes(h.MemBytes))
	}

	switch {
	case h.DiskTotal == 0:
		add(Warn, "Couldn't read how much free disk space this server has.")
	case h.DiskFree < minDiskFree:
		add(Fail, "Free disk space: %s. Linx needs at least 10 GB free.", humanBytes(h.DiskFree))
	case h.DiskFree < goodDiskFree:
		add(Warn, "Free disk space: %s. That's enough to start, but call recordings and voicemail will fill it. 32 GB or more is recommended.", humanBytes(h.DiskFree))
	default:
		add(OK, "Free disk space: %s", humanBytes(h.DiskFree))
	}

	if h.RootOnSDCard() {
		add(Warn, "The system is running from an SD card. SD cards wear out quickly under constant writes. A USB or NVMe SSD is strongly recommended.")
	}
	return fs
}

// supportedOS: Ubuntu 24.04+, Debian 12+ (which includes Raspberry Pi OS
// 64-bit). 32-bit Raspberry Pi OS reports ID=raspbian and is rejected.
func supportedOS(id, version string) bool {
	major, _, _ := strings.Cut(version, ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return false
	}
	switch id {
	case "ubuntu":
		return n >= 24
	case "debian":
		return n >= 12
	}
	return false
}

// HasFailure reports whether any finding blocks setup.
func HasFailure(fs []Finding) bool {
	for _, f := range fs {
		if f.Level == Fail {
			return true
		}
	}
	return false
}

func humanBytes(b uint64) string {
	return fmt.Sprintf("%.1f GB", float64(b)/gib)
}
