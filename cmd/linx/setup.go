package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"linxpbx.com/linx/internal/hostinfo"
	"linxpbx.com/linx/internal/installer"
	"linxpbx.com/linx/internal/version"
)

// setupEnv is everything setup touches on the host, so tests can fake it.
type setupEnv struct {
	detect      func() hostinfo.Info
	runner      installer.Runner
	isRoot      bool
	lan         func() installer.LAN
	savedConfig func() ([]byte, error) // reads installer.ConfigPath
	readFile    func(string) ([]byte, error)
	commit      string // build commit; picks the service image tag
	// executable is this linx binary's path, which setup installs as
	// /usr/local/bin/linx ("" if unknown).
	executable string
	resolve    func(string) (string, error)
	// stat is os.Stat: whether linx-firewall-sync sits next to executable.
	stat func(string) (os.FileInfo, error)
	web  webEnv
}

func realSetupEnv() setupEnv {
	return setupEnv{
		detect:      hostinfo.Detect,
		runner:      installer.ExecRunner{},
		isRoot:      os.Geteuid() == 0,
		lan:         installer.DetectLAN,
		savedConfig: func() ([]byte, error) { return os.ReadFile(installer.ConfigPath) },
		readFile:    os.ReadFile,
		commit:      version.Commit,
		executable:  executablePath(),
		resolve:     filepath.EvalSymlinks,
		stat:        os.Stat,
		web:         realWebEnv(),
	}
}

func executablePath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}

const setupUsage = `Usage: sudo linx setup [--new-link] [--replace-docker] [--dry-run]
       sudo linx setup --config FILE [--dry-run] [--owner-email EMAIL --owner-name NAME]

Checks this server, installs Docker and Linx, and prints one link: open it in
a browser to finish setting up (docs/INSTALL.md). The link works once, for one
hour. Setup carries on if you close the terminal; run it again to see where
it's up to.

  --new-link          cancel the link (or the browser using it) and print a new one
  --replace-docker    replace this server's own Docker package with Docker's
                      official one even though containers are running (they restart)
  --dry-run           show what setup would do without changing anything

With --config, setup asks nothing and uses no browser: it applies FILE (see
` + installer.ConfigPath + `) and saves it there. Put the DNS provider token in
` + installer.DNSTokenPath + ` first if setup hasn't saved one yet.

  --owner-email EMAIL the first admin's sign-in email
  --owner-name NAME   the first admin's name
`

