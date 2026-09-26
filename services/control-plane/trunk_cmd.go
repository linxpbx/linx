package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"golang.org/x/term"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/safehttp"
	"linxpbx.com/linx/internal/store"
	"linxpbx.com/linx/internal/trunk"
	"linxpbx.com/linx/internal/trunkconf"
	"linxpbx.com/linx/internal/trunkprobe"
	"linxpbx.com/linx/internal/trunkstatus"
)

const trunkUsage = `Usage:
  linx trunk cert ADDRESS [--out NAME]
  linx trunk add [options]
  linx trunk list
  linx trunk test NAME
  linx trunk remove NAME [--yes]
  linx trunk wireguard add NAME < provider.conf
  linx trunk wireguard list
  linx trunk wireguard remove NAME [--yes]

cert    Make a certificate for a phone system on your network (a UCM) to
        upload to it, naming exactly its address; then pin it with
        add --pin. Runs on the server; Linx never keeps its key.
add     Connect a phone line: a provider, or another phone system like a
        Grandstream UCM (docs/TRUNKS.md). Asks what it needs, tests the
        connection (address, certificate, login) and only then saves.
        Options answer the questions ahead of time (--yes: ask nothing):
          --template NAME      telnyx, voipms, twilio, grandstream_ucm,
                               linx or generic
          --name NAME          what to call it ("UCM landlines")
          --host ADDRESS       its address (sip.provider.com, 192.168.1.5)
          --port PORT
          --username LOGIN     for a line Linx signs in to
          --password-stdin     read the password from the first line of input
          --pin FILE           a certificate (PEM) to trust for it
          --unencrypted        use TCP without encryption if it can't do TLS
                               (calls can be listened to on the way)
          --did NUMBER[=EXT]   a phone number on the line, and the extension
                               it rings (repeatable)
          --outgoing primary|backup|no
          --wireguard TUNNEL   connect through a WireGuard tunnel (added with
                               linx trunk wireguard add); --host is then the
                               provider's IPv4 address inside the tunnel
          --transport udp|tcp|tls
                               inside a tunnel (default udp: the tunnel
                               already encrypts)
          --yes                don't ask; fail if something's missing
list    Show every line: whether it works, its encryption, and its place
        for outgoing calls.
test    Test a saved line's connection again.
remove  Delete a line and its phone numbers.
wireguard
        Tunnels for providers that offer their lines over WireGuard. add
        reads the provider's WireGuard file (wg-quick format) from input;
        only the lines' own addresses go through the tunnel. list shows
        each tunnel's state and Linx's public key for it.
`

// trunkAdmin is the database access the trunk command needs besides the
// trunk service.
type trunkAdmin interface {
	DefaultTenant(ctx context.Context) (uuid.UUID, error)
	ExtensionByNumber(ctx context.Context, tenant uuid.UUID, number string) (pbx.Extension, error)
}

// trunkCmd is one run of `trunk ...`.
type trunkCmd struct {
	st     trunkAdmin
	svc    *trunk.Service
	render func(ctx context.Context) error
	in     *bufio.Reader
	out    io.Writer
	errw   io.Writer
	// interactive: questions can be asked (stdin is a terminal).
	interactive bool
	// readSecret reads a password without echoing it.
	readSecret func() (string, error)
}

