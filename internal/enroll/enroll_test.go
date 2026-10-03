package enroll

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
)

// phone stands in for the app: a key that never leaves it (here, never
// leaves the test), the certificate Linx signed for it, and the proofs it
// signs with that key.
type phone struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
}

func newPhone(t *testing.T) *phone {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &phone{key: key}
}

func (p *phone) csr(t *testing.T, name string) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
	}, p.key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func (p *phone) proof(t *testing.T, device uuid.UUID, now time.Time) string {
	t.Helper()
	return p.proofAs(t, device.String(), ProofAudience, now, uuid.Must(uuid.NewV7()))
}

func (p *phone) proofAs(t *testing.T, subject, audience string, now time.Time, jti uuid.UUID) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: p.key}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(jwt.Claims{
		Subject: subject, Audience: jwt.Audience{audience}, ID: jti.String(),
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(ProofTTL)),
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type fixture struct {
	svc        *Service
	store      *fakeStore
	ca         *testCA
	tenant     uuid.UUID
	sara, omar uuid.UUID
	now        time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{tenant: uuid.Must(uuid.NewV7()), sara: uuid.Must(uuid.NewV7()), omar: uuid.Must(uuid.NewV7())}
	f.now = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	tokens, err := auth.NewTokens(ed25519From(key))
	if err != nil {
		t.Fatal(err)
	}
	f.store = newFakeStore()
	f.store.targets[f.sara] = Target{UserID: f.sara, ExtensionID: uuid.Must(uuid.NewV7()), PersonName: "Sara Haddad", Number: "101"}
	f.store.targets[f.omar] = Target{UserID: f.omar, ExtensionID: uuid.Must(uuid.NewV7()), PersonName: "Omar", Number: "102"}
	f.ca = newTestCA(func() time.Time { return f.now })
	f.svc = &Service{Store: f.store, Tokens: tokens, CA: f.ca, CARoot: []byte("-----BEGIN CERTIFICATE-----\ntest\n"),
		Now: func() time.Time { return f.now }}
	return f
}

func (f *fixture) ctx(p auth.Principal) context.Context {
	return auth.WithPrincipal(context.Background(), p)
}

func (f *fixture) person(id uuid.UUID) auth.Principal {
	return auth.Principal{Type: auth.TypeUser, ID: id.String(), TenantID: f.tenant, Role: auth.RoleUser,
		Scopes: auth.DeviceScopes()}
}

func (f *fixture) admin() auth.Principal {
	return auth.Principal{Type: auth.TypeUser, ID: uuid.Must(uuid.NewV7()).String(), TenantID: f.tenant,
		Role: auth.RoleAdmin, Scopes: []string{"devices:read", "devices:write"}}
}

func status(err error) int {
	var e *apihttp.Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func TestCreateTicketRules(t *testing.T) {
	f := newFixture(t)

	// Anyone may set up their own phone, with no scopes at all.
	own, err := f.svc.CreateTicket(f.ctx(f.person(f.sara)), TicketInput{DeviceName: "Sara's iPhone"})
	if err != nil {
		t.Fatalf("my own phone: %v", err)
	}
	if len(own.Code) != codeLen || own.Token == "" || own.UserID != f.sara || own.Number != "101" {
		t.Fatalf("ticket: %+v", own)
	}
	if !own.ExpiresAt.Equal(f.now.Add(10 * time.Minute)) {
		t.Errorf("a ticket lasts until %v, want 10 minutes", own.ExpiresAt)
	}

	// Someone else's needs devices:write.
	if _, err := f.svc.CreateTicket(f.ctx(f.person(f.sara)), TicketInput{UserID: f.omar, DeviceName: "Omar's iPad"}); status(err) != http.StatusForbidden {
		t.Errorf("someone else's phone without devices:write: %v", err)
	}
	if _, err := f.svc.CreateTicket(f.ctx(f.admin()), TicketInput{UserID: f.omar, DeviceName: "Omar's iPad"}); err != nil {
		t.Errorf("an admin adding Omar's phone: %v", err)
	}

	// A phone's own token can never set up another phone.
	app := f.person(f.sara)
	device := uuid.Must(uuid.NewV7())
	app.DeviceID = &device
	if _, err := f.svc.CreateTicket(f.ctx(app), TicketInput{DeviceName: "Another iPhone"}); status(err) != http.StatusForbidden {
		t.Errorf("an app adding a phone: %v", err)
	}

	// A half-signed-in session (password accepted, authenticator code not
	// yet) can't set a phone up.
	pending := f.person(f.sara)
	pending.Pending, pending.Scopes = true, nil
	if _, err := f.svc.CreateTicket(f.ctx(pending), TicketInput{DeviceName: "Sara's iPhone"}); status(err) != http.StatusForbidden {
		t.Errorf("a half-finished sign-in: %v", err)
	}

	// A browser session must have confirmed it's them lately, as it must
	// to be shown a device's SIP login (docs/ADMIN.md §7).
	stale := auth.WithSession(f.ctx(f.person(f.sara)), auth.UserSession{
		ID: uuid.Must(uuid.NewV7()), TenantID: f.tenant, UserID: f.sara})
	if _, err := f.svc.CreateTicket(stale, TicketInput{DeviceName: "Sara's iPhone"}); status(err) != http.StatusForbidden {
		t.Errorf("a session that hasn't confirmed lately: %v", err)
	}
	confirmed := f.now.Add(-time.Minute)
	fresh := auth.WithSession(f.ctx(f.person(f.sara)), auth.UserSession{
		ID: uuid.Must(uuid.NewV7()), TenantID: f.tenant, UserID: f.sara, ConfirmedAt: &confirmed})
	if _, err := f.svc.CreateTicket(fresh, TicketInput{DeviceName: "Sara's iPhone"}); err != nil {
		t.Errorf("a session that just confirmed: %v", err)
	}

	// Names, kinds and people that can't have one.
	for _, in := range []TicketInput{
		{DeviceName: "  "},
		{DeviceName: "A phone\nwith a line break"},
		{DeviceName: "Desk phone", Kind: pbx.KindDesk},
		{DeviceName: "iPhone", Delivery: "carrier pigeon"},
	} {
		if _, err := f.svc.CreateTicket(f.ctx(f.person(f.sara)), in); status(err) != http.StatusBadRequest {
			t.Errorf("%+v: %v", in, err)
		}
	}
	if _, err := f.svc.CreateTicket(f.ctx(f.admin()), TicketInput{UserID: uuid.New(), DeviceName: "Nobody's iPhone"}); status(err) != http.StatusConflict {
		t.Errorf("a phone for someone with no extension: %v", err)
	}

	// The list shows a person their own and an admin everyone's.
	mine, err := f.svc.Tickets(f.ctx(f.person(f.sara)))
	if err != nil || len(mine) != 2 || mine[0].UserID != f.sara {
		t.Fatalf("my waiting phones: %+v, %v", mine, err)
	}
	all, err := f.svc.Tickets(f.ctx(f.admin()))
	if err != nil || len(all) != 3 {
		t.Fatalf("every waiting phone: %+v, %v", all, err)
	}

	// Cancelling stops it, and someone else's ticket isn't mine to cancel.
	if err := f.svc.CancelTicket(f.ctx(f.person(f.omar)), own.ID); status(err) != http.StatusForbidden {
		t.Errorf("cancelling someone else's: %v", err)
	}
	if err := f.svc.CancelTicket(f.ctx(f.person(f.sara)), own.ID); err != nil {
		t.Fatalf("cancelling my own: %v", err)
	}
	if left, err := f.svc.Tickets(f.ctx(f.person(f.sara))); err != nil || len(left) != 1 {
		t.Errorf("after cancelling: %+v, %v", left, err)
	}
}

// setUp makes a ticket and redeems it, as a phone scanning the QR code does.
func (f *fixture) setUp(t *testing.T, user uuid.UUID) (*phone, Enrolled) {
	t.Helper()
	ticket, err := f.svc.CreateTicket(f.ctx(f.person(user)), TicketInput{DeviceName: "Sara's iPhone"})
	if err != nil {
		t.Fatal(err)
	}
	p := newPhone(t)
	out, err := f.svc.Redeem(context.Background(), RedeemRequest{
		Token: ticket.Token, CSR: p.csr(t, CertName), AppVersion: "0.1.0", OSVersion: "26.0"})
	if err != nil {
		t.Fatal(err)
	}
	identity, _, err := f.store.IdentityByDevice(context.Background(), out.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(certDER(t, out.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	p.cert = cert
	if len(identity.CertFingerprint) == 0 {
		t.Fatal("no certificate fingerprint stored")
	}
	return p, out
}

func TestSetUpAPhone(t *testing.T) {
	f := newFixture(t)
	p, out := f.setUp(t, f.sara)

	if out.PersonName != "Sara Haddad" || out.Extension != "101" || out.DeviceName != "Sara's iPhone" {
		t.Errorf("what the phone was told: %+v", out)
	}
	if !out.ExpiresAt.Equal(f.now.Add(InactivityWindow)) {
		t.Errorf("set up again by %v, want 7 days", out.ExpiresAt)
	}
	device := f.store.devices[out.DeviceID]
	if device.Kind != pbx.KindIOS || !device.Enabled || device.SIPUsername == "" {
		t.Fatalf("the phone that was made: %+v", device)
	}
	// The SIP password is never handed out: the app asks for a line later.
	if out.Certificate == "" || out.CARoot == "" {
		t.Error("the phone got no certificate or CA to pin")
	}

	// The same ticket can't set up a second phone.
	ticket := f.store.tickets[firstTicketID(f.store)]
	if ticket.UsedAt == nil {
		t.Fatal("the ticket wasn't marked used")
	}

	// A token needs a proof signed by the key inside the phone.
	res, err := f.svc.DeviceToken(context.Background(), TokenRequest{
		Certificate: p.cert.Raw, Proof: p.proof(t, out.DeviceID, f.now)})
	if err != nil {
		t.Fatalf("asking for a token: %v", err)
	}
	if res.Token == "" || res.DeviceID != out.DeviceID || !res.ExpiresAt.After(f.now) {
		t.Fatalf("token: %+v", res)
	}

	// That token speaks for Sara, as an ordinary person, never an admin.
	claims, err := f.svc.Tokens.Verify(res.Token, f.now)
	if err != nil || claims.Kind != auth.KindDevice || claims.ClientID != out.DeviceID {
		t.Fatalf("the token's claims: %+v, %v", claims, err)
	}
	principal, err := f.svc.DevicePrincipal(context.Background(), out.DeviceID, f.now)
	if err != nil || principal.ID != f.sara.String() || principal.Role != auth.RoleUser || principal.DeviceID == nil {
		t.Fatalf("who the token speaks for: %+v, %v", principal, err)
	}
	for _, scope := range []string{"devices:write", "users:write", "settings:write", "calls:control"} {
		if principal.Has(scope) {
			t.Errorf("an app's token holds %s", scope)
		}
	}
}

func TestProofRules(t *testing.T) {
	f := newFixture(t)
	p, out := f.setUp(t, f.sara)

	// A proof may be used once.
	jti := uuid.Must(uuid.NewV7())
	proof := p.proofAs(t, out.DeviceID.String(), ProofAudience, f.now, jti)
	if _, err := f.svc.DeviceToken(context.Background(), TokenRequest{Certificate: p.cert.Raw, Proof: proof}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.DeviceToken(context.Background(), TokenRequest{Certificate: p.cert.Raw, Proof: proof}); status(err) != http.StatusUnauthorized {
		t.Errorf("replaying a proof: %v", err)
	}

	// Another phone's key, another phone's name, another use, and an old
	// one are all refused.
	other := newPhone(t)
	for name, req := range map[string]TokenRequest{
		"another key":    {Certificate: p.cert.Raw, Proof: other.proof(t, out.DeviceID, f.now)},
		"another phone":  {Certificate: p.cert.Raw, Proof: p.proofAs(t, uuid.New().String(), ProofAudience, f.now, uuid.Must(uuid.NewV7()))},
		"another use":    {Certificate: p.cert.Raw, Proof: p.proofAs(t, out.DeviceID.String(), "linx-api", f.now, uuid.Must(uuid.NewV7()))},
		"two hours old":  {Certificate: p.cert.Raw, Proof: p.proofAs(t, out.DeviceID.String(), ProofAudience, f.now.Add(-2*time.Hour), uuid.Must(uuid.NewV7()))},
		"no certificate": {Certificate: []byte("not a certificate"), Proof: p.proof(t, out.DeviceID, f.now)},
	} {
		if _, err := f.svc.DeviceToken(context.Background(), req); status(err) != http.StatusUnauthorized {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRenewAndExpire(t *testing.T) {
	f := newFixture(t)
	p, out := f.setUp(t, f.sara)

	// Six days later the phone renews its certificate for the same key.
	f.now = f.now.Add(6 * 24 * time.Hour)
	res, err := f.svc.DeviceToken(context.Background(), TokenRequest{
		Certificate: p.cert.Raw, Proof: p.proof(t, out.DeviceID, f.now), CSR: p.csr(t, CertName)})
	if err != nil || res.Certificate == "" {
		t.Fatalf("renewing: %+v, %v", res, err)
	}
	if !res.SetUpAgain.Equal(f.now.Add(InactivityWindow)) {
		t.Errorf("after renewing, set up again by %v", res.SetUpAgain)
	}
	renewed, err := x509.ParseCertificate(certDER(t, res.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	// The old certificate is no longer the phone's.
	if _, err := f.svc.DeviceToken(context.Background(), TokenRequest{
		Certificate: p.cert.Raw, Proof: p.proof(t, out.DeviceID, f.now)}); status(err) != http.StatusUnauthorized {
		t.Errorf("the replaced certificate still works: %v", err)
	}
	p.cert = renewed
	if _, err := f.svc.DeviceToken(context.Background(), TokenRequest{
		Certificate: p.cert.Raw, Proof: p.proof(t, out.DeviceID, f.now)}); err != nil {
		t.Fatalf("with the renewed certificate: %v", err)
	}

	// A different key is not that phone.
	other := newPhone(t)
	if _, err := f.svc.DeviceToken(context.Background(), TokenRequest{
		Certificate: p.cert.Raw, Proof: p.proof(t, out.DeviceID, f.now), CSR: other.csr(t, CertName)}); status(err) != http.StatusUnauthorized {
		t.Errorf("renewing with another key: %v", err)
	}

	// Seven days with no contact at all: the phone has to be set up again.
	f.now = f.now.Add(InactivityWindow + time.Hour)
	n, err := f.svc.ExpireOverdue(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("expiring: %d, %v", n, err)
	}
	if _, err := f.svc.DeviceToken(context.Background(), TokenRequest{
		Certificate: p.cert.Raw, Proof: p.proof(t, out.DeviceID, f.now)}); status(err) != http.StatusUnauthorized {
		t.Errorf("an expired phone still gets a token: %v", err)
	}
	if _, err := f.svc.DevicePrincipal(context.Background(), out.DeviceID, f.now); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("an expired phone still speaks for its person: %v", err)
	}
}

func TestRedeemRefusals(t *testing.T) {
	f := newFixture(t)
	p := newPhone(t)

	ticket, err := f.svc.CreateTicket(f.ctx(f.person(f.sara)), TicketInput{DeviceName: "Sara's iPhone", Delivery: DeliveryByHand})
	if err != nil {
		t.Fatal(err)
	}

	// A certificate request for any other name is refused: a phone's
	// certificate must never be able to stand in for a Linx service.
	if _, err := f.svc.Redeem(context.Background(), RedeemRequest{Token: ticket.Token, CSR: p.csr(t, "linx-asterisk")}); status(err) != http.StatusBadRequest {
		t.Errorf("a certificate request for a service's name: %v", err)
	}
	if f.ca.signed != 0 {
		t.Error("the CA was asked to sign a refused request")
	}

	// A wrong code matches no ticket and is simply refused (guessing is
	// stopped by the rate limit on the handler).
	for range MaxAttempts {
		if _, err := f.svc.Redeem(context.Background(), RedeemRequest{Code: "WRONGCOD", CSR: p.csr(t, CertName)}); status(err) != http.StatusBadRequest {
			t.Fatalf("a wrong code: %v", err)
		}
	}
	if _, err := f.svc.Redeem(context.Background(), RedeemRequest{Code: "22222222", CSR: p.csr(t, CertName)}); status(err) != http.StatusBadRequest {
		t.Errorf("a code nobody has: %v", err)
	}

	// The right code still works (wrong ones for a code nobody has don't
	// touch this ticket), and only once.
	if _, err := f.svc.Redeem(context.Background(), RedeemRequest{Code: ticket.Code, CSR: p.csr(t, CertName)}); err != nil {
		t.Fatalf("the code on the screen: %v", err)
	}
	if _, err := f.svc.Redeem(context.Background(), RedeemRequest{Token: ticket.Token, CSR: p.csr(t, CertName)}); status(err) != http.StatusBadRequest {
		t.Errorf("using a ticket twice: %v", err)
	}

	// An expired ticket, and one that was cancelled.
	second, err := f.svc.CreateTicket(f.ctx(f.person(f.sara)), TicketInput{DeviceName: "Sara's iPad"})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(TicketTTL + time.Minute)
	if _, err := f.svc.Redeem(context.Background(), RedeemRequest{Token: second.Token, CSR: p.csr(t, CertName)}); status(err) != http.StatusBadRequest {
		t.Errorf("an expired ticket: %v", err)
	}
}

func certDER(t *testing.T, chainPEM string) []byte {
	t.Helper()
	block, _ := pemDecode([]byte(chainPEM))
	if block == nil {
		t.Fatal("no certificate in the chain")
	}
	return block
}

func firstTicketID(f *fakeStore) uuid.UUID {
	for id := range f.tickets {
		return id
	}
	return uuid.Nil
}

// ed25519From makes a signing key from 32 bytes, as the installer's secret
// does.
func ed25519From(seed []byte) ed25519.PrivateKey { return ed25519.NewKeyFromSeed(seed) }

// pemDecode returns the first PEM block's bytes.
func pemDecode(b []byte) ([]byte, []byte) {
	block, rest := pem.Decode(b)
	if block == nil {
		return nil, rest
	}
	return block.Bytes, rest
}
