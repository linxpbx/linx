// Package enroll sets up an iPhone or iPad and keeps it signed in (ADR-073,
// ADR-077, docs/PHASE2.md §4 and §9).
//
// Setting one up has three parts, and no SIP password appears in any of them:
//
//  1. An admin (or the person themselves) makes a one-time ticket: a signed
//     token for the QR code or the emailed link (ADR-012), and 8 characters
//     to type instead. It is good for 10 minutes and for one phone.
//  2. The phone makes a key pair inside its Secure Enclave — the private half
//     can never be read, copied or backed up, not even by the app — and sends
//     the ticket with a certificate request. Linx creates the device and has
//     the internal CA sign a 7-day certificate for that key.
//  3. From then on the phone proves who it is by signing a short, single-use
//     statement with that key, and gets a 15-minute device token back. That
//     works through every front door, including ones that decrypt and
//     re-encrypt, and inside China, because nothing depends on the phone's
//     TLS client certificate reaching Linx.
//
// A certificate lasts 7 days and is renewed whenever the phone is in touch.
// Seven days with no contact at all and the phone must be set up again, so a
// stolen phone kept offline becomes useless by itself.
package enroll

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
)

// Rules of this slice (docs/PHASE2.md §4, §9).
const (
	// TicketTTL is how long a QR code, link or typed code is good for.
	TicketTTL = auth.EnrollTokenTTL
	// InactivityWindow is how long a phone may go without being in touch
	// before it has to be set up again (ADR-077).
	InactivityWindow = 7 * 24 * time.Hour
	// ProofTTL is how old a phone's signed proof may be. It is also
	// single-use, so a copied one is refused even inside the minute.
	ProofTTL = time.Minute
	// ProofAudience is what a proof may be used for, and nothing else.
	ProofAudience = "linx-device"
	// clockLeeway tolerates a phone's clock being a little out.
	clockLeeway = 30 * time.Second
	// MaxAttempts is how many wrong codes a ticket survives.
	MaxAttempts = 5
	// MaxOpenTickets is how many phones may be waiting to be set up at once.
	MaxOpenTickets = 50
	// maxNameLen is the longest phone name.
	maxNameLen = 100

	codeLen      = 8
	codeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ" // no 0/O or 1/I/L look-alikes
)

// Delivery is how the ticket reaches the phone (only for the audit log and
// the admin's list; every delivery carries the same ticket).
const (
	DeliveryQR     = "qr"
	DeliveryEmail  = "email"
	DeliveryByHand = "by_hand"
)

// Ticket is one phone waiting to be set up.
type Ticket struct {
	ID                            uuid.UUID
	TenantID, UserID, ExtensionID uuid.UUID
	Kind, DeviceName, Delivery    string
	CreatedBy                     string
	CodeHash                      []byte
	TokenJTI                      uuid.UUID
	CreatedAt, ExpiresAt          time.Time
	UsedAt, CanceledAt            *time.Time
	DeviceID                      *uuid.UUID
	Attempts                      int
	// PersonName and Number are who the phone is for; they come from the
	// person, not from the ticket row.
	PersonName string
	Number     string
}

// Open reports whether the ticket can still be used.
func (t Ticket) Open(now time.Time) bool {
	return t.UsedAt == nil && t.CanceledAt == nil && t.Attempts < MaxAttempts && now.Before(t.ExpiresAt)
}

// NewTicket is a ticket with the two secrets, shown this once: the token
// goes into the QR code or the emailed link, the code is typed by hand.
type NewTicket struct {
	Ticket
	Token string
	Code  string
}

// Identity is what a set-up phone is: the Secure Enclave key Linx signed a
// certificate for, and when the phone was last in touch.
type Identity struct {
	DeviceID, TenantID, UserID uuid.UUID
	// PublicKey is the Secure Enclave key, as SPKI DER. It never changes:
	// a renewal signs the same key again.
	PublicKey       []byte
	CertSerial      string
	CertFingerprint []byte
	CertNotAfter    time.Time
	EnrolledAt      time.Time
	LastSeenAt      time.Time
	ExpiresAt       time.Time
	ExpiredAt       *time.Time
	AppVersion      string
	OSVersion       string
}

