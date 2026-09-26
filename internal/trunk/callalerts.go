package trunk

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/pbx"
)

// Alert keys of CallAlerts.
const (
	emergencyAlertPrefix  = "call.emergency:"
	firstRegionPrefix     = "call.first_country:"
	internationalAlertKey = "call.international_unusual"
)

// OutsideCallRetention is how long outside calls are kept for the
// international-calling alert (it only looks back an hour).
const OutsideCallRetention = 30 * 24 * time.Hour

// CallAlertStore is the database access CallAlerts needs.
type CallAlertStore interface {
	Country(ctx context.Context) (string, error)
	// InternationalAlertLimits: more than minutes, or calls, to numbers
	// abroad in an hour is unusual (docs/TRUNKS.md §9).
	InternationalAlertLimits(ctx context.Context) (minutes, calls int, err error)
	// RecordOutsideCall keeps a call that went out on a trunk.
	RecordOutsideCall(ctx context.Context, c pbx.OutsideCall) error
	// CallsAbroadSince counts calls that went out to numbers outside home
	// since the time given, and their talk time.
	CallsAbroadSince(ctx context.Context, tenant uuid.UUID, home string, since time.Time) (calls, talkSeconds int, err error)
	// FirstCallToRegion records region as called, reporting whether it
	// never had been.
	FirstCallToRegion(ctx context.Context, tenant uuid.UUID, region, extension string, at time.Time) (bool, error)
	CleanupOutsideCalls(ctx context.Context, before time.Time) error
}

// CallAnnouncer is the part of the alert engine CallAlerts uses.
type CallAnnouncer interface {
	Announce(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string) error
	FireAfter(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string, holdBack time.Duration) error
	Resolve(ctx context.Context, tenant uuid.UUID, key string) error
}

// CallAlerts raises the alerts about outgoing calls (docs/TRUNKS.md §9):
// every emergency call, unusual calling abroad, and a country called for
// the first time. It's the call tracker's pbx.CallWatcher.
type CallAlerts struct {
	Store  CallAlertStore
	Alerts CallAnnouncer
	// InProgress lists outgoing calls in progress (the call tracker's
	// OutsideCallsInProgress), so a long call abroad counts before it ends.
	InProgress func() []pbx.OutsideCall
	Now        func() time.Time
	Log        *slog.Logger
}

var _ pbx.CallWatcher = (*CallAlerts)(nil)

func (a *CallAlerts) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *CallAlerts) home(ctx context.Context) string {
	home, err := a.Store.Country(ctx)
	if err != nil || home == "" {
		return numbering.DefaultCountry
	}
	return home
}

// abroad reports whether a call's number is in another country.
func abroad(r numbering.Result, home string) bool {
	return r.Region != "" && r.Region != home && (r.Category == numbering.International || r.Category == numbering.Premium)
}

// OutsideCallStarted announces an emergency call the moment it's dialled,
// whether or not a line carries it: someone may need help.
func (a *CallAlerts) OutsideCallStarted(ctx context.Context, c pbx.OutsideCall) {
	if c.Result.Category != numbering.Emergency {
		return
	}
	label := c.Result.Label
	if label == "" {
		label = "emergency"
	}
	msg := fmt.Sprintf("Extension %s called %s (%s). Linx always lets emergency calls through; someone there may need help.",
		c.Extension, c.Dialled, label)
	if err := a.Alerts.Announce(ctx, c.Tenant, emergencyAlertPrefix+c.ID.String(), "critical",
		"Emergency call from extension "+c.Extension, msg, ""); err != nil {
		a.Log.Error("announcing an emergency call", "err", err)
	}
}

// OutsideCallEnded keeps a call that went out and checks the calling
// abroad it adds to.
func (a *CallAlerts) OutsideCallEnded(ctx context.Context, c pbx.OutsideCall) {
	if !c.WentOut {
		return
	}
	if err := a.Store.RecordOutsideCall(ctx, c); err != nil {
		a.Log.Error("recording an outside call", "err", err)
		return
	}
	home := a.home(ctx)
	if !abroad(c.Result, home) {
		return
	}
	first, err := a.Store.FirstCallToRegion(ctx, c.Tenant, c.Result.Region, c.Extension, c.StartedAt)
	if err != nil {
		a.Log.Error("recording a country called", "err", err)
	} else if first {
		country := numbering.CountryName(c.Result.Region, numbering.Countries[c.Result.Region])
		msg := fmt.Sprintf("Extension %s called %s, the first call from Linx to %s. If nobody expected that, check who can call abroad.",
			c.Extension, c.Result.Pretty(), country)
		if err := a.Alerts.Announce(ctx, c.Tenant, firstRegionPrefix+c.ID.String(), "warning",
			"First call to "+country, msg, ""); err != nil {
			a.Log.Error("announcing a first call to a country", "err", err)
		}
	}
	a.checkAbroad(ctx, c.Tenant, home)
}

// Run checks calling abroad every minute (long calls count while they're
// still going) and clears out old calls every hour, until ctx ends.
func (a *CallAlerts) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	var cleaned time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := a.now()
		if now.Sub(cleaned) >= time.Hour {
			if err := a.Store.CleanupOutsideCalls(ctx, now.Add(-OutsideCallRetention)); err != nil && ctx.Err() == nil {
				a.Log.Error("clearing old outside calls", "err", err)
			}
			cleaned = now
		}
		home := a.home(ctx)
		tenants := map[uuid.UUID]bool{}
		if a.InProgress != nil {
			for _, c := range a.InProgress() {
				if abroad(c.Result, home) {
					tenants[c.Tenant] = true
				}
			}
		}
		for tenant := range tenants {
			a.checkAbroad(ctx, tenant, home)
		}
	}
}

// checkAbroad fires "unusual calling abroad" when the last hour's calls
// abroad (those in progress included) pass either limit, to be sent at
// once (fraud costs money by the minute), and resolves it once a check
// finds the last hour back under both.
func (a *CallAlerts) checkAbroad(ctx context.Context, tenant uuid.UUID, home string) {
	now := a.now()
	maxMinutes, maxCalls, err := a.Store.InternationalAlertLimits(ctx)
	if err != nil {
		a.Log.Error("reading the international-calling limits", "err", err)
		return
	}
	calls, talk, err := a.Store.CallsAbroadSince(ctx, tenant, home, now.Add(-time.Hour))
	if err != nil {
		a.Log.Error("counting calls abroad", "err", err)
		return
	}
	if a.InProgress != nil {
		for _, c := range a.InProgress() {
			if c.Tenant != tenant || !abroad(c.Result, home) {
				continue
			}
			calls++
			if c.AnsweredAt != nil {
				talk += int(now.Sub(*c.AnsweredAt) / time.Second)
			}
		}
	}
	minutes := talk / 60
	if calls <= maxCalls && minutes <= maxMinutes {
		if err := a.Alerts.Resolve(ctx, tenant, internationalAlertKey); err != nil {
			a.Log.Error("clearing the international-calling alert", "err", err)
		}
		return
	}
	msg := fmt.Sprintf("In the last hour Linx made %d calls abroad, %d minutes in all (the alert is set for more than %d calls or %d minutes). "+
		"If that's not expected, someone may be misusing a phone or a login: check the calls and turn international calling off for the extensions involved.",
		calls, minutes, maxCalls, maxMinutes)
	if err := a.Alerts.FireAfter(ctx, tenant, internationalAlertKey, "critical", "Unusual calling abroad", msg, "", 0); err != nil {
		a.Log.Error("firing the international-calling alert", "err", err)
	}
}
