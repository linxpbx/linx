package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/safehttp"
)

// RotationOverlap is how long the old secret keeps signing after a rotation.
const RotationOverlap = 24 * time.Hour

// maxReplay caps how many deliveries one "replay failed" call queues.
const maxReplay = 10000

// Service is what the API's webhook and outbound allowlist endpoints do.
// Caller mistakes come back as *apihttp.Error.
type Service struct {
	Store    Store
	Sealer   *dbsecret.Sealer
	Sender   *Sender
	Policy   safehttp.Policy
	Resolver safehttp.Resolver
	Now      func() time.Time
	// OnEnabledChanged, if set, is called after an admin turns an endpoint
	// on or off (not when the worker disables it — Worker.OnDisabled
	// covers that). Admin alerts hook in here to resolve the "webhook
	// disabled" alert once it's turned back on.
	OnEnabledChanged func(ctx context.Context, tenant, endpoint uuid.UUID, enabled bool)
}

var errNoPrincipal = errors.New("no principal on the request: authentication middleware is missing")

func notFound(what string) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "There is no " + what + " with that id."}
}

func invalid(code, detail string) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusUnprocessableEntity, Code: code, Detail: detail}
}

// audit starts an audit entry for the caller in ctx.
func audit(ctx context.Context, action, target string) (auth.Principal, auth.AuditEntry, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return auth.Principal{}, auth.AuditEntry{}, errNoPrincipal
	}
	return p, auth.AuditEntry{
		TenantID: &p.TenantID,
		Actor:    p.Actor(),
		IP:       auth.ClientIPFromContext(ctx),
		Action:   action,
		Target:   target,
		Result:   auth.ResultOK,
	}, nil
}

// ETag is an endpoint version as an HTTP entity tag.
func ETag(version int) string { return `"` + strconv.Itoa(version) + `"` }

// matchETag reports whether an If-Match header allows changing version.
func matchETag(ifMatch string, version int) bool {
	for _, tag := range strings.Split(ifMatch, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || tag == ETag(version) {
			return true
		}
	}
	return false
}

// checkURL refuses a URL that can never be delivered to: not https, or a
// host that resolves to an address the SSRF guard refuses.
func (s *Service) checkURL(ctx context.Context, raw string) error {
	if len(raw) > 2048 {
		return invalid("url_invalid", "The URL is too long (at most 2048 characters).")
	}
	u, err := safehttp.CheckURL(raw)
	if err != nil {
		return invalid("url_invalid", err.Error())
	}
	if err := s.Policy.CheckHost(ctx, s.Resolver, u.Hostname()); err != nil {
		var b *safehttp.BlockedError
		if errors.As(err, &b) {
			return invalid("url_blocked", b.Error())
		}
		return err
	}
	return nil
}

func cleanEventTypes(types []string) ([]string, error) {
	out := []string{}
	for _, t := range types {
		if !KnownEventType(t) {
			return nil, invalid("event_type_unknown", fmt.Sprintf("%q isn't an event type. GET /api/v1/event-types lists them.", t))
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out, nil
}

// EndpointInput is a new endpoint.
type EndpointInput struct {
	URL         string
	Description string
	EventTypes  []string
	Enabled     *bool
}

// Create registers an endpoint and returns it with its signing secret,
// which is shown this once.
func (s *Service) Create(ctx context.Context, in EndpointInput) (Endpoint, string, error) {
	if err := s.checkURL(ctx, in.URL); err != nil {
		return Endpoint{}, "", err
	}
	types, err := cleanEventTypes(in.EventTypes)
	if err != nil {
		return Endpoint{}, "", err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Endpoint{}, "", err
	}
	p, a, err := audit(ctx, "webhook.create", "webhook:"+id.String())
	if err != nil {
		return Endpoint{}, "", err
	}
	secret, err := NewSecret()
	if err != nil {
		return Endpoint{}, "", err
	}
	enc, err := s.Sealer.Seal(sealID(id), []byte(secret))
	if err != nil {
		return Endpoint{}, "", err
	}
	now := s.Now().UTC()
	e := Endpoint{
		ID: id, TenantID: p.TenantID, URL: in.URL, Description: in.Description, EventTypes: types,
		Enabled: in.Enabled == nil || *in.Enabled, SecretEnc: enc, Version: 1,
		CreatedBy: p.Actor(), CreatedAt: now, UpdatedAt: now,
	}
	if !e.Enabled {
		reason := DisabledAdmin
		e.DisabledReason, e.DisabledAt = &reason, &now
	}
	a.Detail = map[string]any{"url": e.URL, "event_types": e.EventTypes, "enabled": e.Enabled}
	if err := s.Store.CreateEndpoint(ctx, e, a); err != nil {
		return Endpoint{}, "", err
	}
	return e, secret, nil
}

// Get returns one of the caller's endpoints.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Endpoint, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return Endpoint{}, errNoPrincipal
	}
	e, err := s.Store.Endpoint(ctx, p.TenantID, id)
	if errors.Is(err, ErrNotFound) {
		return Endpoint{}, notFound("webhook")
	}
	return e, err
}

