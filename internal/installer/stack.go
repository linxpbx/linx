package installer

import (
	"crypto/rand"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/deploy/compose"
	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/dnsapi"
	"linxpbx.com/linx/internal/turn"
)

const (
	// StackDir holds the Linx Compose stack. compose.yaml refers to secrets as
	// ./secrets/..., which is SecretsDir.
	StackDir  = "/etc/linx"
	stackFile = StackDir + "/compose.yaml"
	stackEnv  = StackDir + "/.env"
	// DNSTokenPath is the DNS provider API token (Docker secret linx_dns_token).
	DNSTokenPath = SecretsDir + "/linx_dns_token"
	// DBPasswordPath is the Postgres password (Docker secret linx_db_password).
	DBPasswordPath = SecretsDir + "/linx_db_password"
	// DBEncryptionKeyPath is the AES-256-GCM key (ADR-030) that encrypts
	// secrets stored in the database (Docker secret linx_db_encryption_key).
	DBEncryptionKeyPath = SecretsDir + "/linx_db_encryption_key"
	// dbEncryptionKeySize is the AES-256 key length in bytes.
	dbEncryptionKeySize = 32
	// JWTSigningKeyPath is the Ed25519 seed that signs API access tokens
	// (ADR-027; Docker secret linx_jwt_signing_key).
	JWTSigningKeyPath = SecretsDir + "/linx_jwt_signing_key"
	// jwtSigningKeySize is the Ed25519 seed length in bytes.
	jwtSigningKeySize = 32
	// AsteriskDBPasswordPath is the linx_asterisk Postgres role's password
	// (docs/PBX.md §3; Docker secret linx_asterisk_db_password), read by
	// both the control plane (to set the role's password) and Asterisk (to
	// connect as it).
	AsteriskDBPasswordPath = SecretsDir + "/linx_asterisk_db_password"
	// ARIPasswordPath is the password Asterisk presents when it connects
	// out to the control plane's ARI websocket (ADR-034; Docker secret
	// linx_ari_password), read by both.
	ARIPasswordPath = SecretsDir + "/linx_ari_password"
	// TURNSecretPath is the secret the control plane signs browsers' relay
	// credentials with and coturn checks them with (ADR-039; Docker secret
	// linx_turn_secret).
	TURNSecretPath = SecretsDir + "/linx_turn_secret"
	// nonrootGID is the distroless "nonroot" group that Linx service images
	// run as. The DNS token is root-owned and readable by this group only.
	nonrootGID = 65532
)

// StackSetup is the result of planning the Linx stack.
type StackSetup struct {
	Plan Plan
	// Names describes the certificate's DNS names, for the summary.
	Names string
}

// ImageTag returns the Linx image tag this build of the program belongs
// with (ADR-084): a release's own number when it was built from a tag
// ("1.2.0", "1.3.0-beta.1"), else the commit CI published it under
// ("sha-<full commit>"). Either way a server pins one exact version in its
// .env and never resolves a moving tag at start.
//
// An error means the program can't say what it is, which would leave setup
// guessing which images go with it.
func ImageTag(version, commit string) (string, error) {
	if v, ok := releaseVersion(version); ok {
		return v, nil
	}
	if !commitRE.MatchString(commit) {
		return "", fmt.Errorf("this linx build doesn't say which version it is (version %q, commit %q), so setup can't pick matching service images; build it with make build from a checkout of master, or from a release tag", version, commit)
	}
	return "sha-" + commit, nil
}

// IsRelease reports whether this build of the program is a tagged release
// (ADR-084): a real version like v1.2.0, not a dev or commit build. The
// installer uses it to keep development-only choices — above all the test
// (staging) certificate — out of a real install.
func IsRelease(version string) bool {
	_, ok := releaseVersion(version)
	return ok
}

// releaseVersion reads a release out of what the Makefile stamped in. A
// build from a tag is exactly "v1.2.0" or "v1.3.0-beta.1"; anything with a
// commit count or "-dirty" after it is a build from somewhere past the tag
// and is not that release (git describe: "v1.2.0-4-gabc1234").
func releaseVersion(version string) (string, bool) {
	v, ok := strings.CutPrefix(strings.TrimSpace(version), "v")
	if !ok || !releaseRE.MatchString(v) {
		return "", false
	}
	return v, true
}

