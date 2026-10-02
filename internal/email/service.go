package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/safehttp"
)

// TestsPerMinute bounds one person's test emails.
const TestsPerMinute = 5

// Service is the Email card's setting and test, and the queue every other
// feature sends through.
type Service struct {
	Store  Store
	Sealer *dbsecret.Sealer
	Sender *Sender
	// Policy and Resolver refuse a server on a private network that isn't
	// on the outbound allowlist, with a plain reason, on save (sending
	// checks again as it connects).
	Policy   safehttp.Policy
	Resolver safehttp.Resolver
	Now      func() time.Time
	Log      *slog.Logger
	// Broken is called when sending fails in a way that needs an admin
	// (the "email isn't sending" alert, sent through the other channels),
	// Working when an email goes out again. Either may be nil.
	Broken  func(ctx context.Context, tenant uuid.UUID, detail string)
	Working func(ctx context.Context, tenant uuid.UUID)
	// Voicemail returns a voicemail message as the file to attach (found
	// false: it was deleted since it was queued).
	Voicemail func(ctx context.Context, tenant, id uuid.UUID) (f File, found bool, err error)

	once  sync.Once
	wake  chan struct{}
	tests *auth.Limiters
}

var errNoPrincipal = errors.New("no principal on the request: authentication middleware is missing")

func invalid(code, detail string) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusUnprocessableEntity, Code: code, Detail: detail}
}

var (
	errChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
		Detail: "Someone changed this setting since you loaded it. Reload and try again."}
	errSystemAdmin = &apihttp.Error{Status: http.StatusForbidden, Code: "system_admin_only",
		Detail: "Only a system admin can change how Linx sends email: it holds a mail account's password."}
	// ErrOff is a send asked for while email isn't set up.
	ErrOff = &apihttp.Error{Status: http.StatusConflict, Code: "email_off",
		Detail: "Set up email first (System → Settings)."}
)

func (s *Service) init() {
	s.once.Do(func() {
		s.wake = make(chan struct{}, 1)
		s.tests = auth.NewLimiters(TestsPerMinute, TestsPerMinute)
		if s.Log == nil {
			s.Log = slog.New(slog.DiscardHandler)
		}
	})
}

// Defaults is the setting before anyone has saved one.
func Defaults(tenant uuid.UUID) Config {
	return Config{TenantID: tenant, Preset: "google", Port: 465, Security: SecurityTLS, HourlyLimit: DefaultHourlyLimit}
}

func (s *Service) load(ctx context.Context, tenant uuid.UUID) (Config, error) {
	c, err := s.Store.EmailSettings(ctx, tenant)
	if errors.Is(err, auth.ErrNotFound) {
		return Defaults(tenant), nil
	}
	return c, err
}

// Get returns the setting and the queue.
func (s *Service) Get(ctx context.Context) (Config, Status, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return Config{}, Status{}, errNoPrincipal
	}
	c, err := s.load(ctx, p.TenantID)
	if err != nil {
		return Config{}, Status{}, err
	}
	st, err := s.Store.EmailStatus(ctx, p.TenantID, s.Now())
	return c, st, err
}

// On reports whether tenant's email is set up and on, for the features
// that offer to send (greyed otherwise).
func (s *Service) On(ctx context.Context, tenant uuid.UUID) (bool, error) {
	c, err := s.load(ctx, tenant)
	return err == nil && c.Enabled, err
}

// Patch is a JSON Merge Patch of the setting; nil fields stay as they are.
type Patch struct {
	Enabled                                                 *bool
	Preset, Host, Security, Username, FromAddress, FromName *string
	Port, HourlyLimit                                       *int
	Password                                                *string
	// Arrived records the admin's "Yes, it arrived" after a test.
	Arrived *bool
}

// systemAdmin is the only caller who may change the setting.
func systemAdmin(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return p, errNoPrincipal
	}
	if p.Role != auth.RoleSystemAdmin {
		return p, errSystemAdmin
	}
	return p, nil
}

