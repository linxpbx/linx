package installer

import (
	"crypto/rand"
	"fmt"
	"os"
	"regexp"
	"strings"

	"linxpbx.com/linx/deploy/compose"
	"linxpbx.com/linx/internal/asteriskconf"
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

// ImageTag returns the Linx image tag CI publishes for this build
// ("sha-<full commit>"), or an error if the binary wasn't built from a known
// commit.
func ImageTag(commit string) (string, error) {
	if !commitRE.MatchString(commit) {
		return "", fmt.Errorf("this linx build doesn't say which version it is (commit %q), so setup can't pick matching service images; build it with make build from a checkout of master", commit)
	}
	return "sha-" + commit, nil
}

var commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

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
			fileStep("Write the Linx settings for "+c.Domain.Name, stackEnv, stackDotEnv(c, imageTag, lan), 0o644, 0o755),
			cmdStep("Download the Linx service images", "docker", append(dc, "pull", "--quiet")...),
			cmdStep("Get a "+kind+" for "+certNames(c)+" (can take a few minutes)",
				"docker", append(dc, "run", "--rm", "certd", "-once")...),
			cmdStep("Start the Linx services", "docker", append(dc, "up", "--detach", "--wait")...),
		},
	}
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
# Phones: where the phone ports are published, and the networks they may
# connect from (detected by setup: the local network, or none).
LINX_SIP_ADDRESS=%s
LINX_SIP_NETWORKS=%s
# Calls from outside (docs/WEB.md §3): the front door is %s. Who may send
# the visitor's address (PROXY protocol), where the web port (8443) and
# coturn's TLS port (5349) are published for it (127.0.0.1: nowhere), and
# where coturn's UDP and Linx's own port 443 router are published.
LINX_TRUSTED_PROXIES=%s
LINX_WEB_ADDRESS=%s
LINX_TURN_UDP_ADDRESS=%s
LINX_TURN_UDP_PORT=%d
LINX_SNI_ADDRESS=%s
COMPOSE_PROFILES=%s
`, ConfigPath, imageTag, c.Domain.Name, c.Domain.DNSProvider, c.Certificates.Email, c.Certificates.Staging, c.Certificates.Wildcard,
		lan.BindAddress(), asteriskconf.FormatSIPNetworks(lan.Networks()),
		c.FrontDoor.Kind, fd.TrustedProxies, fd.WebAddress, fd.TURNUDPAddress, fd.TURNUDPPort, fd.SNIAddress, fd.ComposeProfiles)
}

// certNames describes internal/certs.Config.Names for the plan and summary.
func certNames(c Config) string {
	if c.Certificates.Wildcard {
		return "*." + c.Domain.Name
	}
	return "admin., api., meet., provision., sip. and turn." + c.Domain.Name
}

// ValidateDNSToken does a basic sanity check; the provider checks the rest
// when the first certificate is requested.
func ValidateDNSToken(t string) error {
	t = strings.TrimSpace(t)
	switch {
	case t == "":
		return fmt.Errorf("the token is empty")
	case strings.ContainsAny(t, " \t\r\n"):
		return fmt.Errorf("the token can't contain spaces; copy just the token")
	case len(t) < 20 || len(t) > 200:
		return fmt.Errorf("that doesn't look like a DNS provider token (%d characters)", len(t))
	}
	return nil
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