// List returns a page of the caller's endpoints, newest first.
func (s *Service) List(ctx context.Context, before *uuid.UUID, limit int) ([]Endpoint, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipal
	}
	return s.Store.ListEndpoints(ctx, p.TenantID, before, limit)
}

// Patch is a JSON Merge Patch of an endpoint; nil fields stay as they are.
type Patch struct {
	URL         *string
	Description *string
	EventTypes  *[]string
	Enabled     *bool
}

// Update applies patch. ifMatch, when not empty, must match the endpoint's
// current ETag (412 otherwise). Turning an endpoint on clears why it was
// off and its failure run; turning it off cancels pending deliveries.
func (s *Service) Update(ctx context.Context, id uuid.UUID, patch Patch, ifMatch string) (Endpoint, error) {
	e, err := s.Get(ctx, id)
	if err != nil {
		return Endpoint{}, err
	}
	if ifMatch != "" && !matchETag(ifMatch, e.Version) {
		return Endpoint{}, errChanged
	}
	_, a, err := audit(ctx, "webhook.update", "webhook:"+id.String())
	if err != nil {
		return Endpoint{}, err
	}
	changes := map[string]any{}
	if patch.URL != nil {
		if err := s.checkURL(ctx, *patch.URL); err != nil {
			return Endpoint{}, err
		}
		e.URL = *patch.URL
		changes["url"] = e.URL
	}
	if patch.Description != nil {
		e.Description = *patch.Description
		changes["description"] = e.Description
	}
	if patch.EventTypes != nil {
		if e.EventTypes, err = cleanEventTypes(*patch.EventTypes); err != nil {
			return Endpoint{}, err
		}
		changes["event_types"] = e.EventTypes
	}
	now := s.Now().UTC()
	enabledChanged := patch.Enabled != nil && *patch.Enabled != e.Enabled
	if enabledChanged {
		e.Enabled = *patch.Enabled
		if e.Enabled {
			e.DisabledReason, e.DisabledAt, e.FailingSince = nil, nil, nil
		} else {
			reason := DisabledAdmin
			e.DisabledReason, e.DisabledAt = &reason, &now
		}
		changes["enabled"] = e.Enabled
	}
	e.UpdatedAt = now
	a.Detail = changes
	updated, err := s.Store.UpdateEndpoint(ctx, e, a)
	if errors.Is(err, ErrVersionChanged) {
		return Endpoint{}, errChanged
	}
	if errors.Is(err, ErrNotFound) {
		return Endpoint{}, notFound("webhook")
	}
	if err == nil && enabledChanged && s.OnEnabledChanged != nil {
		s.OnEnabledChanged(ctx, updated.TenantID, updated.ID, updated.Enabled)
	}
	return updated, err
}

var errChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
	Detail: "This webhook was changed since you read it. Fetch it again and retry."}

// Delete removes an endpoint and its delivery log.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	p, a, err := audit(ctx, "webhook.delete", "webhook:"+id.String())
	if err != nil {
		return err
	}
	err = s.Store.DeleteEndpoint(ctx, p.TenantID, id, a)
	if errors.Is(err, ErrNotFound) {
		return notFound("webhook")
	}
	return err
}

// RotateSecret issues a new signing secret, shown this once. The previous
// one keeps signing alongside it for RotationOverlap, so the receiver can
// switch without missing messages.
func (s *Service) RotateSecret(ctx context.Context, id uuid.UUID) (Endpoint, string, error) {
	e, err := s.Get(ctx, id)
	if err != nil {
		return Endpoint{}, "", err
	}
	_, a, err := audit(ctx, "webhook.rotate_secret", "webhook:"+id.String())
	if err != nil {
		return Endpoint{}, "", err
	}
	secret, err := NewSecret()
	if err != nil {
		return Endpoint{}, "", err
	}
	enc, err := s.Sealer.Seal(sealID(id), []byte(secret))
	if err != nil {
		return Endpoint{}, "", err
	}
	now := s.Now().UTC()
	expires := now.Add(RotationOverlap)
	e.PreviousSecretEnc, e.PreviousSecretExpiresAt = e.SecretEnc, &expires
	e.SecretEnc = enc
	e.UpdatedAt = now
	a.Detail = map[string]any{"previous_secret_expires_at": expires}
	updated, err := s.Store.UpdateEndpoint(ctx, e, a)
	if errors.Is(err, ErrVersionChanged) {
		return Endpoint{}, "", errChanged
	}
	if errors.Is(err, ErrNotFound) {
		return Endpoint{}, "", notFound("webhook")
	}
	return updated, secret, err
}

