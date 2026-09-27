package store

import (
	"context"
	"net/netip"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

var _ controlplaneAuditSource = (*Store)(nil)

// controlplaneAuditSource mirrors services/control-plane/api.AuditLogSource,
// checked here so a signature drift is caught at compile time in this
// package too.
type controlplaneAuditSource interface {
	ListAuditLog(ctx context.Context, tenant uuid.UUID, f auth.AuditLogFilter, before *uuid.UUID, limit int) ([]auth.AuditLogEntry, error)
}

// ListAuditLog returns a page of audit_log, newest first (docs/ADMIN.md §9).
func (s *Store) ListAuditLog(ctx context.Context, tenant uuid.UUID, f auth.AuditLogFilter, before *uuid.UUID, limit int) ([]auth.AuditLogEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, at, actor, ip, action, target, result, detail FROM audit_log
		WHERE tenant_id = $1 AND ($2::uuid IS NULL OR id < $2)
			AND ($3 = '' OR actor = $3)
			AND ($4 = '' OR action LIKE $4 || '%')
			AND ($5 = '' OR target = $5)
			AND ($6::timestamptz IS NULL OR at >= $6)
			AND ($7::timestamptz IS NULL OR at <= $7)
		ORDER BY id DESC LIMIT $8`,
		tenant, before, f.Actor, f.Action, f.Target, f.Since, f.Until, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []auth.AuditLogEntry{}
	for rows.Next() {
		var e auth.AuditLogEntry
		var ip *netip.Addr
		var target *string
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &ip, &e.Action, &target, &e.Result, &e.Detail); err != nil {
			return nil, err
		}
		if ip != nil {
			e.IP = *ip
		}
		if target != nil {
			e.Target = *target
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
