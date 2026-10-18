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
	"sync"
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
	// newLine is the owner's 5G call of 2026-10-14 20:53: the phone being
	// rung had only just opened its line (woken for the call), and what
	// Linx sent on it arrived seconds late — 5 s for the picture link's
	// first word, 6–7 s for the picture's answer — from the ringing until
	// some seconds after the answer, while what it sent arrived in good
	// time. Its picture came 11 s after the answer and the caller's 9 s.
	newLine bool
	// pictureRelay: every route the picture takes starts at this phone's
	// relay (mobile data).
	pictureRelay bool
	// squeeze holds the relay's way to the phones to a thin rate for a
	// stretch mid-call, then lets go: the picture must come back up to
	// 540p by itself. The owner's iPhone, squeezed to 360p after HD on
	// 5G, never tried a bigger size again (2026-10-15).
	squeeze bool
	// blackout is everything between the phones and Linx stopping for two
	// seconds mid-call — the line, the relay over TLS and over UDP alike —
	// as a phone moving between cells, or getting a new address, does.
	blackout bool
	// starlink is a satellite link that every fifteen seconds hands over
	// to the next satellite: a moment of loss and a jump in delay, on top
	// of an ordinary link (owner, 2026-10-16).
	starlink bool
	// away is both people switching to another app mid-call for a while
	// and coming back: the call carries on in its small window, both
	// pictures still moving (owner, 2026-10-18).
	away bool
	// directCut stops only the direct link between the two phones for a
	// while mid-call (the relay relaying to itself), leaving Asterisk's
	// route alone: the sound must go back through Linx and return
	// (ADR-092, docs/DIRECT_SOUND.md §6).
	directCut bool
	// cameraOff is a voice call turned into a video call, both cameras
	// on, and then the caller's camera off again: the other phone's
	// picture must stay on the caller's screen (owner, build 52: it
	// showed the video screen with no picture until the iPad's camera
	// went off too).
	cameraOff bool
}

// directCutFrom and directCutFor are when in the call the direct link is
// cut, and for how long.
const (
	directCutFrom = 20 * time.Second
	directCutFor  = 5 * time.Second
	// The link takes up to fifteen seconds to find its way again after a
	// cut, and the sound waits five steady seconds before going back to it.
	directCutCallSeconds = 70
)

// awayFrom and awayFor are when in the call both phones go to another app,
// and for how long.
const (
	awayFrom = 18 * time.Second
	awayFor  = 12 * time.Second
)

// squeezeRate, squeezeFrom and squeezeFor are the squeeze: what the relay
// may send the phones, from when in the call, for how long. The call runs
// squeezeCallSeconds so there is time to come back.
const (
	squeezeRate        = "rate 250kbit"
	squeezeFrom        = 15 * time.Second
	squeezeFor         = 15 * time.Second
	squeezeCallSeconds = 90
)

// newLineDelay is how late what Linx sends on a just-opened line arrives
// (newLine).
const newLineDelay = 3 * time.Second

// newLineFor is how long it stays that slow from the ringing: through the
// four seconds' ringing and ten seconds past the answer.
const newLineFor = 14 * time.Second

// benchPictureAfterAnswer is the most, in seconds, from the answer to the
// other phone's picture on a bad mobile link (badStart, newSlow, newLine).
const benchPictureAfterAnswer = 3

