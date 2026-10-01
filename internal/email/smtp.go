package email

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"time"

	"linxpbx.com/linx/internal/safehttp"
)

// Stages of one send, for the test's checklist
// (docs/ui/SCREENS_PHASE1F.md §5.1 step 3).
const (
	StageConnect     = "connect"
	StageEncrypt     = "encrypt"
	StageCertificate = "certificate"
	StageSignIn      = "sign_in"
	StageSend        = "send"
)

// Stages in order.
var Stages = []string{StageConnect, StageEncrypt, StageCertificate, StageSignIn, StageSend}

// SendError is a failed send: the stage it stopped at, a plain reason for
// the admin, and what the server said (for the admin and the log, never
// for everyone: it can name the account).
type SendError struct {
	Stage, Detail, Why string
	// Blocked: the server's address is on a private network an admin may
	// allow (the offer on the card), with Host the name to allow.
	Blocked bool
	Host    string
	// Temporary: worth trying again later (the server was busy or down).
	Temporary bool
}

func (e *SendError) Error() string {
	if e.Why == "" {
		return e.Detail
	}
	return e.Detail + " (" + strings.TrimSuffix(e.Why, ".") + ".)"
}

// Account is where and as whom one email goes out: the setting with its
// password opened.
type Account struct {
	Host     string
	Port     int
	Security string
	Username string
	Password string
	From     mail.Address
}

// Sender sends one email over SMTP.
type Sender struct {
	// Dial connects to host:port: safehttp.Dial with the outbound policy
	// in production, so a server on a private network needs allowing.
	Dial func(ctx context.Context, addr string) (net.Conn, error)
	// RootCAs replaces the system roots (tests only).
	RootCAs *x509.CertPool
	Now     func() time.Time
}

// GuardedDial is Sender.Dial through the private-address guard.
func GuardedDial(policy safehttp.Policy, resolver safehttp.Resolver) func(context.Context, string) (net.Conn, error) {
	return func(ctx context.Context, addr string) (net.Conn, error) {
		return safehttp.Dial(ctx, policy, resolver, addr)
	}
}

