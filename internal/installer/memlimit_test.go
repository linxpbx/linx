package installer

import (
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"linxpbx.com/linx/deploy/compose"
)

// Every Linx Go service has a GOMEMLIMIT below its container's mem_limit,
// so its garbage collector works harder before the container would be
// killed (docs/RESOURCES.md).
func TestGoServicesMemoryTargets(t *testing.T) {
	var f struct {
		Services map[string]struct {
			MemLimit    string            `yaml:"mem_limit"`
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(compose.File, &f); err != nil {
		t.Fatal(err)
	}
	mib := func(s string) int {
		s = strings.TrimSuffix(strings.TrimSuffix(s, "MiB"), "m")
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("size %q: %v", s, err)
		}
		return n
	}
	for _, name := range []string{"certd", "control-plane", "wireguard", "step-ca"} {
		s, ok := f.Services[name]
		if !ok {
			t.Fatalf("no service %s", name)
		}
		target, limit := s.Environment["GOMEMLIMIT"], s.MemLimit
		if target == "" || limit == "" {
			t.Errorf("%s: GOMEMLIMIT %q, mem_limit %q", name, target, limit)
			continue
		}
		if mib(target) >= mib(limit) {
			t.Errorf("%s: GOMEMLIMIT %s isn't below mem_limit %s", name, target, limit)
		}
	}
}
