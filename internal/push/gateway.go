package push

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/safehttp"
)

// Gateway is Linx's side of Apple's push service: one HTTP/2 connection,
// opened the first time there is something to send and kept while it is
// used. Nothing runs when no app phone has ever been set up.
type Gateway struct {
	Store    Store
	Sealer   *dbsecret.Sealer
	Policy   safehttp.Policy
	Resolver safehttp.Resolver
	Now      func() time.Time
	Log      *slog.Logger

	// HTTP replaces the guarded client the gateway makes for itself, and
	// Host the address it sends to (tests only).
	HTTP *http.Client
	Host string

	once   sync.Once
	http   *http.Client
	wakes  *auth.Limiters
	quiets *auth.Limiters

	mu sync.Mutex
	// key is the signing key in use, and keyVersion the settings version
	// it was made from, so a changed key is picked up at once.
	key        *Key
	keyVersion int
	// woke remembers when each phone was sent a wake push, so the time
	// from that to its arrival can be measured (docs/PHASE2.md §14).
	woke  map[string]time.Time
	stats Stats
}

func (g *Gateway) init() {
	g.once.Do(func() {
		if g.Now == nil {
			g.Now = time.Now
		}
		if g.Log == nil {
			g.Log = slog.Default()
		}
		g.wakes = auth.NewLimiters(WakesPerMinute, WakesPerMinute)
		g.quiets = auth.NewLimitersPer(QuietPerHour, time.Hour)
		g.woke = map[string]time.Time{}
		if g.HTTP != nil {
			g.http = g.HTTP
			return
		}
		g.http = safehttp.NewClient(g.Policy, safehttp.Options{
			Resolver: g.Resolver, Timeout: SendTimeout,
			// Apple asks senders to keep the connection: opening a new one
			// for every call would show up as ringing late.
			IdleTimeout: 10 * time.Minute,
		})
	})
}

func (g *Gateway) client() *http.Client {
	g.init()
	return g.http
}

func (g *Gateway) now() time.Time {
	g.init()
	return g.Now()
}

// Settings is what the admin screens show. The key itself never comes back.
func (g *Gateway) Settings(ctx context.Context, p *auth.Principal) (Settings, error) {
	if p == nil {
		return Settings{}, errors.New("no principal on the request")
	}
	s, _, err := g.Store.PushSettings(ctx)
	return s, err
}

// Save stores the Apple key and what to do with it: a system admin only,
// after a fresh "confirm it's you", audited without the key. The key can
// send a push to every phone in the company, which is why it is held to the
// same bar as the mail account's password.
func (g *Gateway) Save(ctx context.Context, in Input, ifMatch string) (Settings, error) {
	g.init()
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return Settings{}, errors.New("no principal on the request: authentication middleware is missing")
	}
	if p.Role != auth.RoleSystemAdmin {
		return Settings{}, errSystemAdmin
	}
	if err := auth.RequireConfirmed(ctx, g.now()); err != nil {
		return Settings{}, err
	}
	current, keyEnc, err := g.Store.PushSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	if ifMatch != "" && ifMatch != auth.ETag(current.Version) {
		return Settings{}, errChanged
	}
	out := Settings{
		Tenant:      current.Tenant,
		Enabled:     in.Enabled,
		TeamID:      strings.ToUpper(strings.TrimSpace(in.TeamID)),
		KeyID:       strings.ToUpper(strings.TrimSpace(in.KeyID)),
		BundleID:    strings.TrimSpace(in.BundleID),
		Environment: in.Environment,
		WaitMS:      in.WaitMS,
	}
	if out.Environment == "" {
		out.Environment = Production
	}
	if err := check(out); err != nil {
		return Settings{}, err
	}
	if len(in.Key) > 0 {
		if _, err := ParseKey(out.TeamID, out.KeyID, in.Key); err != nil {
			return Settings{}, invalid("push_key_invalid", upperFirst(err.Error())+".")
		}
		sealed, err := g.Sealer.Seal(rowID(current.Tenant), in.Key)
		if err != nil {
			return Settings{}, err
		}
		keyEnc = sealed
	}
	if out.Enabled && len(keyEnc) == 0 {
		return Settings{}, invalid("push_key_missing",
			"Add the .p8 key Apple gave you before turning this on.")
	}
	out.HasKey = len(keyEnc) > 0
	out.UpdatedAt = g.now()
	audit := auth.AuditEntry{
		TenantID: &current.Tenant, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: "push.settings", Result: auth.ResultOK,
		Detail: map[string]any{
			"enabled": out.Enabled, "team_id": out.TeamID, "key_id": out.KeyID,
			"bundle_id": out.BundleID, "environment": out.Environment, "wait_ms": out.WaitMS,
			"key_changed": len(in.Key) > 0,
		},
	}
	saved, err := g.Store.SavePushSettings(ctx, out, keyEnc, current.Version, audit)
	if err != nil {
		return Settings{}, err
	}
	// The next push signs with whatever was just saved.
	g.mu.Lock()
	g.key, g.keyVersion = nil, 0
	g.mu.Unlock()
	return saved, nil
}

