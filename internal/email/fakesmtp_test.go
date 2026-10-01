package email

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a mail server for tests: TLS from the start or STARTTLS,
// AUTH PLAIN or LOGIN, and it keeps what it was sent.
type fakeSMTP struct {
	t        *testing.T
	ln       net.Listener
	tls      *tls.Config
	roots    *x509.CertPool
	implicit bool
	// noStartTLS: don't offer STARTTLS.
	noStartTLS bool
	mechs      string
	user, pass string
	// refuseRcpt answers RCPT with this line when set.
	refuseRcpt string

	mu   sync.Mutex
	got  []fakeMail
	seen []string
}

type fakeMail struct {
	From string
	To   []string
	Data string
	TLS  bool
}

func newFakeSMTP(t *testing.T, implicit bool) *fakeSMTP {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mail.test"},
		DNSNames: []string{"mail.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{t: t, ln: ln, roots: roots, implicit: implicit, mechs: "PLAIN LOGIN", user: "pbx@example.com", pass: "app-password",
		tls: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}}
	t.Cleanup(func() { ln.Close() })
	go f.serve()
	return f
}

// sender connects every address to the fake.
func (f *fakeSMTP) sender() *Sender {
	return &Sender{RootCAs: f.roots, Now: time.Now, Dial: func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", f.ln.Addr().String())
	}}
}

func (f *fakeSMTP) account() Account {
	sec := SecuritySTARTTLS
	if f.implicit {
		sec = SecurityTLS
	}
	return Account{Host: "mail.test", Port: 587, Security: sec, Username: f.user, Password: f.pass}
}

func (f *fakeSMTP) mails() []fakeMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeMail(nil), f.got...)
}

func (f *fakeSMTP) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.session(c)
	}
}

func (f *fakeSMTP) session(c net.Conn) {
	defer c.Close()
	secure := false
	if f.implicit {
		c = tls.Server(c, f.tls)
		secure = true
	}
	r, w := bufio.NewReader(c), bufio.NewWriter(c)
	say := func(line string) { w.WriteString(line + "\r\n"); w.Flush() }
	read := func() string {
		l, err := r.ReadString('\n')
		if err != nil {
			return ""
		}
		l = strings.TrimRight(l, "\r\n")
		f.mu.Lock()
		f.seen = append(f.seen, l)
		f.mu.Unlock()
		return l
	}
	say("220 mail.test ESMTP")
	authed := false
	var m fakeMail
	for {
		line := read()
		cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch cmd {
		case "":
			return
		case "EHLO", "HELO":
			w.WriteString("250-mail.test\r\n")
			if !secure && !f.noStartTLS {
				w.WriteString("250-STARTTLS\r\n")
			}
			if secure {
				w.WriteString("250-AUTH " + f.mechs + "\r\n")
			}
			say("250 8BITMIME")
		case "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(c, f.tls)
			if err := tc.Handshake(); err != nil {
				return
			}
			c, secure = tc, true
			r, w = bufio.NewReader(c), bufio.NewWriter(c)
		case "AUTH":
			parts := strings.Fields(line)
			var user, pass string
			switch strings.ToUpper(parts[1]) {
			case "PLAIN":
				resp := ""
				if len(parts) > 2 {
					resp = parts[2]
				} else {
					say("334 ")
					resp = read()
				}
				b, _ := base64.StdEncoding.DecodeString(resp)
				if p := strings.Split(string(b), "\x00"); len(p) == 3 {
					user, pass = p[1], p[2]
				}
			case "LOGIN":
				say("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
				b, _ := base64.StdEncoding.DecodeString(read())
				user = string(b)
				say("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
				b, _ = base64.StdEncoding.DecodeString(read())
				pass = string(b)
			}
			if user == f.user && pass == f.pass {
				authed = true
				say("235 2.7.0 Authentication successful")
			} else {
				say("535 5.7.8 Username and Password not accepted")
			}
		case "MAIL":
			if !authed {
				say("530 5.7.0 Authentication required")
				continue
			}
			m = fakeMail{From: between(line), TLS: secure}
			say("250 OK")
		case "RCPT":
			if f.refuseRcpt != "" {
				say(f.refuseRcpt)
				continue
			}
			m.To = append(m.To, between(line))
			say("250 OK")
		case "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			m.Data = b.String()
			f.mu.Lock()
			f.got = append(f.got, m)
			f.mu.Unlock()
			say("250 OK queued")
		case "QUIT":
			say("221 bye")
			return
		case "RSET", "NOOP":
			say("250 OK")
		default:
			say("502 unknown")
		}
	}
}

// between is the address in "MAIL FROM:<a> BODY=8BITMIME".
func between(line string) string {
	i, j := strings.IndexByte(line, '<'), strings.IndexByte(line, '>')
	if i < 0 || j < i {
		return ""
	}
	return line[i+1 : j]
}

var testNow = time.Date(2026, 10, 1, 9, 14, 0, 0, time.UTC)