// Update applies patch if ifMatch (an ETag, or "") matches: a system admin
// only, after a fresh "confirm it's you", audited without the password.
// A new server or account needs the password typed again (a password only
// goes where it was given for).
func (s *Service) Update(ctx context.Context, patch Patch, ifMatch string) (Config, error) {
	p, err := systemAdmin(ctx)
	if err != nil {
		return Config{}, err
	}
	if err := auth.RequireConfirmed(ctx, s.Now()); err != nil {
		return Config{}, err
	}
	cur, err := s.load(ctx, p.TenantID)
	if err != nil {
		return Config{}, err
	}
	if ifMatch != "" && ifMatch != auth.ETag(cur.Version) {
		return Config{}, errChanged
	}
	next := cur
	changes := map[string]any{}
	str := func(v *string) string { return strings.TrimSpace(*v) }

	if patch.Preset != nil {
		pr, ok := PresetByID(*patch.Preset)
		if !ok {
			return Config{}, invalid("preset_invalid", "Choose who sends Linx's email.")
		}
		next.Preset = pr.ID
		if pr.Host != "" {
			next.Host = pr.Host
		}
		if pr.Port != 0 {
			next.Port, next.Security = pr.Port, pr.Security
		}
		changes["preset"] = next.Preset
	}
	if patch.Host != nil {
		next.Host = strings.ToLower(str(patch.Host))
	}
	if patch.Port != nil {
		next.Port = *patch.Port
	}
	if patch.Security != nil {
		next.Security = *patch.Security
	}
	if patch.Username != nil {
		next.Username = str(patch.Username)
	}
	if patch.FromAddress != nil {
		next.FromAddress = strings.ToLower(str(patch.FromAddress))
	}
	if patch.FromName != nil {
		next.FromName = str(patch.FromName)
	}
	if pr, ok := PresetByID(next.Preset); ok && pr.UsernameIsAddress {
		next.Username = next.FromAddress
	}
	server := next.Host != cur.Host || next.Port != cur.Port || next.Security != cur.Security
	account := next.Username != cur.Username || next.FromAddress != cur.FromAddress
	if server {
		changes["server"] = fmt.Sprintf("%s:%d (%s)", next.Host, next.Port, next.Security)
	}
	if account {
		changes["from_address"] = next.FromAddress
	}
	if next.FromName != cur.FromName {
		changes["from_name"] = next.FromName
	}

	switch {
	case patch.Password != nil && *patch.Password == "":
		next.PasswordEnc = nil
		changes["password"] = "removed"
	case patch.Password != nil:
		pw := *patch.Password
		if pr, _ := PresetByID(next.Preset); pr.ID == "google" {
			// Google shows its app passwords in groups of four.
			pw = strings.ReplaceAll(pw, " ", "")
		}
		if pw == "" || len(pw) > 500 || hasControl(pw) {
			return Config{}, invalid("password_invalid", "That doesn't look like a password: paste it again.")
		}
		enc, err := s.Sealer.Seal(SealID(p.TenantID), []byte(pw))
		if err != nil {
			return Config{}, err
		}
		next.PasswordEnc = enc
		changes["password"] = "changed"
	case (server || account) && cur.PasswordEnc != nil:
		return Config{}, invalid("password_required", "Type the password again for the new server or address.")
	}
	if patch.HourlyLimit != nil {
		if *patch.HourlyLimit < 1 || *patch.HourlyLimit > MaxHourlyLimit {
			return Config{}, invalid("limit_invalid", fmt.Sprintf("Emails an hour is 1 to %d.", MaxHourlyLimit))
		}
		next.HourlyLimit = *patch.HourlyLimit
		changes["hourly_limit"] = next.HourlyLimit
	}
	if patch.Enabled != nil {
		next.Enabled = *patch.Enabled
		changes["enabled"] = next.Enabled
	}
	if server || account || changes["password"] != nil {
		next.ArrivedAt = nil
	}
	if patch.Arrived != nil {
		if *patch.Arrived {
			now := s.Now().UTC()
			next.ArrivedAt = &now
		} else {
			next.ArrivedAt = nil
		}
		changes["test_arrived"] = *patch.Arrived
	}

	if err := s.check(ctx, next); err != nil {
		return Config{}, err
	}
	next.Version = cur.Version + 1
	next.UpdatedAt = s.Now().UTC()
	err = s.Store.SaveEmailSettings(ctx, next, cur.Version, auth.AuditEntry{
		TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: "email.update", Target: "email_settings", Result: auth.ResultOK, Detail: changes,
	})
	if errors.Is(err, auth.ErrVersionChanged) {
		return Config{}, errChanged
	}
	if err == nil && next.Enabled {
		s.poke()
	}
	return next, err
}

