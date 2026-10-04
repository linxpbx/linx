package calltest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/ari"
	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/callhistory"
	"linxpbx.com/linx/internal/calltest/sipws"
	"linxpbx.com/linx/internal/doctor"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/routing"
	"linxpbx.com/linx/internal/siprelay"
	"linxpbx.com/linx/internal/store"
	"linxpbx.com/linx/internal/voicemail"
)

// TestCallsDocker is docs/PBX.md §7's automated suite: sign in, wrong
// password, a call, nobody answering, unknown number, a revoked device, a
// network phones may not connect from, unencrypted audio, old TLS versions —
// plus the echo test, calling yourself, and the call and device events the
// control plane derives from Asterisk's ARI events. Phase 1C adds Opus
// callers hearing messages (ADR-041) and browsers' devices signing in over
// the secure websocket on linx-sipws (docs/WEB.md §2), and through the
// control plane's /sip relay (docs/WEB.md §5): session-bound lines, the
// relay's refusals, and the audio addresses Asterisk offers them. Phase
// 1F adds ring groups (ADR-068): all at once, one after another, "if
// nobody answers", and a loop ending after 10 places; voicemail; and the
// call history made from the records Asterisk adds (ADR-070).
func TestCallsDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	tracker := &pbx.CallTracker{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var voicemails *voicemail.Importer
	e := start(t, ctx, func(e *env) ari.App {
		tracker.Store = e.store
		tracker.Log = slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn}))
		// As the control plane: the dialplan's event takes the message in.
		voicemails = &voicemail.Importer{Dir: filepath.Join(e.dir, "voicemail"), Store: e.store, Now: time.Now, Log: tracker.Log}
		tracker.UserEvent = func(name string, _ map[string]any) {
			if name == voicemail.EventName {
				voicemails.Kick()
			}
		}
		return tracker
	})
	go voicemails.Run(ctx)
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
		// Opus (ADR-041): a phone offering Opus first (Linphone does) is
		// answered in Opus, and one offering nothing else still hears the
		// message, converted to Opus as it plays.
		e.run("opus-first", "call-message-opus.xml", alice, "-s", "556")
		if out := e.asteriskCLI("module show like codec_opus"); !strings.Contains(out, "codec_opus_open_source.so") || !strings.Contains(out, "Running") {
			t.Fatalf("Opus codec not running:\n%s", out)
		}
		opusOnly := e.sipp("opus-only", "call-message-opus-only.xml", alice, "-s", "557")
		eventually(t, "Asterisk encoding a message in Opus", 15*time.Second, func() bool {
			return strings.Contains(e.channelDetails(), "(opus@")
		})
		e.wait(opusOnly)
		if logs := e.asteriskLogs(); strings.Contains(logs, "Playback failed") || strings.Contains(logs, "Unable to find a codec translation path") {
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

	t.Run("voicemail", func(t *testing.T) {
		// Carol has no phone connected, so a call to her goes straight to
		// her voicemail: the greeting, the tone, then recording.
		e.voicemailOn(carol.ext.ID, true)
		defer e.voicemailOn(carol.ext.ID, false)
		heard := func(name string) int { return strings.Count(e.asteriskLogs(), "Playing 'linx/"+name+".g722'") }
		messages := func() int {
			var n int
			if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM voicemail_message WHERE box_id = $1`, carol.ext.ID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		folder := func() []string {
			entries, _ := os.ReadDir(filepath.Join(e.dir, "voicemail"))
			var names []string
			for _, en := range entries {
				names = append(names, en.Name())
			}
			return names
		}
		greeting, tone := heard("vm-greeting"), heard("beep")
		// Alice hangs up just after the tone: SIPp sends no audio, so
		// there's nothing to keep, and the folder is cleared (the note
		// Asterisk wrote, its event and the importer all worked).
		e.run("vm-short", "call.xml", alice, "-s", "103", "-d", "9000")
		if heard("vm-greeting") != greeting+1 || heard("beep") != tone+1 {
			t.Fatalf("no greeting or tone:\n%s", e.asteriskLogs())
		}
		if ended := e.callEnded("103"); ended["outcome"] != pbx.OutcomeVoicemail {
			t.Errorf("call.ended outcome %v, want voicemail", ended["outcome"])
		}
		logs := e.asteriskLogs()
		if !strings.Contains(logs, "FILE("+asteriskconf.VoicemailDir+"/") || !strings.Contains(logs, `UserEvent("PJSIP/`) {
			t.Fatalf("no note or event after the call:\n%s", logs)
		}
		eventually(t, "the voicemail folder emptied", 15*time.Second, func() bool { return len(folder()) == 0 })
		if n := messages(); n != 0 {
			t.Errorf("%d messages kept from a call with no audio", n)
		}

		// Carol records her own greeting (Phase 1F step 14): the control
		// plane copies it into the folder Asterisk reads, and the next
		// caller hears it instead of Linx's own.
		_, err := e.pool.Exec(ctx, `INSERT INTO voicemail_greeting (box_id, kind, tenant_id, audio, recorded_at)
			VALUES ($1, 'unavailable', $2, $3, now())`, carol.ext.ID, carol.ext.TenantID, make([]byte, 2*2*voicemail.GreetingRate))
		if err != nil {
			t.Fatal(err)
		}
		greetings := &voicemail.Greetings{Dir: filepath.Join(e.dir, "greetings"), Store: e.store, Log: tracker.Log}
		if err := greetings.Sync(ctx); err != nil {
			t.Fatal(err)
		}
		own := voicemail.GreetingName(carol.ext.ID, voicemail.GreetingUnavailable)
		os.Chmod(filepath.Join(e.dir, "greetings", own), 0o644) // Asterisk's uid isn't this test's
		e.run("vm-own", "call.xml", alice, "-s", "103", "-d", "6000")
		if !strings.Contains(e.asteriskLogs(), "Playing '"+asteriskconf.GreetingsDir+"/"+strings.TrimSuffix(own, ".sln16")) {
			t.Fatalf("Carol's own greeting wasn't played:\n%s", e.asteriskLogs())
		}
		if heard("vm-greeting") != greeting+1 {
			t.Error("Linx's own greeting played as well as Carol's")
		}
		e.pool.Exec(ctx, `DELETE FROM voicemail_greeting WHERE box_id = $1`, carol.ext.ID)
		greetings.Sync(ctx)
		eventually(t, "the voicemail folder emptied", 15*time.Second, func() bool { return len(folder()) == 0 })

		// A message with audio being kept is the browser suite's
		// (internal/browsertest): SIPp sends no audio.
	})

	t.Run("ring groups", func(t *testing.T) {
		dave := e.newPhone("109", "Dave")
		bobRings := e.sipp("bob-group", "register.xml", bob, "-oocsf", "/scenarios/ring.xml", "-d", "120000")
		daveAnswers := e.sipp("dave-group", "register.xml", dave, "-oocsf", "/scenarios/answer.xml", "-d", "120000")
		defer docker(t, ctx, "rm", "--force", bobRings, daveAnswers)
		eventually(t, "Bob and Dave online", 15*time.Second, func() bool { return online(bob) && online(dave) })

		now := time.Now().UTC()
		group := func(name, number, strategy string, members []phone, noAnswer routing.Destination) routing.RingGroup {
			t.Helper()
			g := routing.RingGroup{ID: uuid.Must(uuid.NewV7()), TenantID: e.tenant, Name: name, Number: number, Strategy: strategy,
				RingSeconds: 25, TurnSeconds: 5, NoAnswer: noAnswer, Version: 1, CreatedAt: now, UpdatedAt: now}
			for _, m := range members {
				g.Members = append(g.Members, routing.Member{ExtensionID: m.ext.ID})
			}
			if err := e.store.CreateRingGroup(ctx, g, e.audit("ring_group.create")); err != nil {
				t.Fatal(err)
			}
			return g
		}
		notAvailable := routing.Destination{Kind: routing.KindMessage, Message: routing.MessageNotAvailable}
		answeredBy := func(to, want string) map[string]any {
			t.Helper()
			ended := e.callEnded(to)
			by, _ := ended["answered_by"].(map[string]any)
			if ended["outcome"] != pbx.OutcomeAnswered || by["extension"] != want {
				t.Errorf("call to %s: %v, want answered by %s", to, ended, want)
			}
			return ended
		}

		// All at once: Bob and Dave ring together, Dave answers.
		group("Sales", "600", routing.StrategyAll, []phone{bob, dave}, notAvailable)
		e.run("group-all", "call.xml", alice, "-s", "600", "-d", "1000")
		answeredBy("600", "109")

		// One after another: Bob rings for 5 seconds, then Dave answers.
		group("Support", "601", routing.StrategyInTurn, []phone{bob, dave}, notAvailable)
		began := time.Now()
		e.run("group-turn", "call.xml", alice, "-s", "601", "-d", "1000")
		answeredBy("601", "109")
		if took := time.Since(began); took < 5*time.Second {
			t.Errorf("Dave answered after %s: Bob's 5 seconds were skipped", took)
		}

		// If nobody answers (Carol never signs in), the call goes to Dave.
		group("Reception", "602", routing.StrategyAll, []phone{carol}, routing.Destination{Kind: routing.KindExtension, ExtensionID: &dave.ext.ID})
		e.run("group-next", "call.xml", alice, "-s", "602", "-d", "1000")
		answeredBy("602", "109")

		// A loop the screens would refuse, made directly: it ends after 10
		// places with "not available" (nothing rang: Carol never signed in).
		l1 := group("Loop one", "603", routing.StrategyAll, []phone{carol}, notAvailable)
		l2 := group("Loop two", "", routing.StrategyAll, []phone{carol}, routing.Destination{Kind: routing.KindRingGroup, RingGroupID: &l1.ID})
		l1.NoAnswer = routing.Destination{Kind: routing.KindRingGroup, RingGroupID: &l2.ID}
		if err := e.store.UpdateRingGroup(ctx, l1, e.audit("ring_group.update")); err != nil {
			t.Fatal(err)
		}
		e.run("group-loop", "call-message.xml", alice, "-s", "603")
		if got := e.callEnded("603")["outcome"]; got != pbx.OutcomeNotAvailable {
			t.Errorf("loop: outcome %v, want not_available", got)
		}
		if logs := e.asteriskLogs(); strings.Contains(logs, "linx_route") && strings.Contains(logs, "ERROR") {
			t.Errorf("routing errors:\n%s", logs)
		}
	})

	t.Run("call history", func(t *testing.T) {
		// The calls above, from the records Asterisk added itself
		// (ADR-070), as call history reads them.
		b := &callhistory.Builder{Store: store.CallHistory{Store: e.store}, Now: time.Now, Log: tracker.Log}
		svc := &callhistory.Service{Store: b.Store, Builder: b, Now: time.Now}
		items, _, err := svc.All(ctx, e.tenant, callhistory.Filter{Limit: callhistory.MaxLimit})
		if err != nil {
			t.Fatal(err)
		}
		find := func(to, result string) callhistory.Listed {
			t.Helper()
			for _, c := range items {
				if c.ToNumber == to && c.Result == result {
					return c
				}
			}
			var all []string
			for _, c := range items {
				all = append(all, c.ToNumber+" "+c.Result+": "+strings.Join(c.Words(), " | "))
			}
			t.Fatalf("no call to %s with result %s in:\n%s", to, result, strings.Join(all, "\n"))
			return callhistory.Listed{}
		}
		answered := find("102", callhistory.ResultAnswered)
		if answered.FromName != "Alice" || answered.AnsweredByName != "Bob (102)" || answered.TalkSeconds < 1 || answered.Direction != callhistory.DirectionInternal {
			t.Errorf("Alice to Bob: %+v", answered)
		}
		missed := find("102", callhistory.ResultMissed)
		// 30 s of ringing, measured from the call records: a busy runner
		// can make it 31.
		if w := strings.Join(missed.Words(), " | "); !rangThirty.MatchString(w) || !missed.RangUnanswered {
			t.Errorf("nobody answered: %s", w)
		}
		vm := find("103", callhistory.ResultVoicemail)
		if vm.VoicemailBoxName != "Carol (103)" || vm.VoicemailSource == "" {
			t.Errorf("Carol's voicemail: %+v", vm)
		}
		group := find("600", callhistory.ResultAnswered)
		if w := strings.Join(group.Words(), " | "); w != "Rang Sales (all at once): Bob (102), Dave (109) · Dave (109) answered" || group.RingGroupName != "Sales" {
			t.Errorf("Sales: %s (%+v)", w, group)
		}
		turn := find("601", callhistory.ResultAnswered)
		if w := strings.Join(turn.Words(), " | "); w != "Rang Support (one after another): Bob (102), Dave (109) · Dave (109) answered" {
			t.Errorf("Support: %s", w)
		}
		if echo := find("*43", callhistory.ResultEchoTest); echo.FromExtensionID == nil || *echo.FromExtensionID != alice.ext.ID {
			t.Errorf("echo test: %+v", echo)
		}
		find("555", callhistory.ResultNotInUse)
		// Bob's own history: his missed call, not Alice's echo tests.
		bobs, _, err := svc.All(ctx, e.tenant, callhistory.Filter{Party: &bob.ext.ID, Missed: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(bobs) == 0 || bobs[0].ToNumber != "102" {
			t.Errorf("Bob's missed calls: %+v", bobs)
		}
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

	t.Run("browsers over the secure websocket", func(t *testing.T) {
		dana := e.newWebPhone("104", "Dana")
		erin := e.newWebPhone("105", "Erin")

		// Browsers get WebRTC media (ICE, DTLS-SRTP, AVPF, rtcp-mux) over
		// the websocket; phones keep SIP over TLS with SDES-SRTP. The app
		// and the browser also get the video codecs and one video stream,
		// for 1:1 video calls (migration 0044, docs/PHASE2.md §7); a desk
		// phone is never offered video at all, which the phone endpoint
		// below is what checks.
		web := e.asteriskCLI("pjsip show endpoint " + dana.dev.SIPUsername)
		for _, want := range []string{`transport\s*:\s*transport-wss`, `media_encryption\s*:\s*dtls`, `ice_support\s*:\s*true`,
			`use_avpf\s*:\s*true`, `rtcp_mux\s*:\s*true`, `dtls_verify\s*:\s*Fingerprint`, `dtls_setup\s*:\s*actpass`,
			`allow\s*:\s*\(opus\|g722\|ulaw\|h264\|vp8\)`, `max_video_streams\s*:\s*1`} {
			if !regexp.MustCompile(want).MatchString(web) {
				t.Errorf("web endpoint doesn't match %s:\n%s", want, web)
			}
		}
		phone := e.asteriskCLI("pjsip show endpoint " + bob.dev.SIPUsername)
		for _, want := range []string{`transport\s*:\s*transport-tls`, `media_encryption\s*:\s*sdes`, `ice_support\s*:\s*false`,
			`allow\s*:\s*\(opus\|g722\|ulaw\)`} {
			if !regexp.MustCompile(want).MatchString(phone) {
				t.Errorf("phone endpoint doesn't match %s:\n%s", want, phone)
			}
		}

		// Never plain HTTP; HTTPS on the websocket network's address only.
		wsIP := docker(t, ctx, "inspect", "--format", "{{(index .NetworkSettings.Networks \""+sipwsNet+"\").IPAddress}}", astName)
		if out := e.asteriskCLI("http show status"); !strings.Contains(out, "Server Disabled") || !strings.Contains(out, "Server: Linx") {
			t.Errorf("http show status (want plain HTTP off, no version in the Server header):\n%s", out)
		}
		tcp := docker(t, ctx, "exec", astName, "cat", "/proc/net/tcp")
		var others []string
		for _, l := range doctor.ListeningTCP(tcp) {
			// 127.0.0.11 is Docker's own DNS resolver, in every container.
			if l.Port() != 5061 && !l.Addr().IsLoopback() {
				others = append(others, l.String())
			}
		}
		if fmt.Sprint(others) != "["+wsIP+":8089]" {
			t.Errorf("Asterisk listens on %v besides 5061, want only %s:8089", others, wsIP)
		}
		// Nothing listens there for the phones' network.
		out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", netName,
			"--volume", filepath.Join(e.dir, "wsphone")+":/wsphone:ro", "--volume", filepath.Join(e.dir, "ca")+":/ca:ro",
			"--entrypoint", "/wsphone/wsphone", sippImage, "-url", "wss://asterisk:8089/ws",
			"-user", dana.dev.SIPUsername, "-pass", dana.password).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "dial") {
			t.Errorf("websocket reachable from the phones' network: %v\n%s", err, out)
		}

		// Dana signs in and stays connected while the certificate renews.
		held := e.wsphone("dana", dana, "-hold", "12s")
		eventually(t, "Dana online", 15*time.Second, func() bool { return online(dana) })
		if reg := e.lastEvent("device.registered"); reg["sip_username"] != dana.dev.SIPUsername {
			t.Errorf("device.registered = %v", reg)
		}
		e.renewSIPWSCert(21)
		eventually(t, "the websocket serving the renewed certificate", 20*time.Second, func() bool {
			return strings.Contains(e.waitWS(e.wsphone("erin", erin)), "cert-serial 21\nregistered\n")
		})
		// Dana's connection stayed up through the reload.
		if out := e.waitWS(held); !strings.Contains(out, "cert-serial 20\nregistered\nregistered again\n") {
			t.Errorf("held connection:\n%s", out)
		}
		if logs := e.asteriskLogs(); !strings.Contains(logs, `"cert":"browser websocket"`) {
			t.Errorf("no websocket certificate reload logged:\n%s", logs)
		}
		eventually(t, "Dana offline after her connection closed", 10*time.Second, func() bool { return !online(dana) })

		wrong := dana
		wrong.password = pbx.NewDevicePassword()
		if out := e.waitWS(e.wsphone("wrong", wrong, "-want-reject")); !strings.Contains(out, "refused 401") {
			t.Errorf("wrong password:\n%s", out)
		}
	})

	t.Run("browsers through the control plane's relay", func(t *testing.T) {
		gail := e.newWebPhone("107", "Gail")
		relay, audits := e.relay(500 * time.Millisecond)
		mediaIP := docker(t, ctx, "inspect", "--format", "{{(index .NetworkSettings.Networks \""+mediaNet+"\").IPAddress}}", astName)

		// Signs in through the relay, as a page does.
		p, _ := e.relayPhone(relay, gail, gail.password)
		if code, err := p.Register(ctx); err != nil || code != 200 {
			t.Fatalf("sign-in through the relay: %d %v", code, err)
		}
		eventually(t, "Gail online", 15*time.Second, func() bool { return online(gail) })

		// A call to her: Asterisk offers her browser its audio on the relay's
		// network (linx-media) and on the LAN address, and nowhere else.
		call := e.sipp("to-gail", "call-message.xml", alice, "-s", "107")
		ictx, icancel := context.WithTimeout(ctx, 30*time.Second)
		invite, err := p.WaitRequest(ictx, "INVITE")
		icancel()
		if err != nil {
			t.Fatalf("waiting for the call: %v\n%s", err, e.asteriskLogs())
		}
		got := map[string]bool{}
		for _, c := range sipws.Candidates(invite) {
			got[c] = true
		}
		if len(got) != 2 || !got[mediaIP] || !got[lanAddr] {
			t.Errorf("Asterisk offers browsers %v, want only %s (relay) and %s (LAN):\n%s", got, mediaIP, lanAddr, invite)
		}
		if !strings.Contains(invite, "UDP/TLS/RTP/SAVPF") || !strings.Contains(invite, "a=fingerprint:") {
			t.Errorf("not a WebRTC offer:\n%s", invite)
		}
		if err := p.Reply(ctx, invite, "486 Busy Here"); err != nil {
			t.Fatal(err)
		}
		e.wait(call) // declined: the caller hears "not available"

		// Signing out ends the line: Asterisk no longer knows the device
		// (its views check the session), and the relay drops the connection.
		if err := e.store.RevokeSession(ctx, gail.session.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
		defer rcancel()
		if _, err := p.WaitRequest(rctx, "NONE"); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
			t.Errorf("relay after sign-out: %v", err)
		}
		if out := e.waitWS(e.wsphone("gail-after", gail, "-want-reject")); !strings.Contains(out, "refused") {
			t.Errorf("Asterisk still accepts a signed-out line:\n%s", out)
		}
		// Asterisk sends no "offline" for a device that's gone from its
		// views; the control plane's sweep revoking the line clears it.
		if _, err := e.store.RevokeDeadWebDevices(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		if online(gail) {
			t.Error("Gail still online after her line was revoked")
		}

		// Another device's name through the relay: refused before Asterisk.
		hal := e.newWebPhone("108", "Hal")
		p2, conn2 := e.relayPhone(relay, hal, hal.password)
		if _, err := p2.RegisterAs(ctx, alice.dev.SIPUsername); err == nil {
			t.Error("the relay passed another device's sign-in")
		}
		conn2.CloseNow()
		if a := <-audits; a != siprelay.ReasonNotYourLine {
			t.Errorf("audited %q", a)
		}

		// Wrong passwords: Asterisk refuses, and the third closes the line.
		p3, conn3 := e.relayPhone(relay, hal, "not-the-password")
		var codes []int
		var closedErr error
		for range 3 {
			code, err := p3.Register(ctx)
			if err != nil {
				closedErr = err
				break
			}
			codes = append(codes, code)
		}
		conn3.CloseNow()
		if len(codes) != 2 || codes[0] != 401 || codes[1] != 401 || closedErr == nil {
			t.Errorf("wrong passwords: answers %v, then %v; want 401, 401, closed", codes, closedErr)
		}
		if a := <-audits; a != siprelay.ReasonAuthFailures {
			t.Errorf("audited %q", a)
		}
		// Hal's right password still works on a new connection.
		p4, conn4 := e.relayPhone(relay, hal, hal.password)
		defer conn4.CloseNow()
		if code, err := p4.Register(ctx); err != nil || code != 200 {
			t.Errorf("right password after the lockout: %d %v", code, err)
		}
	})

	t.Run("survives a restart", func(t *testing.T) {
		// A restarted container's tmpfs mounts aren't the same as a new
		// one's: this once left Asterisk unable to write its config.
		docker(t, ctx, "restart", astName)
		e.waitAsterisk()
		eventually(t, "Asterisk's ARI connection after the restart", 30*time.Second, tracker.Connected)
		e.run("echo-after-restart", "call.xml", alice, "-s", "*43", "-d", "1000")
		e.waitWS(e.wsphone("after-restart", e.newWebPhone("106", "Fay")))
	})

	t.Run("revoked device", func(t *testing.T) {
		if _, err := e.store.RevokeDevice(ctx, e.tenant, alice.dev.ID, time.Now(), e.audit("device.revoke")); err != nil {
			t.Fatal(err)
		}
		e.run("revoked-register", "register-rejected.xml", alice)
		e.run("revoked-call", "call-rejected.xml", alice, "-s", "102")
	})
}

// channelDetails is "core show channel" for every channel Asterisk has.
func (e *env) channelDetails() string {
	var b strings.Builder
	for _, line := range strings.Split(e.asteriskCLI("core show channels concise"), "\n") {
		if name, _, ok := strings.Cut(line, "!"); ok {
			out, _ := exec.Command("docker", "exec", astName, "asterisk", "-rx", "core show channel "+name).CombinedOutput()
			b.Write(out)
		}
	}
	return b.String()
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

var rangThirty = regexp.MustCompile(`^Rang Bob \(102\) · nobody answered in 3[01] s`)
