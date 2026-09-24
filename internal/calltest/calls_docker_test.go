package calltest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/ari"
	"linxpbx.com/linx/internal/doctor"
	"linxpbx.com/linx/internal/pbx"
)

// TestCallsDocker is docs/PBX.md §7's automated suite: sign in, wrong
// password, a call, nobody answering, unknown number, a revoked device, a
// network phones may not connect from, unencrypted audio, old TLS versions —
// plus the echo test, calling yourself, and the call and device events the
// control plane derives from Asterisk's ARI events.
func TestCallsDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	tracker := &pbx.CallTracker{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	e := start(t, ctx, func(e *env) ari.App {
		tracker.Store = e.store
		tracker.Log = slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn}))
		return tracker
	})
	eventually(t, "Asterisk's ARI connection", 30*time.Second, tracker.Connected)

	// What linx doctor reads from Asterisk's console, on the real image.
	if out := e.asteriskCLI("ari show websocket sessions"); !doctor.ARIConnected(out) {
		t.Errorf("doctor doesn't see the ARI connection in:\n%s", out)
	}
	if out := e.asteriskCLI("odbc show asterisk"); !doctor.ODBCConnected(out) {
		t.Errorf("doctor doesn't see the database connection in:\n%s", out)
	}
	out := e.asteriskCLI("pjsip show transports")
	if bad, ok := doctor.PlainSIPTransports(out); !ok || len(bad) > 0 {
		t.Errorf("doctor reads transports %v (found %v) from:\n%s", bad, ok, out)
	}

	alice := e.newPhone("101", "Alice")
	bob := e.newPhone("102", "Bob")
	carol := e.newPhone("103", "Carol") // never signs in

	online := func(p phone) bool {
		d, _, err := e.store.DeviceBySIPUsername(ctx, p.dev.SIPUsername)
		if err != nil {
			t.Fatal(err)
		}
		return d.Online
	}

	t.Run("sign in, call, answer, hang up", func(t *testing.T) {
		callee := e.sipp("bob", "register.xml", bob, "-oocsf", "/scenarios/answer.xml", "-d", "10000")
		eventually(t, "Bob online", 15*time.Second, func() bool { return online(bob) })
		reg := e.lastEvent("device.registered")
		if reg["sip_username"] != bob.dev.SIPUsername || reg["address"] == nil {
			t.Errorf("device.registered = %v", reg)
		}
		d, _, _ := e.store.DeviceBySIPUsername(ctx, bob.dev.SIPUsername)
		if d.LastRegisteredAt == nil || d.LastRegisteredFrom == nil {
			t.Errorf("last registered not recorded: %+v", d)
		}

		// Asterisk's SIP messages are logged during the call, to check it
		// names itself sip.linx.test (from_domain, see start), never its
		// container address: phones keep that name in their call history.
		e.asteriskCLI("pjsip set logger on")
		e.run("call", "call.xml", alice, "-s", "102", "-d", "2000")
		e.asteriskCLI("pjsip set logger off")
		if logs := e.asteriskLogs(); !strings.Contains(logs, `From: "Alice" <sip:101@sip.linx.test>`) {
			t.Errorf("the INVITE to Bob doesn't come from sip.linx.test:\n%s", logs)
		}
		ended := e.callEnded("102")
		if ended["outcome"] != pbx.OutcomeAnswered || ended["duration_seconds"].(float64) < 1 {
			t.Errorf("call.ended = %v", ended)
		}
		from := ended["from"].(map[string]any)
		by := ended["answered_by"].(map[string]any)
		if from["extension"] != "101" || from["device_id"] != alice.dev.ID.String() || by["extension"] != "102" {
			t.Errorf("call.ended parties: from %v, answered by %v", from, by)
		}
		id := ended["id"]
		for _, typ := range []string{"call.started", "call.answered"} {
			if ev := e.lastEvent(typ); ev["id"] != id {
				t.Errorf("%s = %v, want call id %v", typ, ev, id)
			}
		}

		// Bob signs out at the end of his scenario.
		e.wait(callee)
		eventually(t, "Bob offline", 10*time.Second, func() bool { return !online(bob) })
		if ev := e.lastEvent("device.unregistered"); ev["sip_username"] != bob.dev.SIPUsername {
			t.Errorf("device.unregistered = %v", ev)
		}
	})

	t.Run("echo test and spoken messages", func(t *testing.T) {
		// All at once: each is its own SIPp phone.
		echo := e.sipp("echo", "call.xml", alice, "-s", "*43", "-d", "1000")
		unknown := e.sipp("unknown", "call-message.xml", alice, "-s", "555")
		self := e.sipp("self", "call-message.xml", alice, "-s", "101")
		offline := e.sipp("offline", "call-message.xml", alice, "-s", "103")
		for _, c := range []string{echo, unknown, self, offline} {
			e.wait(c)
		}
		// A phone offering Opus first (Linphone does) is answered in G.722,
		// so it hears the message: Asterisk can't encode Opus.
		e.run("opus-first", "call-message-opus.xml", alice, "-s", "556")
		if logs := e.asteriskLogs(); strings.Contains(logs, "Playback failed") {
			t.Errorf("a message didn't play:\n%s", logs)
		}
		for to, want := range map[string]string{
			"*43": pbx.OutcomeEchoTest, "555": pbx.OutcomeNotInUse,
			"101": pbx.OutcomeNotAvailable, "103": pbx.OutcomeNotAvailable,
		} {
			if got := e.callEnded(to)["outcome"]; got != want {
				t.Errorf("call to %s: outcome %v, want %s", to, got, want)
			}
		}
		_ = carol
	})

	t.Run("nobody answers", func(t *testing.T) {
		callee := e.sipp("bob-ring", "register.xml", bob, "-oocsf", "/scenarios/ring.xml", "-d", "60000")
		eventually(t, "Bob online", 15*time.Second, func() bool { return online(bob) })
		caller := e.sipp("noanswer", "call-message.xml", alice, "-s", "102")
		eventually(t, "a ringing call in /calls/active", 10*time.Second, func() bool {
			calls := tracker.ActiveCalls()
			return len(calls) == 1 && calls[0].State == pbx.CallRinging && calls[0].From.Extension == "101" && calls[0].To == "102"
		})
		e.wait(caller) // 30 s of ringing, then "nobody is available"
		ended := e.callEnded("102")
		if ended["outcome"] != pbx.OutcomeMissed {
			t.Errorf("outcome %v, want missed", ended["outcome"])
		}
		if missed := e.lastEvent("call.missed"); missed["id"] != ended["id"] {
			t.Errorf("call.missed = %v, want call %v", missed, ended["id"])
		}
		if n := len(tracker.ActiveCalls()); n != 0 {
			t.Errorf("%d calls still active", n)
		}

		// A phone that just disappears (TLS connection gone) is signed
		// out at once.
		docker(t, ctx, "rm", "--force", callee)
		eventually(t, "Bob offline after his connection dropped", 10*time.Second, func() bool { return !online(bob) })
	})

	t.Run("wrong password", func(t *testing.T) {
		wrong := alice
		wrong.password = pbx.NewDevicePassword()
		e.run("wrong-password", "register-rejected.xml", wrong)
	})

	t.Run("outside the phone networks", func(t *testing.T) {
		e.wait(e.sippOn(outsideNet, "outside", "register-forbidden.xml", alice))
	})

	t.Run("unencrypted audio refused", func(t *testing.T) {
		e.run("plain-audio", "call-unencrypted.xml", alice, "-s", "*43")
	})

	t.Run("TLS 1.2 and 1.3 only", func(t *testing.T) {
		roots := x509.NewCertPool()
		root, err := os.ReadFile(filepath.Join(e.dir, "ca", "root_ca.crt"))
		if err != nil || !roots.AppendCertsFromPEM(root) {
			t.Fatalf("test CA root: %v", err)
		}
		addr := e.sipAddr()
		for _, v := range []struct {
			name    string
			version uint16
			want    bool
		}{{"1.0", tls.VersionTLS10, false}, {"1.1", tls.VersionTLS11, false}, {"1.2", tls.VersionTLS12, true}, {"1.3", tls.VersionTLS13, true}} {
			c, err := tls.Dial("tcp", addr, &tls.Config{ServerName: "asterisk", RootCAs: roots, MinVersion: v.version, MaxVersion: v.version})
			if err == nil {
				c.Close()
			}
			if got := err == nil; got != v.want {
				t.Errorf("TLS %s: accepted = %v, want %v (%v)", v.name, got, v.want, err)
			}
		}
	})

	t.Run("picks up a renewed certificate during a call", func(t *testing.T) {
		roots := x509.NewCertPool()
		root, err := os.ReadFile(filepath.Join(e.dir, "ca", "root_ca.crt"))
		if err != nil || !roots.AppendCertsFromPEM(root) {
			t.Fatalf("test CA root: %v", err)
		}
		served := func() int64 {
			c, err := tls.Dial("tcp", e.sipAddr(), &tls.Config{ServerName: "asterisk", RootCAs: roots})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			return c.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
		}
		call := e.sipp("renew-call", "call.xml", alice, "-s", "*43", "-d", "6000")
		time.Sleep(time.Second)
		e.renewSIPCert(10)
		eventually(t, "Asterisk serving the renewed certificate", 20*time.Second, func() bool { return served() == 10 })
		e.wait(call) // the call in progress carried on to its normal end
		if logs := e.asteriskLogs(); !strings.Contains(logs, "TLS certificate reloaded") {
			t.Errorf("no reload logged:\n%s", logs)
		}
	})

	t.Run("survives a restart", func(t *testing.T) {
		// A restarted container's tmpfs mounts aren't the same as a new
		// one's: this once left Asterisk unable to write its config.
		docker(t, ctx, "restart", astName)
		e.waitAsterisk()
		eventually(t, "Asterisk's ARI connection after the restart", 30*time.Second, tracker.Connected)
		e.run("echo-after-restart", "call.xml", alice, "-s", "*43", "-d", "1000")
	})

	t.Run("revoked device", func(t *testing.T) {
		if _, err := e.store.RevokeDevice(ctx, e.tenant, alice.dev.ID, time.Now(), e.audit("device.revoke")); err != nil {
			t.Fatal(err)
		}
		e.run("revoked-register", "register-rejected.xml", alice)
		e.run("revoked-call", "call-rejected.xml", alice, "-s", "102")
	})
}

// lastEvent returns the data of the newest queued webhook event of a type.
func (e *env) lastEvent(eventType string) map[string]any {
	e.t.Helper()
	var body []byte
	if err := e.pool.QueryRow(e.ctx, `SELECT body FROM event_outbox WHERE type = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
		eventType).Scan(&body); err != nil {
		e.t.Fatalf("no %s event: %v", eventType, err)
	}
	return eventData(e.t, body)
}

// callEnded returns the newest call.ended event for calls to a number.
func (e *env) callEnded(to string) map[string]any {
	e.t.Helper()
	var body []byte
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := e.pool.QueryRow(e.ctx, `SELECT body FROM event_outbox WHERE type = 'call.ended' AND convert_from(body, 'UTF8')::jsonb->'data'->>'to' = $1
			ORDER BY created_at DESC, id DESC LIMIT 1`, to).Scan(&body)
		if err == nil {
			return eventData(e.t, body)
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("no call.ended for a call to %s: %v", to, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func eventData(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var ev struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		t.Fatal(err)
	}
	return ev.Data
}
