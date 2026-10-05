// Package moved is "Moved to a new place?" (docs/INSTALL.md §8,
// docs/ui/INSTALL_SCREENS.md §6). A restore already follows the new
// server for everything setup owns (domain, certificate, front door,
// firewall, public DNS: none of it is in a backup), but what is in the
// backup can still point at the old place: a phone line on the old home
// network, a provider that only accepts the old address, desk phones set
// up there, admins allowed only from the old network, passkeys made for
// the old domain. Every control-plane start writes where this server is
// (Place) to the database, so it travels in each backup; a start that
// finds a place another server wrote keeps both (Move), and the admin home
// shows the checklist until every item is done.
package moved

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Place is where this server is, as a start sees it.
type Place struct {
	// ServerID is setup's own id for this server (LINX_SERVER_ID); ""
	// before setup made one.
	ServerID string `json:"server_id"`
	Domain   string `json:"domain"`
	// LANNetworks are the phone networks from setup (none on a rented
	// server).
	LANNetworks []string `json:"lan_networks"`
	LANAddress  string   `json:"lan_address"`
	// PublicAddress is "" when the start couldn't tell.
	PublicAddress string `json:"public_address"`
	FrontDoor     string `json:"front_door"`
}

// sameLAN reports whether the phone networks are the same.
func (p Place) sameLAN(o Place) bool {
	a, b := slices.Clone(p.LANNetworks), slices.Clone(o.LANNetworks)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// Moved reports whether now is another place than before: another server
// (both have ids), or, without ids to go by, another domain, home network
// or front door. The public address alone never counts: at home it
// changes by itself.
func Moved(before, now Place) bool {
	if before.ServerID != "" && now.ServerID != "" {
		return before.ServerID != now.ServerID
	}
	return before.Domain != now.Domain || !before.sameLAN(now) || before.FrontDoor != now.FrontDoor
}

// Move is a start finding the database from another place.
type Move struct {
	ID          uuid.UUID
	DetectedAt  time.Time
	Before      Place
	After       Place
	Ticks       map[string]bool
	HiddenUntil *time.Time
	DoneAt      *time.Time
}

// Facts are what the checklist looks at in the restored database.
type Facts struct {
	// LANPeers are phone lines to a phone system on the home network.
	LANPeers []Named
	// SignsIn are phone systems that sign in to Linx (docs/SIMPLER.md
	// §1): set up with the old server's sip. name, on the old network.
	SignsIn []Named
	// ByAddress are providers that know Linx by its public address.
	ByAddress []Named
	// Tunnels are WireGuard tunnels.
	Tunnels []Named
	// DeskPhones counts desk phones (not browsers, not app phones).
	DeskPhones int
	// AppPhones counts set-up iPhones and iPads. They are counted apart
	// from desk phones because nothing can be done to them from this end:
	// an app was set up on the old address and can only be set up again
	// (docs/PHASE2.md §9).
	AppPhones int
	// AdminNetworks: "admins only from my home network" is on, and these
	// are the networks it lists.
	AdminRestricted bool
	AdminNetworks   []string
	// BackedUpSince reports whether a backup has worked since t.
	BackedUpSince func(t time.Time) bool
}

// Named is a phone line or tunnel.
type Named struct {
	ID   uuid.UUID
	Name string
}

// Store keeps places and moves.
type Store interface {
	// RecordPlace writes now as this install's place and, in the same
	// transaction, a Move when the place it replaces is another one
	// (Moved). It returns the new move, if any.
	RecordPlace(ctx context.Context, now Place, at time.Time, newID uuid.UUID) (*Move, error)
	// CurrentMove is the newest move not yet done.
	CurrentMove(ctx context.Context) (*Move, error)
	UpdateMove(ctx context.Context, m Move) error
	Facts(ctx context.Context, tenant uuid.UUID) (Facts, error)
}

// Service is the checklist.
type Service struct {
	Store Store
	Now   func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Record writes this start's place.
func (s *Service) Record(ctx context.Context, now Place) (*Move, error) {
	return s.Store.RecordPlace(ctx, now, s.now().UTC(), uuid.Must(uuid.NewV7()))
}

// Item ids. A phone line's or tunnel's has its id after a colon.
const (
	ItemOldServer     = "old_server"
	ItemLANPeer       = "lan_peer"
	ItemProvider      = "provider"
	ItemTunnel        = "tunnel"
	ItemDeskPhones    = "desk_phones"
	ItemAppPhones     = "app_phones"
	ItemSignsIn       = "signs_in"
	ItemAdminNetworks = "admin_networks"
	ItemPasskeys      = "passkeys"
	ItemBackups       = "backups"
)

// Where an item's page is.
const (
	LinkPhoneLines = "phone_lines"
	LinkExtensions = "extensions"
	LinkSettings   = "settings"
	LinkAccount    = "account"
	LinkBackups    = "backups"
)

// Item is one row of the checklist.
type Item struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Why   string `json:"why"`
	Link  string `json:"link,omitempty"`
	Done  bool   `json:"done"`
	// Auto: it ticks itself (it can't be ticked by hand).
	Auto bool `json:"auto,omitempty"`
}

// Checklist is the admin home's card.
type Checklist struct {
	DetectedAt  time.Time  `json:"detected_at"`
	Before      Place      `json:"before"`
	After       Place      `json:"after"`
	Items       []Item     `json:"items"`
	HiddenUntil *time.Time `json:"hidden_until,omitempty"`
}

// Left is how many items aren't done.
func (c Checklist) Left() int {
	n := 0
	for _, it := range c.Items {
		if !it.Done {
			n++
		}
	}
	return n
}

// Items is the checklist for m, from what the restored database holds.
// Only rows that apply are listed; "turn the old server off" always is,
// first.
func Items(m Move, f Facts) []Item {
	b, a := m.Before, m.After
	lanChanged := !b.sameLAN(a) || b.LANAddress != a.LANAddress
	publicChanged := b.PublicAddress != a.PublicAddress
	domainChanged := b.Domain != a.Domain
	newAddress := a.PublicAddress
	if newAddress == "" {
		newAddress = "this server's public address"
	}

	items := []Item{{ID: ItemOldServer, Title: "Turn the old server off",
		Why: "Both servers would point the same names at themselves, and phones and phone lines could reach the wrong one."}}
	if lanChanged {
		for _, t := range f.LANPeers {
			items = append(items, Item{ID: ItemLANPeer + ":" + t.ID.String(), Link: LinkPhoneLines,
				Title: fmt.Sprintf("Phone line %q is tied to the old network", t.Name),
				Why:   "It connects to a phone system on your home network: check its address, and that it knows this server's."})
		}
	}
	if publicChanged {
		for _, t := range f.ByAddress {
			items = append(items, Item{ID: ItemProvider + ":" + t.ID.String(), Link: LinkPhoneLines,
				Title: fmt.Sprintf("Tell the provider of %q your new address: %s", t.Name, newAddress),
				Why:   "It lets calls in only from the address it knows, and that was the old server's."})
		}
		for _, t := range f.Tunnels {
			items = append(items, Item{ID: ItemTunnel + ":" + t.ID.String(), Link: LinkPhoneLines,
				Title: fmt.Sprintf("WireGuard tunnel %q: check the other end", t.Name),
				Why:   "The other end may accept only the old server's address."})
		}
	}
	if f.DeskPhones > 0 && (lanChanged || domainChanged) {
		what := "were set up on the old network"
		if domainChanged && !lanChanged {
			what = "use the old sip." + b.Domain
		}
		phones := fmt.Sprintf("%d desk phones %s", f.DeskPhones, what)
		if f.DeskPhones == 1 {
			phones = "1 desk phone " + strings.Replace(what, "were", "was", 1)
		}
		why := "Give each one this server's address, " + sipName(a.Domain) + ", and check it connects."
		items = append(items, Item{ID: ItemDeskPhones, Link: LinkExtensions, Title: phones, Why: why})
	}
	// An iPhone or iPad can't be re-pointed the way a desk phone can: the
	// app was set up on the old address and keeps asking for it, so it has
	// to be set up again from a new code. Only a new domain does this — a
	// phone reaches Linx over the internet, so the home network changing
	// underneath it means nothing (docs/PHASE2.md §9).
	if f.AppPhones > 0 && domainChanged {
		phones := fmt.Sprintf("%d iPhones and iPads still look for %s", f.AppPhones, b.Domain)
		if f.AppPhones == 1 {
			phones = "1 iPhone or iPad still looks for " + b.Domain
		}
		items = append(items, Item{ID: ItemAppPhones, Link: LinkExtensions, Title: phones,
			Why: "The app can't be given a new address: set each one up again from a new QR code or emailed link " +
				"(People → the person → Phones). The phones themselves keep working until then, on nothing."})
	}
	if lanChanged || domainChanged {
		for _, t := range f.SignsIn {
			items = append(items, Item{ID: ItemSignsIn + ":" + t.ID.String(), Link: LinkPhoneLines,
				Title: fmt.Sprintf("Phone system %q signs in to the old server", t.Name),
				Why:   "On the phone system, set its SIP trunk's server to " + sipName(a.Domain) + ", and check it signs in from this network."})
		}
	}
	if f.AdminRestricted && lanChanged && oldOnly(f.AdminNetworks, b.LANNetworks, a.LANNetworks) {
		items = append(items, Item{ID: ItemAdminNetworks, Link: LinkSettings,
			Title: "Admins only from " + strings.Join(f.AdminNetworks, ", ") + " (the old network)",
			Why:   "Admins can sign in from this server's own network, but not from other places the old list allowed."})
	}
	if domainChanged {
		items = append(items, Item{ID: ItemPasskeys, Link: LinkAccount, Title: "New domain: add new passkeys",
			Why: "Passkeys belong to " + b.Domain + ", so they don't work here. Sign in with your password and authenticator app, then add new ones."})
	}
	backedUp := f.BackedUpSince != nil && f.BackedUpSince(m.DetectedAt)
	items = append(items, Item{ID: ItemBackups, Link: LinkBackups, Auto: true, Done: backedUp,
		Title: "Add your backup places again",
		Why:   "Where backups go, and their keys, stay on the old server. This ticks itself after the first backup here."})
	for i := range items {
		if !items[i].Auto && m.Ticks[items[i].ID] {
			items[i].Done = true
		}
	}
	return items
}

func sipName(domain string) string {
	if domain == "" {
		return "its sip. name"
	}
	return "sip." + domain
}

// oldOnly reports whether the admin networks list names a network of the
// old place that the new place doesn't have.
func oldOnly(admin, before, after []string) bool {
	for _, n := range admin {
		p, err := netip.ParsePrefix(n)
		if err != nil {
			continue
		}
		inBefore := slices.ContainsFunc(before, func(b string) bool {
			bp, err := netip.ParsePrefix(b)
			return err == nil && bp.Overlaps(p)
		})
		if inBefore && !slices.Contains(after, n) {
			return true
		}
	}
	return false
}

// Current is the checklist, or nil when there's none to show (no move, or
// every item done: the move is then marked done).
func (s *Service) Current(ctx context.Context, tenant uuid.UUID) (*Checklist, error) {
	m, err := s.Store.CurrentMove(ctx)
	if err != nil || m == nil {
		return nil, err
	}
	f, err := s.Store.Facts(ctx, tenant)
	if err != nil {
		return nil, err
	}
	c := &Checklist{DetectedAt: m.DetectedAt, Before: m.Before, After: m.After, Items: Items(*m, f), HiddenUntil: m.HiddenUntil}
	if c.Left() == 0 {
		now := s.now().UTC()
		m.DoneAt = &now
		return nil, s.Store.UpdateMove(ctx, *m)
	}
	if c.HiddenUntil != nil && !s.now().Before(*c.HiddenUntil) {
		c.HiddenUntil = nil
	}
	return c, nil
}

// HideFor is how long "Hide for now" folds the card to one line.
const HideFor = 7 * 24 * time.Hour

// Change is the admin ticking items, or hiding the card for now.
type Change struct {
	Ticks map[string]bool
	// Hide: true folds it for HideFor, false opens it again; nil leaves it.
	Hide *bool
}

// ErrNoChecklist: there's nothing to change.
var ErrNoChecklist = fmt.Errorf("there's no moved-server checklist")

// ErrUnknownItem: a tick for an item that isn't on the list (or ticks
// itself).
type ErrUnknownItem struct{ ID string }

func (e ErrUnknownItem) Error() string {
	return fmt.Sprintf("%q isn't an item that can be ticked", e.ID)
}

// Update applies c and returns the checklist after it (nil when it's done).
func (s *Service) Update(ctx context.Context, tenant uuid.UUID, c Change) (*Checklist, error) {
	m, err := s.Store.CurrentMove(ctx)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, ErrNoChecklist
	}
	f, err := s.Store.Facts(ctx, tenant)
	if err != nil {
		return nil, err
	}
	items := Items(*m, f)
	for id := range c.Ticks {
		if !slices.ContainsFunc(items, func(it Item) bool { return it.ID == id && !it.Auto }) {
			return nil, ErrUnknownItem{ID: id}
		}
	}
	if m.Ticks == nil {
		m.Ticks = map[string]bool{}
	}
	for id, v := range c.Ticks {
		if v {
			m.Ticks[id] = true
		} else {
			delete(m.Ticks, id)
		}
	}
	if c.Hide != nil {
		if *c.Hide {
			until := s.now().Add(HideFor).UTC()
			m.HiddenUntil = &until
		} else {
			m.HiddenUntil = nil
		}
	}
	if err := s.Store.UpdateMove(ctx, *m); err != nil {
		return nil, err
	}
	return s.Current(ctx, tenant)
}

// PasskeysMoved reports whether the newest move changed the domain, so
// the sign-in page says passkeys from the old address don't work here.
func (s *Service) PasskeysMoved(ctx context.Context) bool {
	m, err := s.Store.CurrentMove(ctx)
	return err == nil && m != nil && m.Before.Domain != "" && m.Before.Domain != m.After.Domain
}
