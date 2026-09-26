package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/term"

	"linxpbx.com/linx/internal/hostinfo"
	"linxpbx.com/linx/internal/installer"
	"linxpbx.com/linx/internal/version"
)

// setupEnv is everything setup touches on the host, so tests can fake it.
type setupEnv struct {
	detect      func() hostinfo.Info
	runner      installer.Runner
	stdin       io.Reader
	interactive bool // stdin is a terminal
	isRoot      bool
	lan         func() installer.LAN
	savedConfig func() ([]byte, error) // reads installer.ConfigPath
	readFile    func(string) ([]byte, error)
	// readSecret reads a line from the terminal without echoing it.
	readSecret func() (string, error)
	commit     string // build commit; picks the service image tag
	// executable is this linx binary's path, which setup installs as
	// /usr/local/bin/linx ("" if unknown).
	executable string
	resolve    func(string) (string, error)
}

func realSetupEnv() setupEnv {
	fi, err := os.Stdin.Stat()
	return setupEnv{
		detect:      hostinfo.Detect,
		runner:      installer.ExecRunner{},
		stdin:       os.Stdin,
		interactive: err == nil && fi.Mode()&os.ModeCharDevice != 0,
		isRoot:      os.Geteuid() == 0,
		lan:         installer.DetectLAN,
		savedConfig: func() ([]byte, error) { return os.ReadFile(installer.ConfigPath) },
		readFile:    os.ReadFile,
		readSecret: func() (string, error) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			return string(b), err
		},
		commit:     version.Commit,
		executable: executablePath(),
		resolve:    filepath.EvalSymlinks,
	}
}

func executablePath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}

const setupUsage = `Usage: sudo linx setup [--config FILE] [--dry-run]

Checks this server, installs Docker if needed, sets up your domain and its
certificate, starts Linx, and saves your answers to ` + installer.ConfigPath + `.
The DNS provider token is kept in ` + installer.DNSTokenPath + `; with
--config, put it there first if setup hasn't saved one yet.

  --config FILE  use answers from FILE instead of asking questions
  --dry-run      show what setup would do without changing anything
`

