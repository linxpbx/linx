package installer

import (
	"fmt"

	"linxpbx.com/linx/internal/hostinfo"
)

// Resource profiles (ARCHITECTURE §9).
const (
	ProfileLite        = "lite"
	ProfileStandard    = "standard"
	ProfilePerformance = "performance"
)

// Profiles lists the valid resource profiles, smallest first.
var Profiles = []string{ProfileLite, ProfileStandard, ProfilePerformance}

// ProfileDescription is the plain-language summary shown in the wizard.
var ProfileDescription = map[string]string{
	ProfileLite:        "audio first, small meetings, AI off, lighter monitoring",
	ProfileStandard:    "all features, medium-sized meetings",
	ProfilePerformance: "all features, large meetings, can run AI on this server",
}

// SuggestProfile picks a resource profile from the hardware and explains why.
// The capacity benchmark (Phase 5) will refine this; the owner can always
// override it.
func SuggestProfile(h hostinfo.Info) (profile, reason string) {
	mem := float64(h.MemBytes) / gib
	switch {
	case h.IsRaspberryPi():
		return ProfileLite, "this is a Raspberry Pi"
	// "8 GB" machines report slightly less, so the cut-off sits above 8 GiB.
	case h.CPUs <= 4 || mem < 9:
		return ProfileLite, fmt.Sprintf("%d processor cores and %.0f GB memory", h.CPUs, mem)
	case h.CPUs >= 8 && mem >= 30:
		return ProfilePerformance, fmt.Sprintf("%d processor cores and %.0f GB memory", h.CPUs, mem)
	default:
		return ProfileStandard, fmt.Sprintf("%d processor cores and %.0f GB memory", h.CPUs, mem)
	}
}