// Target is the person a ticket is for.
type Target struct {
	UserID, ExtensionID uuid.UUID
	PersonName          string
	Number              string
}

// CertUpdate is a renewed certificate for the same key.
type CertUpdate struct {
	Serial      string
	Fingerprint []byte
	NotAfter    time.Time
}

// Store is the database access this package needs (internal/store).
type Store interface {
	// EnrollmentTarget is the person a ticket may be made for: they must
	// exist, be enabled and have an extension. ErrNotFound otherwise.
	EnrollmentTarget(ctx context.Context, tenant, user uuid.UUID) (Target, error)
	CreateEnrollment(ctx context.Context, t Ticket, audit auth.AuditEntry) error
	// Enrollments lists the open tickets, newest first; user limits them to
	// one person's.
	Enrollments(ctx context.Context, tenant uuid.UUID, user *uuid.UUID) ([]Ticket, error)
	Enrollment(ctx context.Context, id uuid.UUID) (Ticket, error)
	EnrollmentByCodeHash(ctx context.Context, hash []byte) (Ticket, error)
	CancelEnrollment(ctx context.Context, tenant, id uuid.UUID, at time.Time, audit auth.AuditEntry) error
	// FailEnrollmentAttempt counts a wrong code against the ticket.
	FailEnrollmentAttempt(ctx context.Context, id uuid.UUID) error
	// RedeemEnrollment creates the device and its identity and marks the
	// ticket used, all in one transaction. ErrNotFound if the ticket is no
	// longer open (someone else got there first).
	RedeemEnrollment(ctx context.Context, ticket uuid.UUID, d pbx.Device, identity Identity, audit auth.AuditEntry) error
	IdentityByFingerprint(ctx context.Context, fingerprint []byte) (Identity, pbx.Device, error)
	IdentityByDevice(ctx context.Context, device uuid.UUID) (Identity, pbx.Device, error)
	// TouchIdentity records that the phone was in touch (and its new
	// certificate, when it renewed one).
	TouchIdentity(ctx context.Context, device uuid.UUID, cert *CertUpdate, app, os string, seen, expires time.Time) error
	// UseProof records a proof's id, or returns pbx.ErrDuplicate if that
	// proof has been used before.
	UseProof(ctx context.Context, jti, device uuid.UUID, at time.Time) error
	// DevicePrincipalFor is the person a live phone belongs to. ErrNotFound
	// when the phone is revoked, turned off, expired, or its person is gone.
	DevicePrincipalFor(ctx context.Context, device uuid.UUID, now time.Time) (tenant, user uuid.UUID, err error)
	// ExpireIdentities marks every phone that has not been in touch for
	// InactivityWindow, with a device.expired event each.
	ExpireIdentities(ctx context.Context, now time.Time) ([]pbx.Device, error)
	// DeleteUsedProofs removes proof ids that could no longer be replayed.
	DeleteUsedProofs(ctx context.Context, before time.Time) error
	// PhonesForUser is this person's own set-up phones, newest first,
	// revoked ones left out.
	PhonesForUser(ctx context.Context, tenant, user uuid.UUID) ([]pbx.Device, error)
	// RevokeDevice logs a phone out at once and for good.
	RevokeDevice(ctx context.Context, tenant, id uuid.UUID, at time.Time, audit auth.AuditEntry) (pbx.Device, error)
}

// CertIssuer signs a phone's certificate request (internal/stepca's client
// for the linx-devices provisioner).
type CertIssuer interface {
	SignCSR(ctx context.Context, csrDER []byte, commonName string, sans []string) ([][]byte, error)
}