func check(s Settings) error {
	switch {
	case s.Environment != Production && s.Environment != Sandbox:
		return invalid("push_environment_invalid", "Choose Apple's production or sandbox service.")
	case s.WaitMS < 0 || s.WaitMS > MaxWaitMS:
		return invalid("push_wait_invalid",
			fmt.Sprintf("How long a call waits for a woken phone has to be between 0 and %d milliseconds.", MaxWaitMS))
	}
	if !s.Enabled {
		return nil
	}
	switch {
	case len(s.TeamID) != 10:
		return invalid("push_team_invalid", "The Apple team id is the ten characters from your developer account.")
	case len(s.KeyID) != 10:
		return invalid("push_key_id_invalid", "The key id is the ten characters Apple shows beside the key.")
	case s.BundleID == "":
		return invalid("push_bundle_invalid", "The app's bundle id is missing.")
	}
	return nil
}

func rowID(tenant uuid.UUID) string { return "push_settings:" + tenant.String() }

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// signer returns the key to sign with, or ErrOff when push isn't set up.
func (g *Gateway) signer(ctx context.Context) (*Key, Settings, error) {
	g.init()
	s, keyEnc, err := g.Store.PushSettings(ctx)
	if err != nil {
		return nil, Settings{}, err
	}
	if !s.Enabled || len(keyEnc) == 0 {
		return nil, s, ErrOff
	}
	g.mu.Lock()
	if g.key != nil && g.keyVersion == s.Version {
		key := g.key
		g.mu.Unlock()
		return key, s, nil
	}
	g.mu.Unlock()

	plain, err := g.Sealer.Open(rowID(s.Tenant), keyEnc)
	if err != nil {
		return nil, s, fmt.Errorf("opening the Apple key: %w", err)
	}
	key, err := ParseKey(s.TeamID, s.KeyID, plain)
	if err != nil {
		return nil, s, err
	}
	g.mu.Lock()
	g.key, g.keyVersion = key, s.Version
	g.mu.Unlock()
	return key, s, nil
}

// Wake asks Apple to wake the app on these phones, because a call is
// ringing for them right now. It is called from the ARI app when the
// dialplan says so, and must not take long: the call is waiting.
func (g *Gateway) Wake(ctx context.Context, sipUsernames []string, c Call) {
	g.init()
	if len(sipUsernames) == 0 {
		return
	}
	key, settings, err := g.signer(ctx)
	if err != nil {
		if !errors.Is(err, ErrOff) {
			g.Log.Error("waking an app phone", "err", err)
		}
		return
	}
	devices, err := g.Store.WakeDevices(ctx, sipUsernames)
	if err != nil {
		g.Log.Error("looking up the phones to wake", "err", err)
		return
	}
	payload, err := wakePayload(c)
	if err != nil {
		g.Log.Error("writing the wake push", "err", err)
		return
	}
	// The same call, for a phone that may not use CallKit: an ordinary,
	// time-sensitive notification on the app's own topic (ADR-078).
	announcement, err := callPayload(c)
	if err != nil {
		g.Log.Error("writing the call notification", "err", err)
		return
	}
	now := g.now()
	for _, d := range devices {
		if !g.wakes.Allow("wake:"+d.DeviceID.String(), now) {
			g.count(func(s *Stats) { s.Limited++ })
			g.Log.Warn("too many wake pushes for one phone", "device", d.DeviceID)
			continue
		}
		g.mu.Lock()
		// Phones that never arrived are dropped as we go, so nothing
		// accumulates on a server whose phones are all switched off.
		for name, at := range g.woke {
			if now.Sub(at) > time.Minute {
				delete(g.woke, name)
			}
		}
		g.woke[d.SIPUsername] = now
		g.mu.Unlock()
		n := notice{
			Token: d.VoIPToken, Topic: settings.BundleID + ".voip", PushType: "voip",
			// Right now or not at all: a call is no use late.
			Priority: 10, Expiration: 0, Payload: payload,
		}
		kind := KindWake
		if d.VoIPToken == "" {
			// No CallKit where this phone is, so the call is announced
			// instead; it is worth nothing once the caller gives up, so it
			// expires with the ringing.
			kind = KindCall
			n = notice{
				Token: d.AlertToken, Topic: settings.BundleID, PushType: "alert",
				Priority: 10, Expiration: now.Add(CallAlertLife).Unix(), Payload: announcement,
			}
		}
		err := g.send(ctx, key, environmentOf(d, settings), n)
		g.after(ctx, d, kind, err)
	}
}

// Missed tells a person's phones about a call they missed, quietly: no
// VoIP push, because there is no call to ring any more (docs/PHASE2.md §14
// item 1).
func (g *Gateway) Missed(ctx context.Context, extension uuid.UUID, from string) {
	g.quiet(ctx, extension, KindMissed, "Missed call", calledBy(from))
}

