package pbx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
)

// Service is what the API's extension and device endpoints do. Caller
// mistakes come back as *apihttp.Error.
type Service struct {
	Store Store
	Now   func() time.Time
	// Domain is this server's own domain (docs/PBX.md §5): device
	// credentials are shown with server "sip.<domain>".
	Domain string
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

// ETag is an extension or device version as an HTTP entity tag.
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

var errExtensionChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
	Detail: "This extension was changed since you read it. Fetch it again and retry."}

var errDeviceChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
	Detail: "This device was changed since you read it. Fetch it again and retry."}

// numberPattern matches migration 0005's CHECK on extension.number.
var numberPattern = regexp.MustCompile(`^[0-9]{2,6}$`)

func checkNumber(number string) error {
	if !numberPattern.MatchString(number) {
		return invalid("number_invalid", "An extension number is 2 to 6 digits.")
	}
	return nil
}

func checkDisplayName(name string) error {
	n := len([]rune(name))
	if n < 1 || n > 100 {
		return invalid("display_name_invalid", "Give it a name of 1 to 100 characters.")
	}
	return nil
}

// ExtensionInput is a new extension.
type ExtensionInput struct {
	Number      string
	DisplayName string
	Email       string
	Enabled     *bool
}

// CreateExtension adds an extension.
func (s *Service) CreateExtension(ctx context.Context, in ExtensionInput) (Extension, error) {
	if err := checkNumber(in.Number); err != nil {
		return Extension{}, err
	}
	if err := checkDisplayName(in.DisplayName); err != nil {
		return Extension{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Extension{}, err
	}
	p, a, err := audit(ctx, "extension.create", "extension:"+id.String())
	if err != nil {
		return Extension{}, err
	}
	now := s.Now().UTC()
	e := Extension{
		ID: id, TenantID: p.TenantID, Number: in.Number, DisplayName: in.DisplayName, Email: in.Email,
		Enabled: in.Enabled == nil || *in.Enabled, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	a.Detail = map[string]any{"number": e.Number, "display_name": e.DisplayName, "enabled": e.Enabled}
	if err := s.Store.CreateExtension(ctx, e, a); err != nil {
		if errors.Is(err, ErrDuplicate) {
			return Extension{}, &apihttp.Error{Status: http.StatusConflict, Code: "number_duplicate",
				Detail: fmt.Sprintf("Extension %s already exists.", e.Number)}
		}
		return Extension{}, err
	}
	return e, nil
}

// GetExtension returns one of the caller's extensions.
func (s *Service) GetExtension(ctx context.Context, id uuid.UUID) (Extension, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return Extension{}, errNoPrincipal
	}
	e, err := s.Store.Extension(ctx, p.TenantID, id)
	if errors.Is(err, ErrNotFound) {
		return Extension{}, notFound("extension")
	}
	return e, err
}

// ListExtensions returns a page of the caller's extensions, newest first.
func (s *Service) ListExtensions(ctx context.Context, before *uuid.UUID, limit int) ([]Extension, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipal
	}
	return s.Store.ListExtensions(ctx, p.TenantID, before, limit)
}

// ExtensionPatch is a JSON Merge Patch of an extension; nil fields stay as
// they are.
type ExtensionPatch struct {
	Number      *string
	DisplayName *string
	Email       *string
	Enabled     *bool
}

