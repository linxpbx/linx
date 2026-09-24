// Package pbx is what the API's extension and device endpoints do
// (docs/PBX.md §5): plain CRUD plus SIP credential generation (ADR-033).
// Asterisk never talks to this package; it reads the tables this package
// writes through the asterisk schema's realtime views (migration 0005).
package pbx

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

// Device kinds (docs/PBX.md §3). Only KindSoftphone works in this slice
// (ADR-035); the others arrive with the web client and the iOS app.
const (
	KindSoftphone = "softphone"
	KindWeb       = "web"
	KindIOS       = "ios"
	KindDesk      = "desk"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrVersionChanged = errors.New("changed since it was read")
	ErrDuplicate      = errors.New("already exists")
)

// Extension is a number people have, e.g. "101" (docs/PBX.md §1, §3).
type Extension struct {
	ID, TenantID         uuid.UUID
	Number               string
	DisplayName          string
	Email                string
	Enabled              bool
	Version              int
	CreatedAt, UpdatedAt time.Time
	DeletedAt            *time.Time
}

// Device is a phone or app that rings for an extension (docs/PBX.md §1,
// §3). DigestHash is Asterisk's md5_cred; it is never sent to the API.
type Device struct {
	ID, TenantID, ExtensionID uuid.UUID
	Name                      string
	Kind                      string
	SIPUsername               string
	DigestHash                string
	Enabled                   bool
	// Online is whether it's signed in right now, as Asterisk last reported
	// (the call tracker keeps it current).
	Online               bool
	LastRegisteredAt     *time.Time
	LastRegisteredFrom   *netip.Addr
	Version              int
	CreatedAt, UpdatedAt time.Time
}

// Store is the database access extensions and devices need
// (internal/store implements it).
type Store interface {
	CreateExtension(ctx context.Context, e Extension, audit auth.AuditEntry) error
	Extension(ctx context.Context, tenant, id uuid.UUID) (Extension, error)
	ListExtensions(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]Extension, error)
	// UpdateExtension saves e if its stored version is still e.Version and
	// returns it with the new version (ErrVersionChanged otherwise).
	UpdateExtension(ctx context.Context, e Extension, audit auth.AuditEntry) (Extension, error)
	// DeleteExtension soft-deletes the extension (freeing its number for
	// reuse) and revokes every device under it, in one transaction
	// (docs/PBX.md §5). It writes one audit entry (for the extension) and
	// one device.revoked event per device it revoked.
	DeleteExtension(ctx context.Context, tenant, id uuid.UUID, at time.Time, audit auth.AuditEntry) error

	CreateDevice(ctx context.Context, d Device, audit auth.AuditEntry) error
	Device(ctx context.Context, tenant, id uuid.UUID) (Device, error)
	ListDevicesByExtension(ctx context.Context, tenant, extension uuid.UUID, before *uuid.UUID, limit int) ([]Device, error)
	// UpdateDevice saves d if its stored version is still d.Version and
	// returns it with the new version (ErrVersionChanged otherwise).
	UpdateDevice(ctx context.Context, d Device, audit auth.AuditEntry) (Device, error)
	// RevokeDevice disables a device (a no-op if it already is) and writes
	// the audit entry in the same transaction. It returns the device as it
	// now stands.
	RevokeDevice(ctx context.Context, tenant, id uuid.UUID, at time.Time, audit auth.AuditEntry) (Device, error)
}