var (
	commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// Plain semver, with an optional pre-release of the shape releases
	// use (beta.1, rc.2). No build metadata: it has no place in an image
	// tag and nothing produces it.
	releaseRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(alpha|beta|rc)\.(0|[1-9][0-9]*))?$`)
)

// RunningImageTag returns the image tag the installed stack was last set up
// with (LINX_VERSION in its .env), or "" if there's none to read. read is
// os.ReadFile.
func RunningImageTag(read func(string) ([]byte, error)) string {
	b, err := read(stackEnv)
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "LINX_VERSION="); ok {
			return v
		}
	}
	return ""
}

// StackPlan saves the DNS token, installs compose.yaml and its .env, gets the
// first public certificate and starts the stack. It must run after PKIPlan,
// because step-ca's volume and password must exist before "up", and after
// PhonesPlan, so the phone ports are published the way it set Docker up.
func StackPlan(c Config, dnsToken, imageTag string, lan LAN) StackSetup {
	dc := []string{"compose", "--file", stackFile}
	tokenStep := fileStep("Save the DNS token (readable by root and the certificate service only)",
		DNSTokenPath, []byte(strings.TrimSpace(dnsToken)), 0o440, 0o700)
	tokenStep.File.Gid = nonrootGID
	dbPasswordStep := fileStep("Save the database password (readable by root and the Linx services only)",
		DBPasswordPath, []byte(existingOrNewPassword(DBPasswordPath)), 0o440, 0o700)
	dbPasswordStep.File.Gid = nonrootGID
	dbKeyStep := fileStep("Save the database encryption key (readable by root and the Linx services only)",
		DBEncryptionKeyPath, existingOrNewKeyBytes(DBEncryptionKeyPath, dbEncryptionKeySize), 0o440, 0o700)
	dbKeyStep.File.Gid = nonrootGID
	jwtKeyStep := fileStep("Save the API token signing key (readable by root and the Linx services only)",
		JWTSigningKeyPath, existingOrNewKeyBytes(JWTSigningKeyPath, jwtSigningKeySize), 0o440, 0o700)
	jwtKeyStep.File.Gid = nonrootGID
	asteriskDBPasswordStep := fileStep("Save the phone system's database password (readable by root and the Linx services only)",
		AsteriskDBPasswordPath, []byte(existingOrNewPassword(AsteriskDBPasswordPath)), 0o440, 0o700)
	asteriskDBPasswordStep.File.Gid = nonrootGID
	ariPasswordStep := fileStep("Save the phone system's control connection password (readable by root and the Linx services only)",
		ARIPasswordPath, []byte(existingOrNewPassword(ARIPasswordPath)), 0o440, 0o700)
	ariPasswordStep.File.Gid = nonrootGID
	turnSecretStep := fileStep("Save the call relay's secret (readable by root and the Linx services only)",
		TURNSecretPath, []byte(existingOrNewTURNSecret(TURNSecretPath)), 0o440, 0o700)
	turnSecretStep.File.Gid = nonrootGID
	kind := "trusted certificate"
	if c.Certificates.Staging {
		kind = "test certificate"
	}
	return StackSetup{
		Names: certNames(c),
		Plan: Plan{
			tokenStep,
			dbPasswordStep,
			dbKeyStep,
			jwtKeyStep,
			asteriskDBPasswordStep,
			ariPasswordStep,
			turnSecretStep,
			fileStep("Write the Linx services configuration", stackFile, compose.File, 0o644, 0o755),
			fileStep("Remember this server's id (for \"Moved to a new place?\" after a restore)", ServerIDPath, []byte(ServerID()+"\n"), 0o644, 0o755),
			fileStep("Write the Linx settings for "+c.Domain.Name, stackEnv, stackDotEnv(c, imageTag, lan), 0o644, 0o755),
			cmdStep("Download the Linx service images", "docker", append(dc, "pull", "--quiet")...),
			cmdStep("Get a "+kind+" for "+certNames(c)+" (can take a few minutes)",
				"docker", append(dc, "run", "--rm", "certd", "-once")...),
			cmdStep("Start the Linx services", "docker", append(dc, "up", "--detach", "--wait")...),
		},
	}
}

// Split is the plan without its last step, and that step: starting every
// service. The web install starts the control plane on its own first, to
// make the first admin while the rest starts (docs/INSTALL.md §14).
func (s StackSetup) Split() (Plan, Step) {
	return s.Plan[:len(s.Plan)-1], s.Plan[len(s.Plan)-1]
}

// LinxImageLabel is the label every image Linx builds carries.
const LinxImageLabel = "org.opencontainers.image.source=https://github.com/linxpbx/linx"

// PruneOldImagesStep removes Linx's own images that no container uses: the
// versions an update left behind (about 450 MB each time otherwise). Only
// images carrying LinxImageLabel, and only unused ones, so the running
// version, the database's and step-ca's images, and anything else on the
// server are never touched.
func PruneOldImagesStep() Step {
	return cmdStep("Remove the previous Linx versions' images", "docker", "image", "prune", "--all", "--force", "--filter", "label="+LinxImageLabel)
}

// StartServicesStep starts only services (and what they need).
func StartServicesStep(title string, services ...string) Step {
	return cmdStep(title, "docker", append([]string{"compose", "--file", stackFile, "up", "--detach", "--wait"}, services...)...)
}

// stackDotEnv renders the non-secret settings compose.yaml reads. Every value
// is validated before it gets here (Config.Validate, ImageTag), so none needs
// quoting.
func stackDotEnv(c Config, imageTag string, lan LAN) []byte {
	fd := FrontDoorFor(c, lan)
	return fmt.Appendf(nil, `# Generated by linx setup from %s. Changes are overwritten when setup runs again.