// NewVoicemail tells a person's phones that a message is waiting.
func (g *Gateway) NewVoicemail(ctx context.Context, extension uuid.UUID, from string) {
	g.quiet(ctx, extension, KindVoicemail, "New voicemail", calledBy(from))
}

func calledBy(from string) string {
	if strings.TrimSpace(from) == "" {
		return "Number withheld"
	}
	return from
}

func (g *Gateway) quiet(ctx context.Context, extension uuid.UUID, kind, title, body string) {
	g.init()
	key, settings, err := g.signer(ctx)
	if err != nil {
		if !errors.Is(err, ErrOff) {
			g.Log.Error("sending a notification", "kind", kind, "err", err)
		}
		return
	}
	devices, err := g.Store.AlertDevices(ctx, extension)
	if err != nil {
		g.Log.Error("looking up the phones to notify", "kind", kind, "err", err)
		return
	}
	payload, err := quietPayload(kind, title, body, nil)
	if err != nil {
		g.Log.Error("writing a notification", "kind", kind, "err", err)
		return
	}
	now := g.now()
	for _, d := range devices {
		if !g.quiets.Allow("quiet:"+d.DeviceID.String(), now) {
			g.count(func(s *Stats) { s.Limited++ })
			continue
		}
		err := g.send(ctx, key, environmentOf(d, settings), notice{
			Token: d.AlertToken, Topic: settings.BundleID, PushType: "alert",
			Priority: 10, Expiration: now.Add(24 * time.Hour).Unix(), Payload: payload,
		})
		g.after(ctx, d, kind, err)
	}
}

// after records what became of one push and forgets a dead token.
func (g *Gateway) after(ctx context.Context, d Device, kind string, err error) {
	switch {
	case err == nil:
		g.count(func(s *Stats) { s.Sent[kind]++ })
	case isDead(err):
		g.count(func(s *Stats) { s.Dead++ })
		tokenKind := "alert"
		if kind == KindWake {
			tokenKind = "voip"
		}
		if err := g.Store.ForgetPushToken(ctx, d.DeviceID, tokenKind, g.now()); err != nil {
			g.Log.Error("forgetting a dead push token", "device", d.DeviceID, "err", err)
		}
		g.Log.Info("a phone's push token is no longer good; it will send a new one", "device", d.DeviceID)
	default:
		g.count(func(s *Stats) { s.Failed[kind]++ })
		g.Log.Warn("a push didn't go out", "kind", kind, "device", d.DeviceID, "err", err)
	}
}

func isDead(err error) bool {
	var dead *Dead
	return errors.As(err, &dead)
}

// environmentOf is where this phone's token lives: what the app said when
// it sent the token, and the server's setting when it said nothing.
func environmentOf(d Device, s Settings) string {
	if d.Environment == Sandbox || d.Environment == Production {
		return d.Environment
	}
	return s.Environment
}

// Registered is the phone arriving after a wake push: the ARI app calls it
// when a device signs in, and the time from the push to here is what
// "push → ringing" means (docs/PHASE2.md §14).
func (g *Gateway) Registered(sipUsername string) {
	g.init()
	g.mu.Lock()
	sent, ok := g.woke[sipUsername]
	if ok {
		delete(g.woke, sipUsername)
	}
	g.mu.Unlock()
	if !ok {
		return
	}
	took := g.now().Sub(sent)
	if took < 0 || took > time.Minute {
		return
	}
	g.count(func(s *Stats) {
		s.Woke++
		s.WokeSeconds += took.Seconds()
		if took.Seconds() > s.SlowestWake {
			s.SlowestWake = took.Seconds()
		}
	})
}

// SaveTokens is the app saying where to reach it. The phone proves who it
// is with its device token, as it does for its phone line.
func (g *Gateway) SaveTokens(ctx context.Context, device uuid.UUID, t Tokens) error {
	g.init()
	if err := checkToken(t.VoIP); err != nil {
		return err
	}
	if err := checkToken(t.Alert); err != nil {
		return err
	}
	if t.Environment != Production && t.Environment != Sandbox {
		return invalid("push_environment_invalid", "That isn't one of Apple's push services.")
	}
	// A phone that may not use CallKit has nowhere to put a VoIP push, and
	// a phone that can be woken properly is never announced instead.
	if t.CallAlerts && t.VoIP != "" {
		return invalid("push_tokens_conflict",
			"A phone that can't use CallKit has no VoIP token to send.")
	}
	t.VoIP, t.Alert = strings.ToLower(t.VoIP), strings.ToLower(t.Alert)
	return g.Store.SavePushTokens(ctx, device, t, g.now())
}

func checkToken(t string) error {
	if t == "" {
		return nil
	}
	if len(t) > 200 {
		return invalid("push_token_invalid", "That push token is too long.")
	}
	for _, r := range t {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return invalid("push_token_invalid", "That push token isn't the shape Apple's are.")
		}
	}
	return nil
}
