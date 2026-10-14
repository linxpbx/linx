package browsertest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/installer"
)

// The phone bench (make test-phones): the iPhone/iPad app, in two
// simulators, calling itself through a whole Linx on this machine under the
// networks a phone really meets — everything through Linx's relay (a mobile
// network), and no UDP at all (China). A call passes only if both phones
// heard each other, both saw the other's picture, and the call ended when
// it was hung up rather than by itself. ios/tools/bench.sh drives the
// simulators; this brings up Linx, sets the two phones up and reads what
// they reported.

// benchDomain is a name public DNS answers with 127.0.0.1 (nip.io), so the
// simulators reach the stack's published ports with real names and TLS.
const benchDomain = "linx.127.0.0.1.nip.io"

// benchCallSeconds is how long each call lasts once answered.
const benchCallSeconds = 40

type benchScenario struct {
	name  string
	flags []string
	// The relay protocol both phones' sound must end up on ("" = any).
	relay string
	// cut drops every connection to Linx's port 443 for a few seconds in
	// the middle of the call, as a phone moving between networks does.
	cut bool
	// slow is a poor mobile link for the whole call: tc netem's settings
	// on the way in and out of Linx's front door and relay.
	slow string
	// hd is both phones pressing HD: each must then send 720p.
	hd bool
	// badStart is a mobile link that, from just before the answer, opens
	// no new connection for a while (badStartFor) while those already
	// open carry on: the owner's iPhone on 2026-10-14, whose new TLS
	// connections to Linx failed for half a minute and held the picture
	// back 30 s. The answerer rings for six seconds, and the picture must
	// still be there soon after the answer.
	badStart bool
	// newSlow is a mobile link on which, for every connection opened from
	// the moment the call starts, what Linx sends over its first forty
	// packets arrives two seconds late, while connections open before
	// carry on untouched: the owner's iPhone on Etisalat 5G (2026-10-14),
	// whose phone line answered in a tenth of a second while a call link
	// opened for the call took 17–24 s a message and the picture came 45 s
	// after the answer.
	newSlow bool
}

// badStartFor is how long a bad start turns new connections away.
const badStartFor = 12 * time.Second

var benchScenarios = []benchScenario{
	{name: "mobile network: everything through the relay", flags: []string{"relay-only"}},
	// HD on a relayed call: 720p has to go through the relay's per-call
	// cap (turnconf.MaxBPS). The owner pressed it and saw no change
	// (2026-10-14), so the picture's real size is what is checked.
	{name: "mobile network with HD on", flags: []string{"relay-only", "hd"}, hd: true},
	// Phone to phone with no relay forced: several routes work at once, as
	// on the owner's 5G call where the picture went straight between the
	// two (2026-10-14). Reading whichever pair said "nominated" made the
	// route seem to change every few seconds and squeezed the picture.
	{name: "direct between the phones, HD on", flags: []string{"hd"}, hd: true},
	{name: "China: no UDP, the relay over TLS on 443", flags: []string{"no-udp"}, relay: "tls"},
	{name: "China, and the connection cut mid-call", flags: []string{"no-udp"}, relay: "tls", cut: true},
	{name: "poor mobile link: 300 ms round trip, 2% loss", flags: []string{"relay-only"}, slow: "delay 300ms 20ms loss 2%"},
	{name: "China on a poor link: 300 ms round trip, 1% loss", flags: []string{"no-udp"}, relay: "tls",
		slow: "delay 300ms 20ms loss 1%"},
	{name: "a mobile link that opens no new connection as the call is answered",
		flags: []string{"relay-only", "ring=6"}, badStart: true},
	{name: "a mobile link whose new connections are slow for their first part",
		flags: []string{"relay-only", "ring=6"}, newSlow: true},
	// CallKit isn't here: a simulator's own call service ends an app's
	// outgoing CallKit call about a second in (callservicesd asks for the
	// CXEndCallAction itself, 2026-10-13), so it is checked on a phone.
}

