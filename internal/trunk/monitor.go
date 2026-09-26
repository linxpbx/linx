package trunk

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/trunkstatus"
)

// DownHoldBack is how long a trunk must stay down before the alert goes
// out (docs/TRUNKS.md §9): long enough for a provider's brief restart.
const DownHoldBack = 2 * time.Minute

// staleAfter: a status file older than this means Asterisk (or its
// entrypoint) isn't answering. Trunks then keep their last status: Asterisk
// being down has its own checks, and a restart shouldn't flap every trunk.
const staleAfter = time.Minute

// MonitorStore is the database access the Monitor needs.
type MonitorStore interface {
	AllTrunks(ctx context.Context) ([]Trunk, error)
	// SetTrunkStatus records a new status and fires trunk.status_changed,
	// reporting whether it changed.
	SetTrunkStatus(ctx context.Context, tenant, id uuid.UUID, status, detail string, at time.Time) (bool, error)
}

// Alerter is the part of the alert engine the Monitor uses.
type Alerter interface {
	FireAfter(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string, holdBack time.Duration) error
	Resolve(ctx context.Context, tenant uuid.UUID, key string) error
}

// DownAlertKey is the "trunk is down" alert's key.
func DownAlertKey(id uuid.UUID) string { return "trunk.down:" + id.String() }

// Monitor keeps every trunk's status in step with what Asterisk reports
// (internal/trunkstatus), and raises and clears the "trunk is down" alert.
type Monitor struct {
	Store  MonitorStore
	Alerts Alerter
	// Dir is where Asterisk's entrypoint writes the status file.
	Dir      string
	Interval time.Duration
	Now      func() time.Time
	Log      *slog.Logger

	stale bool
}

// Run checks every Interval (default 10 s) until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	interval := m.Interval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := m.Check(ctx); err != nil && ctx.Err() == nil {
			m.Log.Error("checking trunks' state", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Check reads Asterisk's report once and updates every trunk.
func (m *Monitor) Check(ctx context.Context) error {
	now := m.now()
	file, err := trunkstatus.Read(m.Dir)
	fresh := err == nil && now.Sub(file.WrittenAt) <= staleAfter
	if !fresh {
		if !m.stale {
			reason := "it's out of date"
			if err != nil {
				reason = err.Error()
			}
			m.Log.Warn("Asterisk isn't reporting trunks' state; keeping the last known", "reason", reason)
			m.stale = true
		}
	} else if m.stale {
		m.Log.Info("Asterisk is reporting trunks' state again")
		m.stale = false
	}

	trunks, err := m.Store.AllTrunks(ctx)
	if err != nil {
		return err
	}
	type decided struct {
		t              Trunk
		status, detail string
	}
	var all []decided
	// Whether any line for outgoing calls works, per tenant: a trunk down
	// with none left is critical (no outside calls, emergency ones included).
	outboundUp := map[uuid.UUID]bool{}
	for _, t := range trunks {
		status, detail := t.Status, t.StatusDetail
		switch {
		case !t.Enabled:
			status, detail = trunkstatus.StatusDisabled, "It's turned off."
		case fresh:
			status, detail = trunkstatus.Decide(t.Kind == KindRegistration, file.Trunks[t.Endpoint()])
		case t.Status == trunkstatus.StatusDisabled:
			// Turned back on while Asterisk isn't reporting.
			status, detail = trunkstatus.StatusUnknown, "Waiting for Asterisk to report on it."
		}
		all = append(all, decided{t, status, detail})
		if t.Enabled && t.OutboundPriority != nil && !trunkstatus.Down(status) {
			outboundUp[t.TenantID] = true
		}
	}

	var errs []error
	for _, d := range all {
		t := d.t
		if d.status != t.Status || d.detail != t.StatusDetail {
			changed, err := m.Store.SetTrunkStatus(ctx, t.TenantID, t.ID, d.status, d.detail, now)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if changed {
				m.Log.Info("trunk state changed", "trunk", t.ID, "name", t.Name, "from", t.Status, "to", d.status)
			}
		}
		switch {
		case trunkstatus.Down(d.status):
			severity, msg := "warning", d.detail
			if t.OutboundPriority != nil && !outboundUp[t.TenantID] {
				severity = "critical"
				msg += " No other line works, so outside calls can't be made, emergency calls included."
			} else if t.OutboundPriority != nil {
				msg += " Outgoing calls use the next line."
			}
			if t.Kind != KindRegistration || d.status == trunkstatus.StatusUnreachable {
				msg += " Calls to its numbers can't reach Linx either."
			}
			err := m.Alerts.FireAfter(ctx, t.TenantID, DownAlertKey(t.ID), severity,
				fmt.Sprintf("Phone line %q is down", t.Name), msg, "", DownHoldBack)
			if err != nil {
				errs = append(errs, err)
			}
		case trunkstatus.Up(d.status), d.status == trunkstatus.StatusDisabled:
			if err := m.Alerts.Resolve(ctx, t.TenantID, DownAlertKey(t.ID)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
