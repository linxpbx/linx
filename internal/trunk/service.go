package trunk

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
)

// Service is what the API's trunk, DID, WireGuard profile and call
// permission level endpoints do. Caller mistakes come back as
// *apihttp.Error.
type Service struct {
	Store  Store
	Sealer *dbsecret.Sealer
	Now    func() time.Time
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

// ETag is a trunk, DID, WireGuard profile or call permission level version
// as an HTTP entity tag.
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

func principal(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return auth.Principal{}, errNoPrincipal
	}
	return p, nil
}

func checkName(name string) error {
	n := len([]rune(name))
	if n < 1 || n > 100 {
		return invalid("name_invalid", "Give it a name of 1 to 100 characters.")
	}
	return nil
}

// --- Trunks ---------------------------------------------------------------

var errTrunkChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
	Detail: "This trunk was changed since you read it. Fetch it again and retry."}

var errUnencryptedConfirmationRequired = &apihttp.Error{Status: http.StatusUnprocessableEntity, Code: "unencrypted_confirmation_required",
	Detail: "Calls to and from this trunk can be listened to on the way. Send confirm_unencrypted: true to save it anyway."}

func checkHost(host string) error {
	if l := len(host); l < 1 || l > 255 || strings.ContainsAny(host, " \t\n") {
		return invalid("host_invalid", "Give it an address of 1 to 255 characters, with no spaces.")
	}
	return nil
}

func checkPort(port int) error {
	if port < 1 || port > 65535 {
		return invalid("port_invalid", "Give it a port between 1 and 65535.")
	}
	return nil
}

func oneOf(choices []string, value string) bool { return slices.Contains(choices, value) }