// slowDown puts a poor link (tc netem) between the phones and Linx — on
// what the front door and the relay send back to them, once — or takes it
// off again with an empty netem. Only their side facing the phones
// (linx-public): delaying every hop inside Linx as well added up to round
// trips no real network has.
//
// The relay over TLS reaches coturn through linx-sni, across linx-public:
// slowing coturn as well would slow that route twice. So the relay is
// slowed only where the phones reach it directly, over UDP.
func (h *harness) slowDown(netem string, udp bool) {
	prefix := strings.TrimSuffix(publicSubnet, "0/24")
	containers := []string{"linx-sni"}
	if udp {
		containers = append(containers, "linx-coturn")
	}
	for _, c := range containers {
		script := `i=$(ip -o -4 addr show | awk -v p="$2" 'index($4, p) == 1 {print $2; exit}'); ` +
			`[ -n "$i" ] || exit 1; tc qdisc del dev $i root 2>/dev/null; ` +
			`[ -n "$1" ] && tc qdisc add dev $i root netem $1; true`
		h.docker("run", "--rm", "--network", "container:"+c, "--cap-add", "NET_ADMIN",
			"nicolaka/netshoot", "sh", "-c", script, "netem", netem, prefix)
	}
}

// cutFor resets every connection to linx-sni's port 443 (the phones' line
// to Linx, and their relay over TLS) and refuses new ones for a while.
func (h *harness) cutFor(d time.Duration) {
	rule := []string{"INPUT", "-p", "tcp", "--dport", "443", "-j", "REJECT", "--reject-with", "tcp-reset"}
	run := func(op string) {
		h.docker(append([]string{"run", "--rm", "--network", "container:linx-sni", "--cap-add", "NET_ADMIN",
			"nicolaka/netshoot", "iptables", op}, rule...)...)
	}
	run("-I")
	time.Sleep(d)
	run("-D")
}

// noNewConnectionsFor turns away every new connection to linx-sni's port
// 443 (the phones' line to Linx, the call link, the relay over TLS) for a
// while, leaving those already open alone.
func (h *harness) noNewConnectionsFor(d time.Duration) {
	rule := []string{"INPUT", "-p", "tcp", "--dport", "443", "--syn", "-j", "DROP"}
	run := func(op string) {
		h.docker(append([]string{"run", "--rm", "--network", "container:linx-sni", "--cap-add", "NET_ADMIN",
			"nicolaka/netshoot", "iptables", op}, rule...)...)
	}
	run("-I")
	time.Sleep(d)
	run("-D")
}

// newConnectionsSlow marks every connection to linx-sni's port 443 opened
// in the next while, and makes what Linx sends over a marked connection's
// first forty packets arrive two seconds late. Connections already open
// aren't touched. The returned func takes it all off again.
func (h *harness) newConnectionsSlow(window time.Duration) func() {
	prefix := strings.TrimSuffix(publicSubnet, "0/24")
	on := `i=$(ip -o -4 addr show | awk -v p="$1" 'index($4, p) == 1 {print $2; exit}'); [ -n "$i" ] || exit 1; ` +
		`tc qdisc add dev $i root handle 1: prio bands 3 priomap 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 && ` +
		`tc qdisc add dev $i parent 1:3 handle 30: netem delay 2000ms limit 100000 && ` +
		`tc filter add dev $i parent 1:0 protocol ip handle 7 fw flowid 1:3 && ` +
		`iptables -t mangle -A PREROUTING -p tcp --dport 443 -m conntrack --ctstate NEW -j CONNMARK --set-mark 7 && ` +
		`iptables -t mangle -A OUTPUT -p tcp --sport 443 -m connmark --mark 7 -m connbytes --connbytes 0:40 ` +
		`--connbytes-dir both --connbytes-mode packets -j MARK --set-mark 7`
	stopMarking := `iptables -t mangle -D PREROUTING -p tcp --dport 443 -m conntrack --ctstate NEW -j CONNMARK --set-mark 7`
	off := `i=$(ip -o -4 addr show | awk -v p="$1" 'index($4, p) == 1 {print $2; exit}'); ` +
		`iptables -t mangle -F OUTPUT; iptables -t mangle -D PREROUTING -p tcp --dport 443 -m conntrack --ctstate NEW -j CONNMARK --set-mark 7 2>/dev/null; ` +
		`tc qdisc del dev $i root 2>/dev/null; true`
	run := func(script string) {
		h.docker("run", "--rm", "--network", "container:linx-sni", "--cap-add", "NET_ADMIN",
			"nicolaka/netshoot", "sh", "-c", script, "slow", prefix)
	}
	run(on)
	done := make(chan struct{})
	go func() {
		select {
		case <-time.After(window):
			run(stopMarking)
		case <-done:
		}
	}()
	return func() {
		close(done)
		run(off)
	}
}

