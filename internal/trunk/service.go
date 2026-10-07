package trunk

import (
	"context"
	"crypto/x509"
	"encoding/pem"
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
	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/trunkprobe"
	"linxpbx.com/linx/internal/trunkstatus"
	"linxpbx.com/linx/internal/wgconf"
)

// Service is what the API's trunk, DID, WireGuard profile and call
// permission level endpoints do. Caller mistakes come back as
// *apihttp.Error.
type Service struct {
	Store  Store
	Sealer *dbsecret.Sealer
	Now    func() time.Time
	// OnChange, if set, is called after a trunk is created, changed or
	// deleted: Asterisk's copy of the trunks is rendered again
	// (internal/trunkconf).
	OnChange func()
	// OnDeleted, if set, is called after a trunk is deleted (its "trunk is
	// down" alert is closed).
	OnDeleted func(ctx context.Context, tenant, id uuid.UUID)
	// OnWireGuardProfileDeleted, if set, is called after a WireGuard profile
	// is deleted (its "tunnel is down" alert is closed).
	OnWireGuardProfileDeleted func(ctx context.Context, tenant, id uuid.UUID)
	// Prober tests trunks' connections (TestTrunk).
	Prober Prober
}

func (s *Service) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
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

// hostPattern is a DNS name or an IPv4 address: what Asterisk's config
// and a SIP URI can carry as they are (docs/TRUNKS.md §4).
var hostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62})(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}))*\.?$`)

func checkHost(host string) error {
	if len(host) > 253 || !hostPattern.MatchString(host) {
		return invalid("host_invalid", "Give it the provider's name (sip.example.com) or IPv4 address.")
	}
	return nil
}

// ownAddressReason reports why host can never be *any* trunk's address,
// regardless of kind: one of Linx's own container/service names, or a
// loopback/unspecified/multicast/link-local IP literal typed directly.
// This closes the "obviously internal address" case at save time
// (docs/THREAT_MODEL.md residual risk: trunk host wasn't screened except
// by the optional connection test); a name that only *resolves* internally
// still needs the admin to run that test (internal/trunkprobe.RefuseOwn),
// which checks the address actually reached, not just the string given.
func ownAddressReason(host string) string {
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasPrefix(lower, "linx-") {
		return "it looks like one of Linx's own service names, not a phone line."
	}
	if a, err := netip.ParseAddr(host); err == nil {
		switch {
		case a.IsLoopback(), a.IsUnspecified(), a.IsMulticast(), a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast():
			return "that's not an address a phone line can be at."
		}
	}
	return ""
}

// usernamePattern is what a SIP login can be here: it's written into a SIP
// URI and Asterisk's config as is.
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._~+=-]{0,128}$`)

func checkUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return invalid("username_invalid", "A login is up to 128 letters, digits and . _ ~ + = -.")
	}
	return nil
}

// CheckPassword refuses what Asterisk's config can't hold: control
// characters, and spaces at either end (which it trims). ";" is fine: the
// config writer escapes it.
func CheckPassword(password string) error {
	if len(password) > 128 || strings.TrimSpace(password) != password ||
		strings.ContainsFunc(password, func(r rune) bool { return r < 0x20 || r == 0x7f || r > 0x7e }) {
		return invalid("password_invalid", "A password is up to 128 printable ASCII characters, with no spaces at either end.")
	}
	return nil
}

var callerIDPattern = regexp.MustCompile(`^(\+?[0-9]{2,20})?$`)