// runTrunkCommand runs `trunk ...` from the container's own configuration.
// `linx trunk` on the host runs it through docker exec -i (-t when there's
// a terminal), like `linx user`.
func runTrunkCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, trunkUsage)
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	pool, err := db.Connect(ctx, db.ConfigFromEnv(os.Getenv))
	if err != nil {
		fmt.Fprintf(stderr, "Can't reach the Linx database: %v\n", err)
		return 1
	}
	defer pool.Close()
	key, err := dbsecret.LoadKey(dbsecret.KeyPathFromEnv(os.Getenv))
	if err != nil {
		fmt.Fprintf(stderr, "Can't read Linx's database encryption key: %v\n", err)
		return 1
	}
	own, err := safehttp.OwnNetworks()
	if err != nil {
		fmt.Fprintf(stderr, "Can't read this container's networks: %v\n", err)
		return 1
	}
	st := store.New(pool)
	sealer := dbsecret.NewSealer(key)
	svc := &trunk.Service{Store: st, Sealer: sealer, Now: time.Now, Prober: &trunkprobe.Prober{Refuse: trunkprobe.RefuseOwn(own)}}
	// Render for Asterisk at once, rather than waiting for the running
	// control plane's next minute.
	renderer := &trunkconf.Renderer{Store: st, Sealer: sealer, Log: slog.New(slog.DiscardHandler),
		Dir:          envOr(os.Getenv, "LINX_TRUNKS_DIR", "/var/lib/linx/trunks"),
		WireGuardDir: envOr(os.Getenv, "LINX_WIREGUARD_DIR", "/var/lib/linx/wireguard")}
	c := &trunkCmd{st: st, svc: svc, in: bufio.NewReader(stdin), out: stdout, errw: stderr,
		render: func(ctx context.Context) error { _, err := renderer.RenderOnce(ctx); return err }}
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		c.interactive = true
		c.readSecret = func() (string, error) {
			b, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(stdout)
			return string(b), err
		}
	}
	return c.run(ctx, args)
}

func (c *trunkCmd) run(ctx context.Context, args []string) int {
	tenant, err := c.st.DefaultTenant(ctx)
	if err != nil {
		fmt.Fprintf(c.errw, "Can't read the Linx database: %v\n", err)
		return 1
	}
	ctx = auth.WithPrincipal(ctx, auth.SystemPrincipal(tenant))
	switch args[0] {
	case "add":
		return c.add(ctx, tenant, args[1:])
	case "list":
		return c.list(ctx)
	case "test":
		return c.test(ctx, args[1:])
	case "remove":
		return c.remove(ctx, args[1:])
	case "wireguard":
		return c.wireguard(ctx, args[1:])
	}
	fmt.Fprintf(c.errw, "Unknown trunk command %q.\n\n%s", args[0], trunkUsage)
	return 2
}

// explain turns a service error into plain words.
func explain(err error) string {
	var e *apihttp.Error
	if errors.As(err, &e) {
		return e.Detail
	}
	return err.Error()
}

func (c *trunkCmd) all(ctx context.Context) ([]trunk.Trunk, error) {
	var out []trunk.Trunk
	var before *uuid.UUID
	for {
		page, err := c.svc.ListTrunks(ctx, before, 100)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < 100 {
			return out, nil
		}
		before = &page[len(page)-1].ID
	}
}

// find is the trunk named name (case-insensitively), or with that id.
func (c *trunkCmd) find(ctx context.Context, name string) (trunk.Trunk, bool, error) {
	trunks, err := c.all(ctx)
	if err != nil {
		return trunk.Trunk{}, false, err
	}
	for _, t := range trunks {
		if strings.EqualFold(t.Name, name) || t.ID.String() == name {
			return t, true, nil
		}
	}
	return trunk.Trunk{}, false, nil
}

var kindWords = map[string]string{
	trunk.KindRegistration:    "Linx signs in",
	trunk.KindIPAuthenticated: "provider calls in",
	trunk.KindLANPeer:         "phone system on the LAN",
}

func encryptionWords(t trunk.Trunk) string {
	switch {
	case t.WireGuardProfileID != nil:
		return "WireGuard tunnel"
	case t.Transport == trunk.TransportTLS && t.MediaEncryption == trunk.MediaSRTP:
		return "encrypted"
	case t.Transport == trunk.TransportTLS:
		return "UNENCRYPTED audio"
	default:
		return "UNENCRYPTED"
	}
}

