package reach

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/turn"
	"linxpbx.com/linx/internal/weburl"
)

// The phone half of Check it: a link an admin opens on a phone with Wi-Fi
// off. It works once, for LinkTTL; opening it records the address Linx saw
// and hands the phone a relay credential good for RelayTTL, and the phone
// reports whether the relay worked. The admin's page waits for either
// (Wait) instead of asking again and again. Links live in memory only: a
// restart ends them, which is harmless.

const (
	LinkTTL  = 10 * time.Minute
	RelayTTL = 3 * time.Minute
	// maxLinks bounds memory: the oldest goes when there are more.
	maxLinks = 20
	// codeLen characters from codeAlphabet (no 0/O, 1/I/L look-alikes):
	// about 50 bits, and short enough to type into a phone if the picture
	// can't be scanned.
	codeLen      = 10
	codeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
)

// Link states.
const (
	Waiting = "waiting"
	Reached = "reached"
	Expired = "expired"
)

// What the address Linx saw says.
const (
	SeenOutside = "outside" // from outside: it works
	SeenHome    = "home"    // this network's own public address: Wi-Fi still on?
	SeenLocal   = "local"   // a home-network address: Wi-Fi still on
	SeenProxy   = "proxy"   // the front door's own: it doesn't tell Linx who's visiting
)

// ErrNoLink means the code is unknown, used or too old; the phone's page
// says only that.
var ErrNoLink = errors.New("this link can't be used")

// Link is one phone check as the admin's page sees it.
type Link struct {
	ID        uuid.UUID  `json:"id"`
	Code      string     `json:"-"`
	State     string     `json:"state"`
	ExpiresAt time.Time  `json:"expires_at"`
	Address   string     `json:"address,omitempty"`
	Seen      string     `json:"seen,omitempty"`
	Relay     *RelayTest `json:"relay,omitempty"`
	// Version counts changes, for Wait.
	Version int `json:"version"`

	claimedAt time.Time
	changed   chan struct{}
}

// RelayTest is the phone's own relay test.
type RelayTest struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Claimed is what the phone's page gets.
type Claimed struct {
	Domain  string
	Address netip.Addr
	TURN    turn.Credentials
}

// Links holds the open phone links.
type Links struct {
	Domain string
	// Address is Linx's web address (weburl.Origin: with the public port
	// unless it's 443); "" is https://<Domain>.
	Address string
	// Proxies reports the front door's own address (auth.ClientIPResolver.Trusted).
	Proxies func(netip.Addr) bool
	// PublicIP is this network's public address, if known.
	PublicIP func(ctx context.Context) (netip.Addr, error)
	TURN     *turn.Issuer
	Now      func() time.Time

	mu    sync.Mutex
	links []*Link
}

// New opens a link.
func (l *Links) New() Link {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep()
	if len(l.links) >= maxLinks {
		l.links = l.links[1:]
	}
	k := &Link{ID: uuid.New(), Code: newCode(), State: Waiting, ExpiresAt: l.Now().Add(LinkTTL).UTC().Truncate(time.Second),
		changed: make(chan struct{})}
	l.links = append(l.links, k)
	return k.snapshot()
}

// URL is where the phone opens a link.
func (l *Links) URL(k Link) string {
	a := l.Address
	if a == "" {
		a = weburl.Origin(l.Domain, 0)
	}
	return a + "/reach/" + k.Code
}

// Claim uses a code from addr, once.
func (l *Links) Claim(ctx context.Context, code string, addr netip.Addr) (Claimed, error) {
	// A wrong code ends here, before anything below asks the internet:
	// the phone's page is open to anyone.
	l.mu.Lock()
	l.sweep()
	k := l.find(code)
	open := k != nil && k.State == Waiting
	l.mu.Unlock()
	if !open {
		return Claimed{}, ErrNoLink
	}
	// Worked out without the lock: it may ask the internet.
	seen := SeenOutside
	switch {
	case l.Proxies != nil && l.Proxies(addr):
		seen = SeenProxy
	case addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast():
		seen = SeenLocal
	case l.PublicIP != nil:
		if p, err := l.PublicIP(ctx); err == nil && p == addr {
			seen = SeenHome
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep()
	if k = l.find(code); k == nil || k.State != Waiting {
		return Claimed{}, ErrNoLink
	}
	k.State, k.Address, k.Seen, k.claimedAt = Reached, addr.String(), seen, l.Now()
	k.bump()
	exp := l.Now().Add(RelayTTL).Truncate(time.Second)
	user := strconv.FormatInt(exp.Unix(), 10) + ":linx-reach-" + k.ID.String()
	creds := turn.Credentials{URLs: l.TURN.URLs, Username: user, Password: turn.Password(l.TURN.Secret, user), ExpiresAt: exp.UTC()}
	return Claimed{Domain: l.Domain, Address: addr, TURN: creds}, nil
}

// ReportRelay records the phone's relay test, once, soon after the claim.
func (l *Links) ReportRelay(code string, ok bool, detail string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := l.find(code)
	if k == nil || k.State != Reached || k.Relay != nil || l.Now().Sub(k.claimedAt) > RelayTTL {
		return ErrNoLink
	}
	if len(detail) > 200 {
		detail = detail[:200]
	}
	k.Relay = &RelayTest{OK: ok, Detail: detail}
	k.bump()
	return nil
}

// Wait returns the link once its version differs from seen, it expires,
// or ctx ends (then as it is).
func (l *Links) Wait(ctx context.Context, id uuid.UUID, seen int) (Link, bool) {
	for {
		l.mu.Lock()
		l.sweep()
		var k *Link
		for _, x := range l.links {
			if x.ID == id {
				k = x
			}
		}
		if k == nil {
			l.mu.Unlock()
			return Link{}, false
		}
		snap, ch := k.snapshot(), k.changed
		l.mu.Unlock()
		if snap.Version != seen || snap.State == Expired {
			return snap, true
		}
		wait := time.Until(snap.ExpiresAt)
		if wait <= 0 {
			wait = time.Millisecond
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return snap, true
		case <-ch:
			t.Stop()
		case <-t.C:
		}
	}
}

// sweep marks old links expired and forgets them an hour later.
func (l *Links) sweep() {
	now := l.Now()
	kept := l.links[:0]
	for _, k := range l.links {
		if k.State == Waiting && !now.Before(k.ExpiresAt) {
			k.State = Expired
			k.bump()
		}
		if now.Sub(k.ExpiresAt) < time.Hour {
			kept = append(kept, k)
		}
	}
	l.links = kept
}

func (l *Links) find(code string) *Link {
	for _, k := range l.links {
		if len(code) == codeLen && subtle.ConstantTimeCompare([]byte(k.Code), []byte(code)) == 1 {
			return k
		}
	}
	return nil
}

func (k *Link) bump() {
	k.Version++
	close(k.changed)
	k.changed = make(chan struct{})
}

func (k *Link) snapshot() Link {
	s := *k
	if k.Relay != nil {
		r := *k.Relay
		s.Relay = &r
	}
	s.changed = nil
	return s
}

// newCode is codeLen characters from codeAlphabet, each equally likely.
func newCode() string {
	b := make([]byte, 0, codeLen)
	var r [1]byte
	for len(b) < codeLen {
		_, _ = rand.Read(r[:])
		// 248 is the largest multiple of len(codeAlphabet) under 256.
		if int(r[0]) < 256/len(codeAlphabet)*len(codeAlphabet) {
			b = append(b, codeAlphabet[int(r[0])%len(codeAlphabet)])
		}
	}
	return string(b)
}
