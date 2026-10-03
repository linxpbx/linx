package enroll

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
)

// The app's phone line (docs/PHASE2.md §4, Phase 2 step 4). The phone asks
// for it with its device token every time the app starts, keeps it in
// memory only, and talks SIP to Asterisk through the same /sip relay the
// browser uses. No SIP password is ever stored on the phone, and none is in
// the setup code: this is the only place one is handed out, to a phone that
// has just proved who it is with its Secure Enclave key.

// PhoneLine is a set-up phone's SIP login, shown this once.
type PhoneLine struct {
	Device   pbx.Device
	Password string
	// Extension is what the line answers for.
	Extension pbx.Extension
	// UserID is the person the phone belongs to (its relay credentials are
	// issued for them, as a browser's are).
	UserID uuid.UUID
}

var errNotAPhone = &apihttp.Error{Status: http.StatusBadRequest, Code: "not_a_phone",
	Detail: "This only works for the Linx app on a phone that has been set up."}

// IssuePhoneLine gives the calling phone a fresh SIP password for the device
// it was given when it was set up. The username never changes, so nothing
// else has to be told; the password is new every time, because the app keeps
// it in memory only and has lost the old one.
//
// Only a device token can ask (a browser has /me/web-phone), and the phone
// is read again here, so a phone revoked, expired, disabled or moved to
// another extension in the meantime gets nothing.
func (s *Service) IssuePhoneLine(ctx context.Context) (PhoneLine, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return PhoneLine{}, errNoPrincipal
	}
	if p.DeviceID == nil {
		return PhoneLine{}, errNotAPhone
	}
	now := s.now()
	tenant, user, err := s.Store.DevicePrincipalFor(ctx, *p.DeviceID, now)
	if errors.Is(err, pbx.ErrNotFound) {
		return PhoneLine{}, errDeviceGone
	}
	if err != nil {
		return PhoneLine{}, err
	}
	password := pbx.NewDevicePassword()
	a := auth.AuditEntry{TenantID: &tenant, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: "device.phone_line", Target: "device:" + p.DeviceID.String(), Result: auth.ResultOK}
	d, ext, err := s.Store.IssuePhoneLine(ctx, tenant, *p.DeviceID,
		func(username string) string { return pbx.DigestHash(username, password) }, now, a)
	if errors.Is(err, pbx.ErrNotFound) {
		return PhoneLine{}, errDeviceGone
	}
	if err != nil {
		return PhoneLine{}, err
	}
	return PhoneLine{Device: d, Password: password, Extension: ext, UserID: user}, nil
}