// ParsePinnedCertificates returns the certificates in a pinned PEM
// (ADR-045): at least one, and nothing but certificates.
func ParsePinnedCertificates(pemText string) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := []byte(strings.TrimSpace(pemText))
	for len(rest) > 0 {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil || b.Type != "CERTIFICATE" {
			return nil, errors.New("not a PEM certificate")
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, err
		}
		certs = append(certs, c)
		rest = []byte(strings.TrimSpace(string(rest)))
	}
	if len(certs) == 0 {
		return nil, errors.New("no certificate")
	}
	return certs, nil
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
	RingsExtensionID   *uuid.UUID
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
	if t.Kind == KindRegistersHere {
		return checkRegistersHere(t)
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
	if t.CertTrust == CertPinned {
		if _, err := ParsePinnedCertificates(t.PinnedCertificate); err != nil {
			return invalid("pinned_certificate_invalid", "Paste the certificate or CA in PEM form (-----BEGIN CERTIFICATE-----).")
		}
	}
	if err := checkUsername(t.Username); err != nil {
		return err
	}
	if !callerIDPattern.MatchString(t.CallerIDNumber) {
		return invalid("caller_id_number_invalid", "A caller ID is 2 to 20 digits, optionally starting with +.")
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
	if reason := ownAddressReason(t.Host); reason != "" {
		return invalid("host_not_allowed", "That can't be a phone line's address: "+reason)
	}
	if t.WireGuardProfileID != nil {
		// The tunnel carries only addresses Linx knows in advance
		// (docs/TRUNKS.md §7): a name could resolve elsewhere, outside it.
		if a, err := netip.ParseAddr(t.Host); err != nil || !a.Is4() {
			return invalid("host_must_be_address", "Through a WireGuard tunnel, give the provider's IPv4 address (usually its address inside the tunnel), not a name.")
		}
	} else if t.Kind != KindLANPeer {
		// A private address here would have to be a phone system on the
		// LAN — that's the "lan_peer" kind (docs/TRUNKS.md §3); for a
		// provider trunk it's very likely a mistake or a probe at Linx's
		// own internal services, not a real line (a legitimate LAN system
		// still goes through the connection test, which resolves and
		// checks the address actually reached).
		if a, err := netip.ParseAddr(t.Host); err == nil && a.IsPrivate() {
			return invalid("host_not_allowed",
				`That can't be a phone line's address: it's a private address. If this is a phone system on your own network, choose "Grandstream UCM (on the LAN)" or another LAN kind.`)
		}
	}
	return nil
}

// errRegistersHereFixed is a phone system that signs in to Linx given
// settings it doesn't have: it connects to Linx the way desk phones do.
var errRegistersHereFixed = invalid("registers_here_fixed",
	"A phone system that signs in to Linx has no address, login or connection settings to set here: it uses Linx's own (TLS and encrypted audio), with the login Linx made for it.")

// checkRegistersHere is checkTrunkFields for KindRegistersHere
// (docs/SIMPLER.md §1): Linx never connects out to it, so only its name,
// numbers, caller ID, codecs and call limit are settings; the rest is
// fixed (migration 0028 checks the same).
func checkRegistersHere(t *Trunk) error {
	if t.Host != "" || t.Port != 5061 || t.Transport != TransportTLS || t.MediaEncryption != MediaSRTP ||
		t.CertTrust != CertPublic || t.PinnedCertificate != "" || t.WireGuardProfileID != nil ||
		t.Username != t.Endpoint() || len(t.PasswordEnc) > 0 {
		return errRegistersHereFixed
	}
	if !callerIDPattern.MatchString(t.CallerIDNumber) {
		return invalid("caller_id_number_invalid", "A caller ID is 2 to 20 digits, optionally starting with +.")
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

// newLogin gives a KindRegistersHere trunk a new password (ADR-033: 130
// random bits, only its digest hash kept), returned in t.NewPassword.
func newLogin(t *Trunk) {
	t.NewPassword = pbx.NewDevicePassword()
	t.DigestHash = pbx.DigestHash(t.Endpoint(), t.NewPassword)
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

// ptrTime is *t, or the zero time.
func ptrTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
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
	registersHere := in.Kind == KindRegistersHere
	if registersHere {
		// Its login is shown in the answer: one of "confirm it's you"'s
		// actions, as for a device's (docs/ADMIN.md §7).
		if err := auth.RequireConfirmed(ctx, s.Now()); err != nil {
			return Trunk{}, err
		}
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
		WireGuardProfileID: in.WireGuardProfileID, RingsExtensionID: in.RingsExtensionID,
		Enabled: in.Enabled == nil || *in.Enabled, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if registersHere {
		if in.Username != "" || in.Password != "" {
			return Trunk{}, errRegistersHereFixed
		}
		t.Username = t.Endpoint()
		if in.DialFormat == "" {
			// A phone system on the network usually dials numbers as
			// people there write them.
			t.DialFormat = DialLocal
		}
	}
	if err := checkTrunkFields(&t); err != nil {
		return Trunk{}, err
	}
	if registersHere {
		newLogin(&t)
	}
	if err := applyUnencrypted(&t, in.ConfirmUnencrypted, p.Actor(), now); err != nil {
		return Trunk{}, err
	}
	if err := CheckPassword(in.Password); err != nil {
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
	if t.UnencryptedConfirmedAt != nil {
		a.Detail["unencrypted_confirmed"] = true
	}
	if err := s.Store.CreateTrunk(ctx, t, a); err != nil {
		if errors.Is(err, ErrDuplicate) {
			return Trunk{}, &apihttp.Error{Status: http.StatusConflict, Code: "name_duplicate",
				Detail: fmt.Sprintf("A trunk named %q already exists.", t.Name)}
		}
		if errors.Is(err, ErrExtensionNotFound) {
			return Trunk{}, errRingsExtensionNotFound
		}
		if errors.Is(err, ErrNotFound) {
			return Trunk{}, invalid("wireguard_profile_not_found", "That WireGuard profile doesn't exist.")
		}
		return Trunk{}, err
	}
	s.changed()
	return t, nil
}

var errRingsExtensionNotFound = invalid("extension_not_found", "That extension doesn't exist.")

// ResetTrunkPassword gives a phone system that signs in to Linx a new
// password, shown this once in the result's NewPassword; its login name
// doesn't change, and the old password stops working at Asterisk's next
// reload.
func (s *Service) ResetTrunkPassword(ctx context.Context, id uuid.UUID) (Trunk, error) {
	if err := auth.RequireConfirmed(ctx, s.Now()); err != nil {
		return Trunk{}, err
	}
	t, err := s.GetTrunk(ctx, id)
	if err != nil {
		return Trunk{}, err
	}
	if t.Kind != KindRegistersHere {
		return Trunk{}, &apihttp.Error{Status: http.StatusConflict, Code: "no_login_here",
			Detail: "Only a phone system that signs in to Linx has a login Linx made. Change this line's own password instead."}
	}
	newLogin(&t)
	t.UpdatedAt = s.Now().UTC()
	_, a, err := audit(ctx, "trunk.update", "trunk:"+id.String())
	if err != nil {
		return Trunk{}, err
	}
	a.Detail = map[string]any{"password": "new"}
	updated, err := s.Store.UpdateTrunk(ctx, t, a)
	if errors.Is(err, ErrVersionChanged) {
		return Trunk{}, errTrunkChanged
	}
	if err != nil {
		return Trunk{}, err
	}
	s.changed()
	updated.NewPassword = t.NewPassword
	return updated, nil
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
	// RingsExtensionID: non-nil sets it (a nil inside clears it).
	RingsExtensionID   **uuid.UUID
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
	before := t
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
		// Which certificate Linx trusts: audited, but the PEM itself is
		// long and public, so only that it changed.
		changes["pinned_certificate"] = "changed"
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
		changes["wireguard_profile_id"] = t.WireGuardProfileID
	}
	if patch.RingsExtensionID != nil {
		t.RingsExtensionID, t.RingsRingGroupID = *patch.RingsExtensionID, nil
		changes["rings_extension_id"] = t.RingsExtensionID
	}
	if patch.Enabled != nil {
		t.Enabled = *patch.Enabled
		changes["enabled"] = t.Enabled
	}
	if t.Kind == KindRegistersHere && patch.Password != nil {
		return Trunk{}, errRegistersHereFixed
	}
	if err := checkTrunkFields(&t); err != nil {
		return Trunk{}, err
	}
	// An ADR-023 confirmation covers the provider and the way it was
	// unencrypted when the admin read the warning: another address,
	// transport or audio encryption asks again (Phase 1D review), e.g. a
	// trunk confirmed as TLS without SRTP moving to plain UDP.
	if t.Host != before.Host || t.Transport != before.Transport || t.MediaEncryption != before.MediaEncryption {
		t.UnencryptedConfirmedBy, t.UnencryptedConfirmedAt = "", nil
	}
	now := s.Now().UTC()
	confirmed := patch.ConfirmUnencrypted != nil && *patch.ConfirmUnencrypted
	if err := applyUnencrypted(&t, confirmed, p.Actor(), now); err != nil {
		return Trunk{}, err
	}
	if t.UnencryptedConfirmedAt != nil && !t.UnencryptedConfirmedAt.Equal(ptrTime(before.UnencryptedConfirmedAt)) {
		changes["unencrypted_confirmed"] = true
	}
	if patch.Password != nil {
		if err := CheckPassword(*patch.Password); err != nil {
			return Trunk{}, err
		}
		changes["password"] = "changed"
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
	if errors.Is(err, ErrExtensionNotFound) {
		return Trunk{}, errRingsExtensionNotFound
	}
	if errors.Is(err, ErrNotFound) {
		return Trunk{}, notFound("trunk")
	}
	if errors.Is(err, ErrDuplicate) {
		return Trunk{}, &apihttp.Error{Status: http.StatusConflict, Code: "name_duplicate",
			Detail: fmt.Sprintf("A trunk named %q already exists.", t.Name)}
	}
	if err == nil {
		s.changed()
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
	if err == nil {
		s.changed()
		if s.OnDeleted != nil {
			s.OnDeleted(ctx, p.TenantID, id)
		}
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

// DIDByNumber finds one of the caller's phone numbers, for POST
// /route-test's inbound direction (docs/ADMIN.md §9); ErrNotFound if it
// isn't one.
func (s *Service) DIDByNumber(ctx context.Context, number string) (DID, error) {
	p, err := principal(ctx)
	if err != nil {
		return DID{}, err
	}
	return s.Store.DIDByNumber(ctx, p.TenantID, number)
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
		d.RingGroupID = nil
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

	allowedIPs string // from an imported config: only for splitNote
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
	if _, err := wgconf.ParseTunnelAddress(fields.Address); err != nil {
		return WireGuardProfile{}, invalid("address_invalid", "The tunnel address this end uses must include an IPv4 address (e.g. 10.6.0.2/32): "+err.Error()+".")
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
		Status: wgconf.StateUnknown,
	}
	if note := splitNote(fields.allowedIPs); note != "" {
		w.Notes = append(w.Notes, note)
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
	s.changed()
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
	if err == nil {
		s.changed()
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
	if err == nil {
		s.changed()
		if s.OnWireGuardProfileDeleted != nil {
			s.OnWireGuardProfileDeleted(ctx, p.TenantID, id)
		}
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

// checkAbroadCountries checks a level's list of countries calls abroad may
// go to: each one a country Linx knows, each once, sorted. Empty means
// everywhere.
func checkAbroadCountries(countries []string) ([]string, error) {
	out := make([]string, 0, len(countries))
	for _, c := range countries {
		if !numbering.Supported(c) {
			return nil, invalid("country_unknown", fmt.Sprintf("%q isn't a country code Linx knows (use ISO 3166 codes such as GB or SA).", c))
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out, nil
}

// widensAbroad says whether going from before to after lets calls abroad
// reach a country they couldn't (an empty list is everywhere).
func widensAbroad(before, after []string) bool {
	if len(after) == 0 {
		return len(before) > 0
	}
	if len(before) == 0 {
		return false
	}
	for _, c := range after {
		if !slices.Contains(before, c) {
			return true
		}
	}
	return false
}

// CallPermissionLevelInput is a new permission level.
type CallPermissionLevelInput struct {
	Name              string
	AllowedCategories []string
	AbroadCountries   []string // empty: everywhere
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
	if err := s.requireConfirmedForCostly(ctx, nil, categories); err != nil {
		return CallPermissionLevel{}, err
	}
	countries, err := checkAbroadCountries(in.AbroadCountries)
	if err != nil {
		return CallPermissionLevel{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return CallPermissionLevel{}, err
	}
	now := s.Now().UTC()
	l := CallPermissionLevel{ID: id, TenantID: p.TenantID, Name: in.Name, AllowedCategories: categories, AbroadCountries: countries,
		WithholdCallerID: in.WithholdCallerID != nil && *in.WithholdCallerID, Version: 1, CreatedAt: now, UpdatedAt: now}
	_, a, err := audit(ctx, "call_permission_level.create", "call_permission_level:"+id.String())
	if err != nil {
		return CallPermissionLevel{}, err
	}
	a.Detail = map[string]any{"name": l.Name, "allowed_categories": l.AllowedCategories, "abroad_countries": l.AbroadCountries}
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
	AbroadCountries   *[]string // an empty list: everywhere
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
		if err := s.requireConfirmedForCostly(ctx, l.AllowedCategories, categories); err != nil {
			return CallPermissionLevel{}, err
		}
		l.AllowedCategories = categories
		changes["allowed_categories"] = l.AllowedCategories
	}
	if patch.AbroadCountries != nil {
		countries, err := checkAbroadCountries(*patch.AbroadCountries)
		if err != nil {
			return CallPermissionLevel{}, err
		}
		// Calls abroad reaching more countries costs the same confirmation
		// as allowing calls abroad at all.
		if slices.Contains(l.AllowedCategories, "international") && widensAbroad(l.AbroadCountries, countries) {
			if err := auth.RequireConfirmed(ctx, s.Now()); err != nil {
				return CallPermissionLevel{}, err
			}
		}
		l.AbroadCountries = countries
		changes["abroad_countries"] = l.AbroadCountries
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

// costlyCategories are where phone fraud costs money (docs/ui/
// ADMIN_SCREENS_PHASE1E.md §8): allowing one is a "confirm it's you"
// action in a session (docs/ADMIN.md §7).
var costlyCategories = []string{"international", "premium"}

func (s *Service) requireConfirmedForCostly(ctx context.Context, before, after []string) error {
	for _, c := range costlyCategories {
		if slices.Contains(after, c) && !slices.Contains(before, c) {
			return auth.RequireConfirmed(ctx, s.Now())
		}
	}
	return nil
}

// --- Testing a trunk ------------------------------------------------------

// Prober tests a trunk's connection (internal/trunkprobe's Prober).
type Prober interface {
	Run(ctx context.Context, t trunkprobe.Target) trunkprobe.Result
}

// ProbeTarget is what trunkprobe tests for t, with its opened password.
func ProbeTarget(t Trunk, password string) trunkprobe.Target {
	target := trunkprobe.Target{Host: t.Host, Port: t.Port, Transport: t.Transport, Registers: t.Kind == KindRegistration,
		Username: t.Username, Password: password, SRTP: t.MediaEncryption == MediaSRTP, WireGuard: t.WireGuardProfileID != nil}
	if t.CertTrust == CertPinned {
		target.Pinned = t.PinnedCertificate
	}
	return target
}

// TestTrunk tests a saved trunk's connection (POST /trunks/{id}/test).
func (s *Service) TestTrunk(ctx context.Context, id uuid.UUID) (trunkprobe.Result, error) {
	if s.Prober == nil {
		return trunkprobe.Result{}, errors.New("trunk service has no prober")
	}
	t, err := s.GetTrunk(ctx, id)
	if err != nil {
		return trunkprobe.Result{}, err
	}
	if t.Kind == KindRegistersHere {
		return SignedInResult(t), nil
	}
	password, err := OpenPassword(s.Sealer, t)
	if err != nil {
		return trunkprobe.Result{}, fmt.Errorf("opening the trunk's password: %w", err)
	}
	target := ProbeTarget(t, password)
	if t.WireGuardProfileID != nil {
		w, err := s.GetWireGuardProfile(ctx, *t.WireGuardProfileID)
		if err != nil {
			return trunkprobe.Result{}, err
		}
		target.TunnelName, target.TunnelState, target.TunnelDetail = w.Name, w.Status, w.StatusDetail
	}
	return s.Prober.Run(ctx, target), nil
}

// SignedInResult is a test of a phone system that signs in to Linx: Linx
// never connects out to it, so the only question is whether it's signed
// in now (what the Monitor last saw), and if not, what to check on it.
func SignedInResult(t Trunk) trunkprobe.Result {
	step := trunkprobe.Step{Name: "signed_in"}
	switch {
	case !t.Enabled:
		step.Result, step.Words = "failed", "It's turned off: Linx refuses its sign-in until you turn it on."
	case trunkstatus.Up(t.Status):
		step.Result, step.Words = "ok", "It's signed in to Linx."
	case t.Status == trunkstatus.StatusUnreachable && t.StatusDetail == trunkstatus.DetailStoppedAnswering:
		step.Result, step.Words = "failed", t.StatusDetail
	default:
		step.Result, step.Words = "failed", "It isn't signed in to Linx. On the phone system, check the server, port 5061, TLS, "+
			"the username and password exactly as shown when this line was added, and that it's on one of your phone networks."
	}
	return trunkprobe.Result{OK: step.Result == "ok", Steps: []trunkprobe.Step{step}}
}

// InternationalAlert returns the "unusual calling abroad" alert's limits.
func (s *Service) InternationalAlert(ctx context.Context) (minutes, calls int, err error) {
	return s.Store.InternationalAlertLimits(ctx)
}

// SetInternationalAlert changes them: more than minutes, or calls, to
// numbers abroad in an hour raises the alert.
func (s *Service) SetInternationalAlert(ctx context.Context, minutes, calls int) error {
	if minutes < 1 || minutes > 10000 || calls < 1 || calls > 10000 {
		return invalid("international_alert_invalid", "Give 1 to 10000 minutes and 1 to 10000 calls.")
	}
	_, a, err := audit(ctx, "routing.international_alert", "pbx_setting")
	if err != nil {
		return err
	}
	a.Detail = map[string]any{"minutes": minutes, "calls": calls}
	return s.Store.SetInternationalAlertLimits(ctx, minutes, calls, a)
}