// whenRinging returns once Asterisk has a channel ringing.
func (h *harness) whenRinging(t *testing.T, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !strings.Contains(h.docker("exec", "linx-asterisk", "asterisk", "-rx", "core show channels concise"), "!Ringing!") {
		if time.Now().After(deadline) {
			t.Fatal("no call rang")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestPhoneBenchDocker(t *testing.T) {
	if os.Getenv("LINX_PHONE_BENCH") != "1" {
		t.Skip("set LINX_PHONE_BENCH=1 (make test-phones)")
	}
	images := map[string]string{
		"control-plane": envOr("LINX_CONTROL_PLANE_IMAGE", "linx-control-plane:dev"),
		"asterisk":      envOr("LINX_ASTERISK_IMAGE", "linx-asterisk:dev"),
		"coturn":        envOr("LINX_COTURN_IMAGE", "linx-coturn:dev"),
		"livekit":       envOr("LINX_LIVEKIT_IMAGE", "linx-livekit:dev"),
	}
	for svc, img := range images {
		if !imageExists(img) {
			t.Skipf("no %s image %s: run make image SERVICE=%s", svc, img, svc)
		}
	}
	domain = benchDomain
	bench := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("../../ios/tools/bench.sh", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("bench.sh %s: %v\n%s", args[0], err, out)
		}
		return string(out)
	}
	if os.Getenv("LINX_BENCH_NO_BUILD") != "1" {
		bench("build")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	h := &harness{door: installer.FrontDoorLinx443, t: t, ctx: ctx, dir: t.TempDir(), bench: true}
	for svc, img := range images {
		h.docker("tag", img, imagePrefix+"/linx-"+svc+":test")
	}
	h.start()
	caller, answerer := h.benchPhones(t)
	bench("prepare", filepath.Join(h.dir, "tls", "root_ca.crt"))

	only := os.Getenv("LINX_BENCH_ONLY") // part of a scenario's name, or several joined with |
	for _, sc := range benchScenarios {
		if only != "" && !slices.ContainsFunc(strings.Split(only, "|"), func(o string) bool { return strings.Contains(sc.name, o) }) {
			continue
		}
		t.Run(sc.name, func(t *testing.T) {
			since := h.psql("select now()")
			if sc.slow != "" {
				udp := !slices.Contains(sc.flags, "no-udp")
				h.slowDown(sc.slow, udp)
				defer h.slowDown("", udp)
			}
			if sc.newSlow {
				// Five seconds after the answerer's app starts and three
				// before the caller's: the answerer's line, open by then, is
				// the phone line that stays good; everything opened from
				// here is bad for its first part, the caller's line too.
				undone := make(chan func(), 1)
				go func() {
					time.Sleep(5 * time.Second)
					undone <- h.newConnectionsSlow(45 * time.Second)
				}()
				defer func() { (<-undone)() }()
			}
			bench(append([]string{"call", caller, answerer, "202", strconv.Itoa(benchCallSeconds)}, sc.flags...)...)
			// LINX_BENCH_SHOTS=DIR: both screens at moments through the
			// call, to look at what a person holding them would see.
			if dir := os.Getenv("LINX_BENCH_SHOTS"); dir != "" {
				name := strings.Fields(sc.name)[0]
				go func() {
					start := time.Now()
					for _, at := range []time.Duration{8, 12, 18, 30} {
						time.Sleep(time.Until(start.Add(at * time.Second)))
						_ = exec.Command("../../ios/tools/bench.sh", "shot", dir,
							fmt.Sprintf("%s-%02ds", strings.TrimSuffix(name, ":"), at)).Run()
					}
				}()
			}
			if sc.badStart {
				// Two and a half seconds into the ringing: what the phones
				// open as the call starts ringing gets through, what they
				// open at the answer (three and a half seconds later) doesn't.
				h.whenRinging(t, 60*time.Second)
				time.Sleep(2500 * time.Millisecond)
				h.noNewConnectionsFor(badStartFor)
			}
			if sc.cut {
				// About fifteen seconds into the answered call.
				time.Sleep(20 * time.Second)
				h.cutFor(4 * time.Second)
			}
			reports := h.benchReports(t, since, 2, time.Duration(benchCallSeconds+60)*time.Second)
			for who, r := range reports {
				t.Logf("--- %s:\n%s", who, r)
			}
			for who, r := range reports {
				h.benchCheck(t, who, r, sc)
			}
		})
	}
}

// benchPhones makes two people with extensions 201 and 202 and a setup link
// for each one's phone: the caller's and the answerer's.
func (h *harness) benchPhones(t *testing.T) (caller, answerer string) {
	out := h.docker("exec", "linx-control-plane", "/usr/local/bin/service", "api-key", "create",
		"--name", "phone bench", "--role", "admin", "--scopes", "all,devices:write")
	h.key = keyRe.FindString(out)
	if h.key == "" {
		t.Fatalf("no API key in:\n%s", out)
	}
	links := map[string]string{}
	for _, n := range []string{"201", "202"} {
		h.call("POST", "/api/v1/extensions", map[string]string{"number": n, "display_name": "Bench " + n}, nil)
		email := "bench" + n + "@" + benchDomain
		h.docker("exec", "linx-control-plane", "/usr/local/bin/service", "user", "create",
			"--email", email, "--name", "Bench "+n, "--role", "user", "--extension", n)
		var users struct {
			Items []struct {
				ID    string `json:"id"`
				Email string `json:"email"`
			} `json:"items"`
		}
		h.call("GET", "/api/v1/users?limit=100", nil, &users)
		var id string
		for _, u := range users.Items {
			if u.Email == email {
				id = u.ID
			}
		}
		if id == "" {
			t.Fatalf("no user %s", email)
		}
		var enrolled struct {
			Token string `json:"token"`
		}
		h.call("POST", "/api/v1/enrollments", map[string]string{"name": "Bench phone " + n, "user_id": id}, &enrolled)
		links[n] = "https://" + benchDomain + "/set-up-phone#" + enrolled.Token
	}
	return links["201"], links["202"]
}

// psql runs one query on the stack's database and returns its answer.
func (h *harness) psql(query string) string {
	return strings.TrimSpace(h.docker("exec", "linx-postgres", "psql", "-U", "linx", "-d", "linx", "-tA", "-c", query))
}

// benchReports waits for n call records sent since `since`, by device name.
func (h *harness) benchReports(t *testing.T, since string, n int, within time.Duration) map[string]string {
	t.Helper()
	q := fmt.Sprintf(`select d.name || E'\x1f' || r.report from phone_call_report r join device d on d.id = r.device_id
		where r.created_at > '%s' and r.report like 'Linx call details%%' order by r.created_at`, since)
	deadline := time.Now().Add(within)
	for {
		out := h.psql(q + ";")
		got := map[string]string{}
		for _, rec := range strings.Split(out, "Bench phone ") {
			name, report, ok := strings.Cut(rec, "\x1f")
			if ok {
				got["Bench phone "+name] = report
			}
		}
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d phones reported the call:\n%s", len(got), n, out)
		}
		time.Sleep(3 * time.Second)
	}
}

var (
	soundInRe = regexp.MustCompile(`sound in: (\d+) bytes`)
	framesRe  = regexp.MustCompile(`Their picture: (\d+) frames`)
	routeRe   = regexp.MustCompile(`route: relayed \((\w+)\)`)
)

var markRe = regexp.MustCompile(`\+([0-9.]+) s (.+)`)

// markAt is when a phone's record first says what (seconds into the call).
func markAt(r, what string) (float64, bool) {
	for _, m := range markRe.FindAllStringSubmatch(r, -1) {
		if strings.HasPrefix(m[2], what) {
			at, _ := strconv.ParseFloat(m[1], 64)
			return at, true
		}
	}
	return 0, false
}

// benchCheck holds one phone's record of the call to what a good call is.
func (h *harness) benchCheck(t *testing.T, who, r string, sc benchScenario) {
	t.Helper()
	fail := func(why string) { t.Errorf("%s: %s", who, why) }
	if !strings.Contains(r, "connection: connected") {
		fail("the call never connected")
	}
	// A simulator's microphone is usually silent, and Opus sends next to
	// nothing for silence (DTX): what counts is that sound flowed both ways
	// for the whole call, not how loud it was.
	if !strings.Contains(r, "First sound arrived") {
		fail("no sound arrived")
	}
	if m := soundInRe.FindStringSubmatch(r); m == nil {
		fail("no count of the sound that arrived")
	} else if b, _ := strconv.Atoi(m[1]); b < 1_500 {
		fail(fmt.Sprintf("hardly any sound arrived (%d bytes)", b))
	}
	if !strings.Contains(r, "Their picture arrived") {
		fail("the other phone's picture never arrived")
	}
	if m := framesRe.FindStringSubmatch(r); m == nil {
		fail("no count of the other phone's picture")
	} else if f, _ := strconv.Atoi(m[1]); f < 10*(benchCallSeconds-15) {
		fail(fmt.Sprintf("too little of the other phone's picture (%d frames)", f))
	}
	if strings.Contains(r, "ended: failed") {
		fail("the call ended by itself")
	}
	if strings.Contains(r, "Line to Linx lost") && !strings.Contains(r, "Line to Linx back after") {
		fail("the line to Linx never came back during the call")
	}
	if sc.cut {
		// After the cut, sound and picture both have to be flowing again:
		// the last thing either says about its route is that it works.
		if lost := strings.LastIndex(r, "Sound route "); lost >= 0 && !strings.Contains(r[lost:], "Sound route restarted") &&
			!strings.HasPrefix(r[lost:], "Sound route restarted") {
			fail("the sound's route was lost and never restarted")
		}
	}
	if sc.hd {
		on, ok := markAt(r, "HD on")
		at, sent := markAt(r, "Sending 720p")
		switch {
		case !sent:
			fail("HD was on and the picture never went out at 720p")
		case ok && at-on > 20:
			fail(fmt.Sprintf("HD took %.0f s to reach 720p", at-on))
		}
	}
	if sc.badStart || sc.newSlow {
		answered, ok := markAt(r, "Answered")
		if !ok {
			answered, ok = markAt(r, "→ 200 OK (INVITE)")
		}
		ready, linked := markAt(r, "Picture link ready")
		arrived, shown := markAt(r, "Their picture arrived")
		switch {
		case !strings.Contains(r, "Picture link open, waiting for the answer"):
			fail("the picture link wasn't open before the answer")
		case !ok || !linked || !shown:
			fail("no answer, picture link or picture in the record")
		case ready-answered > 3:
			fail(fmt.Sprintf("the picture link was ready %.1f s after the answer", ready-answered))
		case arrived-answered > 8:
			fail(fmt.Sprintf("their picture arrived %.1f s after the answer", arrived-answered))
		}
	}
	if !sc.cut && strings.Count(r, "Picture route now") > 2 {
		fail(fmt.Sprintf("the picture's route changed %d times", strings.Count(r, "Picture route now")))
	}
	if sc.relay != "" {
		if m := routeRe.FindStringSubmatch(r); m == nil || m[1] != sc.relay {
			fail("the sound wasn't relayed over " + sc.relay)
		}
	}
}