LINX_VERSION=%s
LINX_DOMAIN=%s
LINX_DNS_PROVIDER=%s
LINX_ACME_EMAIL=%s
LINX_ACME_STAGING=%t
LINX_CERT_WILDCARD=%t
# dns-01 (the DNS token) or tls-alpn-01 (no token: renewed through port 443).
LINX_CERT_CHALLENGE=%s
# Phones: where the phone ports are published, and the networks they may
# connect from (detected by setup: the local network, or none).
LINX_SIP_ADDRESS=%s
LINX_SIP_NETWORKS=%s
# Calls from outside (docs/WEB.md §3): the front door is %s. Who may send
# the visitor's address (PROXY protocol), where the web port (8443) and
# coturn's TLS port (5349) are published for it (127.0.0.1: nowhere), and
# where coturn's UDP and Linx's own port 443 router are published.
LINX_TRUSTED_PROXIES=%s
LINX_PROXY_PROTOCOL=%t
LINX_WEB_ADDRESS=%s
LINX_TURN_UDP_ADDRESS=%s
LINX_TURN_UDP_PORT=%d
LINX_TURN_URLS=%s
LINX_SNI_ADDRESS=%s
# The public port browsers use (https://<domain>:<port> unless 443; Linx's
# own port 443 router is published there), and the web port's own port on
# this server (empty: one Docker picks, when the public port is 8443).
LINX_PUBLIC_PORT=%d
LINX_WEB_HOST_PORT=%s
COMPOSE_PROFILES=%s
# The public names linx-certd keeps pointed at this network's public address
# (or at LINX_DNS_ADDRESS, when set), following it when it changes.
LINX_DNS_RECORDS=%s
LINX_DNS_ADDRESS=%s
# Where this server is, for "Moved to a new place?" after a restore
# (docs/INSTALL.md §8): setup's own id for it, kept in %s, and its front
# door.
LINX_SERVER_ID=%s
LINX_FRONT_DOOR=%s
# The time zone schedules use, so "03:00" in the backup schedule means 03:00
# there (the services otherwise run on UTC): setup.yaml's time_zone, or this
# server's own when that's empty.
LINX_TZ=%s
# The database's image: %s (docs/RESOURCES.md §3; an install whose database
# was made on Debian keeps it, a new one gets Alpine).
LINX_POSTGRES_IMAGE=%s
`, ConfigPath, imageTag, c.Domain.Name, c.Domain.DNSProvider, c.Certificates.Email, c.Certificates.Staging, c.Certificates.Wildcard, certChallenge(c),
		lan.BindAddress(), asteriskconf.FormatSIPNetworks(lan.Networks()),
		c.FrontDoor.Kind, fd.TrustedProxies, fd.ProxyProtocol, fd.WebAddress, fd.TURNUDPAddress, fd.TURNUDPPort, fd.TURNURLs,
		fd.SNIAddress, fd.SNIPort, fd.WebHostPort, fd.ComposeProfiles, DNSRecords(c, lan), fd.DNSAddress, ServerIDPath, ServerID(), c.FrontDoor.Kind, c.Zone(),
		c.databaseName(), c.PostgresImage())
}

// certChallenge is LINX_CERT_CHALLENGE.
func certChallenge(c Config) string {
	if c.Certificates.NoDNSToken {
		return "tls-alpn-01"
	}
	return "dns-01"
}

// localtimePath is where the host's time zone is set (timedatectl
// set-timezone points it at the zone's file); a variable for tests.
var localtimePath = "/etc/localtime"

var tzNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+){0,2}$`)