func (c *trunkCmd) list(ctx context.Context) int {
	trunks, err := c.all(ctx)
	if err != nil {
		fmt.Fprintf(c.errw, "Can't read the Linx database: %v\n", err)
		return 1
	}
	if len(trunks) == 0 {
		fmt.Fprintln(c.out, "No phone lines yet. Add one with: sudo linx trunk add")
		return 0
	}
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tADDRESS\tSTATUS\tSECURITY\tOUTGOING")
	unencrypted := 0
	for _, t := range trunks {
		outgoing := "no"
		if t.OutboundPriority != nil {
			outgoing = ordinal(*t.OutboundPriority)
		}
		if t.Unencrypted() {
			unencrypted++
		}
		fmt.Fprintf(w, "%s\t%s\t%s:%d/%s\t%s\t%s\t%s\n", t.Name, kindWords[t.Kind], t.Host, t.Port, t.Transport,
			t.Status, encryptionWords(t), outgoing)
	}
	w.Flush()
	for _, t := range trunks {
		if trunkstatus.Down(t.Status) || t.Status == trunkstatus.StatusUnknown {
			fmt.Fprintf(c.out, "\n%s: %s", t.Name, t.StatusDetail)
			if t.StatusSince != nil {
				fmt.Fprintf(c.out, " (since %s)", t.StatusSince.Local().Format("2 Jan 15:04"))
			}
		}
	}
	if unencrypted > 0 {
		fmt.Fprintf(c.out, "\n\n%d line(s) without encryption: calls on them can be listened to on the way.", unencrypted)
	}
	fmt.Fprintln(c.out)
	return 0
}

func ordinal(n int) string {
	switch n {
	case 1:
		return "1st"
	case 2:
		return "2nd (backup)"
	case 3:
		return "3rd"
	}
	return strconv.Itoa(n) + "th"
}

func (c *trunkCmd) test(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(c.errw, "Name the line to test: linx trunk test \"UCM landlines\" (see linx trunk list)")
		return 2
	}
	t, ok, err := c.find(ctx, args[0])
	if err != nil {
		fmt.Fprintf(c.errw, "Can't read the Linx database: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintf(c.errw, "There is no line %q (see linx trunk list).\n", args[0])
		return 1
	}
	fmt.Fprintf(c.out, "Testing %q (%s:%d)...\n", t.Name, t.Host, t.Port)
	res, err := c.svc.TestTrunk(ctx, t.ID)
	if err != nil {
		fmt.Fprintf(c.errw, "Couldn't test it: %s\n", explain(err))
		return 1
	}
	c.printResult(res)
	if t.Status != "" {
		fmt.Fprintf(c.out, "\nAsterisk last reported it as %s: %s\n", t.Status, t.StatusDetail)
	}
	if !res.OK {
		return 1
	}
	return 0
}

var stepNames = map[string]string{
	"address": "Address", "connection": "Connection", "certificate": "Certificate", "sip": "Phone signalling",
	"login": "Login", "audio_encryption": "Audio encryption", "tls_offered": "Encryption offered",
}

var resultMarks = map[string]string{trunkprobe.OK: "ok  ", trunkprobe.Warning: "note", trunkprobe.Failed: "FAIL", trunkprobe.Skipped: "  - "}

func (c *trunkCmd) printResult(res trunkprobe.Result) {
	for _, s := range res.Steps {
		fmt.Fprintf(c.out, "  [%s] %s: %s\n", resultMarks[s.Result], stepNames[s.Name], s.Words)
	}
}

