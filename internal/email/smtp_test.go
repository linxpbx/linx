package email

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
)

func TestSend(t *testing.T) {
	content := Content{Subject: "Linx can send email ✓", Text: "This is a test.\n\nOpen https://pbx.example.com/admin."}
	for _, tc := range []struct {
		name     string
		implicit bool
		mechs    string
	}{
		{"TLS from the start, PLAIN", true, "PLAIN LOGIN"},
		{"STARTTLS, PLAIN", false, "PLAIN"},
		{"STARTTLS, LOGIN only (Microsoft 365)", false, "LOGIN XOAUTH2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSMTP(t, tc.implicit)
			f.mechs = tc.mechs
			a := f.account()
			a.From = mail.Address{Name: "Linx at Example Co", Address: "pbx@example.com"}
			if err := f.sender().Send(context.Background(), a, []string{"sara@example.com"}, content); err != nil {
				t.Fatal(err)
			}
			got := f.mails()
			if len(got) != 1 || got[0].From != "pbx@example.com" || len(got[0].To) != 1 || got[0].To[0] != "sara@example.com" || !got[0].TLS {
				t.Fatalf("%+v", got)
			}
			d := got[0].Data
			body, _ := io.ReadAll(quotedprintable.NewReader(strings.NewReader(d[strings.Index(d, "\r\n\r\n"):])))
			subject, _ := new(mime.WordDecoder).DecodeHeader(headerOf(d, "Subject"))
			if subject != content.Subject || !strings.Contains(headerOf(d, "From"), "pbx@example.com") ||
				!strings.Contains(string(body), `<a href="https://pbx.example.com/admin">`) || !strings.Contains(d, "text/plain") {
				t.Fatalf("message:\n%s", d)
			}
		})
	}
}

func headerOf(data, name string) string {
	for _, l := range strings.Split(data, "\r\n") {
		if l == "" {
			break
		}
		if v, ok := strings.CutPrefix(l, name+": "); ok {
			return v
		}
	}
	return ""
}

func TestSendRefusals(t *testing.T) {
	from := mail.Address{Address: "pbx@example.com"}
	c := Content{Subject: "Hello", Text: "Hi"}
	stage := func(err error) string {
		var se *SendError
		if !errors.As(err, &se) {
			t.Fatalf("not a SendError: %v", err)
		}
		return se.Stage
	}

	// No STARTTLS offered: never sent in the clear.
	f := newFakeSMTP(t, false)
	f.noStartTLS = true
	a := f.account()
	a.From = from
	if err := f.sender().Send(context.Background(), a, []string{"a@example.com"}, c); stage(err) != StageEncrypt {
		t.Errorf("no STARTTLS: %v", err)
	}
	if len(f.mails()) != 0 {
		t.Error("sent without encryption")
	}
	for _, l := range f.seen {
		if strings.HasPrefix(l, "AUTH") || strings.HasPrefix(l, "MAIL") {
			t.Errorf("said %q before encrypting", l)
		}
	}

	// A certificate Linx can't trust.
	f = newFakeSMTP(t, true)
	a = f.account()
	a.From = from
	s := f.sender()
	s.RootCAs = nil
	if err := s.Send(context.Background(), a, []string{"a@example.com"}, c); stage(err) != StageCertificate {
		t.Errorf("untrusted certificate: %v", err)
	}
	// The wrong name on it.
	a.Host = "other.test"
	if err := f.sender().Send(context.Background(), a, []string{"a@example.com"}, c); stage(err) != StageCertificate {
		t.Errorf("wrong name: %v", err)
	}

	// The wrong password: not worth trying again.
	a = f.account()
	a.From, a.Password = from, "wrong"
	err := f.sender().Send(context.Background(), a, []string{"a@example.com"}, c)
	var se *SendError
	if stage(err) != StageSignIn || !errors.As(err, &se) || se.Temporary || !strings.Contains(se.Why, "535") {
		t.Errorf("wrong password: %#v", err)
	}

	// A refused recipient, temporarily.
	f.refuseRcpt = "450 4.2.1 Mailbox busy"
	a.Password = f.pass
	err = f.sender().Send(context.Background(), a, []string{"a@example.com"}, c)
	if stage(err) != StageSend || !errors.As(err, &se) || !se.Temporary {
		t.Errorf("busy mailbox: %#v", err)
	}

	// Plain is never an option.
	a.Security = "none"
	if err := f.sender().Send(context.Background(), a, []string{"a@example.com"}, c); stage(err) != StageEncrypt {
		t.Errorf("security none: %v", err)
	}
}

func TestBuildRefusesHeaderInjection(t *testing.T) {
	from := mail.Address{Address: "pbx@example.com"}
	for _, c := range []Content{
		{Subject: "Hi\r\nBcc: everyone@example.com", Text: "x"},
		{Subject: "Hi\nBcc: everyone@example.com", Text: "x"},
		{Subject: "", Text: "x"},
	} {
		if _, err := build(from, []string{"a@example.com"}, c, testNow); err == nil {
			t.Errorf("built %q", c.Subject)
		}
	}
	if _, err := build(mail.Address{Name: "Linx\r\nBcc: x@example.com", Address: "pbx@example.com"}, []string{"a@example.com"}, Content{Subject: "Hi", Text: "x"}, testNow); err == nil {
		t.Error("built a From name with a line break")
	}
	for _, to := range []string{"a@example.com\r\nBcc: x@example.com", "Sara <a@example.com>", "a@example.com, b@example.com", "nobody"} {
		if _, err := build(from, []string{to}, Content{Subject: "Hi", Text: "x"}, testNow); err == nil {
			t.Errorf("built To %q", to)
		}
	}
}

func TestToHTMLEscapes(t *testing.T) {
	h := toHTML("<script>alert(1)</script>\n\nSee https://pbx.example.com/a?b=1&c=<x>.")
	if strings.Contains(h, "<script>") || !strings.Contains(h, "&lt;script&gt;") ||
		!strings.Contains(h, `<a href="https://pbx.example.com/a?b=1&amp;c=">`) {
		t.Fatal(h)
	}
}