func runSetup(ctx context.Context, args []string, stdout, stderr io.Writer, env setupEnv) int {
	fl := flag.NewFlagSet("setup", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	configFile := fl.String("config", "", "")
	dryRun := fl.Bool("dry-run", false, "")
	if err := fl.Parse(args); err != nil || fl.NArg() > 0 {
		fmt.Fprint(stderr, setupUsage)
		return 2
	}

	cfg, err := loadSetupConfig(*configFile, env)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ask := *configFile == ""
	if !*dryRun && !env.isRoot {
		fmt.Fprintln(stderr, "linx setup changes system settings and must run as root. Try: sudo linx setup")
		return 1
	}
	if ask && !env.interactive {
		fmt.Fprintln(stderr, "linx setup needs answers. Run it in a terminal, or pass --config setup.yaml.")
		return 1
	}
	p := &prompter{in: bufio.NewReader(env.stdin), out: stdout}

	// 1. Host checks.
	fmt.Fprintln(stdout, "Checking this server…")
	host := env.detect()
	findings := installer.CheckHost(host)
	for _, f := range findings {
		fmt.Fprintf(stdout, "  %-8s %s\n", f.Level, f.Message)
	}
	if installer.HasFailure(findings) {
		fmt.Fprintln(stderr, "\nSetup can't continue until the problems above are fixed.")
		return 1
	}

	// 2. Resource profile.
	suggested, reason := installer.SuggestProfile(host)
	fmt.Fprintf(stdout, "\nSuggested resource profile: %s (%s).\n", suggested, reason)
	profile := suggested
	if cfg.ResourceProfile != installer.ProfileAuto {
		profile = cfg.ResourceProfile
	}
	if ask {
		labels := map[string]string{}
		for _, pr := range installer.Profiles {
			labels[pr] = installer.ProfileDescription[pr]
		}
		if profile, err = p.choose("Which resource profile should Linx use?", installer.Profiles, labels, profile); err != nil {
			return inputError(stderr, err)
		}
	}
	cfg.ResourceProfile = profile
	if profile == suggested {
		cfg.ResourceProfile = installer.ProfileAuto
	}

	// 3. Docker.
	var plan installer.Plan
	fmt.Fprintln(stdout, "\nChecking Docker…")
	docker := installer.DetectDocker(ctx, env.runner)
	action, msg := docker.Assess()
	fmt.Fprintln(stdout, "  "+msg)
	switch action {
	case installer.DockerBlocked:
		return 1
	case installer.DockerInstall, installer.DockerUpgrade:
		if ask {
			q := "Install Docker from Docker's official repository now?"
			if docker.Source == installer.SourceDistro {
				q = "Replace it with Docker's official version? Your containers, images and volumes are kept; running containers restart."
			}
			ok, err := p.confirm(q, true)
			if err != nil {
				return inputError(stderr, err)
			}
			cfg.Docker.Install = ok
		}
		if !cfg.Docker.Install {
			fmt.Fprintln(stderr, "Linx needs Docker. Run setup again when you're ready to install it (in setup.yaml: docker.install: true).")
			return 1
		}
		dp, err := installer.DockerPlan(ctx, env.runner, host)
		if err != nil {
			fmt.Fprintln(stderr, "Can't install Docker:", err)
			return 1
		}
		plan = append(plan, dp...)
	}

	// 4. Phones: from the local network only (docs/PBX.md §6).
	lan := env.lan()
	if lan.OK() {
		fmt.Fprintf(stdout, "\nPhones will be able to connect from your local network (%s), where this server is %s.\n", lan.Network, lan.Address)
	} else {
		fmt.Fprintln(stdout, "\nThis server isn't on a local network (its address is public), so phones can't connect yet.\n"+
			"Connecting from anywhere arrives in a later Linx update, with protection against password guessing.")
	}
	// 4b. Calls from outside: what sits in front of Linx (docs/WEB.md §3).
	if err := askFrontDoor(p, &cfg, ask, lan); err != nil {
		if errors.Is(err, errSetupRefused) {
			return 1
		}
		return inputError(stderr, err)
	}
	pp, err := installer.PhonesPlan(ctx, env.runner, lan, installer.FrontDoorFor(cfg, lan), env.readFile)
	if err != nil {
		fmt.Fprintln(stderr, "Can't set up the phone connections:", err)
		return 1
	}
	plan = append(plan, pp...)
	plan = append(plan, installer.WireGuardPlan()...)

	// 5. Optional container management UI.
	if ask {
		fmt.Fprint(stdout, "\nA container management screen lets you see and control Docker containers from a web browser.\n"+
			"It has full control of this server, so Linx makes it reachable from your local network only.\n")
		labels := map[string]string{
			installer.ContainerUINone:      "don't install one (recommended if you're unsure)",
			installer.ContainerUIPortainer: "Portainer CE, with a generated admin password",
		}
		if cfg.ContainerUI, err = p.choose("Install a container management screen?", installer.ContainerUIs, labels, cfg.ContainerUI); err != nil {
			return inputError(stderr, err)
		}
	}
	var portainer installer.PortainerSetup
	if cfg.ContainerUI == installer.ContainerUIPortainer {
		portainer = installer.PortainerPlan(lan.BindAddress())
		plan = append(plan, portainer.Plan...)
	}

	// 6. Domain, DNS provider token and certificate settings.
	token, err := askDomain(p, &cfg, ask, env)
	if err != nil {
		return inputError(stderr, err)
	}
	imageTag, err := installer.ImageTag(env.commit)
	if err != nil && !*dryRun {
		fmt.Fprintln(stderr, "\nCan't set up the Linx services:", err)
		return 1
	}

	// 7. Internal certificate authority (ADR-011). If Docker is being
	// installed now there can't be an existing CA to detect.
	pki := installer.PKIPlan(action != installer.DockerInstall && installer.CAExists(ctx, env.runner))
	plan = append(plan, pki.Plan...)

	// 8. The Linx services: first certificate, then start everything; the
	// front door's files first and its DNS names once they're up.
	fdFiles, fdDNS := installer.FrontDoorPlan(cfg, lan)
	plan = append(plan, fdFiles...)
	stack := installer.StackPlan(cfg, token, imageTag, lan)
	plan = append(plan, stack.Plan...)
	plan = append(plan, fdDNS...)

	plan = append(plan, installer.CLIPlan(env.executable, env.resolve)...)
	plan = append(plan, installer.Step{Title: "Save your answers to " + installer.ConfigPath, File: &installer.File{
		Path: installer.ConfigPath, Data: cfg.Marshal(), Mode: 0o600, DirMode: 0o755,
	}})

	// 9. Confirm and apply.
	fmt.Fprintln(stdout, "\nSetup will:")
	for i, s := range plan {
		fmt.Fprintf(stdout, "  %2d. %s\n", i+1, s.Title)
		if *dryRun && s.Cmd != nil {
			fmt.Fprintf(stdout, "        $ %s\n", s.Cmd)
		}
	}
	if *dryRun {
		fmt.Fprintln(stdout, "\nDry run: nothing was changed.")
		return 0
	}
	if ask {
		ok, err := p.confirm("Go ahead?", true)
		if err != nil {
			return inputError(stderr, err)
		}
		if !ok {
			fmt.Fprintln(stdout, "Nothing was changed.")
			return 1
		}
	}
	fmt.Fprintln(stdout)
	err = plan.Execute(ctx, env.runner, func(s installer.Step) { fmt.Fprintf(stdout, "  → %s\n", s.Title) })
	if err != nil {
		var se *installer.StepError
		fmt.Fprintf(stderr, "\nSetup stopped: %v\n", err)
		if errors.As(err, &se) && se.Output != "" {
			fmt.Fprintf(stderr, "Last output:\n%s\n", indent(se.Output))
		}
		fmt.Fprintln(stderr, "Fix the problem above and run setup again; steps that already finished are safe to repeat.")
		return 1
	}

	// 10. Summary.
	fmt.Fprintf(stdout, "\nLinx is running. Resource profile: %s.\n", profile)
	printCertificate(stdout, cfg, stack)
	if cfg.ContainerUI == installer.ContainerUIPortainer {
		printPortainer(stdout, portainer)
	}
	printPhones(stdout, cfg, lan)
	printFrontDoor(stdout, cfg, lan)
	if pki.Passphrase != "" {
		printCABackup(stdout, pki.Passphrase)
	}
	return 0
}

// askDomain asks for the domain, DNS provider, token and certificate settings
// (or checks them in --config mode) and returns the DNS token. The token is
// never stored in setup.yaml; a saved one is reused.
func askDomain(p *prompter, cfg *installer.Config, ask bool, env setupEnv) (string, error) {
	saved := ""
	if b, err := env.readFile(installer.DNSTokenPath); err == nil {
		saved = strings.TrimSpace(string(b))
	}
	if !ask {
		if cfg.Domain.Name == "" {
			return "", errors.New("setup.yaml: domain.name: required, e.g. pbx.example.com")
		}
		if saved == "" {
			return "", fmt.Errorf("no DNS provider token saved yet. Put it in %s (one line, root only) and run setup again", installer.DNSTokenPath)
		}
		return saved, nil
	}

	fmt.Fprint(p.out, "\nLinx needs a domain name. It creates addresses under it, like admin.<domain> and meet.<domain>,\n"+
		"and gets a certificate for them so browsers and apps connect securely.\n")
	oldDomain, oldProvider := cfg.Domain.Name, cfg.Domain.DNSProvider
	for {
		d, err := p.text("Domain (e.g. pbx.example.com or yourname.duckdns.org)", cfg.Domain.Name)
		if err != nil {
			return "", err
		}
		d = strings.ToLower(d)
		provider := cfg.Domain.DNSProvider
		if strings.HasSuffix(d, ".duckdns.org") {
			provider = installer.DNSDuckDNS
		} else if provider == installer.DNSDuckDNS {
			provider = installer.DNSCloudflare
		}
		if err := installer.ValidateDomain(d, provider); err != nil {
			fmt.Fprintln(p.out, "  "+err.Error())
			continue
		}
		cfg.Domain.Name, cfg.Domain.DNSProvider = d, provider
		break
	}
	if cfg.Domain.DNSProvider == installer.DNSCloudflare {
		fmt.Fprintln(p.out, "Linx proves you own the domain by adding a temporary DNS record, so your DNS must be managed at Cloudflare.")
	}

	// A changed domain can keep the token too (e.g. sip.lab.example.com
	// corrected to lab.example.com: same Cloudflare zone, same token), so
	// it's offered whenever the provider is the same. It defaults to yes
	// only when the domain is unchanged or ends in the same last two labels;
	// a token for another Cloudflare zone would fail at the certificate.
	if saved != "" && (oldDomain == "" || cfg.Domain.DNSProvider == oldProvider) {
		q, def := "Keep the saved DNS token?", true
		if oldDomain != "" && cfg.Domain.Name != oldDomain {
			q = "Keep the saved DNS token (saved for " + oldDomain + ")?"
			def = baseDomain(cfg.Domain.Name) == baseDomain(oldDomain)
		}
		keep, err := p.confirm(q, def)
		if err != nil {
			return "", err
		}
		if keep {
			return saved, p.askCertificates(cfg)
		}
	}
	fmt.Fprintln(p.out, dnsTokenHelp[cfg.Domain.DNSProvider])
	for {
		fmt.Fprint(p.out, "Paste the token (it won't be shown): ")
		t, err := env.readSecret()
		fmt.Fprintln(p.out)
		if err != nil {
			return "", err
		}
		if err := installer.ValidateDNSToken(t); err != nil {
			fmt.Fprintln(p.out, "  "+err.Error())
			continue
		}
		return strings.TrimSpace(t), p.askCertificates(cfg)
	}
}

// baseDomain is a domain's last two labels ("lab.linxpbx.com" →
// "linxpbx.com"): a guess at its DNS zone, only used to pick a default.
func baseDomain(d string) string {
	labels := strings.Split(d, ".")
	if len(labels) <= 2 {
		return d
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

var dnsTokenHelp = map[string]string{
	installer.DNSCloudflare: "Create a Cloudflare API token: dash.cloudflare.com → My Profile → API Tokens → Create Token →\n" +
		"\"Edit zone DNS\" template. Permissions: Zone · DNS · Edit and Zone · Zone · Read.\n" +
		"Zone Resources: Include · Specific zone · your domain. Only this token is needed.",
	installer.DNSDuckDNS: "Your DuckDNS token is shown at the top of duckdns.org after you sign in.",
}

// askCertificates asks whether to use test certificates and for a contact email.
func (p *prompter) askCertificates(cfg *installer.Config) error {
	var err error
	fmt.Fprint(p.out, "\nTest certificates come from Let's Encrypt's test service. Browsers warn about them, but they\n"+
		"let you check everything works without hitting Let's Encrypt's limits. Switch to trusted ones later.\n")
	if cfg.Certificates.Staging, err = p.confirm("Use test certificates for now?", cfg.Certificates.Staging); err != nil {
		return err
	}
	q := "Email for certificate expiry notices (optional while testing, Enter to skip)"
	if !cfg.Certificates.Staging {
		q = "Email for certificate expiry notices (required for trusted certificates)"
	}
	for {
		e, err := p.text(q, cfg.Certificates.Email)
		if err != nil {
			return err
		}
		if err := installer.ValidateEmail(e, cfg.Certificates.Staging); err != nil {
			fmt.Fprintln(p.out, "  "+err.Error())
			continue
		}
		cfg.Certificates.Email = e
		return nil
	}
}

func printCertificate(w io.Writer, cfg installer.Config, s installer.StackSetup) {
	if cfg.Certificates.Staging {
		fmt.Fprintf(w, "\nTest certificate issued for %s. Browsers will warn about it; that's expected.\n", s.Names)
		fmt.Fprintln(w, "When everything works, set certificates.staging: false and an email in "+installer.ConfigPath)
		fmt.Fprintln(w, "and run: sudo linx setup --config "+installer.ConfigPath)
		return
	}
	fmt.Fprintf(w, "\nTrusted certificate issued for %s. It renews automatically.\n", s.Names)
}

func printPhones(w io.Writer, cfg installer.Config, lan installer.LAN) {
	if !lan.OK() {
		return
	}
	fmt.Fprintln(w, "\nPhones:")
	fmt.Fprintf(w, "  Phones sign in to sip.%s (port 5061, TLS) from your local network (%s) only.\n", cfg.Domain.Name, lan.Network)
	fmt.Fprintf(w, "  So they can find this server, add a DNS record at your DNS provider: sip.%s, type A, value %s\n",
		cfg.Domain.Name, lan.Address)
	fmt.Fprintln(w, "  (at Cloudflare: \"DNS only\", not proxied). If this server's address changes, run setup again.")
	fmt.Fprintln(w, "  linx doctor checks all of this.")
}

func printCABackup(w io.Writer, passphrase string) {
	fmt.Fprintln(w, "\nInternal certificate authority created. Linx uses it to secure connections")
	fmt.Fprintln(w, "between its own parts and to your apps. Its master key (the \"root key\") is")
	fmt.Fprintln(w, "not kept on this server. Its only copy is locked with this backup passphrase:")
	fmt.Fprintf(w, "\n    %s\n\n", passphrase)
	fmt.Fprintln(w, "  1. Write the passphrase down and store it somewhere safe. It is shown only now")
	fmt.Fprintln(w, "     and is not saved anywhere.")
	fmt.Fprintf(w, "  2. Copy the folder %s to a USB stick or your password manager, e.g.\n", installer.CABackupDir)
	fmt.Fprintf(w, "     from your computer:  scp -r root@<this server>:%s .\n", installer.CABackupDir)
	fmt.Fprintf(w, "  3. Then delete it from this server:  sudo rm -r %s\n", installer.CABackupDir)
	fmt.Fprintln(w, "You only need the backup and passphrase if the certificate authority has to be")
	fmt.Fprintln(w, "renewed (in about 10 years) or rebuilt. See docs/ops/INTERNAL_CA.md.")
}

func printPortainer(w io.Writer, s installer.PortainerSetup) {
	fmt.Fprintln(w, "\nPortainer (container management):")
	if s.LocalOnly {
		fmt.Fprintln(w, "  This server has no local-network address, so Portainer only listens on the server itself.")
		fmt.Fprintln(w, "  From your computer run:  ssh -L 9443:127.0.0.1:9443 <you>@<this server>")
		fmt.Fprintln(w, "  then open:               https://localhost:9443")
	} else {
		fmt.Fprintf(w, "  Address:  %s (from your local network only)\n", s.URL)
	}
	fmt.Fprintln(w, "  Username: admin")
	fmt.Fprintf(w, "  Password: %s\n", s.Password)
	fmt.Fprintln(w, "  The password is also saved in /etc/linx/secrets/portainer_admin_password (root only).")
	fmt.Fprintln(w, "  Your browser will warn about Portainer's own certificate the first time; that's expected on your local network.")
	fmt.Fprintln(w, "  Portainer can control everything on this server. Never forward its port on your router.")
}

func loadSetupConfig(path string, env setupEnv) (installer.Config, error) {
	var (
		b   []byte
		err error
	)
	if path != "" {
		if b, err = env.readFile(path); err != nil {
			return installer.Config{}, fmt.Errorf("reading %s: %w", path, err)
		}
	} else if b, err = env.savedConfig(); err != nil {
		// No saved answers yet (or not readable without sudo): start fresh.
		return installer.DefaultConfig(), nil
	}
	return installer.ParseConfig(strings.NewReader(string(b)))
}

func inputError(w io.Writer, err error) int {
	fmt.Fprintln(w, "\nSetup cancelled:", err)
	return 1
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

// prompter asks questions on the terminal.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func (p *prompter) line() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && (s == "" || !errors.Is(err, io.EOF)) {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

// text asks for a line of text. Enter keeps def.
func (p *prompter) text(q, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.out, "%s [Enter = %s]: ", q, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", q)
	}
	s, err := p.line()
	if s == "" {
		s = def
	}
	return s, err
}

func (p *prompter) confirm(q string, def bool) (bool, error) {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	for {
		fmt.Fprintf(p.out, "%s %s ", q, hint)
		s, err := p.line()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(p.out, "Please answer y or n.")
	}
}

// choose shows numbered options and returns the chosen one. Enter picks def.
func (p *prompter) choose(q string, options []string, labels map[string]string, def string) (string, error) {
	fmt.Fprintln(p.out, q)
	for i, o := range options {
		mark := " "
		if o == def {
			mark = "*"
		}
		fmt.Fprintf(p.out, "  %s %d) %-12s %s\n", mark, i+1, o, labels[o])
	}
	for {
		fmt.Fprintf(p.out, "Choose 1-%d [Enter = %s]: ", len(options), def)
		s, err := p.line()
		if err != nil {
			return "", err
		}
		if s == "" {
			return def, nil
		}
		if slices.Contains(options, strings.ToLower(s)) {
			return strings.ToLower(s), nil
		}
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n >= 1 && n <= len(options) {
			return options[n-1], nil
		}
		fmt.Fprintln(p.out, "Please enter one of the numbers shown.")
	}
}

// errSetupRefused means setup already explained why it stops.
var errSetupRefused = errors.New("setup refused")

// askFrontDoor asks what sits in front of Linx on the internet.
func askFrontDoor(p *prompter, cfg *installer.Config, ask bool, lan installer.LAN) error {
	if ask {
		fmt.Fprint(p.out, "\nPeople reach Linx in a browser at https://meet.<domain>. From outside your home that goes through port 443.\n")
		k, err := p.choose("What sits in front of Linx on the internet?", installer.FrontDoors, installer.FrontDoorDescription, cfg.FrontDoor.Kind)
		if err != nil {
			return err
		}
		if k != cfg.FrontDoor.Kind || !installer.NeedsProxyAddress(k) {
			cfg.FrontDoor.ProxyAddress = ""
			cfg.FrontDoor.TURNUDPPort = 0
		}
		cfg.FrontDoor.Kind = k
		if installer.NeedsProxyAddress(k) && lan.OK() {
			what := map[string]string{installer.FrontDoorPangolin: "Pangolin", installer.FrontDoorNginx: "nginx or HAProxy",
				installer.FrontDoorHTTPProxy: "the proxy"}[k]
			def := cfg.FrontDoor.ProxyAddress
			for {
				a, err := p.text("The address of the machine "+what+" runs on, on your home network ("+lan.Address.String()+" if it's this one)", def)
				if err != nil {
					return err
				}
				if err := installer.ValidateProxyAddress(a); err != nil {
					fmt.Fprintln(p.out, "  "+err.Error())
					continue
				}
				cfg.FrontDoor.ProxyAddress = strings.TrimSpace(a)
				break
			}
			fmt.Fprint(p.out, "\nCall audio from outside comes in on a UDP port your router forwards straight to Linx. Use 443 if\n"+
				"your router lets UDP 443 go to Linx while TCP 443 goes to "+what+"; some (UniFi) don't, so use 3478 then.\n")
			for {
				a, err := p.text("UDP port for call audio", strconv.Itoa(cfg.FrontDoor.UDPPort()))
				if err != nil {
					return err
				}
				n, err := strconv.Atoi(strings.TrimSpace(a))
				if err == nil {
					err = installer.ValidateTURNUDPPort(n)
				}
				if err != nil {
					fmt.Fprintln(p.out, "  Give a port number, like 443 or 3478.")
					continue
				}
				cfg.FrontDoor.TURNUDPPort = n
				if n == installer.PublicPort {
					cfg.FrontDoor.TURNUDPPort = 0
				}
				break
			}
		}
	}
	needsLAN := installer.NeedsProxyAddress(cfg.FrontDoor.Kind) || cfg.FrontDoor.Kind == installer.FrontDoorHomeOnly
	if needsLAN && !lan.OK() {
		fmt.Fprintln(p.out, "That choice needs this server on a home network, and it isn't on one.\n"+
			"Choose linx-443 (Linx takes port 443 itself) instead, or run setup on the home server.")
		return errSetupRefused
	}
	return nil
}

// printFrontDoor tells the owner what to do outside this server.
func printFrontDoor(w io.Writer, cfg installer.Config, lan installer.LAN) {
	fd := cfg.FrontDoor
	switch fd.Kind {
	case installer.FrontDoorPangolin, installer.FrontDoorNginx, installer.FrontDoorHTTPProxy:
		name := map[string]string{installer.FrontDoorPangolin: "Pangolin", installer.FrontDoorNginx: "nginx or HAProxy",
			installer.FrontDoorHTTPProxy: "proxy"}[fd.Kind]
		fmt.Fprintf(w, "\nCalls from outside go through your %s (%s). A few things to do there and on your router;\n"+
			"the steps are in %s. Then check with: sudo linx doctor\n", name, fd.ProxyAddress, installer.StepsFile(fd.Kind))
	case installer.FrontDoorLinx443:
		where := "this server"
		if lan.OK() {
			where = "this server (" + lan.Address.String() + ")"
		}
		fmt.Fprintf(w, "\nLinx now answers on port 443 itself. If a router is in front, forward TCP and UDP port 443 to %s.\n"+
			"Then check with: sudo linx doctor\n", where)
	case installer.FrontDoorHomeOnly:
		fmt.Fprintf(w, "\nLinx answers at https://meet.%s on this home network only (%s). Calls from outside aren't set up.\n",
			cfg.Domain.Name, lan.Address)
	default:
		fmt.Fprintln(w, "\nLinx has no web address yet, and calls from outside aren't set up. Run setup again and pick a front door when you want them.")
	}
}
