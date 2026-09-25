package pbx

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
)

// WebPhone is a browser session's phone line (docs/WEB.md §5): its web
// device, with the password shown this once (the page keeps it in memory
// only), and the extension it answers for.
type WebPhone struct {
	Device    Device
	Password  string
	Extension Extension
}

var errNoExtension = &apihttp.Error{Status: http.StatusConflict, Code: "no_extension",
	Detail: "You don't have an extension yet, so you can't make or take calls. Ask an admin to give you one."}

var errExtensionUnavailable = &apihttp.Error{Status: http.StatusConflict, Code: "extension_unavailable",
	Detail: "Your extension is turned off or was deleted, so you can't make or take calls. Ask an admin."}

// IssueWebPhone gives the session its phone line for extension (the
// person's own, which the caller looked up): a web device tied to this
// session, created the first time and given a fresh password every time
// after (a reloaded page has lost the old one). Asterisk accepts it only
// while the session is live (migration 0013).
func (s *Service) IssueWebPhone(ctx context.Context, sess auth.UserSession, extension *uuid.UUID) (WebPhone, error) {
	if extension == nil {
		return WebPhone{}, errNoExtension
	}
	ext, err := s.Store.Extension(ctx, sess.TenantID, *extension)
	if errors.Is(err, ErrNotFound) || err == nil && (!ext.Enabled || ext.DeletedAt != nil) {
		return WebPhone{}, errExtensionUnavailable
	}
	if err != nil {
		return WebPhone{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return WebPhone{}, err
	}
	now := s.Now().UTC()
	username, password := NewSIPUsername(), NewDevicePassword()
	sessionID := sess.ID
	d := Device{
		ID: id, TenantID: sess.TenantID, ExtensionID: ext.ID, Name: "Web browser", Kind: KindWeb,
		SIPUsername: username, Enabled: true,
		UserSessionID: &sessionID, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	a := auth.AuditEntry{TenantID: &sess.TenantID, Actor: auth.TypeUser + ":" + sess.UserID.String(),
		IP: auth.ClientIPFromContext(ctx), Action: "device.web_phone", Target: "session:" + sess.ID.String(),
		Result: auth.ResultOK, Detail: map[string]any{"extension_id": ext.ID}}
	// The session's existing line keeps its username, so the digest is
	// computed for whichever username the line ends up with.
	stored, err := s.Store.IssueWebDevice(ctx, d, func(user string) string { return DigestHash(user, password) }, a)
	if err != nil {
		return WebPhone{}, err
	}
	return WebPhone{Device: stored, Password: password, Extension: ext}, nil
}
