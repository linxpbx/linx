package enroll

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
)

// fakeStore is the database in memory: enough to test the rules, with the
// real SQL covered by internal/store's Docker test.
type fakeStore struct {
	targets    map[uuid.UUID]Target
	tickets    map[uuid.UUID]*Ticket
	identities map[uuid.UUID]*Identity
	devices    map[uuid.UUID]*pbx.Device
	proofs     map[uuid.UUID]bool
	audits     []auth.AuditEntry
	// dead makes DevicePrincipalFor refuse, as a disabled person would.
	dead map[uuid.UUID]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		targets: map[uuid.UUID]Target{}, tickets: map[uuid.UUID]*Ticket{},
		identities: map[uuid.UUID]*Identity{}, devices: map[uuid.UUID]*pbx.Device{},
		proofs: map[uuid.UUID]bool{}, dead: map[uuid.UUID]bool{},
	}
}

func (f *fakeStore) EnrollmentTarget(_ context.Context, _, user uuid.UUID) (Target, error) {
	t, ok := f.targets[user]
	if !ok {
		return Target{}, pbx.ErrNotFound
	}
	return t, nil
}

func (f *fakeStore) CreateEnrollment(_ context.Context, t Ticket, a auth.AuditEntry) error {
	copied := t
	f.tickets[t.ID] = &copied
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) Enrollments(_ context.Context, tenant uuid.UUID, user *uuid.UUID) ([]Ticket, error) {
	var out []Ticket
	for _, t := range f.tickets {
		if t.TenantID != tenant || t.UsedAt != nil || t.CanceledAt != nil {
			continue
		}
		if user != nil && t.UserID != *user {
			continue
		}
		out = append(out, *t)
	}
	return out, nil
}

func (f *fakeStore) Enrollment(_ context.Context, id uuid.UUID) (Ticket, error) {
	t, ok := f.tickets[id]
	if !ok {
		return Ticket{}, pbx.ErrNotFound
	}
	return *t, nil
}

func (f *fakeStore) EnrollmentByCodeHash(_ context.Context, hash []byte) (Ticket, error) {
	for _, t := range f.tickets {
		if bytes.Equal(t.CodeHash, hash) && t.UsedAt == nil && t.CanceledAt == nil {
			return *t, nil
		}
	}
	return Ticket{}, pbx.ErrNotFound
}

func (f *fakeStore) CancelEnrollment(_ context.Context, _, id uuid.UUID, at time.Time, a auth.AuditEntry) error {
	if t, ok := f.tickets[id]; ok && t.UsedAt == nil && t.CanceledAt == nil {
		when := at
		t.CanceledAt = &when
	}
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) FailEnrollmentAttempt(_ context.Context, id uuid.UUID) error {
	if t, ok := f.tickets[id]; ok {
		t.Attempts++
	}
	return nil
}

func (f *fakeStore) RedeemEnrollment(_ context.Context, ticket uuid.UUID, d pbx.Device, i Identity, a auth.AuditEntry) error {
	t, ok := f.tickets[ticket]
	if !ok || t.UsedAt != nil || t.CanceledAt != nil {
		return pbx.ErrNotFound
	}
	used := d.CreatedAt
	t.UsedAt, t.DeviceID = &used, &d.ID
	device, identity := d, i
	f.devices[d.ID], f.identities[d.ID] = &device, &identity
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) IdentityByFingerprint(_ context.Context, fp []byte) (Identity, pbx.Device, error) {
	for id, i := range f.identities {
		if bytes.Equal(i.CertFingerprint, fp) {
			return *i, *f.devices[id], nil
		}
	}
	return Identity{}, pbx.Device{}, pbx.ErrNotFound
}

func (f *fakeStore) IdentityByDevice(_ context.Context, device uuid.UUID) (Identity, pbx.Device, error) {
	i, ok := f.identities[device]
	if !ok {
		return Identity{}, pbx.Device{}, pbx.ErrNotFound
	}
	return *i, *f.devices[device], nil
}

func (f *fakeStore) TouchIdentity(_ context.Context, device uuid.UUID, cert *CertUpdate, app, os string, seen, expires time.Time) error {
	i, ok := f.identities[device]
	if !ok {
		return pbx.ErrNotFound
	}
	i.LastSeenAt, i.ExpiresAt, i.AppVersion, i.OSVersion = seen, expires, app, os
	if cert != nil {
		i.CertSerial, i.CertFingerprint, i.CertNotAfter, i.ExpiredAt = cert.Serial, cert.Fingerprint, cert.NotAfter, nil
	}
	return nil
}

func (f *fakeStore) UseProof(_ context.Context, jti, _ uuid.UUID, _ time.Time) error {
	if f.proofs[jti] {
		return pbx.ErrDuplicate
	}
	f.proofs[jti] = true
	return nil
}

func (f *fakeStore) DevicePrincipalFor(_ context.Context, device uuid.UUID, now time.Time) (uuid.UUID, uuid.UUID, error) {
	i, ok := f.identities[device]
	d := f.devices[device]
	if !ok || f.dead[device] || d == nil || !d.Enabled || d.RevokedAt != nil ||
		i.ExpiredAt != nil || !now.Before(i.ExpiresAt) {
		return uuid.Nil, uuid.Nil, pbx.ErrNotFound
	}
	return i.TenantID, i.UserID, nil
}

func (f *fakeStore) ExpireIdentities(_ context.Context, now time.Time) ([]pbx.Device, error) {
	var out []pbx.Device
	for id, i := range f.identities {
		if i.ExpiredAt == nil && !now.Before(i.ExpiresAt) {
			when := now
			i.ExpiredAt = &when
			out = append(out, *f.devices[id])
		}
	}
	return out, nil
}

func (f *fakeStore) DeleteUsedProofs(_ context.Context, _ time.Time) error { return nil }

// testCA is a certificate authority in the test, standing in for step-ca's
// linx-devices provisioner.
type testCA struct {
	key      *ecdsa.PrivateKey
	cert     *x509.Certificate
	lifetime time.Duration
	now      func() time.Time
	signed   int
}

func newTestCA(now func() time.Time) *testCA {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Linx Internal CA (test)"},
		NotBefore: now().Add(-time.Hour), NotAfter: now().Add(24 * 365 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return &testCA{key: key, cert: cert, lifetime: 7 * 24 * time.Hour, now: now}
}

func (c *testCA) SignCSR(_ context.Context, csrDER []byte, commonName string, sans []string) ([][]byte, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, err
	}
	c.signed++
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(int64(c.signed + 1)), Subject: pkix.Name{CommonName: commonName}, DNSNames: sans,
		NotBefore: c.now().Add(-time.Minute), NotAfter: c.now().Add(c.lifetime),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	return [][]byte{der, c.cert.Raw}, nil
}