// Test sends a webhook.test event to one endpoint now (even a turned-off
// one, to check it before turning it back on) and returns the delivery
// with its attempt. It's tried once, and doesn't count towards turning the
// endpoint off.
func (s *Service) Test(ctx context.Context, id uuid.UUID) (Delivery, error) {
	e, err := s.Get(ctx, id)
	if err != nil {
		return Delivery{}, err
	}
	_, a, err := audit(ctx, "webhook.test", "webhook:"+id.String())
	if err != nil {
		return Delivery{}, err
	}
	now := s.Now().UTC()
	ev, err := NewEvent(e.TenantID, TestEventType, map[string]any{
		"webhook_id": e.ID,
		"message":    "This is a test message from Linx. If you can read it, your endpoint works.",
	}, now)
	if err != nil {
		return Delivery{}, err
	}
	did, err := uuid.NewV7()
	if err != nil {
		return Delivery{}, err
	}
	// Leased from the start so the background worker leaves it alone.
	leased := now.Add(lease)
	d := Delivery{
		ID: did, TenantID: e.TenantID, EndpointID: e.ID, EventID: ev.ID, EventType: ev.Type,
		Status: StatusPending, MaxAttempts: 1, NextAttemptAt: &leased, CreatedAt: now,
	}
	if err := s.Store.CreateTestDelivery(ctx, ev, d, a); err != nil {
		return Delivery{}, err
	}
	job := Job{Delivery: d, URL: e.URL, SecretEnc: e.SecretEnc, PreviousSecretEnc: e.PreviousSecretEnc,
		PreviousSecretExpiresAt: e.PreviousSecretExpiresAt, Body: ev.Body}
	att, ok, gone := s.Sender.Send(ctx, job)
	o := outcome(job, att, ok, gone)
	// A test never turns the endpoint off (RecordAttempt skips health for
	// webhook.test), so the returned reason is always empty.
	if _, err := s.Store.RecordAttempt(ctx, o, time.Time{}); err != nil {
		return Delivery{}, err
	}
	return s.Store.Delivery(ctx, e.TenantID, did)
}

// Delivery returns one delivery of the caller's, with its attempts.
func (s *Service) Delivery(ctx context.Context, id uuid.UUID) (Delivery, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return Delivery{}, errNoPrincipal
	}
	d, err := s.Store.Delivery(ctx, p.TenantID, id)
	if errors.Is(err, ErrNotFound) {
		return Delivery{}, notFound("webhook delivery")
	}
	return d, err
}