// check is what a usable setting needs.
func (s *Service) check(ctx context.Context, c Config) error {
	if msg := CheckName(c.FromName); msg != "" {
		return invalid("from_name_invalid", msg)
	}
	if !c.Enabled && c.Host == "" {
		return nil
	}
	switch {
	case c.Host == "" || len(c.Host) > 253 || strings.ContainsAny(c.Host, " /:@") || !utf8.ValidString(c.Host):
		return invalid("host_invalid", "Give the mail server's name, e.g. smtp.example.com.")
	case c.Port < 1 || c.Port > 65535:
		return invalid("port_invalid", "The port is a number from 1 to 65535 (usually 465 or 587).")
	case c.Port == 25:
		return invalid("port_invalid", "Port 25 is for mail servers talking to each other, and is blocked on most connections. Use 465 or 587.")
	case c.Security != SecurityTLS && c.Security != SecuritySTARTTLS:
		return invalid("security_invalid", "Linx only sends email encrypted: TLS from the start (465) or STARTTLS (587).")
	}
	if msg := CheckAddress(c.FromAddress); msg != "" {
		return invalid("from_address_invalid", msg)
	}
	if len(c.Username) > 254 || hasControl(c.Username) {
		return invalid("username_invalid", "That user name can't be right: type it again.")
	}
	if c.Enabled && c.PasswordEnc == nil {
		return invalid("password_required", "Give the account's password (an app password for Google, Microsoft and iCloud).")
	}
	if err := s.Policy.CheckHost(ctx, s.Resolver, c.Host); err != nil {
		var b *safehttp.BlockedError
		if errors.As(err, &b) {
			return invalid("host_blocked", b.Error())
		}
		// The name doesn't resolve right now: said by the test, not here.
	}
	return nil
}

func (s *Service) account(c Config) (Account, error) {
	a := Account{Host: c.Host, Port: c.Port, Security: c.Security, Username: c.Username,
		From: mail.Address{Name: c.FromName, Address: c.FromAddress}}
	if c.PasswordEnc != nil {
		b, err := s.Sealer.Open(SealID(c.TenantID), c.PasswordEnc)
		if err != nil {
			return a, fmt.Errorf("opening the email password: %w", err)
		}
		a.Password = string(b)
	}
	return a, nil
}

// TestResult is the test email's checklist: the stages passed, and the
// failure if one stopped it.
type TestResult struct {
	To     string
	Passed []string
	Err    *SendError
}