func (c *trunkCmd) remove(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("trunk remove", flag.ContinueOnError)
	fs.SetOutput(c.errw)
	yes := fs.Bool("yes", false, "don't ask to confirm")
	var name []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		name = append(name, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(name) != 1 {
		fmt.Fprintln(c.errw, "Name the line to remove: linx trunk remove \"UCM landlines\"")
		return 2
	}
	t, ok, err := c.find(ctx, name[0])
	if err != nil {
		fmt.Fprintf(c.errw, "Can't read the Linx database: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintf(c.errw, "There is no line %q (see linx trunk list).\n", name[0])
		return 1
	}
	if !*yes {
		if !c.interactive {
			fmt.Fprintln(c.errw, "Add --yes to remove it without a terminal to confirm on.")
			return 2
		}
		if !c.confirm(fmt.Sprintf("Remove %q and every phone number on it? Calls to those numbers stop reaching Linx.", t.Name)) {
			fmt.Fprintln(c.out, "Nothing removed.")
			return 1
		}
	}
	if err := c.svc.DeleteTrunk(ctx, t.ID); err != nil {
		fmt.Fprintf(c.errw, "Couldn't remove it: %s\n", explain(err))
		return 1
	}
	c.applied(ctx)
	fmt.Fprintf(c.out, "Removed %q.\n", t.Name)
	return 0
}

// applied renders the trunks for Asterisk now.
func (c *trunkCmd) applied(ctx context.Context) {
	if c.render == nil {
		return
	}
	if err := c.render(ctx); err != nil {
		fmt.Fprintf(c.errw, "Saved, but couldn't pass it to the phone engine yet (%v); the control plane does within a minute.\n", err)
	}
}

// --- Questions ------------------------------------------------------------

var errNoAnswer = errors.New("no answer")

// ask asks a question (with a default shown in brackets) and returns the
// answer, trimmed; without a terminal it returns def, or errNoAnswer if
// there's none.
func (c *trunkCmd) ask(question, def string) (string, error) {
	if !c.interactive {
		if def == "" {
			return "", fmt.Errorf("%w: %s", errNoAnswer, question)
		}
		return def, nil
	}
	if def != "" {
		fmt.Fprintf(c.out, "%s [%s]: ", question, def)
	} else {
		fmt.Fprintf(c.out, "%s: ", question)
	}
	line, err := c.in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	if line = strings.TrimSpace(line); line == "" {
		return def, nil
	}
	return line, nil
}

// confirm asks a yes/no question; only "y" or "yes" is yes.
func (c *trunkCmd) confirm(question string) bool {
	if !c.interactive {
		return false
	}
	a, err := c.ask(question+" (yes/no)", "no")
	return err == nil && (strings.EqualFold(a, "y") || strings.EqualFold(a, "yes"))
}

// --- add ------------------------------------------------------------------

type didFlag []string

func (d *didFlag) String() string     { return strings.Join(*d, ",") }
func (d *didFlag) Set(v string) error { *d = append(*d, v); return nil }

func (c *trunkCmd) add(ctx context.Context, tenant uuid.UUID, args []string) int {
	fs := flag.NewFlagSet("trunk add", flag.ContinueOnError)
	fs.SetOutput(c.errw)
	tmplName := fs.String("template", "", "")
	name := fs.String("name", "", "")
	host := fs.String("host", "", "")
	port := fs.Int("port", 0, "")
	username := fs.String("username", "", "")
	passwordStdin := fs.Bool("password-stdin", false, "")
	pinFile := fs.String("pin", "", "")
	// --pin-pem: the certificate itself, base64; linx reads --pin FILE on
	// the server (this runs in a container that can't see its files).
	pinPEM := fs.String("pin-pem", "", "")
	unencrypted := fs.Bool("unencrypted", false, "")
	outgoing := fs.String("outgoing", "", "")
	tunnel := fs.String("wireguard", "", "")
	transport := fs.String("transport", "", "")
	yes := fs.Bool("yes", false, "")
	var dids didFlag
	fs.Var(&dids, "did", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(c.errw, "Unexpected %q. Options start with --; see linx trunk help.\n", fs.Arg(0))
		return 2
	}
	if *yes {
		c.interactive = false
	}
	fail := func(err error) int {
		if errors.Is(err, errNoAnswer) {
			fmt.Fprintf(c.errw, "Missing an answer (%v). Give it as an option, or run without --yes in a terminal.\n",
				strings.TrimPrefix(err.Error(), errNoAnswer.Error()+": "))
			return 2
		}
		fmt.Fprintf(c.errw, "%v\n", err)
		return 1
	}

	// Template.
	tmpl, err := c.chooseTemplate(*tmplName)
	if err != nil {
		return fail(err)
	}
	if tmpl.Notes != "" && c.interactive {
		fmt.Fprintf(c.out, "\n%s\n\n", tmpl.Notes)
	}
	in := trunk.TrunkInput{Kind: tmpl.Kind, Template: tmpl.Name, Transport: tmpl.Transport, MediaEncryption: tmpl.MediaEncryption,
		CertTrust: defaultOr(tmpl.CertTrust, trunk.CertPublic), DialFormat: tmpl.DialFormat, Codecs: tmpl.Codecs}
	if in.Name, err = c.ask("Name for this line", defaultOr(*name, tmpl.Label)); err != nil {
		return fail(err)
	}
	// Connection: the internet, or a WireGuard tunnel (ADR-024).
	if *tunnel == "" && c.interactive {
		if profiles, err := c.profiles(ctx); err == nil && len(profiles) > 0 {
			var names []string
			for _, p := range profiles {
				names = append(names, fmt.Sprintf("%q", p.Name))
			}
			a, err := c.ask("Connect through the internet, or a WireGuard tunnel ("+strings.Join(names, ", ")+")", "internet")
			if err != nil {
				return fail(err)
			}
			if !strings.EqualFold(a, "internet") {
				*tunnel = strings.Trim(a, `"`)
			}
		}
	}
	if *tunnel != "" {
		prof, ok, err := c.findProfile(ctx, *tunnel)
		if err != nil {
			return fail(err)
		}
		if !ok {
			return fail(fmt.Errorf("there is no WireGuard tunnel %q (see linx trunk wireguard list)", *tunnel))
		}
		in.WireGuardProfileID = &prof.ID
		// The tunnel encrypts: plain SIP inside it by default, never with
		// the ADR-023 warning.
		tr, err := c.ask("Inside the tunnel, connect with udp, tcp or tls", defaultOr(*transport, trunk.TransportUDP))
		if err != nil {
			return fail(err)
		}
		switch tr = strings.ToLower(tr); tr {
		case trunk.TransportUDP, trunk.TransportTCP:
			in.Transport, in.MediaEncryption, in.CertTrust = tr, trunk.MediaNone, trunk.CertPublic
		case trunk.TransportTLS:
			in.Transport, in.MediaEncryption = tr, trunk.MediaSRTP
		default:
			return fail(fmt.Errorf("%q isn't udp, tcp or tls", tr))
		}
	} else if *transport != "" {
		return fail(fmt.Errorf("--transport is only for a line through a WireGuard tunnel; without one, Linx uses TLS (see --unencrypted)"))
	}
	question := "Its address (name or IP address)"
	if in.WireGuardProfileID != nil {
		question = "Its IPv4 address inside the tunnel"
	}
	if in.Host, err = c.ask(question, *host); err != nil {
		return fail(err)
	}
	p := *port
	if p == 0 && in.WireGuardProfileID != nil && in.Transport != trunk.TransportTLS {
		p = 5060
	}
	if p == 0 {
		p = tmpl.Port
	}
	if p == 0 {
		p = 5061
	}
	if a, err := c.ask("Port", strconv.Itoa(p)); err != nil {
		return fail(err)
	} else if p, err = strconv.Atoi(a); err != nil {
		return fail(fmt.Errorf("%q isn't a port number", a))
	}
	in.Port = &p
	if in.Kind == trunk.KindRegistration {
		if in.Username, err = c.ask("Login (username)", *username); err != nil {
			return fail(err)
		}
		switch {
		case *passwordStdin:
			line, _ := c.in.ReadString('\n')
			in.Password = strings.TrimRight(line, "\r\n")
		case c.interactive && c.readSecret != nil:
			fmt.Fprint(c.out, "Password (not shown): ")
			if in.Password, err = c.readSecret(); err != nil {
				return fail(err)
			}
		default:
			return fail(fmt.Errorf("%w: the password (use --password-stdin)", errNoAnswer))
		}
	}
	if *pinFile != "" {
		b, err := os.ReadFile(*pinFile)
		if err != nil {
			return fail(fmt.Errorf("can't read %s: %w", *pinFile, err))
		}
		in.CertTrust, in.PinnedCertificate = trunk.CertPinned, string(b)
	}
	if *pinPEM != "" {
		b, err := base64.StdEncoding.DecodeString(*pinPEM)
		if err != nil {
			return fail(errors.New("the certificate to pin didn't arrive intact"))
		}
		in.CertTrust, in.PinnedCertificate = trunk.CertPinned, string(b)
	}

	// Test, and fix what can be fixed here: pin a certificate the admin
	// recognises, or (confirmed) go without encryption.
	res, ok := c.probe(ctx, in)
	if !ok {
		return 1
	}
	if !res.OK && res.Untrusted && in.PinnedCertificate == "" {
		top := res.Certificates[len(res.Certificates)-1]
		fmt.Fprintf(c.out, "\nThe certificate it presented:\n  For:         %s\n  Issued by:   %s\n  Valid until: %s\n  Fingerprint: SHA-256 %s\n",
			strings.Join(top.Names, ", "), top.Issuer, top.NotAfter.Format("2 Jan 2006"), top.SHA256)
		fmt.Fprintln(c.out, "Compare the fingerprint with the one the phone system or provider shows for its certificate.")
		if c.confirm("Does it match, and should Linx trust this certificate for this line?") {
			in.CertTrust, in.PinnedCertificate = trunk.CertPinned, top.PEM
			if res, ok = c.probe(ctx, in); !ok {
				return 1
			}
		}
	}
	if !res.OK && in.Transport == trunk.TransportTLS && (failed(res, "connection") || failed(res, "certificate")) &&
		(*unencrypted || c.interactive) {
		fmt.Fprintln(c.out, "\nIt can't be reached with encryption (TLS).")
		if *unencrypted || c.confirmUnencrypted() {
			in.Transport, in.MediaEncryption, in.CertTrust, in.PinnedCertificate = trunk.TransportTCP, trunk.MediaNone, trunk.CertPublic, ""
			if p == 5061 {
				p = 5060
			}
			in.ConfirmUnencrypted = true
			if res, ok = c.probe(ctx, in); !ok {
				return 1
			}
		}
	}
	if (in.Transport != trunk.TransportTLS || in.MediaEncryption != trunk.MediaSRTP) && !in.ConfirmUnencrypted && in.WireGuardProfileID == nil {
		if !*unencrypted && !c.confirmUnencrypted() {
			fmt.Fprintln(c.out, "Nothing saved.")
			return 1
		}
		in.ConfirmUnencrypted = true
	}
	if !res.OK && !c.confirm("\nThe test failed. Save the line anyway, to fix it later?") {
		fmt.Fprintln(c.out, "Nothing saved. Fix what the test found and run linx trunk add again.")
		return 1
	}

	// Save.
	t, err := c.svc.CreateTrunk(ctx, in)
	if err != nil {
		fmt.Fprintf(c.errw, "Couldn't save it: %s\n", explain(err))
		return 1
	}
	fmt.Fprintf(c.out, "\nSaved %q.\n", t.Name)
	code := 0
	if err := c.addDIDs(ctx, tenant, t, dids); err != nil {
		fmt.Fprintf(c.errw, "%v\n", err)
		code = 1
	}
	if err := c.setOutgoing(ctx, t, *outgoing); err != nil {
		fmt.Fprintf(c.errw, "%v\n", err)
		code = 1
	}
	c.applied(ctx)
	fmt.Fprintln(c.out, "The phone engine picks it up within a few seconds; check with: sudo linx trunk list")
	return code
}

func defaultOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func failed(res trunkprobe.Result, step string) bool {
	for _, s := range res.Steps {
		if s.Name == step {
			return s.Result == trunkprobe.Failed
		}
	}
	return false
}

func (c *trunkCmd) confirmUnencrypted() bool {
	fmt.Fprintln(c.out, "Without encryption, calls to and from this line can be listened to on the way (ADR-023).")
	return c.confirm("Use it without encryption anyway?")
}

func (c *trunkCmd) chooseTemplate(name string) (trunk.Template, error) {
	if name == "" && c.interactive {
		fmt.Fprintln(c.out, "What are you connecting?")
		for i, t := range trunk.Templates {
			fmt.Fprintf(c.out, "  %d. %s\n", i+1, t.Label)
		}
		def := strconv.Itoa(len(trunk.Templates))
		a, err := c.ask("Choose a number", def)
		if err != nil {
			return trunk.Template{}, err
		}
		n, err := strconv.Atoi(a)
		if err != nil || n < 1 || n > len(trunk.Templates) {
			return trunk.Template{}, fmt.Errorf("choose a number from 1 to %d", len(trunk.Templates))
		}
		return trunk.Templates[n-1], nil
	}
	if t, ok := trunk.TemplateByName(defaultOr(name, "generic")); ok {
		return t, nil
	}
	var names []string
	for _, t := range trunk.Templates {
		names = append(names, t.Name)
	}
	return trunk.Template{}, fmt.Errorf("there's no template %q; choose one of %s", name, strings.Join(names, ", "))
}

// probe tests the line as in describes it and prints the result.
func (c *trunkCmd) probe(ctx context.Context, in trunk.TrunkInput) (trunkprobe.Result, bool) {
	t := trunk.Trunk{Kind: in.Kind, Host: in.Host, Port: *in.Port, Transport: in.Transport, MediaEncryption: in.MediaEncryption,
		CertTrust: in.CertTrust, PinnedCertificate: in.PinnedCertificate, Username: in.Username, WireGuardProfileID: in.WireGuardProfileID}
	fmt.Fprintf(c.out, "\nTesting the connection to %s:%d...\n", t.Host, t.Port)
	target := trunk.ProbeTarget(t, in.Password)
	if t.WireGuardProfileID != nil {
		w, err := c.svc.GetWireGuardProfile(ctx, *t.WireGuardProfileID)
		if err != nil {
			fmt.Fprintf(c.errw, "Can't read the WireGuard tunnel: %s\n", explain(err))
			return trunkprobe.Result{}, false
		}
		target.TunnelName, target.TunnelState, target.TunnelDetail = w.Name, w.Status, w.StatusDetail
	}
	res := c.svc.Prober.Run(ctx, target)
	c.printResult(res)
	return res, true
}

func (c *trunkCmd) addDIDs(ctx context.Context, tenant uuid.UUID, t trunk.Trunk, given []string) error {
	if len(given) == 0 && c.interactive {
		fmt.Fprintln(c.out, "\nPhone numbers on this line: calls to them ring the extension you choose.")
		for {
			n, err := c.ask("A phone number on this line (blank when done)", "")
			if err != nil || n == "" {
				break
			}
			ext, _ := c.ask("Which extension should it ring? (blank: none yet)", "")
			if ext != "" {
				n += "=" + ext
			}
			given = append(given, n)
		}
	}
	var errs []error
	for _, g := range given {
		number, ext, _ := strings.Cut(g, "=")
		in := trunk.DIDInput{Number: strings.ReplaceAll(number, " ", "")}
		if ext != "" {
			e, err := c.st.ExtensionByNumber(ctx, tenant, ext)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s not added: there's no extension %s", number, ext))
				continue
			}
			in.ExtensionID = &e.ID
		}
		if _, err := c.svc.CreateDID(ctx, t.ID, in); err != nil {
			errs = append(errs, fmt.Errorf("%s not added: %s", number, explain(err)))
			continue
		}
		if ext != "" {
			fmt.Fprintf(c.out, "Calls to %s ring extension %s.\n", number, ext)
		} else {
			fmt.Fprintf(c.out, "Added %s (it rings nobody yet).\n", number)
		}
	}
	return errors.Join(errs...)
}

func (c *trunkCmd) setOutgoing(ctx context.Context, t trunk.Trunk, choice string) error {
	trunks, err := c.all(ctx)
	if err != nil {
		return err
	}
	var order []uuid.UUID
	for p := 1; ; p++ {
		found := false
		for _, x := range trunks {
			if x.OutboundPriority != nil && *x.OutboundPriority == p {
				order, found = append(order, x.ID), true
			}
		}
		if !found {
			break
		}
	}
	if choice == "" {
		def := "primary"
		if len(order) > 0 {
			def = "backup"
		}
		if !c.interactive {
			choice = def
		} else if choice, err = c.ask("\nUse it for outgoing calls? (primary, backup or no)", def); err != nil {
			return err
		}
	}
	switch strings.ToLower(choice) {
	case "no", "n":
		fmt.Fprintln(c.out, "Not used for outgoing calls.")
		return nil
	case "primary":
		order = append([]uuid.UUID{t.ID}, order...)
	case "backup":
		order = append(order, t.ID)
	default:
		return fmt.Errorf("%q: choose primary, backup or no", choice)
	}
	if _, err := c.svc.SetOutboundOrder(ctx, order); err != nil {
		return fmt.Errorf("couldn't set it for outgoing calls: %s", explain(err))
	}
	fmt.Fprintf(c.out, "Outgoing calls use it as the %s line.\n", strings.ToLower(choice))
	return nil
}
