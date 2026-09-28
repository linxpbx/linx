package installer

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestSwapPlan(t *testing.T) {
	noFile := func(string) ([]byte, error) { return nil, fs.ErrNotExist }
	fstab := func(s string) func(string) ([]byte, error) {
		return func(string) ([]byte, error) { return []byte(s), nil }
	}
	small := ubuntu
	small.MemBytes = 960 << 20

	p, err := SwapPlan(small, fstab("UUID=abc / ext4 defaults 0 1"))
	if err != nil || len(p) != 7 {
		t.Fatalf("small server: %d steps, %v", len(p), err)
	}
	if got := string(p[4].File.Data); got != "UUID=abc / ext4 defaults 0 1\n/swapfile none swap sw 0 0\n" {
		t.Errorf("fstab %q", got)
	}
	if !strings.Contains(p[0].Cmd.String(), "fallocate --length 1G /swapfile") {
		t.Errorf("first step %s", p[0].Cmd)
	}

	for name, tc := range map[string]struct {
		mem, swap uint64
		container string
		read      func(string) ([]byte, error)
	}{
		"enough memory":            {mem: 4 << 30, read: noFile},
		"swap already there":       {mem: 960 << 20, swap: 2 << 30, read: noFile},
		"proxmox lxc":              {mem: 960 << 20, container: "lxc", read: noFile},
		"swap file already set up": {mem: 960 << 20, read: fstab("/swapfile none swap sw 0 0\n")},
	} {
		h := ubuntu
		h.MemBytes, h.SwapBytes, h.Container = tc.mem, tc.swap, tc.container
		if p, err := SwapPlan(h, tc.read); err != nil || p != nil {
			t.Errorf("%s: %v %v", name, p, err)
		}
	}
	if _, err := SwapPlan(small, func(string) ([]byte, error) { return nil, errors.New("disk error") }); err == nil {
		t.Error("an unreadable fstab should stop setup")
	}
}