// benchSoundDirectAfterAnswer is the most, in seconds, from the answer to
// the sound moving onto the direct link on the bench's links. The sound
// carries on through Linx until then; in flight (600 ms over TLS at
// 256 kbit/s) the link took 16.7 s to be ready once.
const benchSoundDirectAfterAnswer = 30

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
	// Both phones on mobile data with every route open to their sound:
	// the picture goes through the relay alone, from its first route to
	// its last. On the owner's 5G the direct route came and went all call
	// long and the picture froze each time (2026-10-15).
	{name: "on mobile data: the picture through the relay alone", flags: []string{"mobile-data", "hd"}, hd: true, pictureRelay: true},
	{name: "a thin stretch mid-call, then room again", flags: []string{"relay-only"}, squeeze: true},
	{name: "China: no UDP, the relay over TLS on 443", flags: []string{"no-udp"}, relay: "tls"},
	{name: "China, and the connection cut mid-call", flags: []string{"no-udp"}, relay: "tls", cut: true},
	{name: "poor mobile link: 300 ms round trip, 2% loss", flags: []string{"relay-only"}, slow: "delay 300ms 20ms loss 2%"},
	{name: "China on a poor link: 300 ms round trip, 1% loss", flags: []string{"no-udp"}, relay: "tls",
		slow: "delay 300ms 20ms loss 1%"},
	{name: "a mobile link that opens no new connection as the call is answered",
		flags: []string{"relay-only", "ring=6"}, badStart: true},
	{name: "a mobile link whose new connections are slow for their first part",
		flags: []string{"relay-only", "ring=6"}, newSlow: true},
	{name: "the phone being rung has just opened its line, and it is slow",
		flags: []string{"relay-only", "ring=4", "late-answerer"}, newLine: true},
	{name: "a two-second blackout mid-call: a new cell or a new address",
		flags: []string{"relay-only", "mobile-data"}, blackout: true},
	{name: "a mobile network with a long queue: delay that grows with what is sent",
		flags: []string{"relay-only", "mobile-data"}, slow: "delay 120ms 20ms rate 1500kbit limit 600"},
	{name: "Starlink: a handover every fifteen seconds",
		flags: []string{"relay-only", "mobile-data"}, slow: "delay 30ms 10ms", starlink: true},
	{name: "switching to another app mid-call: the small window", flags: []string{"relay-only"}, away: true},
	{name: "the direct link cut mid-call: the sound carries on through Linx",
		flags: []string{"relay-only", "audio"}, directCut: true},
	{name: "video added to a voice call, then one camera off: the other picture stays",
		flags: []string{"relay-only", "audio", "camera-on=6", "camera-off=22", "mine-on-too"}, cameraOff: true},
	// The same the other way round, on mobile data, as the owner did it:
	// the phone that was rung turned its camera on and off.
	{name: "video added by the phone that was rung, then its camera off: the other picture stays",
		flags:     []string{"relay-only", "mobile-data", "audio", "camera-on=6", "camera-off=22", "mine-on-too", "answerer-camera"},
		cameraOff: true},
	// And with the line to Linx dropped just after the camera went on, so
	// the picture's link is made again with both cameras on: the owner's
	// call of 2026-10-18 (build 52), where the picture then stayed off the
	// screen once that phone's camera went off.
	{name: "video added, the line dropped, then the camera off: the other picture stays",
		flags:     []string{"relay-only", "mobile-data", "audio", "camera-on=6", "camera-off=30", "mine-on-too", "answerer-camera"},
		cameraOff: true, cut: true},
	// With the pictures swapped first (this phone's own big), as a person
	// looking at themselves does.
	{name: "video added, pictures swapped, then the camera off: the other picture stays",
		flags:     []string{"relay-only", "mobile-data", "audio", "camera-on=6", "swap=14", "camera-off=22", "mine-on-too", "answerer-camera"},
		cameraOff: true},
	{name: "a ship's satellite: 800 ms, 512 kbit/s, 1% loss",
		flags: []string{"relay-only", "mobile-data"}, slow: "delay 800ms 40ms loss 1% rate 512kbit"},
	{name: "in flight: 443 only, 256 kbit/s, a voice call",
		flags: []string{"no-udp", "audio"}, relay: "tls", slow: "delay 600ms 50ms loss 1% rate 256kbit"},
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

// blackoutFor stops everything between the phones and Linx for a while:
// their TCP to the front door is reset and their UDP to the relay dropped.
func (h *harness) blackoutFor(d time.Duration) {
	peer := strings.TrimSuffix(publicSubnet, "0/24") + "1"
	rules := map[string][]string{
		"linx-sni":    {"INPUT", "-p", "tcp", "--dport", "443", "-j", "REJECT", "--reject-with", "tcp-reset"},
		"linx-coturn": {"INPUT", "-s", peer, "-p", "udp", "-j", "DROP"},
	}
	run := func(op string) {
		for c, rule := range rules {
			h.docker(append([]string{"run", "--rm", "--network", "container:" + c, "--cap-add", "NET_ADMIN",
				"nicolaka/netshoot", "iptables", op}, rule...)...)
		}
	}
	run("-I")
	time.Sleep(d)
	run("-D")
}

