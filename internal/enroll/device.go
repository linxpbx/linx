package enroll

import (
	"context"
	"crypto/subtle"
	"crypto/x509"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
)

// CertName is the only name Linx will sign a phone's certificate request
// for. Every phone's certificate carries it: a phone is told apart by its
// Secure Enclave key, not by a name, and a fixed name that belongs to no
// Linx service means a phone's certificate can never stand in for one
// (docs/PHASE2.md §4).
const CertName = "linx-phone"

// RedeemRequest is what a phone sends to set itself up: the ticket (the
// token from the QR code or the link, or the 8 characters typed by hand)
// and a certificate request for the key it has just made in its Secure
// Enclave.
type RedeemRequest struct {
	Token      string
	Code       string
	CSR        []byte // DER
	AppVersion string
	OSVersion  string
}

// Enrolled is what the phone gets back. There is no SIP password here: the
// app asks for its phone line separately, every time it runs.
type Enrolled struct {
	DeviceID     uuid.UUID
	DeviceName   string
	PersonName   string
	Extension    string
	Certificate  string // PEM: the phone's certificate and the CA's intermediate
	CARoot       string // PEM: what the app pins
	CertNotAfter time.Time
	ExpiresAt    time.Time // when the phone must be set up again if it goes quiet
}

// Redeem turns a ticket into a set-up phone. Everything that can go wrong —
// an unknown, used, cancelled or expired ticket, a bad certificate request —
// comes back as the same answer, so nothing can be learned by trying.
func (s *Service) Redeem(ctx context.Context, req RedeemRequest) (Enrolled, error) {
	now := s.now()
	t, err := s.ticketFor(ctx, req, now)
	if err != nil {
		return Enrolled{}, err
	}

	csr, err := x509.ParseCertificateRequest(req.CSR)
	if err != nil || csr.CheckSignature() != nil {
		return Enrolled{}, errTicket
	}
	if csr.Subject.CommonName != CertName || len(csr.EmailAddresses) > 0 || len(csr.IPAddresses) > 0 ||
		len(csr.URIs) > 0 || len(csr.DNSNames) > 1 || (len(csr.DNSNames) == 1 && csr.DNSNames[0] != CertName) {
		return Enrolled{}, errTicket
	}
	pub, err := publicKeyDER(csr.PublicKey)
	if err != nil {
		return Enrolled{}, errTicket
	}

	chain, err := s.CA.SignCSR(ctx, req.CSR, CertName, []string{CertName})
	if err != nil {
		return Enrolled{}, err
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return Enrolled{}, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return Enrolled{}, err
	}
	// The device is created with a SIP password nobody keeps: the app asks
	// for its phone line with its device token, and gets a fresh password
	// then, exactly as a browser does (docs/WEB.md §5).
	username, password := pbx.NewSIPUsername(), pbx.NewDevicePassword()
	d := pbx.Device{
		ID: id, TenantID: t.TenantID, ExtensionID: t.ExtensionID, Name: t.DeviceName, Kind: t.Kind,
		SIPUsername: username, DigestHash: pbx.DigestHash(username, password),
		Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	identity := Identity{
		DeviceID: id, TenantID: t.TenantID, UserID: t.UserID,
		PublicKey: pub, CertSerial: leaf.SerialNumber.String(), CertFingerprint: fingerprint(chain[0]),
		CertNotAfter: leaf.NotAfter.UTC(), EnrolledAt: now, LastSeenAt: now,
		ExpiresAt:  now.Add(InactivityWindow),
		AppVersion: clean(req.AppVersion), OSVersion: clean(req.OSVersion),
	}
	a := auth.AuditEntry{TenantID: &t.TenantID, Actor: auth.TypeUser + ":" + t.UserID.String(),
		IP: auth.ClientIPFromContext(ctx), Action: "device.enrolled", Target: "device:" + id.String(),
		Result: auth.ResultOK,
		Detail: map[string]any{"enrollment_id": t.ID, "name": t.DeviceName, "kind": t.Kind,
			"delivery": t.Delivery, "cert_serial": identity.CertSerial}}
	if err := s.Store.RedeemEnrollment(ctx, t.ID, d, identity, a); err != nil {
		if errors.Is(err, pbx.ErrNotFound) {
			return Enrolled{}, errTicket
		}
		return Enrolled{}, err
	}
	s.log().Info("phone set up", "device", id, "user", t.UserID, "name", t.DeviceName)

	var certs strings.Builder
	for _, der := range chain {
		certs.WriteString(pemCert(der))
	}
	return Enrolled{
		DeviceID: id, DeviceName: t.DeviceName, PersonName: t.PersonName, Extension: t.Number,
		Certificate: certs.String(), CARoot: string(s.CARoot),
		CertNotAfter: identity.CertNotAfter, ExpiresAt: identity.ExpiresAt,
	}, nil
}

// ticketFor finds the open ticket a phone is holding, by its token or by
// the code someone typed.
func (s *Service) ticketFor(ctx context.Context, req RedeemRequest, now time.Time) (Ticket, error) {
	var t Ticket
	switch {
	case req.Token != "":
		claims, err := s.Tokens.VerifyEnrollment(req.Token, now)
		if err != nil {
			return Ticket{}, errTicket
		}
		t, err = s.Store.Enrollment(ctx, claims.EnrollmentID)
		if err != nil {
			if errors.Is(err, pbx.ErrNotFound) {
				return Ticket{}, errTicket
			}
			return Ticket{}, err
		}
		// The token is single-use: its id is the ticket's, and a ticket is
		// redeemed once (ADR-012).
		if t.TokenJTI != claims.JTI || t.TenantID != claims.TenantID {
			return Ticket{}, errTicket
		}
	case len(req.Code) == codeLen:
		code := strings.ToUpper(strings.TrimSpace(req.Code))
		var err error
		t, err = s.Store.EnrollmentByCodeHash(ctx, auth.HashSecret(code))
		if err != nil {
			if errors.Is(err, pbx.ErrNotFound) {
				return Ticket{}, errTicket
			}
			return Ticket{}, err
		}
		if subtle.ConstantTimeCompare(t.CodeHash, auth.HashSecret(code)) != 1 {
			return Ticket{}, errTicket
		}
	default:
		return Ticket{}, errTicket
	}
	if !t.Open(now) {
		// Guessing the 8 characters is stopped by the per-address rate
		// limit on /v1/enroll, not here: a wrong code matches no ticket at
		// all. This counts the tries against a ticket someone keeps
		// presenting after it is finished.
		if t.UsedAt == nil && t.CanceledAt == nil {
			_ = s.Store.FailEnrollmentAttempt(ctx, t.ID)
		}
		return Ticket{}, errTicket
	}
	return t, nil
}

// TokenRequest is a phone asking for a device token with the certificate it
// was given and a statement signed by its Secure Enclave key. CSR, when
// present, renews the certificate for the same key.
type TokenRequest struct {
	Certificate []byte // DER
	Proof       string // compact JWS, ES256
	CSR         []byte // DER, optional
	AppVersion  string
	OSVersion   string
}

// TokenResult is the phone's 15-minute token, and a renewed certificate
// when it asked for one.
type TokenResult struct {
	Token        string
	ExpiresAt    time.Time
	DeviceID     uuid.UUID
	Certificate  string // PEM, only when renewed
	CertNotAfter time.Time
	// SetUpAgain is the moment the phone has to be set up again if it stops
	// coming back (six months from now).
	SetUpAgain time.Time
}

// DeviceToken checks a phone's proof and hands it a device token. The proof
// is good for one minute and can be used once, so a copy of it is worthless;
// the key that signed it cannot leave the phone.
func (s *Service) DeviceToken(ctx context.Context, req TokenRequest) (TokenResult, error) {
	now := s.now()
	cert, err := x509.ParseCertificate(req.Certificate)
	if err != nil {
		return TokenResult{}, errProof
	}
	identity, device, err := s.Store.IdentityByFingerprint(ctx, fingerprint(cert.Raw))
	if errors.Is(err, pbx.ErrNotFound) {
		return TokenResult{}, errProof
	}
	if err != nil {
		return TokenResult{}, err
	}
	switch {
	case !device.Enabled || device.RevokedAt != nil,
		identity.ExpiredAt != nil, !now.Before(identity.ExpiresAt),
		!now.Before(cert.NotAfter), now.Before(cert.NotBefore):
		return TokenResult{}, errDeviceGone
	}
	jti, err := verifyProof(req.Proof, cert, device.ID, now)
	if err != nil {
		return TokenResult{}, err
	}
	if err := s.Store.UseProof(ctx, jti, device.ID, now); err != nil {
		if errors.Is(err, pbx.ErrDuplicate) {
			// Someone is replaying a proof they copied.
			s.log().Warn("device proof used twice", "device", device.ID)
			return TokenResult{}, errProof
		}
		return TokenResult{}, err
	}

	// The person must still be there with that extension; this is the same
	// check Asterisk's view makes on every lookup.
	tenant, _, err := s.Store.DevicePrincipalFor(ctx, device.ID, now)
	if errors.Is(err, pbx.ErrNotFound) {
		return TokenResult{}, errDeviceGone
	}
	if err != nil {
		return TokenResult{}, err
	}

	out := TokenResult{DeviceID: device.ID, CertNotAfter: identity.CertNotAfter, SetUpAgain: now.Add(InactivityWindow)}
	var renewed *CertUpdate
	if len(req.CSR) > 0 {
		update, pem, notAfter, err := s.renew(ctx, req.CSR, identity)
		if err != nil {
			return TokenResult{}, err
		}
		renewed, out.Certificate, out.CertNotAfter = update, pem, notAfter
	}
	if err := s.Store.TouchIdentity(ctx, device.ID, renewed, clean(req.AppVersion), clean(req.OSVersion),
		now, out.SetUpAgain); err != nil {
		return TokenResult{}, err
	}

	token, claims, err := s.Tokens.IssueDevice(device.ID, tenant, auth.DeviceScopes(), now)
	if err != nil {
		return TokenResult{}, err
	}
	out.Token, out.ExpiresAt = token, claims.ExpiresAt
	return out, nil
}

// renew signs the phone's key again. The key must be the one the phone
// already has: a new key means setting the phone up again.
func (s *Service) renew(ctx context.Context, csrDER []byte, identity Identity) (*CertUpdate, string, time.Time, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil || csr.CheckSignature() != nil || csr.Subject.CommonName != CertName {
		return nil, "", time.Time{}, errProof
	}
	pub, err := publicKeyDER(csr.PublicKey)
	if err != nil || subtle.ConstantTimeCompare(pub, identity.PublicKey) != 1 {
		return nil, "", time.Time{}, errProof
	}
	chain, err := s.CA.SignCSR(ctx, csrDER, CertName, []string{CertName})
	if err != nil {
		return nil, "", time.Time{}, err
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return nil, "", time.Time{}, err
	}
	var certs strings.Builder
	for _, der := range chain {
		certs.WriteString(pemCert(der))
	}
	return &CertUpdate{Serial: leaf.SerialNumber.String(), Fingerprint: fingerprint(chain[0]), NotAfter: leaf.NotAfter.UTC()},
		certs.String(), leaf.NotAfter.UTC(), nil
}

// DevicePrincipal is auth.DeviceAuthenticator: who a phone's token speaks
// for. The app always acts as an ordinary person, whatever its owner's own
// role is, so a device token can never reach the admin area.
func (s *Service) DevicePrincipal(ctx context.Context, device uuid.UUID, now time.Time) (auth.Principal, error) {
	tenant, user, err := s.Store.DevicePrincipalFor(ctx, device, now.UTC())
	if err != nil {
		if errors.Is(err, pbx.ErrNotFound) {
			return auth.Principal{}, auth.ErrNotFound
		}
		return auth.Principal{}, err
	}
	id := device
	return auth.Principal{
		Type: auth.TypeUser, ID: user.String(), TenantID: tenant,
		Role: auth.RoleUser, Scopes: auth.DeviceScopes(), DeviceID: &id,
	}, nil
}

// ExpireOverdue marks every phone that has not been in touch for six months
// (ADR-077) and clears away proofs that could no longer be replayed. It is
// run once an hour; nothing else waits on it, because Asterisk's own view
// stops an expired phone at the same moment.
func (s *Service) ExpireOverdue(ctx context.Context) (int, error) {
	now := s.now()
	expired, err := s.Store.ExpireIdentities(ctx, now)
	if err != nil {
		return 0, err
	}
	for _, d := range expired {
		s.log().Info("phone expired after 7 days with no contact", "device", d.ID, "name", d.Name)
	}
	if err := s.Store.DeleteUsedProofs(ctx, now.Add(-2*ProofTTL)); err != nil {
		return len(expired), err
	}
	return len(expired), nil
}

// expireEvery is how often phones that have gone quiet are looked for.
const expireEvery = time.Hour

// RunExpiry marks phones expired once an hour until ctx ends.
func (s *Service) RunExpiry(ctx context.Context) {
	t := time.NewTicker(expireEvery)
	defer t.Stop()
	for {
		if _, err := s.ExpireOverdue(ctx); err != nil && ctx.Err() == nil {
			s.log().Error("expiring phones failed; trying again in an hour", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// clean keeps a phone's reported app and iOS versions short and printable.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len([]rune(s)) > 40 {
		s = string([]rune(s)[:40])
	}
	return s
}