// Test sends the test email to the caller's own address with the saved
// setting, on or off, straight away (not queued), and says how far it got.
func (s *Service) Test(ctx context.Context, to string) (TestResult, error) {
	s.init()
	p, err := systemAdmin(ctx)
	if err != nil {
		return TestResult{}, err
	}
	if !s.tests.Allow(p.ID, s.Now()) {
		return TestResult{}, &apihttp.Error{Status: http.StatusTooManyRequests, Code: "rate_limited",
			Detail: fmt.Sprintf("That's %d test emails in a minute. Wait a moment.", TestsPerMinute)}
	}
	if msg := CheckAddress(to); msg != "" {
		return TestResult{}, invalid("to_invalid", "Your account has no email address to send the test to.")
	}
	c, err := s.load(ctx, p.TenantID)
	if err != nil {
		return TestResult{}, err
	}
	if c.Host == "" || c.FromAddress == "" {
		return TestResult{}, invalid("email_not_set_up", "Save the mail account first.")
	}
	a, err := s.account(c)
	if err != nil {
		return TestResult{}, err
	}
	err = s.Sender.Send(ctx, a, []string{to}, TestContent())
	r := TestResult{To: to}
	var se *SendError
	switch {
	case err == nil:
		r.Passed = Stages
	case errors.As(err, &se):
		r.Err = se
		for _, st := range Stages {
			if st == se.Stage {
				break
			}
			r.Passed = append(r.Passed, st)
		}
	default:
		return TestResult{}, err
	}
	result, detail := auth.ResultOK, map[string]any{"to": to}
	if r.Err != nil {
		result, detail["error"] = auth.ResultFailed, r.Err.Detail
	}
	s.audit(ctx, auth.AuditEntry{TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: "email.test", Target: "email_settings", Result: result, Detail: detail})
	return r, nil
}

// TestContent is the test email (docs/ui/SCREENS_PHASE1F.md §5.3).
func TestContent() Content {
	return Content{Subject: "Linx can send email",
		Text: "This is a test from System → Settings → Email. Everything works: Linx can now send invites, password resets, voicemail and alerts by email."}
}

// auditor is the store's audit log, when it has one.
type auditor interface {
	Audit(ctx context.Context, e auth.AuditEntry) error
}

func (s *Service) audit(ctx context.Context, e auth.AuditEntry) {
	if a, ok := s.Store.(auditor); ok {
		if err := a.Audit(ctx, e); err != nil {
			s.Log.Error("writing the email audit entry failed", "err", err)
		}
	}
}

// Enqueue queues c for to, as kind, with audit (Linx's own sends pass a
// system entry). ErrOff when the tenant's email isn't on.
func (s *Service) Enqueue(ctx context.Context, tenant uuid.UUID, kind string, to []string, c Content, audit auth.AuditEntry) (uuid.UUID, error) {
	s.init()
	cfg, err := s.load(ctx, tenant)
	if err != nil {
		return uuid.Nil, err
	}
	if !cfg.Enabled {
		return uuid.Nil, ErrOff
	}
	if len(to) == 0 || len(to) > MaxRecipients {
		return uuid.Nil, fmt.Errorf("an email goes to 1 to %d addresses", MaxRecipients)
	}
	for _, a := range to {
		if msg := CheckAddress(a); msg != "" {
			return uuid.Nil, invalid("to_invalid", msg)
		}
	}
	if err := checkContent(c); err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	plain, err := json.Marshal(c)
	if err != nil {
		return uuid.Nil, err
	}
	enc, err := s.Sealer.Seal(outboxSealID(id), plain)
	if err != nil {
		return uuid.Nil, err
	}
	now := s.Now().UTC()
	m := Message{ID: id, TenantID: tenant, Kind: kind, To: to, ContentEnc: enc, Status: StatusPending, NextAttemptAt: &now, CreatedAt: now}
	if audit.Detail == nil {
		audit.Detail = map[string]any{}
	}
	audit.TenantID, audit.Target = &tenant, "email:"+id.String()
	audit.Detail["kind"], audit.Detail["to"] = kind, strings.Join(to, ", ")
	if err := s.Store.EnqueueEmail(ctx, m, audit); err != nil {
		return uuid.Nil, err
	}
	s.poke()
	return id, nil
}

// poke wakes the worker.
func (s *Service) poke() {
	s.init()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