// UpdateExtension applies patch. ifMatch, when not empty, must match the
// extension's current ETag (412 otherwise).
func (s *Service) UpdateExtension(ctx context.Context, id uuid.UUID, patch ExtensionPatch, ifMatch string) (Extension, error) {
	e, err := s.GetExtension(ctx, id)
	if err != nil {
		return Extension{}, err
	}
	if ifMatch != "" && !matchETag(ifMatch, e.Version) {
		return Extension{}, errExtensionChanged
	}
	_, a, err := audit(ctx, "extension.update", "extension:"+id.String())
	if err != nil {
		return Extension{}, err
	}
	changes := map[string]any{}
	if patch.Number != nil {
		if err := checkNumber(*patch.Number); err != nil {
			return Extension{}, err
		}
		e.Number = *patch.Number
		changes["number"] = e.Number
	}
	if patch.DisplayName != nil {
		if err := checkDisplayName(*patch.DisplayName); err != nil {
			return Extension{}, err
		}
		e.DisplayName = *patch.DisplayName
		changes["display_name"] = e.DisplayName
	}
	if patch.Email != nil {
		e.Email = *patch.Email
		changes["email"] = e.Email
	}
	if patch.Enabled != nil {
		e.Enabled = *patch.Enabled
		changes["enabled"] = e.Enabled
	}
	e.UpdatedAt = s.Now().UTC()
	a.Detail = changes
	updated, err := s.Store.UpdateExtension(ctx, e, a)
	if errors.Is(err, ErrVersionChanged) {
		return Extension{}, errExtensionChanged
	}
	if errors.Is(err, ErrNotFound) {
		return Extension{}, notFound("extension")
	}
	if errors.Is(err, ErrDuplicate) {
		return Extension{}, &apihttp.Error{Status: http.StatusConflict, Code: "number_duplicate",
			Detail: fmt.Sprintf("Extension %s already exists.", e.Number)}
	}
	return updated, err
}

// DeleteExtension deletes an extension (freeing its number) and revokes
// every device under it.
func (s *Service) DeleteExtension(ctx context.Context, id uuid.UUID) error {
	p, a, err := audit(ctx, "extension.delete", "extension:"+id.String())
	if err != nil {
		return err
	}
	err = s.Store.DeleteExtension(ctx, p.TenantID, id, s.Now().UTC(), a)
	if errors.Is(err, ErrNotFound) {
		return notFound("extension")
	}
	return err
}

func checkDeviceName(name string) error {
	n := len([]rune(name))
	if n < 1 || n > 100 {
		return invalid("name_invalid", "Give it a name of 1 to 100 characters.")
	}
	return nil
}

func checkDeviceKind(kind string) error {
	if kind != KindSoftphone {
		return invalid("kind_not_supported", "Only softphone devices work in this version of Linx; web, iOS and desk phone support is coming.")
	}
	return nil
}

// DeviceInput is a new device.
type DeviceInput struct {
	Name    string
	Kind    string // defaults to KindSoftphone
	Enabled *bool
}

// Credentials is a device with the SIP login shown this once (ADR-033).
type Credentials struct {
	Device
	Password string
}

// CreateDevice adds a device under extension and returns it with its SIP
// login, shown this once.
func (s *Service) CreateDevice(ctx context.Context, extension uuid.UUID, in DeviceInput) (Credentials, error) {
	ext, err := s.GetExtension(ctx, extension)
	if err != nil {
		return Credentials{}, err
	}
	if err := checkDeviceName(in.Name); err != nil {
		return Credentials{}, err
	}
	kind := in.Kind
	if kind == "" {
		kind = KindSoftphone
	}
	if err := checkDeviceKind(kind); err != nil {
		return Credentials{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Credentials{}, err
	}
	p, a, err := audit(ctx, "device.create", "device:"+id.String())
	if err != nil {
		return Credentials{}, err
	}
	username, password := NewSIPUsername(), NewDevicePassword()
	now := s.Now().UTC()
	d := Device{
		ID: id, TenantID: p.TenantID, ExtensionID: ext.ID, Name: in.Name, Kind: kind,
		SIPUsername: username, DigestHash: DigestHash(username, password),
		Enabled: in.Enabled == nil || *in.Enabled, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	a.Detail = map[string]any{"extension_id": ext.ID, "name": d.Name, "kind": d.Kind, "sip_username": d.SIPUsername}
	if err := s.Store.CreateDevice(ctx, d, a); err != nil {
		return Credentials{}, err
	}
	return Credentials{Device: d, Password: password}, nil
}

// GetDevice returns one of the caller's devices.
func (s *Service) GetDevice(ctx context.Context, id uuid.UUID) (Device, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return Device{}, errNoPrincipal
	}
	d, err := s.Store.Device(ctx, p.TenantID, id)
	if errors.Is(err, ErrNotFound) {
		return Device{}, notFound("device")
	}
	return d, err
}

// ListDevices returns a page of extension's devices, newest first.
func (s *Service) ListDevices(ctx context.Context, extension uuid.UUID, before *uuid.UUID, limit int) ([]Device, error) {
	if _, err := s.GetExtension(ctx, extension); err != nil {
		return nil, err
	}
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipal
	}
	return s.Store.ListDevicesByExtension(ctx, p.TenantID, extension, before, limit)
}

