// Package trunk is what the API's trunk, DID, WireGuard profile and call
// permission level endpoints do (docs/TRUNKS.md): plain CRUD, sealed
// secrets (ADR-030) and the ADR-023 unencrypted-trunk confirmation. Asterisk
// isn't rendered from any of this yet — that's Phase 1D step 3
// (pjsip_trunks.conf) and step 5 (WireGuard).
package trunk

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
)

// Trunk kinds (docs/TRUNKS.md §3).
const (
	KindRegistration    = "registration"
	KindIPAuthenticated = "ip_authenticated"
	KindLANPeer         = "lan_peer"
)

// Kinds lists every trunk kind.
var Kinds = []string{KindRegistration, KindIPAuthenticated, KindLANPeer}

// Transports a trunk can use. Only TransportTLS needs no ADR-023 confirmation.
const (
	TransportTLS = "tls"
	TransportTCP = "tcp"
	TransportUDP = "udp"
)

// Transports lists every transport.
var Transports = []string{TransportTLS, TransportTCP, TransportUDP}

// Media encryption. Only MediaSRTP needs no ADR-023 confirmation.
const (
	MediaSRTP = "srtp"
	MediaNone = "none"
)

// MediaEncryptions lists every media encryption choice.
var MediaEncryptions = []string{MediaSRTP, MediaNone}

// Certificate trust (docs/TRUNKS.md §6, ADR-045).
const (
	CertPublic = "public"
	CertPinned = "pinned"
)

// CertTrusts lists every certificate trust choice.
var CertTrusts = []string{CertPublic, CertPinned}

// How a trunk expects outgoing numbers to be written (docs/TRUNKS.md §5).
const (
	DialE164  = "e164"
	Dial00    = "00_prefix"
	DialLocal = "local"
)

// DialFormats lists every dial format.
var DialFormats = []string{DialE164, Dial00, DialLocal}

// Codecs Asterisk can offer a trunk (the menuselect set built in 1B/1C).
var Codecs = []string{"ulaw", "alaw", "g722", "opus"}

// AllowedCategories are the numbering.Category values a call permission
// level can name. Emergency and invalid are never gated (numbering_route
// checks emergency before a level is even read).
var AllowedCategories = []string{"landline", "mobile", "national", "shared_cost", "toll_free", "premium", "international", "service"}

var (
	ErrNotFound       = errors.New("not found")
	ErrVersionChanged = errors.New("changed since it was read")
	ErrDuplicate      = errors.New("already exists")
	// ErrInUse is a delete refused because something else still references
	// the row (a trunk on a WireGuard profile, an extension on a
	// permission level).
	ErrInUse = errors.New("in use")
)

// Trunk is a phone line to a provider or another phone system
// (docs/TRUNKS.md §3). Password is sealed (ADR-030) under sealID(ID); the
// API never returns it.
type Trunk struct {
	ID, TenantID           uuid.UUID
	Name                   string
	Kind                   string
	Template               string
	Host                   string
	Port                   int
	Transport              string
	MediaEncryption        string
	CertTrust              string
	PinnedCertificate      string
	Username               string
	PasswordEnc            []byte
	DialFormat             string
	Codecs                 []string
	CallerIDNumber         string
	MaxCalls               int
	WireGuardProfileID     *uuid.UUID
	OutboundPriority       *int
	UnencryptedConfirmedBy string
	UnencryptedConfirmedAt *time.Time
	Enabled                bool
	Version                int
	CreatedAt, UpdatedAt   time.Time
	// Status is what Asterisk last said about it (internal/trunkstatus's
	// Status constants), kept by the Monitor; not a setting.
	Status       string
	StatusDetail string
	StatusSince  *time.Time
}

// Unencrypted reports whether calls on t travel without TLS or without SRTP
// media (docs/TRUNKS.md §6): the ADR-023 warning applies, unless a
// WireGuard tunnel already encrypts everything (ADR-024).
func (t Trunk) Unencrypted() bool {
	return (t.Transport != TransportTLS || t.MediaEncryption != MediaSRTP) && t.WireGuardProfileID == nil
}

func sealID(id uuid.UUID) string { return "trunk:" + id.String() }

// DID is a phone number a trunk owns (docs/TRUNKS.md §5). ExtensionID nil:
// a call to it hears "not in use".
type DID struct {
	ID, TenantID, TrunkID uuid.UUID
	Number                string
	Label                 string
	ExtensionID           *uuid.UUID
	Version               int
	CreatedAt, UpdatedAt  time.Time
}

// WireGuardProfile is a tunnel trunks can be reached through
// (docs/TRUNKS.md §7, ADR-046). PrivateKeyEnc and PresharedKeyEnc are
// sealed (ADR-030); the API never returns them. PublicKey is derived from
// the private key and isn't secret.
type WireGuardProfile struct {
	ID, TenantID         uuid.UUID
	Name                 string
	Address              string
	PrivateKeyEnc        []byte
	PublicKey            string
	PeerPublicKey        string
	PeerEndpointHost     string
	PeerEndpointPort     int
	PresharedKeyEnc      []byte
	PersistentKeepalive  *int
	Version              int
	CreatedAt, UpdatedAt time.Time
	// Status is the tunnel's state as linx-wireguard last reported it
	// (wgconf's State constants), kept by the Monitor; not a setting.
	Status          string
	StatusDetail    string
	StatusSince     *time.Time
	LastHandshakeAt *time.Time
	// Notes are said once, when the profile is created (an imported
	// configuration's AllowedIPs narrowed, say); not stored.
	Notes []string
}

