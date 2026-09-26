package nftset

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// fakeSet is a minimal nft stand-in: only what List/Sync call.
type fakeSet struct {
	addrs   []netip.Addr
	calls   []string
	listErr error
}

func (f *fakeSet) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	switch {
	case len(args) > 0 && args[0] == "-j":
		if f.listErr != nil {
			return nil, f.listErr
		}
		elems := make([]string, len(f.addrs))
		for i, a := range f.addrs {
			elems[i] = `"` + a.String() + `"`
		}
		return []byte(`{"nftables":[{"set":{"elem":[` + strings.Join(elems, ",") + `]}}]}`), nil
	case len(args) > 0 && args[0] == "add":
		f.addrs = append(f.addrs, parseElementList(args[len(args)-1])...)
		return nil, nil
	case len(args) > 0 && args[0] == "delete":
		remove := parseElementList(args[len(args)-1])
		var kept []netip.Addr
		for _, a := range f.addrs {
			drop := false
			for _, r := range remove {
				if a == r {
					drop = true
				}
			}
			if !drop {
				kept = append(kept, a)
			}
		}
		f.addrs = kept
		return nil, nil
	}
	return nil, nil
}

func parseElementList(s string) []netip.Addr {
	s = strings.TrimPrefix(s, "{ ")
	s = strings.TrimSuffix(s, " }")
	var out []netip.Addr
	for _, p := range strings.Split(s, ", ") {
		out = append(out, netip.MustParseAddr(p))
	}
	return out
}

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestList(t *testing.T) {
	f := &fakeSet{addrs: []netip.Addr{addr("203.0.113.1"), addr("203.0.113.2")}}
	got, err := List(context.Background(), f.run, "linx", "trunk_addresses")
	if err != nil || len(got) != 2 || got[0] != addr("203.0.113.1") {
		t.Fatalf("got %v, %v", got, err)
	}

	f = &fakeSet{listErr: errors.New("boom")}
	if _, err := List(context.Background(), f.run, "linx", "trunk_addresses"); err == nil {
		t.Error("expected an error")
	}
}

func TestParseEmptySet(t *testing.T) {
	got, err := parse([]byte(`{"nftables":[{"set":{}}]}`))
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestParseRejectsANetwork(t *testing.T) {
	// This package's sets never hold a network (docs/TRUNKS.md §12): a
	// prefix element (nft's non-string JSON shape) must be an error, not
	// silently accepted.
	_, err := parse([]byte(`{"nftables":[{"set":{"elem":[{"prefix":{"addr":"203.0.113.0","len":24}}]}}]}`))
	if err == nil {
		t.Error("a network element was accepted")
	}
}

func TestSync(t *testing.T) {
	f := &fakeSet{addrs: []netip.Addr{addr("203.0.113.1"), addr("203.0.113.2")}}
	added, removed, err := Sync(context.Background(), f.run, "linx", "trunk_addresses",
		[]netip.Addr{addr("203.0.113.2"), addr("203.0.113.3")})
	if err != nil {
		t.Fatal(err)
	}
	if !equal(added, []netip.Addr{addr("203.0.113.3")}) {
		t.Errorf("added = %v", added)
	}
	if !equal(removed, []netip.Addr{addr("203.0.113.1")}) {
		t.Errorf("removed = %v", removed)
	}
	if !equal(f.addrs, []netip.Addr{addr("203.0.113.2"), addr("203.0.113.3")}) {
		t.Errorf("resulting set = %v", f.addrs)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "-f") || strings.Contains(c, "flush") {
			t.Errorf("Sync touched more than elements: %q", c)
		}
	}

	// Already in sync: no add/delete calls.
	f2 := &fakeSet{addrs: []netip.Addr{addr("203.0.113.1")}}
	added, removed, err = Sync(context.Background(), f2.run, "linx", "trunk_addresses", []netip.Addr{addr("203.0.113.1")})
	if err != nil || len(added) != 0 || len(removed) != 0 {
		t.Fatalf("added=%v removed=%v err=%v", added, removed, err)
	}
	for _, c := range f2.calls {
		if strings.HasPrefix(c, "nft add") || strings.HasPrefix(c, "nft delete") {
			t.Errorf("unnecessary call: %q", c)
		}
	}
}

func equal(a, b []netip.Addr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