// Service makes tickets, turns them into set-up phones, and hands out the
// device tokens the app calls Linx with.
type Service struct {
	Store Store
	// Tokens signs enrollment tokens and device tokens (one signing key).
	Tokens *auth.Tokens
	CA     CertIssuer
	// CARoot is the internal CA's root certificate, PEM, which the app pins
	// so it can tell this Linx from any other.
	CARoot []byte
	Now    func() time.Time
	Log    *slog.Logger
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Errors. The ones a phone sees say as little as possible: a wrong, used,
// cancelled, expired or unknown ticket all look the same, so nobody can
// learn anything by trying.
var (
	errTicket = &apihttp.Error{Status: http.StatusBadRequest, Code: "enrollment_invalid",
		Detail: "This setup code can't be used any more. Ask for a new one."}
	errProof = &apihttp.Error{Status: http.StatusUnauthorized, Code: "device_proof_invalid",
		Detail: "This phone couldn't prove who it is. Set it up again."}
	errDeviceGone = &apihttp.Error{Status: http.StatusUnauthorized, Code: "device_inactive",
		Detail: "This phone is no longer set up. Set it up again."}
	errNoPrincipal = &apihttp.Error{Status: http.StatusUnauthorized, Code: "auth_required",
		Detail: "Sign in first."}
	errPending = &apihttp.Error{Status: http.StatusForbidden, Code: "sign_in_unfinished",
		Detail: "Finish signing in first."}
	errNotAllowed = &apihttp.Error{Status: http.StatusForbidden, Code: "forbidden",
		Detail: "You can only add a phone for yourself. An admin can add one for someone else."}
	errNoExtension = &apihttp.Error{Status: http.StatusConflict, Code: "no_extension",
		Detail: "This person has no extension yet, so a phone has nothing to answer for. Give them one first."}
	errTooMany = &apihttp.Error{Status: http.StatusConflict, Code: "too_many_enrollments",
		Detail: "Too many phones are waiting to be set up. Finish or cancel one first."}
)

// TicketInput asks for a new ticket.
type TicketInput struct {
	// UserID is whose phone it is; the zero value means the caller's own.
	UserID     uuid.UUID
	DeviceName string
	Kind       string
	Delivery   string
}

// CreateTicket makes a one-time ticket for a phone. Anyone may make one for
// themselves; making one for someone else needs devices:write.
func (s *Service) CreateTicket(ctx context.Context, in TicketInput) (NewTicket, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return NewTicket{}, errNoPrincipal
	}
	if p.Pending {
		return NewTicket{}, errPending
	}
	// Setting up a phone is one of "confirm it's you"'s actions, like being
	// shown a device's SIP login (docs/ADMIN.md §7): a stolen but still
	// valid session cookie isn't enough on its own.
	if err := auth.RequireConfirmed(ctx, s.now()); err != nil {
		return NewTicket{}, err
	}
	self, selfErr := uuid.Parse(p.ID)
	user := in.UserID
	if user == uuid.Nil {
		if p.Type != auth.TypeUser || selfErr != nil {
			return NewTicket{}, errNotAllowed
		}
		user = self
	}
	if !(p.Type == auth.TypeUser && selfErr == nil && self == user) && !p.Has("devices:write") {
		return NewTicket{}, errNotAllowed
	}
	// An app's own token can never add another phone (docs/PHASE2.md §4).
	if p.DeviceID != nil {
		return NewTicket{}, errNotAllowed
	}
	kind := in.Kind
	if kind == "" {
		kind = pbx.KindIOS
	}
	if kind != pbx.KindIOS {
		return NewTicket{}, &apihttp.Error{Status: http.StatusBadRequest, Code: "kind_not_supported",
			Detail: "Only iPhones and iPads are set up this way."}
	}
	name := strings.TrimSpace(in.DeviceName)
	if name == "" || len([]rune(name)) > maxNameLen || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return NewTicket{}, &apihttp.Error{Status: http.StatusBadRequest, Code: "name_invalid",
			Detail: fmt.Sprintf("Give the phone a name of 1 to %d characters, without line breaks.", maxNameLen)}
	}
	delivery := in.Delivery
	if delivery == "" {
		delivery = DeliveryQR
	}
	if delivery != DeliveryQR && delivery != DeliveryEmail && delivery != DeliveryByHand {
		return NewTicket{}, &apihttp.Error{Status: http.StatusBadRequest, Code: "delivery_invalid",
			Detail: "Choose a QR code, an emailed link, or setting it up by hand."}
	}

	target, err := s.Store.EnrollmentTarget(ctx, p.TenantID, user)
	if errors.Is(err, pbx.ErrNotFound) {
		return NewTicket{}, errNoExtension
	}
	if err != nil {
		return NewTicket{}, err
	}
	open, err := s.Store.Enrollments(ctx, p.TenantID, nil)
	if err != nil {
		return NewTicket{}, err
	}
	if len(open) >= MaxOpenTickets {
		return NewTicket{}, errTooMany
	}

	id, err := uuid.NewV7()
	if err != nil {
		return NewTicket{}, err
	}
	jti, err := uuid.NewV7()
	if err != nil {
		return NewTicket{}, err
	}
	now := s.now()
	code := newCode()
	t := Ticket{
		ID: id, TenantID: p.TenantID, UserID: target.UserID, ExtensionID: target.ExtensionID,
		Kind: kind, DeviceName: name, Delivery: delivery, CreatedBy: p.Actor(),
		CodeHash: auth.HashSecret(code), TokenJTI: jti,
		CreatedAt: now, ExpiresAt: now.Add(TicketTTL), PersonName: target.PersonName, Number: target.Number,
	}
	token, err := s.Tokens.IssueEnrollment(id, p.TenantID, jti, now)
	if err != nil {
		return NewTicket{}, err
	}
	a := auth.AuditEntry{TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: "device.enroll_ticket", Target: "user:" + target.UserID.String(), Result: auth.ResultOK,
		Detail: map[string]any{"enrollment_id": id, "name": name, "kind": kind, "delivery": delivery}}
	if err := s.Store.CreateEnrollment(ctx, t, a); err != nil {
		return NewTicket{}, err
	}
	return NewTicket{Ticket: t, Token: token, Code: code}, nil
}