// directCutFor stops what the relay relays to itself, which is the direct
// link between two phones that both go through it, for a while; their
// routes to Asterisk go to another address and carry on.
func (h *harness) directCutFor(d time.Duration) {
	rule := []string{"OUTPUT", "-p", "udp", "-m", "addrtype", "--src-type", "LOCAL", "--dst-type", "LOCAL", "-j", "DROP"}
	run := func(op string) {
		h.docker(append([]string{"run", "--rm", "--network", "container:linx-coturn", "--cap-add", "NET_ADMIN",
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

// newLineSlow marks the connections opened from now for `window` (the
// answerer's line, as its app starts: newLine). The first func returned
// makes what Linx sends on them arrive newLineDelay late from then on;
// the second undoes it all.
func (h *harness) newLineSlow(t *testing.T, window time.Duration) (slow, undo func()) {
	prefix := strings.TrimSuffix(publicSubnet, "0/24")
	iface := `i=$(ip -o -4 addr show | awk -v p="$1" 'index($4, p) == 1 {print $2; exit}'); [ -n "$i" ] || exit 1; `
	mark := `iptables -t mangle -A PREROUTING -p tcp --dport 443 -m conntrack --ctstate NEW -j CONNMARK --set-mark 7`
	on := iface + fmt.Sprintf(`tc qdisc add dev $i root handle 1: prio bands 3 priomap 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 && `+
		`tc qdisc add dev $i parent 1:3 handle 30: netem delay %dms limit 100000 && `+
		`tc filter add dev $i parent 1:0 protocol ip handle 7 fw flowid 1:3 && `, newLineDelay.Milliseconds()) + mark
	delay := `iptables -t mangle -A OUTPUT -p tcp --sport 443 -m connmark --mark 7 ` + os.Getenv("LINX_BENCH_SLOW_MATCH") + ` -j MARK --set-mark 7`
	off := iface + `iptables -t mangle -F OUTPUT; ` + strings.Replace(mark, "-A", "-D", 1) + ` 2>/dev/null; ` +
		`tc qdisc del dev $i root 2>/dev/null; true`
	run := func(script string) string {
		return h.docker("run", "--rm", "--network", "container:linx-sni", "--cap-add", "NET_ADMIN",
			"nicolaka/netshoot", "sh", "-c", script, "slow", prefix)
	}
	run(on)
	t.Logf("slow line: marking from %s", time.Now().Format("15:04:05.0"))
	stop := time.AfterFunc(window, func() {
		run(strings.Replace(mark, "-A", "-D", 1))
		t.Logf("slow line: marking stopped %s", time.Now().Format("15:04:05.0"))
	})
	var once sync.Once
	return func() {
			run(delay)
			t.Logf("slow line: delaying from %s", time.Now().Format("15:04:05.0"))
		}, func() {
			once.Do(func() {
				stop.Stop()
				t.Logf("slow line, at the end:\n%s\n%s", run("iptables -t mangle -L -v -n; conntrack -L -m 7 2>&1 | head"), time.Now().Format("15:04:05.0"))
				run(off)
			})
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
			// Relay places still held from earlier cases: the apps are
			// stopped between cases, and what they leave behind counts
			// against each person's 20 (turnconf.UserQuota) until it
			// expires.
			t.Logf("relay places open: %s", strings.TrimSpace(h.docker("run", "--rm", "--network", "container:linx-coturn",
				"nicolaka/netshoot", "sh", "-c", `ss -uan | awk '{print $4}' | grep -cE ':(49|5[0-9]|6[0-5])[0-9]{3}$' || true`)))
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
			var slowLine func()
			if sc.newLine {
				// The answerer's app starts eight seconds after the caller's
				// (late-answerer): its line is what opens around then.
				type model struct{ slow, undo func() }
				marked := make(chan model, 1)
				go func() {
					time.Sleep(6 * time.Second)
					slow, undo := h.newLineSlow(t, 6*time.Second)
					marked <- model{slow, undo}
				}()
				var m model
				got := func() model {
					if m.slow == nil {
						m = <-marked
					}
					return m
				}
				defer func() { got().undo() }()
				slowLine = func() {
					got().slow()
					time.AfterFunc(newLineFor, got().undo)
				}
			}
			// LINX_BENCH_FLAGS: more bench.sh flags for every call, to
			// measure against (no-early: the picture set up after the
			// answer, as before build 35).
			flags := append(slices.Clone(sc.flags), strings.Fields(os.Getenv("LINX_BENCH_FLAGS"))...)
			t.Logf("bench call from %s", time.Now().Format("15:04:05.0"))
			seconds := benchCallSeconds
			if sc.directCut {
				seconds = directCutCallSeconds
			}
			if sc.squeeze {
				seconds = squeezeCallSeconds
			}
			bench(append([]string{"call", caller, answerer, "202", strconv.Itoa(seconds)}, flags...)...)
			t.Logf("bench call launched %s", time.Now().Format("15:04:05.0"))
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
			if sc.newLine {
				// Slow from the ringing until ten seconds after the answer.
				h.whenRinging(t, 60*time.Second)
				slowLine()
			}
			if sc.badStart {
				// Two and a half seconds into the ringing: what the phones
				// open as the call starts ringing gets through, what they
				// open at the answer (three and a half seconds later) doesn't.
				h.whenRinging(t, 60*time.Second)
				time.Sleep(2500 * time.Millisecond)
				h.noNewConnectionsFor(badStartFor)
			}
			if sc.away {
				h.whenRinging(t, 60*time.Second)
				time.Sleep(awayFrom)
				bench("away", strconv.Itoa(int(awayFor.Seconds())))
			}
			if sc.cut {
				// About fifteen seconds into the answered call.
				time.Sleep(20 * time.Second)
				h.cutFor(4 * time.Second)
			}
			if sc.blackout {
				time.Sleep(20 * time.Second)
				h.blackoutFor(2 * time.Second)
			}
			if sc.directCut {
				h.whenRinging(t, 60*time.Second)
				time.Sleep(directCutFrom)
				h.directCutFor(directCutFor)
			}
			if sc.starlink {
				stop := make(chan struct{})
				defer close(stop)
				go func() {
					for {
						select {
						case <-stop:
							return
						case <-time.After(15 * time.Second):
						}
						h.slowDown("delay 110ms 20ms loss 30%", true)
						time.Sleep(400 * time.Millisecond)
						h.slowDown(sc.slow, true)
					}
				}()
			}
			if sc.squeeze {
				h.whenRinging(t, 60*time.Second)
				time.Sleep(squeezeFrom)
				t.Logf("squeeze from %s", time.Now().Format("15:04:05.0"))
				h.slowDown(squeezeRate, true)
				time.Sleep(squeezeFor)
				h.slowDown("", true)
				t.Logf("squeeze off %s", time.Now().Format("15:04:05.0"))
			}
			reports := h.benchReports(t, since, 2, time.Duration(seconds+60)*time.Second)
			for who, r := range reports {
				t.Logf("--- %s:\n%s", who, r)
			}
			for who, r := range reports {
				h.benchCheck(t, who, r, sc)
			}
			// Linx judged each phone's leg itself (internal/callquality), and
			// found the phone's own report of the same call beside it.
			judged := h.psql("select count(*) from call_quality where kind = 'call' and ended_at > '" + since + "'")
			if n, _ := strconv.Atoi(strings.TrimSpace(judged)); n < 2 {
				t.Errorf("Linx judged %s legs of the call, want both phones'", strings.TrimSpace(judged))
			}
			t.Logf("Linx's verdicts: %s", h.psql("select string_agg(verdict || ' ' || array_to_string(reasons, '; '), ' | ') "+
				"from call_quality where ended_at > '"+since+"'"))
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
	soundInRe     = regexp.MustCompile(`sound in: (\d+) bytes`)
	framesRe      = regexp.MustCompile(`Their picture: (\d+) frames`)
	smallWindowRe = regexp.MustCompile(`Small window off after (\d+) frames`)
	routeRe       = regexp.MustCompile(`route: relayed \((\w+)\)`)
	encodeRe      = regexp.MustCompile(`AV1 (\d+) ms a frame`)
	stoppedRe     = regexp.MustCompile(`Their picture stopped (\d+) times, ([0-9.]+) s in all`)
)

// benchEncodeMostMs is the most an AV1 frame may take to make: at 30
// frames a second there are 33 ms for each, and the phone has other work.
const benchEncodeMostMs = 25

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
// pictureRouteRe is one of the picture's routes, "local → remote".
var pictureRouteRe = regexp.MustCompile(`Picture route(?: now)?: (\S+) →`)

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
	video := !slices.Contains(sc.flags, "audio")
	if video && !strings.Contains(r, "Their picture arrived") {
		fail("the other phone's picture never arrived")
	}
	if !video {
		// A voice call.
	} else if m := framesRe.FindStringSubmatch(r); m == nil {
		fail("no count of the other phone's picture")
	} else if f, _ := strconv.Atoi(m[1]); f < 10*(benchCallSeconds-15) {
		fail(fmt.Sprintf("too little of the other phone's picture (%d frames)", f))
	}
	// Each phone's microphone reaches the call: sound goes out, and the
	// app never had to start it again (the owner's iPhone sent none for a
	// whole call, build 37).
	if !strings.Contains(r, "First sound went out") {
		fail("no sound ever went out")
	}
	if strings.Contains(r, "No sound going out") {
		fail("the phone found no sound going out mid-call")
	}
	// Two apps send their pictures in AV1 (owner, 2026-10-15), and keep up
	// with it: the encoder never held back by the processor for long.
	if video && !strings.Contains(r, "Picture in AV1") {
		fail("the picture didn't go in AV1")
	}
	if strings.Contains(r, "can't keep up with AV1") {
		fail("the phone couldn't keep up with AV1")
	}
	for _, m := range encodeRe.FindAllStringSubmatch(r, -1) {
		if ms, _ := strconv.Atoi(m[1]); ms > benchEncodeMostMs {
			fail(fmt.Sprintf("AV1 took %d ms a frame", ms))
		}
	}
	if strings.Contains(r, "ended: failed") {
		fail("the call ended by itself")
	}
	// The sound moves onto the direct link (ADR-092), unless the bench is
	// measuring the old way (LINX_BENCH_FLAGS=no-direct-sound). How well it
	// arrived on each route goes in the log, for comparing the two.
	for _, line := range strings.Split(r, "\n") {
		if i := strings.Index(line, "Sound through Linx: "); i >= 0 {
			t.Logf("%s: %s", who, line[i:])
		}
		if i := strings.Index(line, "Sound on the direct link: "); i >= 0 {
			t.Logf("%s: %s", who, line[i:])
		}
	}
	if !strings.Contains(os.Getenv("LINX_BENCH_FLAGS"), "no-direct-sound") {
		switched, ok := markAt(r, "Sound: playing theirs from the direct link")
		answered, _ := markAt(r, "Answered")
		switch {
		case !ok:
			fail("the sound never moved onto the direct link")
		case answered > 0 && switched-answered > benchSoundDirectAfterAnswer:
			fail(fmt.Sprintf("the sound moved onto the direct link %.1f s after the answer", switched-answered))
		}
		if !strings.Contains(r, "Sound: they hear mine directly") {
			fail("the other phone never played this one's sound from the direct link")
		}
	}
	if sc.directCut {
		fell, ok := markAt(r, "Sound: theirs stopped on the direct link")
		back := strings.LastIndex(r, "Sound: playing theirs from the direct link")
		switch {
		case !ok:
			fail("the direct link was cut and the sound never went back through Linx")
		case back < strings.Index(r, "Sound: theirs stopped on the direct link"):
			fail(fmt.Sprintf("the sound went back through Linx at +%.1f s and never returned to the direct link", fell))
		case !strings.Contains(r, "Sound: they hear mine through Linx again"):
			fail("the other phone fell back and this one never sent through Linx again")
		}
	}
	if sc.cameraOff {
		// The phone whose camera goes off: the caller (201), or the phone
		// that was rung (202).
		mine := strings.Contains(who, "201") != slices.Contains(sc.flags, "answerer-camera")
		// From the camera going off to the call's end (its record's
		// summary), the other phone's picture stays on the screen.
		off, wentOff := markAt(r, "Camera off")
		end, _ := markAt(r, "Their picture: ")
		shown := false
		for _, m := range markRe.FindAllStringSubmatch(r, -1) {
			at, _ := strconv.ParseFloat(m[1], 64)
			switch {
			case strings.HasPrefix(m[2], "Their picture on the screen"):
				shown = true
			case strings.HasPrefix(m[2], "Their picture off the screen") && mine && wentOff && at > off && at < end-1:
				fail(fmt.Sprintf("the camera went off at +%.1f s and the other phone's picture left the screen at +%.1f s", off, at))
			}
		}
		switch {
		case !strings.Contains(r, "Camera on"):
			fail("the camera never came on")
		case mine && !wentOff:
			fail("the camera never went off")
		case mine && !shown:
			fail("the other phone's picture never came on the screen")
		}
	}
	if strings.Contains(r, "Line to Linx lost") && !strings.Contains(r, "Line to Linx back after") {
		fail("the line to Linx never came back during the call")
	}
	if sc.cut || sc.blackout {
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
	if sc.badStart || sc.newSlow || sc.newLine {
		answered, ok := markAt(r, "Answered")
		if !ok {
			answered, ok = markAt(r, "→ 200 OK (INVITE)")
		}
		arrived, shown := markAt(r, "Their picture arrived")
		switch {
		case !strings.Contains(r, "Picture link open, waiting for the answer"):
			fail("the picture link wasn't open before the answer")
		case !strings.Contains(r, "while ringing"):
			fail("the picture wasn't set up while the call rang")
		case !ok || !shown:
			fail("no answer or picture in the record")
		case arrived-answered > benchPictureAfterAnswer:
			fail(fmt.Sprintf("their picture arrived %.1f s after the answer", arrived-answered))
		}
	}
	if sc.away {
		// The small window opened, showed the other phone's picture all
		// the while, and closed again when the call came back.
		switch m := smallWindowRe.FindStringSubmatch(r); {
		case strings.Contains(r, "This device can't show a small window") && strings.Contains(who, "201"):
			// The bench's iPhone simulator has no Picture in Picture
			// (a real iPhone has); the iPad proves it.
		case !strings.Contains(r, "Small window on"):
			fail("the call didn't go into its small window")
		case m == nil:
			fail("the small window never closed")
		default:
			if f, _ := strconv.Atoi(m[1]); f < 8*int(awayFor.Seconds()) {
				fail(fmt.Sprintf("the small window showed only %d frames", f))
			}
		}
	}
	if sc.squeeze {
		// Squeezed, and back to 540p by the end.
		down := strings.LastIndex(r, "Picture down to ")
		switch {
		case down < 0:
			fail("the squeeze never brought the picture down")
		case !strings.Contains(r[down:], "Sending 540p") && !strings.Contains(r[down:], "Sending 720p"):
			fail("the picture never came back up to 540p after the squeeze")
		}
	}
	if sc.pictureRelay {
		if !strings.Contains(r, "On mobile data: the picture goes through the relay") {
			fail("the phone didn't know it was on mobile data")
		}
		for _, m := range pictureRouteRe.FindAllStringSubmatch(r, -1) {
			if !strings.HasPrefix(m[1], "relay/") {
				fail("the picture took a route that isn't the relay: " + m[1])
			}
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