// Zone is the time zone Linx's schedules use: setup.yaml's time_zone, or
// the host's own (HostTimezone).
func (c Config) Zone() string {
	if c.TimeZone != "" {
		return c.TimeZone
	}
	return HostTimezone()
}

// ValidateTimeZone checks an IANA time zone name ("Asia/Dubai").
func ValidateTimeZone(name string) error {
	if name == "UTC" {
		return nil
	}
	if !tzNameRE.MatchString(name) {
		return fmt.Errorf("%q isn't a time zone like Asia/Dubai", name)
	}
	if _, err := time.LoadLocation(name); err != nil {
		return fmt.Errorf("%q isn't a time zone this server knows", name)
	}
	return nil
}

// HostTimezone is the host's time zone name ("Asia/Dubai"), read from where
// /etc/localtime points — not /etc/timezone, which timedatectl can leave
// stale — or "UTC" if it can't be told.
func HostTimezone() string {
	target, err := os.Readlink(localtimePath)
	if err != nil {
		return "UTC"
	}
	_, name, ok := strings.Cut(target, "zoneinfo/")
	if !ok || !tzNameRE.MatchString(name) {
		return "UTC"
	}
	if _, err := time.LoadLocation(name); err != nil {
		return "UTC"
	}
	return name
}

// certNames describes internal/certs.Config.Names for the plan and summary.
func certNames(c Config) string {
	if c.Certificates.Wildcard {
		return c.Domain.Name + " and *." + c.Domain.Name
	}
	return c.Domain.Name + " and admin., provision., sip. and turn." + c.Domain.Name
}

// ValidateDNSKey checks the form of a DNS company's key as the secret
// file holds it (dnsapi.Company.Encode); the company checks the rest. The
// error never includes any of it.
func ValidateDNSKey(provider, secret string) error {
	c, ok := dnsapi.Find(provider)
	if !ok {
		return fmt.Errorf("Linx can't use a key for %q", provider)
	}
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("the %s key is empty", c.Name)
	}
	_, err := c.Decode(secret)
	return err
}

// existingOrNewTURNSecret returns the relay secret already saved at path,
// or a new one: 52 base32 characters (260 bits), which coturn's
// configuration holds as they are.
func existingOrNewTURNSecret(path string) string {
	if b, err := os.ReadFile(path); err == nil && turn.CheckSecret(strings.TrimSpace(string(b))) == nil {
		return strings.TrimSpace(string(b))
	}
	return rand.Text() + rand.Text()
}

// existingOrNewKeyBytes returns n random bytes, or the ones already saved at
// path so re-running setup doesn't rotate a key still encrypting rows in the
// database.
func existingOrNewKeyBytes(path string, n int) []byte {
	if b, err := os.ReadFile(path); err == nil && len(b) == n {
		return b
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand.Read only fails if the OS can't provide randomness
	}
	return b
}

// DNSRecords is LINX_DNS_RECORDS, and what setup's own certd -records
// run points: the public names once there's a front door, and at home
// sip.<domain> pinned to this server's home address for desk phones (owner
// decision 2026-09-27; not at DuckDNS, which gives every name one
// address). "" without a DNS token, or when the owner keeps the records
// (domain.dns_by_hand): certd leaves DNS alone.
func DNSRecords(c Config, lan LAN) string {
	if c.Certificates.NoDNSToken || c.Domain.DNSByHand {
		return ""
	}
	var r []string
	if c.FrontDoor.Kind != FrontDoorNone && c.FrontDoor.Kind != "" {
		r = append(r, PublicHosts...)
	}
	if co, _ := dnsapi.Find(c.Domain.DNSProvider); lan.OK() && !co.OneAddress {
		r = append(r, SIPHost+"="+lan.BindAddress().String())
	}
	return strings.Join(r, ",")
}

// SIPHost is desk phones' name under the domain.
const SIPHost = "sip"

// ServerIDPath keeps this server's id: made once by setup, never in a
// backup, so a restore onto another server can tell (docs/INSTALL.md §8).
const ServerIDPath = StackDir + "/server-id"

var serverID = sync.OnceValue(func() string {
	if b, err := os.ReadFile(ServerIDPath); err == nil {
		if id, err := uuid.Parse(strings.TrimSpace(string(b))); err == nil {
			return id.String()
		}
	}
	return uuid.New().String()
})

// ServerID is this server's id: the kept one, or a new one setup writes.
func ServerID() string { return serverID() }