// Deliveries lists an endpoint's deliveries, newest first, optionally only
// those with status.
func (s *Service) Deliveries(ctx context.Context, endpoint uuid.UUID, status string, before *uuid.UUID, limit int) ([]Delivery, error) {
	e, err := s.Get(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return s.Store.ListDeliveries(ctx, e.TenantID, e.ID, status, before, limit)
}

var errEndpointOff = &apihttp.Error{Status: http.StatusConflict, Code: "webhook_disabled",
	Detail: "This webhook is turned off. Turn it on first (you can send a test while it's off)."}

// Replay queues a new delivery of the same event to the same endpoint.
func (s *Service) Replay(ctx context.Context, id uuid.UUID) (Delivery, error) {
	orig, err := s.Delivery(ctx, id)
	if err != nil {
		return Delivery{}, err
	}
	if orig.Status == StatusPending {
		return Delivery{}, &apihttp.Error{Status: http.StatusConflict, Code: "delivery_pending",
			Detail: "This delivery is still being tried; wait for it to finish."}
	}
	if orig.EventType == TestEventType {
		return Delivery{}, &apihttp.Error{Status: http.StatusConflict, Code: "delivery_is_test",
			Detail: "Test messages can't be replayed; send a new test instead."}
	}
	e, err := s.Get(ctx, orig.EndpointID)
	if err != nil {
		return Delivery{}, err
	}
	if !e.Enabled {
		return Delivery{}, errEndpointOff
	}
	_, a, err := audit(ctx, "webhook_delivery.replay", "webhook_delivery:"+id.String())
	if err != nil {
		return Delivery{}, err
	}
	did, err := uuid.NewV7()
	if err != nil {
		return Delivery{}, err
	}
	now := s.Now().UTC()
	d := Delivery{
		ID: did, TenantID: orig.TenantID, EndpointID: orig.EndpointID, EventID: orig.EventID, EventType: orig.EventType,
		Status: StatusPending, MaxAttempts: MaxAttempts, NextAttemptAt: &now, ReplayOf: &orig.ID, CreatedAt: now,
	}
	a.Detail = map[string]any{"new_delivery": did}
	if err := s.Store.ReplayDelivery(ctx, orig, d, a); err != nil {
		return Delivery{}, err
	}
	return d, nil
}

// ReplayFailed queues every event that failed (or was cancelled when the
// endpoint was turned off) since the given time and hasn't been delivered
// or queued again since. It returns how many were queued.
func (s *Service) ReplayFailed(ctx context.Context, endpoint uuid.UUID, since time.Time) (int, error) {
	e, err := s.Get(ctx, endpoint)
	if err != nil {
		return 0, err
	}
	if !e.Enabled {
		return 0, errEndpointOff
	}
	_, a, err := audit(ctx, "webhook.replay", "webhook:"+endpoint.String())
	if err != nil {
		return 0, err
	}
	a.Detail = map[string]any{"since": since}
	return s.Store.ReplayFailed(ctx, e, since, s.Now().UTC(), MaxAttempts, maxReplay, a)
}

// hostPattern is a lower-case DNS name (the database checks it too).
var hostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// AddAllowlistEntry lets outbound connections reach a private range (a
// CIDR or single address inside one) or any address of one host name.
func (s *Service) AddAllowlistEntry(ctx context.Context, value, description string) (AllowlistEntry, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return AllowlistEntry{}, err
	}
	p, a, err := audit(ctx, "outbound_allowlist.create", "outbound_allowlist:"+id.String())
	if err != nil {
		return AllowlistEntry{}, err
	}
	entry := AllowlistEntry{ID: id, Description: description, CreatedBy: p.Actor(), CreatedAt: s.Now().UTC()}
	value = strings.TrimSpace(value)
	prefix, perr := netip.ParsePrefix(value)
	if perr != nil {
		if addr, err := netip.ParseAddr(value); err == nil {
			prefix, perr = netip.PrefixFrom(addr, addr.BitLen()), nil
		}
	}
	switch {
	case perr == nil:
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()).Masked()
		if prefix.Addr().Zone() != "" || !safehttp.Allowlistable(prefix) {
			return AllowlistEntry{}, invalid("allowlist_range_refused", fmt.Sprintf(
				"%s isn't inside one private range. Only LAN ranges (such as 192.168.1.0/24) can be allowed; public addresses need no entry, and Linx never connects to itself or cloud metadata services.", prefix))
		}
		c := prefix.String()
		entry.CIDR = &c
	default:
		host := strings.TrimSuffix(strings.ToLower(value), ".")
		if !hostPattern.MatchString(host) {
			return AllowlistEntry{}, invalid("allowlist_value_invalid", "Enter a CIDR range (192.168.1.0/24), an address, or a host name (nas.home.arpa).")
		}
		entry.Host = &host
	}
	a.Detail = map[string]any{"cidr": entry.CIDR, "host": entry.Host}
	err = s.Store.CreateAllowlistEntry(ctx, entry, a)
	if errors.Is(err, ErrDuplicate) {
		return AllowlistEntry{}, &apihttp.Error{Status: http.StatusConflict, Code: "allowlist_duplicate",
			Detail: "That range or host is already on the outbound allowlist."}
	}
	if err != nil {
		return AllowlistEntry{}, err
	}
	return entry, nil
}

// DeleteAllowlistEntry removes an entry; new connections stop using it at once.
func (s *Service) DeleteAllowlistEntry(ctx context.Context, id uuid.UUID) error {
	_, a, err := audit(ctx, "outbound_allowlist.delete", "outbound_allowlist:"+id.String())
	if err != nil {
		return err
	}
	err = s.Store.DeleteAllowlistEntry(ctx, id, a)
	if errors.Is(err, ErrNotFound) {
		return notFound("outbound allowlist entry")
	}
	return err
}

// Allowlist is the safehttp.AllowlistSource backed by the database.
func Allowlist(st Store) safehttp.AllowlistSource {
	return func(ctx context.Context) (safehttp.Allowlist, error) {
		entries, err := st.ListAllowlist(ctx)
		if err != nil {
			return safehttp.Allowlist{}, err
		}
		var l safehttp.Allowlist
		for _, e := range entries {
			switch {
			case e.CIDR != nil:
				if p, err := netip.ParsePrefix(*e.CIDR); err == nil {
					l.Prefixes = append(l.Prefixes, p)
				}
			case e.Host != nil:
				l.Hosts = append(l.Hosts, *e.Host)
			}
		}
		return l, nil
	}
}