// Send sends c from a.From to to. A failure is a *SendError.
func (s *Sender) Send(ctx context.Context, a Account, to []string, c Content) error {
	msg, err := build(a.From, to, c, s.Now())
	if err != nil {
		return &SendError{Stage: StageSend, Detail: "Linx couldn't put this email together.", Why: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()

	addr := net.JoinHostPort(a.Host, strconv.Itoa(a.Port))
	conn, err := s.Dial(ctx, addr)
	if err != nil {
		var b *safehttp.BlockedError
		if errors.As(err, &b) {
			return &SendError{Stage: StageConnect, Detail: b.Error(), Blocked: b.Allowlistable, Host: a.Host}
		}
		return &SendError{Stage: StageConnect, Detail: fmt.Sprintf("Linx couldn't connect to %s.", addr), Why: safehttp.Describe(err), Temporary: true}
	}
	defer conn.Close()
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(d)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	tlsConfig := &tls.Config{ServerName: a.Host, MinVersion: tls.VersionTLS12, RootCAs: s.RootCAs}
	var c2 *smtp.Client
	switch a.Security {
	case SecurityTLS:
		tc := tls.Client(conn, tlsConfig)
		if err := tc.HandshakeContext(ctx); err != nil {
			return tlsError(err, addr)
		}
		if c2, err = smtp.NewClient(tc, a.Host); err != nil {
			return &SendError{Stage: StageConnect, Detail: fmt.Sprintf("%s didn't answer like a mail server.", addr), Why: err.Error(), Temporary: true}
		}
	case SecuritySTARTTLS:
		if c2, err = smtp.NewClient(conn, a.Host); err != nil {
			return &SendError{Stage: StageConnect, Detail: fmt.Sprintf("%s didn't answer like a mail server.", addr), Why: err.Error(), Temporary: true}
		}
		if err := c2.Hello(helloName(a.From.Address)); err != nil {
			return smtpError(StageConnect, err, "The mail server didn't accept Linx's hello.")
		}
		if ok, _ := c2.Extension("STARTTLS"); !ok {
			return &SendError{Stage: StageEncrypt, Detail: fmt.Sprintf("%s doesn't offer STARTTLS, and Linx never sends email unencrypted. Try TLS from the start (port 465).", addr)}
		}
		if err := c2.StartTLS(tlsConfig); err != nil {
			return tlsError(err, addr)
		}
	default:
		return &SendError{Stage: StageEncrypt, Detail: "Linx only sends email encrypted: choose TLS from the start or STARTTLS."}
	}
	defer c2.Close()
	if a.Security == SecurityTLS {
		if err := c2.Hello(helloName(a.From.Address)); err != nil {
			return smtpError(StageConnect, err, "The mail server didn't accept Linx's hello.")
		}
	}

	if a.Username != "" || a.Password != "" {
		ok, mechs := c2.Extension("AUTH")
		if !ok {
			return &SendError{Stage: StageSignIn, Detail: "The mail server doesn't offer signing in. Check the server and port."}
		}
		var auth smtp.Auth
		switch list := strings.Fields(strings.ToUpper(mechs)); {
		case slices.Contains(list, "PLAIN"):
			auth = smtp.PlainAuth("", a.Username, a.Password, a.Host)
		case slices.Contains(list, "LOGIN"):
			auth = loginAuth{a.Username, a.Password}
		default:
			return &SendError{Stage: StageSignIn, Detail: "The mail server only offers ways of signing in Linx doesn't use (" + mechs + ")."}
		}
		if err := c2.Auth(auth); err != nil {
			return smtpError(StageSignIn, err, "The mail server didn't accept the user name and password.")
		}
	}

	if err := c2.Mail(a.From.Address); err != nil {
		return smtpError(StageSend, err, fmt.Sprintf("The mail server won't send from %s with this account.", a.From.Address))
	}
	for _, r := range to {
		if err := c2.Rcpt(r); err != nil {
			return smtpError(StageSend, err, fmt.Sprintf("The mail server won't send to %s.", r))
		}
	}
	w, err := c2.Data()
	if err != nil {
		return smtpError(StageSend, err, "The mail server didn't take the email.")
	}
	if _, err := w.Write(msg); err != nil {
		return &SendError{Stage: StageSend, Detail: "The connection broke while sending.", Why: err.Error(), Temporary: true}
	}
	if err := w.Close(); err != nil {
		return smtpError(StageSend, err, "The mail server didn't take the email.")
	}
	_ = c2.Quit()
	return nil
}

// helloName is what Linx calls itself in EHLO: the sending domain (a bare
// "localhost" is refused by some servers).
func helloName(from string) string {
	if i := strings.LastIndexByte(from, '@'); i >= 0 && i < len(from)-1 {
		return from[i+1:]
	}
	return "localhost"
}

func tlsError(err error, addr string) *SendError {
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verify *tls.CertificateVerificationError
	switch {
	case errors.As(err, &unknown), errors.As(err, &host), errors.As(err, &invalid), errors.As(err, &verify):
		return &SendError{Stage: StageCertificate, Detail: fmt.Sprintf("%s's certificate isn't one Linx can trust, so Linx didn't send.", addr), Why: err.Error()}
	}
	return &SendError{Stage: StageEncrypt, Detail: fmt.Sprintf("Linx couldn't set up encryption with %s. Check the port and the encryption choice.", addr), Why: err.Error(), Temporary: true}
}

// smtpError describes a refusal: 4xx is worth trying again, 5xx isn't.
func smtpError(stage string, err error, detail string) *SendError {
	e := &SendError{Stage: stage, Detail: detail, Why: clip(err.Error())}
	var te *textproto.Error
	if errors.As(err, &te) {
		e.Temporary = te.Code >= 400 && te.Code < 500
	} else {
		e.Temporary = true
	}
	return e
}

// clip bounds a server's own words kept with a failure.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return s
}

// loginAuth is AUTH LOGIN (Microsoft 365 offers it and not PLAIN). Only
// over TLS, as smtp.PlainAuth.
type loginAuth struct{ username, password string }

func (l loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, errors.New("refusing to sign in over an unencrypted connection")
	}
	return "LOGIN", nil, nil
}

func (l loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:", "user name":
		return []byte(l.username), nil
	case "password:":
		return []byte(l.password), nil
	}
	return nil, fmt.Errorf("unexpected sign-in question %q", fromServer)
}