// Tickets lists the phones waiting to be set up: everyone's for an admin
// with devices:read, the caller's own otherwise.
func (s *Service) Tickets(ctx context.Context) ([]Ticket, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipal
	}
	if p.Pending {
		return nil, errPending
	}
	var mine *uuid.UUID
	if !p.Has("devices:read") {
		self, err := uuid.Parse(p.ID)
		if p.Type != auth.TypeUser || err != nil {
			return nil, errNotAllowed
		}
		mine = &self
	}
	list, err := s.Store.Enrollments(ctx, p.TenantID, mine)
	if err != nil {
		return nil, err
	}
	now := s.now()
	open := make([]Ticket, 0, len(list))
	for _, t := range list {
		if t.Open(now) {
			open = append(open, t)
		}
	}
	return open, nil
}

// CancelTicket stops a ticket being used. Cancelling a ticket that is
// already finished or gone changes nothing.
func (s *Service) CancelTicket(ctx context.Context, id uuid.UUID) error {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return errNoPrincipal
	}
	if p.Pending {
		return errPending
	}
	t, err := s.Store.Enrollment(ctx, id)
	if errors.Is(err, pbx.ErrNotFound) || err == nil && t.TenantID != p.TenantID {
		return &apihttp.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "No such setup code."}
	}
	if err != nil {
		return err
	}
	self, selfErr := uuid.Parse(p.ID)
	if !(p.Type == auth.TypeUser && selfErr == nil && self == t.UserID) && !p.Has("devices:write") {
		return errNotAllowed
	}
	a := auth.AuditEntry{TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: "device.enroll_cancel", Target: "enrollment:" + id.String(), Result: auth.ResultOK}
	return s.Store.CancelEnrollment(ctx, p.TenantID, id, s.now(), a)
}