func runSetup(ctx context.Context, args []string, stdout, stderr io.Writer, env setupEnv) int {
	fl := flag.NewFlagSet("setup", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	configFile := fl.String("config", "", "")
	dryRun := fl.Bool("dry-run", false, "")
	ownerEmail := fl.String("owner-email", "", "")
	ownerName := fl.String("owner-name", "", "")
	newLink := fl.Bool("new-link", false, "")
	replaceDocker := fl.Bool("replace-docker", false, "")
	if err := fl.Parse(args); err != nil || fl.NArg() > 0 {
		fmt.Fprint(stderr, setupUsage)
		return 2
	}
	if *configFile == "" {
		if *ownerEmail != "" || *ownerName != "" {
			fmt.Fprintln(stderr, "--owner-email and --owner-name go with --config. Without it, you give your name and email in the browser.")
			return 2
		}
		return runWebSetup(ctx, webOptions{dryRun: *dryRun, newLink: *newLink, replaceDocker: *replaceDocker}, stdout, stderr, env)
	}
	if *newLink {
		fmt.Fprintln(stderr, "--new-link is for the browser install, not --config.")
		return 2
	}

	cfg, err := loadSetupConfig(*configFile, env)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !*dryRun && !env.isRoot {
		fmt.Fprintln(stderr, "linx setup changes system settings and must run as root. Try: sudo linx setup")
		return 1
	}

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
	if needsLAN := installer.NeedsProxyAddress(cfg.FrontDoor.Kind) || cfg.FrontDoor.Kind == installer.FrontDoorHomeOnly; needsLAN && !lan.OK() {
		fmt.Fprintln(stderr, "front_door.kind "+cfg.FrontDoor.Kind+" needs this server on a home network, and it isn't on one.\n"+
			"Choose linx-443 (Linx takes port 443 itself) instead, or run setup on the home server.")
		return 1
	}
	pp, err := installer.PhonesPlan(ctx, env.runner, lan, installer.FrontDoorFor(cfg, lan), env.readFile)
	if err != nil {
		fmt.Fprintln(stderr, "Can't set up the phone connections:", err)
		return 1
	}
	plan = append(plan, pp...)
	plan = append(plan, installer.WireGuardPlan()...)

	// 5. Optional container management UI.
	var portainer installer.PortainerSetup
	if cfg.ContainerUI == installer.ContainerUIPortainer {
		portainer = installer.PortainerPlan(lan.BindAddress())
		plan = append(plan, portainer.Plan...)
	}

	// 6. Domain and DNS provider token.
	token, err := configDomain(cfg, env)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
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
	plan = append(plan, installer.FirewallSyncPlan(env.executable, env.resolve, env.stat)...)
	plan = append(plan, installer.ResticPlan(ctx, env.runner)...)
	plan = append(plan, installer.BackupAgentPlan(env.executable, env.resolve, env.stat)...)
	plan = append(plan, installer.OpsAgentPlan(env.executable, env.resolve, env.stat)...)
	plan = append(plan, installer.Step{Title: "Save your answers to " + installer.ConfigPath, File: &installer.File{
		Path: installer.ConfigPath, Data: cfg.Marshal(), Mode: 0o600, DirMode: 0o755,
	}})

	// 8b. The first admin account (docs/ADMIN.md §4): created once the
	// stack is up, in the Summary step below. Its email and name aren't
	// infrastructure config, so they're never saved to setup.yaml, unlike
	// everything above; --config mode takes them as flags, and skips
	// creating one if they're not given (a person can always be added
	// later: sudo linx user create).
	email, name := *ownerEmail, *ownerName

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
	printFirstAdmin(ctx, stdout, stderr, env, email, name)
	return 0
}

// printFirstAdmin creates the first system_admin account, once the stack is
// up, the same way `sudo linx user create` does (docker exec into the
// control plane), and relays its one-time set-password link (docs/ADMIN.md
// §4). email == "": nobody was given, so nothing is created.
func printFirstAdmin(ctx context.Context, stdout, stderr io.Writer, env setupEnv, email, name string) {
	if email == "" {
		fmt.Fprintln(stdout, "\nNo first admin account created (no email given).\n"+
			`Create one: sudo linx user create --email "you@example.com" --name "Your Name" --role system_admin`)
		return
	}
	if name == "" {
		name = email
	}
	out, err := env.runner.Run(ctx, nil, "docker", "exec", controlPlaneContainer, controlPlaneBinary,
		"user", "create", "--email", email, "--name", name, "--role", "system_admin")
	if err != nil {
		fmt.Fprintf(stderr, "\nCouldn't create the first admin account: %v\n%s\n", err, out)
		fmt.Fprintln(stderr, `Create one yourself: sudo linx user create --email "you@example.com" --name "Your Name" --role system_admin`)
		return
	}
	fmt.Fprintln(stdout)
	stdout.Write(out)
}

// configDomain checks --config mode's domain and returns the saved DNS
// token: never in setup.yaml, always in installer.DNSTokenPath.
func configDomain(cfg installer.Config, env setupEnv) (string, error) {
	if cfg.Domain.Name == "" {
		return "", errors.New("setup.yaml: domain.name: required, e.g. pbx.example.com")
	}
	saved := ""
	if b, err := env.readFile(installer.DNSTokenPath); err == nil {
		saved = strings.TrimSpace(string(b))
	}
	if saved == "" {
		return "", fmt.Errorf("no DNS provider token saved yet. Put it in %s (one line, root only) and run setup again", installer.DNSTokenPath)
	}
	return saved, nil
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

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
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
