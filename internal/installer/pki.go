package installer

import (
	"context"
	"crypto/rand"
	_ "embed"
	"strings"
)

// StepCAImage is Smallstep step-ca, pinned by digest (multi-arch index). It
// must match the step-ca image in deploy/compose/compose.yaml.
const StepCAImage = "smallstep/step-ca:0.30.2@sha256:a2b17872915c193259b75a5474c398326f41bd199f0842093e52cf4182bc8270"

const (
	// SecretsDir holds the Docker secrets for the Linx stack.
	SecretsDir = "/etc/linx/secrets"
	// CABackupDir receives the encrypted root key. The owner copies it off the
	// server and deletes it.
	CABackupDir = "/etc/linx/ca-backup"
	// StepCAVolume holds the CA's config, certificates, intermediate key and
	// database. compose.yaml refers to it as an external volume.
	StepCAVolume = "linx-step-ca"
	// stepGID is the "step" group in the step-ca image. CA secrets are
	// root-owned and readable by this group only.
	stepGID = 1000
)

// CA secrets, by Docker secret name.
const (
	secretStepCAPassword   = "linx_step_ca_password"     // intermediate key
	secretServicesPassword = "linx_ca_services_password" // linx-services provisioner
	secretDevicesPassword  = "linx_ca_devices_password"  // linx-devices provisioner
)

// CAServicesPasswordPath is the linx-services provisioner's password: the
// control plane uses it to get its ARI certificate (docs/PBX.md §4).
const CAServicesPasswordPath = SecretsDir + "/" + secretServicesPassword

var caSecrets = []string{secretStepCAPassword, secretServicesPassword, secretDevicesPassword}

//go:embed ca-init.sh
var caInitScript string

// PKISetup is the result of planning the internal CA bootstrap.
type PKISetup struct {
	Plan Plan
	// Passphrase encrypts the root key backup. It is shown to the owner once
	// and never saved on the server. Empty when the CA already exists.
	Passphrase string
}

// CAExists reports whether the internal CA has already been created. Any
// error (no Docker yet, no volume) counts as "not created"; the bootstrap
// script itself refuses to replace an existing CA.
func CAExists(ctx context.Context, r Runner) bool {
	if _, err := r.Run(ctx, nil, "docker", "volume", "inspect", StepCAVolume); err != nil {
		return false
	}
	_, err := r.Run(ctx, nil, "docker", "run", "--rm", "--network", "none",
		"--volume", StepCAVolume+":/home/step:ro", "--entrypoint", "test",
		StepCAImage, "-e", "/home/step/config/ca.json")
	return err == nil
}

// PKIPlan creates the step-ca secrets and, unless the CA already exists,
// bootstraps it: an offline root (only an encrypted backup leaves the
// bootstrap container), an online intermediate, and the linx-services (24 h)
// and linx-devices (7 d) provisioners. Existing passwords are reused so
// re-running setup never breaks a running CA.
func PKIPlan(exists bool) PKISetup {
	var s PKISetup
	for _, name := range caSecrets {
		path := SecretsDir + "/" + name
		st := fileStep("Save the "+secretTitle[name]+" (root and the CA only)", path, []byte(existingOrNewPassword(path)), 0o440, 0o700)
		st.File.Gid = stepGID
		s.Plan = append(s.Plan, st)
	}
	if exists {
		s.Plan = append(s.Plan, caCertsReadableStep())
		return s
	}

	s.Passphrase = backupPassphrase()
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--tmpfs", "/tmp",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--env", "LINX_ROOT_BACKUP_PASSPHRASE",
		"--volume", StepCAVolume + ":/home/step",
		"--volume", CABackupDir + ":/backup",
	}
	for _, name := range caSecrets {
		args = append(args, "--volume", SecretsDir+"/"+name+":/run/secrets/"+name+":ro")
	}
	args = append(args, "--entrypoint", "bash", StepCAImage, "-c", caInitScript)

	// The container's step user writes the backup, then root takes it back.
	s.Plan = append(s.Plan,
		cmdStep("Prepare the folder for the root key backup", "install", "-d", "-m", "0700", "-o", "1000", "-g", "1000", CABackupDir),
		Step{Title: "Create the internal certificate authority", Cmd: &Cmd{
			Env: []string{"LINX_ROOT_BACKUP_PASSPHRASE=" + s.Passphrase}, Name: "docker", Args: args,
		}},
		cmdStep("Make the root key backup readable by root only", "chown", "-R", "root:root", CABackupDir),
	)
	return s
}

// caCertsReadableStep lets Linx services read the CA's public certificates
// (the root they verify internal TLS against). CAs made before Phase 1B had
// them owner-only; ca-init.sh does this itself for new ones, so it's a no-op
// there.
func caCertsReadableStep() Step {
	return cmdStep("Let the Linx services read the internal certificate authority's public certificates",
		"docker", "run", "--rm", "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--volume", StepCAVolume+":/home/step", "--entrypoint", "sh", StepCAImage,
		"-c", "chmod 0755 /home/step/certs && chmod 0644 /home/step/certs/*.crt")
}

var secretTitle = map[string]string{
	secretStepCAPassword:   "internal certificate authority password",
	secretServicesPassword: "service certificate password",
	secretDevicesPassword:  "device certificate password",
}

// backupPassphrase returns 24 random base32 characters (120 bits) in groups of
// four, so it's easy to write down: ABCD-EFGH-IJKL-MNOP-QRST-UVWX.
func backupPassphrase() string {
	t := rand.Text()[:24]
	var b strings.Builder
	for i := 0; i < len(t); i += 4 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(t[i : i+4])
	}
	return b.String()
}