// MyPhones is the signed-in person's own iPhones and iPads, for their
// Settings page. It needs no scope: these are their own phones.
func (s *Service) MyPhones(ctx context.Context) ([]pbx.Device, error) {
	p, user, err := person(ctx)
	if err != nil {
		return nil, err
	}
	return s.Store.PhonesForUser(ctx, p.TenantID, user)
}

// RevokeMyPhone stops one of the caller's own phones at once and for good
// ("I've lost my phone"). Someone else's needs devices:write, as before.
func (s *Service) RevokeMyPhone(ctx context.Context, id uuid.UUID) error {
	p, user, err := person(ctx)
	if err != nil {
		return err
	}
	mine, err := s.Store.PhonesForUser(ctx, p.TenantID, user)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(mine, func(d pbx.Device) bool { return d.ID == id }) {
		return &apihttp.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "That isn't one of your phones."}
	}
	a := auth.AuditEntry{TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: "device.revoke", Target: "device:" + id.String(), Result: auth.ResultOK,
		Detail: map[string]any{"by": "owner"}}
	_, err = s.Store.RevokeDevice(ctx, p.TenantID, id, s.now(), a)
	return err
}

// person is the signed-in caller, refusing a half-finished sign-in and
// anything that isn't a person.
func person(ctx context.Context) (auth.Principal, uuid.UUID, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return p, uuid.Nil, errNoPrincipal
	}
	if p.Pending {
		return p, uuid.Nil, errPending
	}
	id, err := uuid.Parse(p.ID)
	if p.Type != auth.TypeUser || err != nil {
		return p, uuid.Nil, errNotAllowed
	}
	return p, id, nil
}

// newCode is codeLen characters from codeAlphabet, each equally likely.
func newCode() string {
	b := make([]byte, 0, codeLen)
	var r [1]byte
	for len(b) < codeLen {
		_, _ = rand.Read(r[:])
		// The largest multiple of the alphabet's length under 256.
		if int(r[0]) < 256/len(codeAlphabet)*len(codeAlphabet) {
			b = append(b, codeAlphabet[int(r[0])%len(codeAlphabet)])
		}
	}
	return string(b)
}

func fingerprint(der []byte) []byte {
	sum := sha256.Sum256(der)
	return sum[:]
}

func pemCert(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// publicKeyDER is the SPKI DER of a certificate request's or certificate's
// public key, which must be a P-256 key (what the Secure Enclave makes).
func publicKeyDER(pub any) ([]byte, error) {
	key, ok := pub.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("not a P-256 public key")
	}
	return x509.MarshalPKIXPublicKey(key)
}

// proofClaims is what a phone signs with its Secure Enclave key.
type proofClaims struct {
	jwt.Claims
}

// verifyProof checks a compact JWS against cert's public key and returns the
// proof's single-use id.
func verifyProof(raw string, cert *x509.Certificate, device uuid.UUID, now time.Time) (uuid.UUID, error) {
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return uuid.Nil, errProof
	}
	var c proofClaims
	if err := tok.Claims(cert.PublicKey, &c); err != nil {
		return uuid.Nil, errProof
	}
	if c.Expiry == nil || c.IssuedAt == nil || c.ID == "" {
		return uuid.Nil, errProof
	}
	if c.Expiry.Time().Sub(c.IssuedAt.Time()) > ProofTTL || c.IssuedAt.Time().After(now.Add(ProofTTL)) {
		return uuid.Nil, errProof
	}
	if err := c.Validate(jwt.Expected{
		Subject:     device.String(),
		AnyAudience: jwt.Audience{ProofAudience},
		Time:        now,
	}); err != nil {
		return uuid.Nil, errProof
	}
	jti, err := uuid.Parse(c.ID)
	if err != nil {
		return uuid.Nil, errProof
	}
	return jti, nil
}
