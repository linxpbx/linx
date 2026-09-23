package alert

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/safehttp"
	"linxpbx.com/linx/internal/webhook"
)

// Service is what the API's alert-channel and alert-listing endpoints do.
// Caller mistakes come back as *apihttp.Error.
type Service struct {
	Store    Store
	Sealer   *dbsecret.Sealer
	Sender   *Sender
	Policy   safehttp.Policy
	Resolver safehttp.Resolver
	Now      func() time.Time
}

var errNoPrincipal = errors.New("no principal on the request: authentication middleware is missing")

func notFound(what string) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "There is no " + what + " with that id."}
}

func invalid(code, detail string) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusUnprocessableEntity, Code: code, Detail: detail}
}

func audit(ctx context.Context, action, target string) (auth.Principal, auth.AuditEntry, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return auth.Principal{}, auth.AuditEntry{}, errNoPrincipal
	}
	return p, auth.AuditEntry{
		TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: action, Target: target, Result: auth.ResultOK,
	}, nil
}

// ETag is a channel version as an HTTP entity tag.
func ETag(version int) string { return `"` + strconv.Itoa(version) + `"` }

func matchETag(ifMatch string, version int) bool {
	for _, tag := range strings.Split(ifMatch, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || tag == ETag(version) {
			return true
		}
	}
	return false
}

var errChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
	Detail: "This alert channel was changed since you read it. Fetch it again and retry."}

var validKinds = []string{KindNtfy, KindGotify, KindSlack, KindTeams, KindTelegram, KindWebhook}

func validKind(k string) bool {
	for _, v := range validKinds {
		if v == k {
			return true
		}
	}
	return false
}

var validSeverities = []string{SeverityInfo, SeverityWarning, SeverityCritical}

func validSeverity(s string) bool {
	for _, v := range validSeverities {
		if v == s {
			return true
		}
	}
	return false
}

var timePattern = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

// parseClock turns "HH:MM" into minutes since midnight.
func parseClock(s string) (int, error) {
	m := timePattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q isn't a time in HH:MM form", s)
	}
	h, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	return h*60 + min, nil
}

// FormatClock turns minutes since midnight back into "HH:MM", for the API
// response.
func FormatClock(minutes int) string { return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60) }

// QuietHoursInput is a channel's quiet-hours setting from the API.
type QuietHoursInput struct {
	Enabled              bool
	Start, End, Timezone string
	BypassCritical       *bool
}

func (in *QuietHoursInput) toModel() (*QuietHours, error) {
	if in == nil || !in.Enabled {
		return nil, nil
	}
	start, err := parseClock(in.Start)
	if err != nil {
		return nil, invalid("quiet_hours_invalid", "Quiet hours start: "+err.Error())
	}
	end, err := parseClock(in.End)
	if err != nil {
		return nil, invalid("quiet_hours_invalid", "Quiet hours end: "+err.Error())
	}
	if in.Timezone == "" {
		return nil, invalid("quiet_hours_invalid", "Quiet hours need a time zone.")
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return nil, invalid("quiet_hours_invalid", fmt.Sprintf("%q isn't a time zone Linx recognises.", in.Timezone))
	}
	bypass := in.BypassCritical == nil || *in.BypassCritical
	return &QuietHours{Start: start, End: end, Timezone: in.Timezone, BypassCritical: bypass}, nil
}

// checkChannelURL is checkURL for a field name, so the error names which
// field was wrong.
func (s *Service) checkChannelURL(ctx context.Context, field, raw string) error {
	if len(raw) > 2048 {
		return invalid("url_invalid", field+" is too long (at most 2048 characters).")
	}
	u, err := safehttp.CheckURL(raw)
	if err != nil {
		return invalid("url_invalid", field+": "+err.Error())
	}
	if err := s.Policy.CheckHost(ctx, s.Resolver, u.Hostname()); err != nil {
		var b *safehttp.BlockedError
		if errors.As(err, &b) {
			return invalid("url_blocked", field+": "+b.Error())
		}
		return err
	}
	return nil
}

