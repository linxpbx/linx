package main

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/email"
	"linxpbx.com/linx/internal/install"
)

// accountMail is auth.Mailer on Linx's email queue: the reset link and
// "your password was changed" (ADR-067, docs/ui/SCREENS_PHASE1F.md §5.3).
type accountMail struct {
	email *email.Service
	// web is https://<domain>[:port], what links start with.
	web string
}

func (m accountMail) On(ctx context.Context, tenant uuid.UUID) (bool, error) {
	return m.email.On(ctx, tenant)
}

func (m accountMail) send(ctx context.Context, u auth.User, kind, action string, c email.Content) error {
	_, err := m.email.Enqueue(ctx, u.TenantID, kind, []string{u.Email}, c, auth.AuditEntry{
		Actor: "user:" + u.ID.String(), Action: action, Result: auth.ResultOK,
		Detail: map[string]any{"user": u.ID.String()},
	})
	return err
}

func (m accountMail) ResetLink(ctx context.Context, u auth.User, token string) error {
	return m.send(ctx, u, email.KindReset, "email.queue", email.Content{Subject: "Choose a new Linx password",
		Text: fmt.Sprintf("Hello %s,\n\nSomeone (hopefully you) asked to reset your Linx password at %s. "+
			"The link works once, for 30 minutes:\n%s/reset/%s\n\n"+
			"You'll still be asked for your authenticator app or passkey, if you have one.\n\n"+
			"If this wasn't you, ignore this email: nothing changes.",
			u.Name, strings.TrimPrefix(m.web, "https://"), m.web, token)})
}

func (m accountMail) PasswordChanged(ctx context.Context, u auth.User, at time.Time, ip netip.Addr, userAgent string) error {
	from := describeBrowser(userAgent)
	if ip.IsValid() {
		from += " (" + ip.String() + ")"
	}
	return m.send(ctx, u, email.KindPasswordChanged, "email.queue", email.Content{Subject: "Your Linx password was changed",
		Text: fmt.Sprintf("Hello %s,\n\nYour Linx password at %s was changed on %s from %s. "+
			"You've been signed out everywhere else.\n\n"+
			"If this wasn't you, tell your admin now.",
			u.Name, strings.TrimPrefix(m.web, "https://"), at.Local().Format("2 January 2006 at 15:04 MST"), from)})
}

// describeBrowser is "Chrome on Mac" from a User-Agent.
func describeBrowser(ua string) string {
	name := install.BrowserName(ua)
	switch {
	case strings.Contains(ua, "iPhone"):
		return name + " on iPhone"
	case strings.Contains(ua, "iPad"):
		return name + " on iPad"
	case strings.Contains(ua, "Android"):
		return name + " on Android"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		return name + " on Mac"
	case strings.Contains(ua, "Windows"):
		return name + " on Windows"
	case strings.Contains(ua, "Linux"):
		return name + " on Linux"
	}
	return name
}