// DevicePatch is a JSON Merge Patch of a device; nil fields stay as they are.
type DevicePatch struct {
	Name    *string
	Enabled *bool
}

// UpdateDevice applies patch. ifMatch, when not empty, must match the
// device's current ETag (412 otherwise).
func (s *Service) UpdateDevice(ctx context.Context, id uuid.UUID, patch DevicePatch, ifMatch string) (Device, error) {
	d, err := s.GetDevice(ctx, id)
	if err != nil {
		return Device{}, err
	}
	if ifMatch != "" && !matchETag(ifMatch, d.Version) {
		return Device{}, errDeviceChanged
	}
	_, a, err := audit(ctx, "device.update", "device:"+id.String())
	if err != nil {
		return Device{}, err
	}
	changes := map[string]any{}
	if patch.Name != nil {
		if err := checkDeviceName(*patch.Name); err != nil {
			return Device{}, err
		}
		d.Name = *patch.Name
		changes["name"] = d.Name
	}
	if patch.Enabled != nil {
		d.Enabled = *patch.Enabled
		changes["enabled"] = d.Enabled
	}
	d.UpdatedAt = s.Now().UTC()
	a.Detail = changes
	updated, err := s.Store.UpdateDevice(ctx, d, a)
	if errors.Is(err, ErrVersionChanged) {
		return Device{}, errDeviceChanged
	}
	if errors.Is(err, ErrNotFound) {
		return Device{}, notFound("device")
	}
	return updated, err
}

// RevokeDevice logs a device out at once. Revoking a revoked device
// succeeds and changes nothing.
func (s *Service) RevokeDevice(ctx context.Context, id uuid.UUID) error {
	p, a, err := audit(ctx, "device.revoke", "device:"+id.String())
	if err != nil {
		return err
	}
	_, err = s.Store.RevokeDevice(ctx, p.TenantID, id, s.Now().UTC(), a)
	if errors.Is(err, ErrNotFound) {
		return notFound("device")
	}
	return err
}

// ResetDevicePassword issues a new SIP password for a device, shown this
// once; its SIP username doesn't change.
func (s *Service) ResetDevicePassword(ctx context.Context, id uuid.UUID) (Credentials, error) {
	d, err := s.GetDevice(ctx, id)
	if err != nil {
		return Credentials{}, err
	}
	_, a, err := audit(ctx, "device.reset_password", "device:"+id.String())
	if err != nil {
		return Credentials{}, err
	}
	password := NewDevicePassword()
	d.DigestHash = DigestHash(d.SIPUsername, password)
	d.UpdatedAt = s.Now().UTC()
	updated, err := s.Store.UpdateDevice(ctx, d, a)
	if errors.Is(err, ErrVersionChanged) {
		return Credentials{}, errDeviceChanged
	}
	if errors.Is(err, ErrNotFound) {
		return Credentials{}, notFound("device")
	}
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{Device: updated, Password: password}, nil
}

// SIPServer is "sip.<domain>", where devices connect (docs/PBX.md §5).
func (s *Service) SIPServer() string { return "sip." + s.Domain }