func checkCodecs(codecs []string) ([]string, error) {
	if len(codecs) == 0 {
		return []string{"alaw", "ulaw"}, nil
	}
	out := make([]string, 0, len(codecs))
	for _, c := range codecs {
		if !oneOf(Codecs, c) {
			return nil, invalid("codec_unsupported", fmt.Sprintf("%q isn't a codec this Linx offers. Choices: %s.", c, strings.Join(Codecs, ", ")))
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out, nil
}

func checkMaxCalls(n int) error {
	if n < 1 || n > 500 {
		return invalid("max_calls_invalid", "Give it a call limit between 1 and 500.")
	}
	return nil
}

// TrunkInput is a new trunk.
type TrunkInput struct {
	Name               string
	Kind               string
	Template           string
	Host               string
	Port               *int
	Transport          string
	MediaEncryption    string
	CertTrust          string
	PinnedCertificate  string
	Username           string
	Password           string
	DialFormat         string
	Codecs             []string
	CallerIDNumber     string
	MaxCalls           *int
	WireGuardProfileID *uuid.UUID
	ConfirmUnencrypted bool
	Enabled            *bool
}

func defaultOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// checkTrunkFields validates and normalises the fields shared by create and
// update, mutating t in place. It doesn't touch t.PasswordEnc.
func checkTrunkFields(t *Trunk) error {
	if err := checkName(t.Name); err != nil {
		return err
	}
	if !oneOf(Kinds, t.Kind) {
		return invalid("kind_invalid", "Choose a trunk kind: "+strings.Join(Kinds, ", ")+".")
	}
	if err := checkHost(t.Host); err != nil {
		return err
	}
	if err := checkPort(t.Port); err != nil {
		return err
	}
	if !oneOf(Transports, t.Transport) {
		return invalid("transport_invalid", "Choose a transport: "+strings.Join(Transports, ", ")+".")
	}
	if !oneOf(MediaEncryptions, t.MediaEncryption) {
		return invalid("media_encryption_invalid", "Choose srtp or none.")
	}
	if !oneOf(CertTrusts, t.CertTrust) {
		return invalid("cert_trust_invalid", "Choose public or pinned.")
	}
	if t.CertTrust == CertPinned && strings.TrimSpace(t.PinnedCertificate) == "" {
		return invalid("pinned_certificate_required", "Paste the certificate or CA you compared and approved.")
	}
	if t.CertTrust == CertPublic {
		t.PinnedCertificate = ""
	}
	if !oneOf(DialFormats, t.DialFormat) {
		return invalid("dial_format_invalid", "Choose how numbers are dialled: "+strings.Join(DialFormats, ", ")+".")
	}
	if err := checkMaxCalls(t.MaxCalls); err != nil {
		return err
	}
	codecs, err := checkCodecs(t.Codecs)
	if err != nil {
		return err
	}
	t.Codecs = codecs
	return nil
}

// applyUnencrypted resolves the ADR-023 confirmation once every other field
// is set on t: if the result is unencrypted and wasn't already confirmed,
// confirmed must be true, and the confirmation is stamped with actor and
// now; otherwise (encrypted, or through WireGuard) any stale confirmation
// is cleared.
func applyUnencrypted(t *Trunk, confirmed bool, actor string, now time.Time) error {
	if !t.Unencrypted() {
		t.UnencryptedConfirmedBy, t.UnencryptedConfirmedAt = "", nil
		return nil
	}
	if t.UnencryptedConfirmedAt != nil {
		return nil // confirmed before and still unencrypted the same way
	}
	if !confirmed {
		return errUnencryptedConfirmationRequired
	}
	t.UnencryptedConfirmedBy, t.UnencryptedConfirmedAt = actor, &now
	return nil
}

// CreateTrunk adds a trunk.
func (s *Service) CreateTrunk(ctx context.Context, in TrunkInput) (Trunk, error) {
	p, err := principal(ctx)
	if err != nil {
		return Trunk{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Trunk{}, err
	}
	now := s.Now().UTC()
	port := 5061
	if in.Port != nil {
		port = *in.Port
	}
	maxCalls := 4
	if in.MaxCalls != nil {
		maxCalls = *in.MaxCalls
	}
	t := Trunk{
		ID: id, TenantID: p.TenantID, Name: in.Name, Kind: in.Kind, Template: in.Template,
		Host: in.Host, Port: port, Transport: defaultOr(in.Transport, TransportTLS),
		MediaEncryption: defaultOr(in.MediaEncryption, MediaSRTP), CertTrust: defaultOr(in.CertTrust, CertPublic),
		PinnedCertificate: in.PinnedCertificate, Username: in.Username, DialFormat: defaultOr(in.DialFormat, DialE164),
		Codecs: in.Codecs, CallerIDNumber: in.CallerIDNumber, MaxCalls: maxCalls,
		WireGuardProfileID: in.WireGuardProfileID, Enabled: in.Enabled == nil || *in.Enabled,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := checkTrunkFields(&t); err != nil {
		return Trunk{}, err
	}
	if err := applyUnencrypted(&t, in.ConfirmUnencrypted, p.Actor(), now); err != nil {
		return Trunk{}, err
	}
	if in.Password != "" {
		enc, err := s.Sealer.Seal(sealID(id), []byte(in.Password))
		if err != nil {
			return Trunk{}, err
		}
		t.PasswordEnc = enc
	}
	_, a, err := audit(ctx, "trunk.create", "trunk:"+id.String())
	if err != nil {
		return Trunk{}, err
	}
	a.Detail = map[string]any{"name": t.Name, "kind": t.Kind, "host": t.Host, "enabled": t.Enabled}
	if err := s.Store.CreateTrunk(ctx, t, a); err != nil {
		if errors.Is(err, ErrDuplicate) {
			return Trunk{}, &apihttp.Error{Status: http.StatusConflict, Code: "name_duplicate",
				Detail: fmt.Sprintf("A trunk named %q already exists.", t.Name)}
		}
		if errors.Is(err, ErrNotFound) {
			return Trunk{}, invalid("wireguard_profile_not_found", "That WireGuard profile doesn't exist.")
		}
		return Trunk{}, err
	}
	return t, nil
}

// GetTrunk returns one of the caller's trunks.
func (s *Service) GetTrunk(ctx context.Context, id uuid.UUID) (Trunk, error) {
	p, err := principal(ctx)
	if err != nil {
		return Trunk{}, err
	}
	t, err := s.Store.Trunk(ctx, p.TenantID, id)
	if errors.Is(err, ErrNotFound) {
		return Trunk{}, notFound("trunk")
	}
	return t, err
}

// ListTrunks returns a page of the caller's trunks, newest first.
func (s *Service) ListTrunks(ctx context.Context, before *uuid.UUID, limit int) ([]Trunk, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	return s.Store.ListTrunks(ctx, p.TenantID, before, limit)
}

// TrunkPatch is a JSON Merge Patch of a trunk; nil fields stay as they are.
// Password, when non-nil, reseals it; an empty string removes it.
// ConfirmUnencrypted must be true in the same request that makes the trunk
// unencrypted for the first time (or again, after it wasn't).
type TrunkPatch struct {
	Name               *string
	Host               *string
	Port               *int
	Transport          *string
	MediaEncryption    *string
	CertTrust          *string
	PinnedCertificate  *string
	Username           *string
	Password           *string
	DialFormat         *string
	Codecs             []string
	CallerIDNumber     *string
	MaxCalls           *int
	WireGuardProfileID **uuid.UUID
	ConfirmUnencrypted *bool
	Enabled            *bool
}

// UpdateTrunk applies patch. ifMatch, when not empty, must match the
// trunk's current ETag (412 otherwise).
func (s *Service) UpdateTrunk(ctx context.Context, id uuid.UUID, patch TrunkPatch, ifMatch string) (Trunk, error) {
	p, err := principal(ctx)
	if err != nil {
		return Trunk{}, err
	}
	t, err := s.GetTrunk(ctx, id)
	if err != nil {
		return Trunk{}, err
	}
	if ifMatch != "" && !matchETag(ifMatch, t.Version) {
		return Trunk{}, errTrunkChanged
	}
	changes := map[string]any{}
	if patch.Name != nil {
		t.Name = *patch.Name
		changes["name"] = t.Name
	}
	if patch.Host != nil {
		t.Host = *patch.Host
		changes["host"] = t.Host
	}
	if patch.Port != nil {
		t.Port = *patch.Port
		changes["port"] = t.Port
	}
	if patch.Transport != nil {
		t.Transport = *patch.Transport
		changes["transport"] = t.Transport
	}
	if patch.MediaEncryption != nil {
		t.MediaEncryption = *patch.MediaEncryption
		changes["media_encryption"] = t.MediaEncryption
	}
	if patch.CertTrust != nil {
		t.CertTrust = *patch.CertTrust
		changes["cert_trust"] = t.CertTrust
	}
	if patch.PinnedCertificate != nil {
		t.PinnedCertificate = *patch.PinnedCertificate
	}
	if patch.Username != nil {
		t.Username = *patch.Username
		changes["username"] = t.Username
	}
	if patch.DialFormat != nil {
		t.DialFormat = *patch.DialFormat
		changes["dial_format"] = t.DialFormat
	}
	if patch.Codecs != nil {
		t.Codecs = patch.Codecs
	}
	if patch.CallerIDNumber != nil {
		t.CallerIDNumber = *patch.CallerIDNumber
		changes["caller_id_number"] = t.CallerIDNumber
	}
	if patch.MaxCalls != nil {
		t.MaxCalls = *patch.MaxCalls
		changes["max_calls"] = t.MaxCalls
	}
	if patch.WireGuardProfileID != nil {
		t.WireGuardProfileID = *patch.WireGuardProfileID
	}
	if patch.Enabled != nil {
		t.Enabled = *patch.Enabled
		changes["enabled"] = t.Enabled
	}
	if err := checkTrunkFields(&t); err != nil {
		return Trunk{}, err
	}
	now := s.Now().UTC()
	confirmed := patch.ConfirmUnencrypted != nil && *patch.ConfirmUnencrypted
	if err := applyUnencrypted(&t, confirmed, p.Actor(), now); err != nil {
		return Trunk{}, err
	}
	if patch.Password != nil {
		if *patch.Password == "" {
			t.PasswordEnc = nil
		} else {
			enc, err := s.Sealer.Seal(sealID(t.ID), []byte(*patch.Password))
			if err != nil {
				return Trunk{}, err
			}
			t.PasswordEnc = enc
		}
	}
	t.UpdatedAt = now
	_, a, err := audit(ctx, "trunk.update", "trunk:"+id.String())
	if err != nil {
		return Trunk{}, err
	}
	a.Detail = changes
	updated, err := s.Store.UpdateTrunk(ctx, t, a)
	if errors.Is(err, ErrVersionChanged) {
		return Trunk{}, errTrunkChanged
	}
	if errors.Is(err, ErrNotFound) {
		return Trunk{}, notFound("trunk")
	}
	if errors.Is(err, ErrDuplicate) {
		return Trunk{}, &apihttp.Error{Status: http.StatusConflict, Code: "name_duplicate",
			Detail: fmt.Sprintf("A trunk named %q already exists.", t.Name)}
	}
	return updated, err
}

// DeleteTrunk removes a trunk and every DID it owns.
func (s *Service) DeleteTrunk(ctx context.Context, id uuid.UUID) error {
	_, a, err := audit(ctx, "trunk.delete", "trunk:"+id.String())
	if err != nil {
		return err
	}
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	err = s.Store.DeleteTrunk(ctx, p.TenantID, id, s.Now().UTC(), a)
	if errors.Is(err, ErrNotFound) {
		return notFound("trunk")
	}
	return err
}

// SetOutboundOrder sets which trunks are tried, in order, for outgoing
// calls (docs/TRUNKS.md §5): order[0] is primary, order[1] the backup, and
// so on. Trunks not named stop being used for outgoing calls (their DIDs
// still ring in). Every id must be one of the caller's trunks.
func (s *Service) SetOutboundOrder(ctx context.Context, order []uuid.UUID) ([]Trunk, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range order {
		if seen[id] {
			return nil, invalid("outbound_order_duplicate", "Each trunk can appear at most once in the order.")
		}
		seen[id] = true
	}
	_, a, err := audit(ctx, "outbound_routing.update", "")
	if err != nil {
		return nil, err
	}
	a.Detail = map[string]any{"order": order}
	trunks, err := s.Store.SetOutboundOrder(ctx, p.TenantID, order, s.Now().UTC(), a)
	if errors.Is(err, ErrNotFound) {
		return nil, invalid("trunk_not_found", "One of those trunks doesn't exist.")
	}
	return trunks, err
}

// --- DIDs -------------------------------------------------------------

var errDIDChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
	Detail: "This number was changed since you read it. Fetch it again and retry."}

func checkDIDNumber(number string) error {
	n := strings.TrimPrefix(number, "+")
	if len(n) < 2 || len(n) > 20 {
		return invalid("number_invalid", "Give it a number of 2 to 20 digits, optionally starting with +.")
	}
	for _, r := range n {
		if r < '0' || r > '9' {
			return invalid("number_invalid", "A DID is digits only, optionally starting with +.")
		}
	}
	return nil
}

func checkLabel(label string) error {
	if len([]rune(label)) > 100 {
		return invalid("label_invalid", "Keep the label to 100 characters or fewer.")
	}
	return nil
}

// DIDInput is a new DID under a trunk.
type DIDInput struct {
	Number      string
	Label       string
	ExtensionID *uuid.UUID
}

// CreateDID adds a DID to trunkID.
func (s *Service) CreateDID(ctx context.Context, trunkID uuid.UUID, in DIDInput) (DID, error) {
	t, err := s.GetTrunk(ctx, trunkID)
	if err != nil {
		return DID{}, err
	}
	if err := checkDIDNumber(in.Number); err != nil {
		return DID{}, err
	}
	if err := checkLabel(in.Label); err != nil {
		return DID{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return DID{}, err
	}
	now := s.Now().UTC()
	d := DID{ID: id, TenantID: t.TenantID, TrunkID: t.ID, Number: in.Number, Label: in.Label,
		ExtensionID: in.ExtensionID, Version: 1, CreatedAt: now, UpdatedAt: now}
	_, a, err := audit(ctx, "trunk_did.create", "trunk_did:"+id.String())
	if err != nil {
		return DID{}, err
	}
	a.Detail = map[string]any{"trunk_id": t.ID, "number": d.Number}
	if err := s.Store.CreateDID(ctx, d, a); err != nil {
		if errors.Is(err, ErrDuplicate) {
			return DID{}, &apihttp.Error{Status: http.StatusConflict, Code: "number_duplicate",
				Detail: fmt.Sprintf("%s already belongs to a trunk.", d.Number)}
		}
		if errors.Is(err, ErrNotFound) {
			return DID{}, invalid("extension_not_found", "That extension doesn't exist.")
		}
		return DID{}, err
	}
	return d, nil
}

// GetDID returns one of the caller's DIDs.
func (s *Service) GetDID(ctx context.Context, id uuid.UUID) (DID, error) {
	p, err := principal(ctx)
	if err != nil {
		return DID{}, err
	}
	d, err := s.Store.DID(ctx, p.TenantID, id)
	if errors.Is(err, ErrNotFound) {
		return DID{}, notFound("phone number")
	}
	return d, err
}

// ListDIDs returns a page of trunkID's DIDs, newest first.
func (s *Service) ListDIDs(ctx context.Context, trunkID uuid.UUID, before *uuid.UUID, limit int) ([]DID, error) {
	t, err := s.GetTrunk(ctx, trunkID)
	if err != nil {
		return nil, err
	}
	return s.Store.ListDIDsByTrunk(ctx, t.TenantID, t.ID, before, limit)
}

// ListInboundRoutes returns a page of every DID of the caller's tenant,
// across every trunk, newest first (docs/TRUNKS.md §10).
func (s *Service) ListInboundRoutes(ctx context.Context, before *uuid.UUID, limit int) ([]DID, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	return s.Store.ListDIDs(ctx, p.TenantID, before, limit)
}

// DIDPatch is a JSON Merge Patch of a DID. ExtensionID, when non-nil and
// empty, clears the route (the number hears "not in use").
type DIDPatch struct {
	Label       *string
	ExtensionID *string
}

// UpdateDID applies patch. ifMatch, when not empty, must match the DID's
// current ETag (412 otherwise).
func (s *Service) UpdateDID(ctx context.Context, id uuid.UUID, patch DIDPatch, ifMatch string) (DID, error) {
	d, err := s.GetDID(ctx, id)
	if err != nil {
		return DID{}, err
	}
	if ifMatch != "" && !matchETag(ifMatch, d.Version) {
		return DID{}, errDIDChanged
	}
	changes := map[string]any{}
	if patch.Label != nil {
		if err := checkLabel(*patch.Label); err != nil {
			return DID{}, err
		}
		d.Label = *patch.Label
		changes["label"] = d.Label
	}
	if patch.ExtensionID != nil {
		if *patch.ExtensionID == "" {
			d.ExtensionID = nil
		} else {
			extID, err := uuid.Parse(*patch.ExtensionID)
			if err != nil {
				return DID{}, invalid("extension_id_invalid", "That isn't a valid extension id.")
			}
			d.ExtensionID = &extID
		}
		changes["extension_id"] = d.ExtensionID
	}
	d.UpdatedAt = s.Now().UTC()
	_, a, err := audit(ctx, "trunk_did.update", "trunk_did:"+id.String())
	if err != nil {
		return DID{}, err
	}
	a.Detail = changes
	updated, err := s.Store.UpdateDID(ctx, d, a)
	if errors.Is(err, ErrVersionChanged) {
		return DID{}, errDIDChanged
	}
	if errors.Is(err, ErrNotFound) {
		if patch.ExtensionID != nil && *patch.ExtensionID != "" {
			return DID{}, invalid("extension_not_found", "That extension doesn't exist.")
		}
		return DID{}, notFound("phone number")
	}
	return updated, err
}

// DeleteDID removes a DID; the number can be added to another trunk after.
func (s *Service) DeleteDID(ctx context.Context, id uuid.UUID) error {
	_, a, err := audit(ctx, "trunk_did.delete", "trunk_did:"+id.String())
	if err != nil {
		return err
	}
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	err = s.Store.DeleteDID(ctx, p.TenantID, id, a)
	if errors.Is(err, ErrNotFound) {
		return notFound("phone number")
	}
	return err
}

// --- WireGuard profiles ----------------------------------------------

var errWireGuardProfileChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
	Detail: "This WireGuard profile was changed since you read it. Fetch it again and retry."}

var errWireGuardProfileInUse = &apihttp.Error{Status: http.StatusConflict, Code: "wireguard_profile_in_use",
	Detail: "A trunk still connects through this profile. Move it to Internet (or another profile) first."}

// WireGuardProfileInput is a new WireGuard profile, either from an imported
// wg-quick config (Config) or from its fields directly.
type WireGuardProfileInput struct {
	Name   string
	Config string // a whole wg-quick [Interface]/[Peer] file; takes priority over the fields below
	Fields WireGuardFields
}

// WireGuardFields are a profile's fields when not importing a raw config.
type WireGuardFields struct {
	PrivateKey          string
	Address             string
	PeerPublicKey       string
	PeerEndpointHost    string
	PeerEndpointPort    *int
	PresharedKey        string
	PersistentKeepalive *int
}

// CreateWireGuardProfile adds a profile, importing in.Config if given.
func (s *Service) CreateWireGuardProfile(ctx context.Context, in WireGuardProfileInput) (WireGuardProfile, error) {
	p, err := principal(ctx)
	if err != nil {
		return WireGuardProfile{}, err
	}
	if err := checkName(in.Name); err != nil {
		return WireGuardProfile{}, err
	}
	fields := in.Fields
	if strings.TrimSpace(in.Config) != "" {
		parsed, err := parseWireGuardConf(in.Config)
		if err != nil {
			return WireGuardProfile{}, err
		}
		fields = parsed
	}
	pub, err := checkPrivateKey(fields.PrivateKey)
	if err != nil {
		return WireGuardProfile{}, err
	}
	if err := checkWireGuardKey("peer_public_key", fields.PeerPublicKey); err != nil {
		return WireGuardProfile{}, err
	}
	if strings.TrimSpace(fields.Address) == "" {
		return WireGuardProfile{}, invalid("address_required", "Give the tunnel address this end uses (e.g. 10.6.0.2/32).")
	}
	if err := checkHost(fields.PeerEndpointHost); err != nil {
		return WireGuardProfile{}, err
	}
	port := 51820
	if fields.PeerEndpointPort != nil {
		port = *fields.PeerEndpointPort
	}
	if err := checkPort(port); err != nil {
		return WireGuardProfile{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return WireGuardProfile{}, err
	}
	now := s.Now().UTC()
	w := WireGuardProfile{
		ID: id, TenantID: p.TenantID, Name: in.Name, Address: fields.Address, PublicKey: pub,
		PeerPublicKey: fields.PeerPublicKey, PeerEndpointHost: fields.PeerEndpointHost, PeerEndpointPort: port,
		PersistentKeepalive: fields.PersistentKeepalive, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	enc, err := s.Sealer.Seal(wgSealID(id), []byte(fields.PrivateKey))
	if err != nil {
		return WireGuardProfile{}, err
	}
	w.PrivateKeyEnc = enc
	if fields.PresharedKey != "" {
		if err := checkWireGuardKey("preshared_key", fields.PresharedKey); err != nil {
			return WireGuardProfile{}, err
		}
		enc, err := s.Sealer.Seal(wgPresharedSealID(id), []byte(fields.PresharedKey))
		if err != nil {
			return WireGuardProfile{}, err
		}
		w.PresharedKeyEnc = enc
	}
	_, a, err := audit(ctx, "wireguard_profile.create", "wireguard_profile:"+id.String())
	if err != nil {
		return WireGuardProfile{}, err
	}
	a.Detail = map[string]any{"name": w.Name, "peer_endpoint_host": w.PeerEndpointHost}
	if err := s.Store.CreateWireGuardProfile(ctx, w, a); err != nil {
		if errors.Is(err, ErrDuplicate) {
			return WireGuardProfile{}, &apihttp.Error{Status: http.StatusConflict, Code: "name_duplicate",
				Detail: fmt.Sprintf("A WireGuard profile named %q already exists.", w.Name)}
		}
		return WireGuardProfile{}, err
	}
	return w, nil
}

// GetWireGuardProfile returns one of the caller's profiles.
func (s *Service) GetWireGuardProfile(ctx context.Context, id uuid.UUID) (WireGuardProfile, error) {
	p, err := principal(ctx)
	if err != nil {
		return WireGuardProfile{}, err
	}
	w, err := s.Store.WireGuardProfile(ctx, p.TenantID, id)
	if errors.Is(err, ErrNotFound) {
		return WireGuardProfile{}, notFound("WireGuard profile")
	}
	return w, err
}

// ListWireGuardProfiles returns a page of the caller's profiles, newest first.
func (s *Service) ListWireGuardProfiles(ctx context.Context, before *uuid.UUID, limit int) ([]WireGuardProfile, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	return s.Store.ListWireGuardProfiles(ctx, p.TenantID, before, limit)
}

// WireGuardProfilePatch is a JSON Merge Patch of a profile's non-secret
// fields; reissue the profile to change its keys.
type WireGuardProfilePatch struct {
	Name                *string
	PeerEndpointHost    *string
	PeerEndpointPort    *int
	PersistentKeepalive *int
}

// UpdateWireGuardProfile applies patch. ifMatch, when not empty, must match
// the profile's current ETag (412 otherwise).
func (s *Service) UpdateWireGuardProfile(ctx context.Context, id uuid.UUID, patch WireGuardProfilePatch, ifMatch string) (WireGuardProfile, error) {
	w, err := s.GetWireGuardProfile(ctx, id)
	if err != nil {
		return WireGuardProfile{}, err
	}
	if ifMatch != "" && !matchETag(ifMatch, w.Version) {
		return WireGuardProfile{}, errWireGuardProfileChanged
	}
	changes := map[string]any{}
	if patch.Name != nil {
		if err := checkName(*patch.Name); err != nil {
			return WireGuardProfile{}, err
		}
		w.Name = *patch.Name
		changes["name"] = w.Name
	}
	if patch.PeerEndpointHost != nil {
		if err := checkHost(*patch.PeerEndpointHost); err != nil {
			return WireGuardProfile{}, err
		}
		w.PeerEndpointHost = *patch.PeerEndpointHost
		changes["peer_endpoint_host"] = w.PeerEndpointHost
	}
	if patch.PeerEndpointPort != nil {
		if err := checkPort(*patch.PeerEndpointPort); err != nil {
			return WireGuardProfile{}, err
		}
		w.PeerEndpointPort = *patch.PeerEndpointPort
		changes["peer_endpoint_port"] = w.PeerEndpointPort
	}
	if patch.PersistentKeepalive != nil {
		w.PersistentKeepalive = patch.PersistentKeepalive
	}
	w.UpdatedAt = s.Now().UTC()
	_, a, err := audit(ctx, "wireguard_profile.update", "wireguard_profile:"+id.String())
	if err != nil {
		return WireGuardProfile{}, err
	}
	a.Detail = changes
	updated, err := s.Store.UpdateWireGuardProfile(ctx, w, a)
	if errors.Is(err, ErrVersionChanged) {
		return WireGuardProfile{}, errWireGuardProfileChanged
	}
	if errors.Is(err, ErrNotFound) {
		return WireGuardProfile{}, notFound("WireGuard profile")
	}
	if errors.Is(err, ErrDuplicate) {
		return WireGuardProfile{}, &apihttp.Error{Status: http.StatusConflict, Code: "name_duplicate",
			Detail: fmt.Sprintf("A WireGuard profile named %q already exists.", w.Name)}
	}
	return updated, err
}

// DeleteWireGuardProfile removes a profile not used by any trunk.
func (s *Service) DeleteWireGuardProfile(ctx context.Context, id uuid.UUID) error {
	_, a, err := audit(ctx, "wireguard_profile.delete", "wireguard_profile:"+id.String())
	if err != nil {
		return err
	}
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	err = s.Store.DeleteWireGuardProfile(ctx, p.TenantID, id, a)
	if errors.Is(err, ErrNotFound) {
		return notFound("WireGuard profile")
	}
	if errors.Is(err, ErrInUse) {
		return errWireGuardProfileInUse
	}
	return err
}

// --- Call permission levels --------------------------------------------

var errCallPermissionLevelChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
	Detail: "This permission level was changed since you read it. Fetch it again and retry."}

var errCallPermissionLevelInUse = &apihttp.Error{Status: http.StatusConflict, Code: "permission_level_in_use",
	Detail: "An extension still has this level assigned. Move it to another level first."}

func checkAllowedCategories(categories []string) ([]string, error) {
	out := make([]string, 0, len(categories))
	for _, c := range categories {
		if !oneOf(AllowedCategories, c) {
			return nil, invalid("category_unknown", fmt.Sprintf("%q isn't a call category. Choices: %s.", c, strings.Join(AllowedCategories, ", ")))
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out, nil
}

// CallPermissionLevelInput is a new permission level.
type CallPermissionLevelInput struct {
	Name              string
	AllowedCategories []string
	WithholdCallerID  *bool
}

// CreateCallPermissionLevel adds a level.
func (s *Service) CreateCallPermissionLevel(ctx context.Context, in CallPermissionLevelInput) (CallPermissionLevel, error) {
	p, err := principal(ctx)
	if err != nil {
		return CallPermissionLevel{}, err
	}
	if err := checkName(in.Name); err != nil {
		return CallPermissionLevel{}, err
	}
	categories, err := checkAllowedCategories(in.AllowedCategories)
	if err != nil {
		return CallPermissionLevel{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return CallPermissionLevel{}, err
	}
	now := s.Now().UTC()
	l := CallPermissionLevel{ID: id, TenantID: p.TenantID, Name: in.Name, AllowedCategories: categories,
		WithholdCallerID: in.WithholdCallerID != nil && *in.WithholdCallerID, Version: 1, CreatedAt: now, UpdatedAt: now}
	_, a, err := audit(ctx, "call_permission_level.create", "call_permission_level:"+id.String())
	if err != nil {
		return CallPermissionLevel{}, err
	}
	a.Detail = map[string]any{"name": l.Name, "allowed_categories": l.AllowedCategories}
	if err := s.Store.CreateCallPermissionLevel(ctx, l, a); err != nil {
		if errors.Is(err, ErrDuplicate) {
			return CallPermissionLevel{}, &apihttp.Error{Status: http.StatusConflict, Code: "name_duplicate",
				Detail: fmt.Sprintf("A permission level named %q already exists.", l.Name)}
		}
		return CallPermissionLevel{}, err
	}
	return l, nil
}

// GetCallPermissionLevel returns one of the caller's levels.
func (s *Service) GetCallPermissionLevel(ctx context.Context, id uuid.UUID) (CallPermissionLevel, error) {
	p, err := principal(ctx)
	if err != nil {
		return CallPermissionLevel{}, err
	}
	l, err := s.Store.CallPermissionLevel(ctx, p.TenantID, id)
	if errors.Is(err, ErrNotFound) {
		return CallPermissionLevel{}, notFound("call permission level")
	}
	return l, err
}

// ListCallPermissionLevels returns a page of the caller's levels, newest first.
func (s *Service) ListCallPermissionLevels(ctx context.Context, before *uuid.UUID, limit int) ([]CallPermissionLevel, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	return s.Store.ListCallPermissionLevels(ctx, p.TenantID, before, limit)
}

// CallPermissionLevelPatch is a JSON Merge Patch of a level; nil fields
// stay as they are.
type CallPermissionLevelPatch struct {
	Name              *string
	AllowedCategories []string
	WithholdCallerID  *bool
}

// UpdateCallPermissionLevel applies patch. ifMatch, when not empty, must
// match the level's current ETag (412 otherwise).
func (s *Service) UpdateCallPermissionLevel(ctx context.Context, id uuid.UUID, patch CallPermissionLevelPatch, ifMatch string) (CallPermissionLevel, error) {
	l, err := s.GetCallPermissionLevel(ctx, id)
	if err != nil {
		return CallPermissionLevel{}, err
	}
	if ifMatch != "" && !matchETag(ifMatch, l.Version) {
		return CallPermissionLevel{}, errCallPermissionLevelChanged
	}
	changes := map[string]any{}
	if patch.Name != nil {
		if err := checkName(*patch.Name); err != nil {
			return CallPermissionLevel{}, err
		}
		l.Name = *patch.Name
		changes["name"] = l.Name
	}
	if patch.AllowedCategories != nil {
		categories, err := checkAllowedCategories(patch.AllowedCategories)
		if err != nil {
			return CallPermissionLevel{}, err
		}
		l.AllowedCategories = categories
		changes["allowed_categories"] = l.AllowedCategories
	}
	if patch.WithholdCallerID != nil {
		l.WithholdCallerID = *patch.WithholdCallerID
		changes["withhold_caller_id"] = l.WithholdCallerID
	}
	l.UpdatedAt = s.Now().UTC()
	_, a, err := audit(ctx, "call_permission_level.update", "call_permission_level:"+id.String())
	if err != nil {
		return CallPermissionLevel{}, err
	}
	a.Detail = changes
	updated, err := s.Store.UpdateCallPermissionLevel(ctx, l, a)
	if errors.Is(err, ErrVersionChanged) {
		return CallPermissionLevel{}, errCallPermissionLevelChanged
	}
	if errors.Is(err, ErrNotFound) {
		return CallPermissionLevel{}, notFound("call permission level")
	}
	if errors.Is(err, ErrDuplicate) {
		return CallPermissionLevel{}, &apihttp.Error{Status: http.StatusConflict, Code: "name_duplicate",
			Detail: fmt.Sprintf("A permission level named %q already exists.", l.Name)}
	}
	return updated, err
}

// DeleteCallPermissionLevel removes a level not assigned to any extension.
func (s *Service) DeleteCallPermissionLevel(ctx context.Context, id uuid.UUID) error {
	_, a, err := audit(ctx, "call_permission_level.delete", "call_permission_level:"+id.String())
	if err != nil {
		return err
	}
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	err = s.Store.DeleteCallPermissionLevel(ctx, p.TenantID, id, a)
	if errors.Is(err, ErrNotFound) {
		return notFound("call permission level")
	}
	if errors.Is(err, ErrInUse) {
		return errCallPermissionLevelInUse
	}
	return err
}