// checkConfig validates cfg for kind and checks every URL-shaped field
// against the SSRF guard.
func (s *Service) checkConfig(ctx context.Context, kind string, cfg Config) error {
	if msg := ValidateFor(kind, cfg); msg != "" {
		return invalid("channel_config_invalid", msg)
	}
	if cfg.URL != "" {
		if err := s.checkChannelURL(ctx, "url", cfg.URL); err != nil {
			return err
		}
	}
	if cfg.ServerURL != "" {
		if err := s.checkChannelURL(ctx, "server_url", cfg.ServerURL); err != nil {
			return err
		}
	} else if kind == KindGotify {
		return invalid("channel_config_invalid", "gotify needs server_url.")
	}
	return nil
}

// ChannelInput is a new or changed channel.
type ChannelInput struct {
	Kind        string
	Name        string
	Config      Config
	MinSeverity string
	QuietHours  *QuietHoursInput
	Enabled     *bool
}

// Create registers a channel. For a generic webhook channel, the signing
// secret is generated here and returned once (it's never accepted from
// the caller).
func (s *Service) Create(ctx context.Context, in ChannelInput) (Channel, string, error) {
	if !validKind(in.Kind) {
		return Channel{}, "", invalid("channel_kind_invalid", fmt.Sprintf("%q isn't a channel kind.", in.Kind))
	}
	if err := s.checkConfig(ctx, in.Kind, in.Config); err != nil {
		return Channel{}, "", err
	}
	minSeverity := in.MinSeverity
	if minSeverity == "" {
		minSeverity = SeverityInfo
	} else if !validSeverity(minSeverity) {
		return Channel{}, "", invalid("severity_invalid", fmt.Sprintf("%q isn't a severity.", minSeverity))
	}
	qh, err := in.QuietHours.toModel()
	if err != nil {
		return Channel{}, "", err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Channel{}, "", err
	}
	p, a, err := audit(ctx, "alert_channel.create", "alert_channel:"+id.String())
	if err != nil {
		return Channel{}, "", err
	}

	var secret string
	cfg := in.Config
	if in.Kind == KindWebhook {
		var err error
		if secret, err = webhook.NewSecret(); err != nil {
			return Channel{}, "", err
		}
		cfg.Secret = secret
	}
	plain, err := cfg.Marshal()
	if err != nil {
		return Channel{}, "", err
	}
	enc, err := s.Sealer.Seal(sealID(id), plain)
	if err != nil {
		return Channel{}, "", err
	}
	now := s.Now().UTC()
	c := Channel{
		ID: id, TenantID: p.TenantID, Kind: in.Kind, Name: in.Name, ConfigEnc: enc, MinSeverity: minSeverity,
		QuietHours: qh, Enabled: in.Enabled == nil || *in.Enabled, Version: 1,
		CreatedBy: p.Actor(), CreatedAt: now, UpdatedAt: now,
	}
	a.Detail = map[string]any{"kind": c.Kind, "name": c.Name, "min_severity": c.MinSeverity, "enabled": c.Enabled}
	if err := s.Store.CreateChannel(ctx, c, a); err != nil {
		return Channel{}, "", err
	}
	return c, secret, nil
}

// Get returns one of the caller's channels.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Channel, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return Channel{}, errNoPrincipal
	}
	c, err := s.Store.Channel(ctx, p.TenantID, id)
	if errors.Is(err, ErrNotFound) {
		return Channel{}, notFound("alert channel")
	}
	return c, err
}

// List returns a page of the caller's channels, newest first.
func (s *Service) List(ctx context.Context, before *uuid.UUID, limit int) ([]Channel, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipal
	}
	return s.Store.ListChannels(ctx, p.TenantID, before, limit)
}

// Patch is a JSON Merge Patch of a channel; nil fields stay as they are.
// Config and QuietHours, when sent, replace as a whole (not deep-merged).
type Patch struct {
	Name        *string
	Config      *Config
	MinSeverity *string
	QuietHours  *QuietHoursInput
	Enabled     *bool
}

