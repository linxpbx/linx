package firewallsync

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/installer"
)

// fakeHost stands in for both docker exec (the control plane's answer) and
// nft (the sets' current elements): a bare-bones nftables, enough for Sync
// to read and change.
type fakeHost struct {
	dockerOut string
	dockerErr error
	sets      map[string][]string // set -> current elements
	nftCalls  []string
}

func (f *fakeHost) run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "docker" {
		return []byte(f.dockerOut), f.dockerErr
	}
	f.nftCalls = append(f.nftCalls, strings.Join(args, " "))
	switch args[0] {
	case "-j": // nft -j list set inet <table> <set>
		set := args[len(args)-1]
		elems := make([]string, len(f.sets[set]))
		for i, a := range f.sets[set] {
			elems[i] = `"` + a + `"`
		}
		return []byte(`{"nftables":[{"set":{"elem":[` + strings.Join(elems, ",") + `]}}]}`), nil
	case "add": // nft add element inet <table> <set> { ... }
		set := args[len(args)-2]
		f.sets[set] = append(f.sets[set], stripElements(args[len(args)-1])...)
	case "delete": // nft delete element inet <table> <set> { ... }
		set := args[len(args)-2]
		remove := stripElements(args[len(args)-1])
		var kept []string
		for _, a := range f.sets[set] {
			if !contains(remove, a) {
				kept = append(kept, a)
			}
		}
		f.sets[set] = kept
	}
	return nil, nil
}

func stripElements(s string) []string {
	s = strings.TrimPrefix(s, "{ ")
	s = strings.TrimSuffix(s, " }")
	return strings.Split(s, ", ")
}

func contains(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}

func TestOnceKeepWithdrawsNothing(t *testing.T) {
	// A trunk's name didn't resolve: add what's new, withdraw nothing.
	f := &fakeHost{
		dockerOut: "keep\ntrunk_addresses 203.0.113.1\n",
		sets:      map[string][]string{installer.TrunkAddressSet: {"203.0.113.9"}, installer.TrunkPlainAddressSet: {"203.0.113.9"}},
	}
	env := Env{Exec: f.run, Log: slog.New(slog.NewTextHandler(logDiscard{}, nil))}
	if err := env.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.sets[installer.TrunkAddressSet]; !sameSet(got, []string{"203.0.113.1", "203.0.113.9"}) {
		t.Errorf("trunk_addresses = %v", got)
	}
	if got := f.sets[installer.TrunkPlainAddressSet]; !sameSet(got, []string{"203.0.113.9"}) {
		t.Errorf("trunk_plain_addresses = %v", got)
	}
}

func TestOnceAddsAndRemoves(t *testing.T) {
	f := &fakeHost{
		dockerOut: "trunk_addresses 203.0.113.1\ntrunk_addresses 203.0.113.2\ntrunk_plain_addresses 203.0.113.2\n",
		sets:      map[string][]string{installer.TrunkAddressSet: {"203.0.113.9"}}, // stale: no longer a trunk
	}
	env := Env{Exec: f.run, Log: slog.New(slog.NewTextHandler(logDiscard{}, nil))}
	if err := env.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.sets[installer.TrunkAddressSet]; !sameSet(got, []string{"203.0.113.1", "203.0.113.2"}) {
		t.Errorf("trunk_addresses = %v", got)
	}
	if got := f.sets[installer.TrunkPlainAddressSet]; !sameSet(got, []string{"203.0.113.2"}) {
		t.Errorf("trunk_plain_addresses = %v", got)
	}
}

// TestOnceIgnoresUnknownSets proves the hard security property: whatever
// the control plane prints, only the two fixed sets are ever touched.
func TestOnceIgnoresUnknownSets(t *testing.T) {
	f := &fakeHost{
		dockerOut: "trunk_addresses 203.0.113.1\nfront_door 203.0.113.66\nphone_networks 10.0.0.0/8\n",
		sets:      map[string][]string{},
	}
	env := Env{Exec: f.run, Log: discardLogger()}
	if err := env.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.nftCalls {
		if strings.Contains(call, "front_door") || strings.Contains(call, "phone_networks") {
			t.Errorf("touched a set it doesn't own: %q", call)
		}
	}
	if got := f.sets[installer.TrunkAddressSet]; !sameSet(got, []string{"203.0.113.1"}) {
		t.Errorf("trunk_addresses = %v", got)
	}
}

func TestOnceLeavesSetsAloneWhenTheControlPlaneIsUnreachable(t *testing.T) {
	f := &fakeHost{dockerErr: errors.New("no such container")}
	env := Env{Exec: f.run, Log: discardLogger()}
	if err := env.Once(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if len(f.nftCalls) != 0 {
		t.Errorf("touched nft despite the control plane being unreachable: %v", f.nftCalls)
	}
}

func TestOnceOneSetFailingDoesntStopTheOther(t *testing.T) {
	calls := 0
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "docker" {
			return []byte("trunk_addresses 203.0.113.1\ntrunk_plain_addresses 203.0.113.1\n"), nil
		}
		calls++
		set := args[len(args)-1]
		if set == installer.TrunkAddressSet {
			return nil, errors.New("nft: permission denied")
		}
		return []byte(`{"nftables":[{"set":{}}]}`), nil
	}
	env := Env{Exec: run, Log: discardLogger()}
	err := env.Once(context.Background())
	if err == nil || !strings.Contains(err.Error(), "trunk_addresses") {
		t.Fatalf("err = %v", err)
	}
	if calls == 0 {
		t.Error("the other set was never attempted")
	}
}

func TestParseIgnoresGarbage(t *testing.T) {
	want, _ := parse([]byte("trunk_addresses 203.0.113.1\nnot a valid line\ntrunk_addresses not-an-address\n\n"))
	got := want[installer.TrunkAddressSet]
	if len(got) != 1 || got[0] != netip.MustParseAddr("203.0.113.1") {
		t.Errorf("got %v", got)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			return false
		}
	}
	return true
}

type logDiscard struct{}

func (logDiscard) Write(p []byte) (int, error) { return len(p), nil }

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(logDiscard{}, nil)) }