func wgSealID(id uuid.UUID) string          { return "wireguard_profile:" + id.String() }
func wgPresharedSealID(id uuid.UUID) string { return "wireguard_profile_psk:" + id.String() }

// CallPermissionLevel is what a group of extensions may dial out
// (docs/TRUNKS.md §5).
type CallPermissionLevel struct {
	ID, TenantID         uuid.UUID
	Name                 string
	AllowedCategories    []string
	WithholdCallerID     bool
	Version              int
	CreatedAt, UpdatedAt time.Time
}

// Store is the database access trunks, DIDs, WireGuard profiles and call
// permission levels need (internal/store implements it).
type Store interface {
	CreateTrunk(ctx context.Context, t Trunk, audit auth.AuditEntry) error
	Trunk(ctx context.Context, tenant, id uuid.UUID) (Trunk, error)
	ListTrunks(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]Trunk, error)
	// UpdateTrunk saves t if its stored version is still t.Version and
	// returns it with the new version (ErrVersionChanged otherwise).
	UpdateTrunk(ctx context.Context, t Trunk, audit auth.AuditEntry) (Trunk, error)
	// DeleteTrunk deletes the trunk and every DID it owns, in one
	// transaction, and writes the audit entry.
	DeleteTrunk(ctx context.Context, tenant, id uuid.UUID, at time.Time, audit auth.AuditEntry) error
	// SetOutboundOrder sets the outbound priority of exactly the trunks in
	// order (1-based, tried in that sequence) and clears it for every other
	// enabled trunk of the tenant, in one transaction. It returns every
	// trunk of the tenant as it now stands.
	SetOutboundOrder(ctx context.Context, tenant uuid.UUID, order []uuid.UUID, at time.Time, audit auth.AuditEntry) ([]Trunk, error)

	CreateDID(ctx context.Context, d DID, audit auth.AuditEntry) error
	DID(ctx context.Context, tenant, id uuid.UUID) (DID, error)
	ListDIDsByTrunk(ctx context.Context, tenant, trunkID uuid.UUID, before *uuid.UUID, limit int) ([]DID, error)
	ListDIDs(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]DID, error)
	UpdateDID(ctx context.Context, d DID, audit auth.AuditEntry) (DID, error)
	DeleteDID(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error

	CreateWireGuardProfile(ctx context.Context, w WireGuardProfile, audit auth.AuditEntry) error
	WireGuardProfile(ctx context.Context, tenant, id uuid.UUID) (WireGuardProfile, error)
	ListWireGuardProfiles(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]WireGuardProfile, error)
	UpdateWireGuardProfile(ctx context.Context, w WireGuardProfile, audit auth.AuditEntry) (WireGuardProfile, error)
	// DeleteWireGuardProfile fails with ErrInUse if a trunk still uses it.
	DeleteWireGuardProfile(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error

	CreateCallPermissionLevel(ctx context.Context, l CallPermissionLevel, audit auth.AuditEntry) error
	CallPermissionLevel(ctx context.Context, tenant, id uuid.UUID) (CallPermissionLevel, error)
	ListCallPermissionLevels(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]CallPermissionLevel, error)
	UpdateCallPermissionLevel(ctx context.Context, l CallPermissionLevel, audit auth.AuditEntry) (CallPermissionLevel, error)
	// DeleteCallPermissionLevel fails with ErrInUse if an extension still
	// has it assigned.
	DeleteCallPermissionLevel(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error

	// The "unusual calling abroad" alert's limits (docs/TRUNKS.md §9).
	InternationalAlertLimits(ctx context.Context) (minutes, calls int, err error)
	SetInternationalAlertLimits(ctx context.Context, minutes, calls int, audit auth.AuditEntry) error
}

// OpenPassword opens t's sealed provider password ("" if it has none). Only
// the control plane's pjsip_trunks.conf render needs it (ADR-043); the API
// never returns it.
func OpenPassword(sealer *dbsecret.Sealer, t Trunk) (string, error) {
	if len(t.PasswordEnc) == 0 {
		return "", nil
	}
	b, err := sealer.Open(sealID(t.ID), t.PasswordEnc)
	return string(b), err
}

// OpenWireGuardKeys opens w's private key and (if it has one) preshared
// key, for rendering its tunnel (internal/trunkconf).
func OpenWireGuardKeys(sealer *dbsecret.Sealer, w WireGuardProfile) (private, preshared string, err error) {
	b, err := sealer.Open(wgSealID(w.ID), w.PrivateKeyEnc)
	if err != nil {
		return "", "", err
	}
	if len(w.PresharedKeyEnc) > 0 {
		p, err := sealer.Open(wgPresharedSealID(w.ID), w.PresharedKeyEnc)
		if err != nil {
			return "", "", err
		}
		preshared = string(p)
	}
	return string(b), preshared, nil
}

// Endpoint is the name Asterisk knows t by (its PJSIP endpoint, AOR, auth,
// registration and identify objects; migration 0018's functions build the
// same name).
func (t Trunk) Endpoint() string { return "trunk-" + t.ID.String() }