// Update applies patch. ifMatch, when not empty, must match the channel's
// current ETag (412 otherwise).
func (s *Service) Update(ctx context.Context, id uuid.UUID, patch Patch, ifMatch string) (Channel, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return Channel{}, err
	}
	if ifMatch != "" && !matchETag(ifMatch, c.Version) {
		return Channel{}, errChanged
	}
	_, a, err := audit(ctx, "alert_channel.update", "alert_channel:"+id.String())
	if err != nil {
		return Channel{}, err
	}
	changes := map[string]any{}
	if patch.Name != nil {
		c.Name = *patch.Name
		changes["name"] = c.Name
	}
	if patch.Config != nil {
		if err := s.checkConfig(ctx, c.Kind, *patch.Config); err != nil {
			return Channel{}, err
		}
		cfg := *patch.Config
		if c.Kind == KindWebhook {
			// The signing secret is only ever changed by rotation, not a
			// config patch, so it survives even though config is replaced.
			plain, err := s.Sealer.Open(sealID(c.ID), c.ConfigEnc)
			if err == nil {
				if old, err := UnmarshalConfig(plain); err == nil {
					cfg.Secret = old.Secret
				}
			}
		}
		plain, err := cfg.Marshal()
		if err != nil {
			return Channel{}, err
		}
		enc, err := s.Sealer.Seal(sealID(c.ID), plain)
		if err != nil {
			return Channel{}, err
		}
		c.ConfigEnc = enc
		changes["config"] = "updated"
	}
	if patch.MinSeverity != nil {
		if !validSeverity(*patch.MinSeverity) {
			return Channel{}, invalid("severity_invalid", fmt.Sprintf("%q isn't a severity.", *patch.MinSeverity))
		}
		c.MinSeverity = *patch.MinSeverity
		changes["min_severity"] = c.MinSeverity
	}
	if patch.QuietHours != nil {
		qh, err := patch.QuietHours.toModel()
		if err != nil {
			return Channel{}, err
		}
		c.QuietHours = qh
		changes["quiet_hours"] = "updated"
	}
	if patch.Enabled != nil {
		c.Enabled = *patch.Enabled
		changes["enabled"] = c.Enabled
	}
	c.UpdatedAt = s.Now().UTC()
	a.Detail = changes
	updated, err := s.Store.UpdateChannel(ctx, c, a)
	if errors.Is(err, ErrVersionChanged) {
		return Channel{}, errChanged
	}
	if errors.Is(err, ErrNotFound) {
		return Channel{}, notFound("alert channel")
	}
	return updated, err
}

// Delete removes a channel.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	p, a, err := audit(ctx, "alert_channel.delete", "alert_channel:"+id.String())
	if err != nil {
		return err
	}
	err = s.Store.DeleteChannel(ctx, p.TenantID, id, a)
	if errors.Is(err, ErrNotFound) {
		return notFound("alert channel")
	}
	return err
}

// TestResult is one immediate send attempt for the Test button.
type TestResult struct {
	Succeeded  bool
	StatusCode *int
	DurationMS int
	Error      *string
}

// Test sends a test message to a channel right away, whether or not it's
// enabled, and returns the result. It's tried once and isn't logged.
func (s *Service) Test(ctx context.Context, id uuid.UUID) (TestResult, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	if _, _, err := audit(ctx, "alert_channel.test", "alert_channel:"+id.String()); err != nil {
		return TestResult{}, err
	}
	did, err := uuid.NewV7()
	if err != nil {
		return TestResult{}, err
	}
	job := Job{
		Delivery:    Delivery{ID: did, TenantID: c.TenantID, ChannelID: c.ID, Kind: DeliveryTest, MaxAttempts: 1},
		ChannelKind: c.Kind,
		ConfigEnc:   c.ConfigEnc,
		Alerts: []Alert{{
			Severity: SeverityInfo, Title: "Test alert from Linx",
			Message: "This is a test message. If you can read it, this channel works.",
		}},
	}
	a, ok := s.Sender.Send(ctx, job)
	return TestResult{Succeeded: ok, StatusCode: a.StatusCode, DurationMS: a.DurationMS, Error: a.Error}, nil
}

// Alerts lists open and recent alerts for the caller's tenant, newest
// (by last activity) first.
func (s *Service) Alerts(ctx context.Context, status string, before *uuid.UUID, limit int) ([]Alert, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipal
	}
	return s.Store.ListAlerts(ctx, p.TenantID, status, before, limit)
}
