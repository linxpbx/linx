package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/moved"
)

var _ moved.Store = (*Store)(nil)

// RecordPlace writes this start's place, and a move when the one it
// replaces is another place (docs/INSTALL.md §8).
func (s *Store) RecordPlace(ctx context.Context, now moved.Place, at time.Time, newID uuid.UUID) (*moved.Move, error) {
	var out *moved.Move
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var before moved.Place
		err := tx.QueryRow(ctx, `SELECT server_id, domain, lan_networks, lan_address, public_address, front_door
			FROM install_place FOR UPDATE`).Scan(&before.ServerID, &before.Domain, &before.LANNetworks, &before.LANAddress,
			&before.PublicAddress, &before.FrontDoor)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		default:
			if moved.Moved(before, now) {
				m := moved.Move{ID: newID, DetectedAt: at, Before: before, After: now}
				if _, err := tx.Exec(ctx, `INSERT INTO place_move (id, detected_at, before, after) VALUES ($1, $2, $3, $4)`,
					m.ID, m.DetectedAt, before, now); err != nil {
					return err
				}
				out = &m
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO install_place (id, server_id, domain, lan_networks, lan_address, public_address, front_door, recorded_at)
			VALUES (true, $1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (id) DO UPDATE SET server_id = $1, domain = $2, lan_networks = $3, lan_address = $4,
				-- A start that couldn't tell the public address keeps the last one known.
				public_address = CASE WHEN $5 = '' THEN install_place.public_address ELSE $5 END,
				front_door = $6, recorded_at = $7`,
			now.ServerID, now.Domain, stringsOrEmpty(now.LANNetworks), now.LANAddress, now.PublicAddress, now.FrontDoor, at)
		return err
	})
	return out, err
}

// CurrentMove is the newest move not yet done.
func (s *Store) CurrentMove(ctx context.Context) (*moved.Move, error) {
	var m moved.Move
	var before, after, ticks []byte
	err := s.pool.QueryRow(ctx, `SELECT id, detected_at, before, after, ticks, hidden_until, done_at FROM place_move
		ORDER BY detected_at DESC, id DESC LIMIT 1`).Scan(&m.ID, &m.DetectedAt, &before, &after, &ticks, &m.HiddenUntil, &m.DoneAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if m.DoneAt != nil {
		return nil, nil
	}
	for _, x := range []struct {
		b []byte
		v any
	}{{before, &m.Before}, {after, &m.After}, {ticks, &m.Ticks}} {
		if err := json.Unmarshal(x.b, x.v); err != nil {
			return nil, err
		}
	}
	return &m, nil
}

func (s *Store) UpdateMove(ctx context.Context, m moved.Move) error {
	ticks := m.Ticks
	if ticks == nil {
		ticks = map[string]bool{}
	}
	_, err := s.pool.Exec(ctx, `UPDATE place_move SET ticks = $2, hidden_until = $3, done_at = $4 WHERE id = $1`,
		m.ID, ticks, m.HiddenUntil, m.DoneAt)
	return err
}

// Facts are what the moved-server checklist looks at.
func (s *Store) Facts(ctx context.Context, tenant uuid.UUID) (moved.Facts, error) {
	var f moved.Facts
	rows, err := s.pool.Query(ctx, `SELECT id, name, kind, wireguard_profile_id IS NOT NULL FROM trunk
		WHERE tenant_id = $1 AND enabled ORDER BY name COLLATE "unicode"`, tenant)
	if err != nil {
		return f, err
	}
	for rows.Next() {
		var n moved.Named
		var kind string
		var tunneled bool
		if err := rows.Scan(&n.ID, &n.Name, &kind, &tunneled); err != nil {
			rows.Close()
			return f, err
		}
		switch {
		case tunneled:
		case kind == "lan_peer":
			f.LANPeers = append(f.LANPeers, n)
		case kind == "ip_authenticated":
			f.ByAddress = append(f.ByAddress, n)
		case kind == "registers_here":
			f.SignsIn = append(f.SignsIn, n)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return f, err
	}
	rows, err = s.pool.Query(ctx, `SELECT id, name FROM wireguard_profile WHERE tenant_id = $1 ORDER BY name COLLATE "unicode"`, tenant)
	if err != nil {
		return f, err
	}
	for rows.Next() {
		var n moved.Named
		if err := rows.Scan(&n.ID, &n.Name); err != nil {
			rows.Close()
			return f, err
		}
		f.Tunnels = append(f.Tunnels, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return f, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM device
		WHERE tenant_id = $1 AND kind <> 'web' AND enabled AND revoked_at IS NULL`, tenant).Scan(&f.DeskPhones); err != nil {
		return f, err
	}
	var admin []netip.Prefix
	if err := s.pool.QueryRow(ctx, `SELECT admin_network_restricted, admin_networks FROM pbx_setting`).
		Scan(&f.AdminRestricted, &admin); err != nil {
		return f, err
	}
	for _, p := range admin {
		f.AdminNetworks = append(f.AdminNetworks, p.String())
	}
	var last *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT max(finished_at) FROM backup_run
		WHERE tenant_id = $1 AND status IN ('success', 'partial')`, tenant).Scan(&last); err != nil {
		return f, err
	}
	f.BackedUpSince = func(t time.Time) bool { return last != nil && last.After(t) }
	return f, nil
}

func stringsOrEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
