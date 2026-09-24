package installer

import (
	"context"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestPKIPlanBootstrap(t *testing.T) {
	s := PKIPlan(false)
	if !regexp.MustCompile(`^([A-Z2-7]{4}-){5}[A-Z2-7]{4}$`).MatchString(s.Passphrase) {
		t.Errorf("passphrase %q isn't six groups of four", s.Passphrase)
	}
	var secrets int
	var boot *Cmd
	for _, st := range s.Plan {
		if st.File != nil {
			secrets++
			if !strings.HasPrefix(st.File.Path, SecretsDir+"/") || st.File.Mode != 0o440 || st.File.Gid != stepGID || len(st.File.Data) < 20 {
				t.Errorf("secret %s: mode %v gid %d len %d", st.File.Path, st.File.Mode, st.File.Gid, len(st.File.Data))
			}
		}
		if st.Cmd != nil && st.Cmd.Name == "docker" {
			boot = st.Cmd
		}
	}
	if secrets != 3 || boot == nil {
		t.Fatalf("want 3 secrets and a bootstrap command, got %d, %v", secrets, boot)
	}
	line := boot.String()
	for _, want := range []string{"--network none", "--read-only", "--cap-drop ALL", StepCAImage, "-c <script>", CABackupDir + ":/backup"} {
		if !strings.Contains(line, want) {
			t.Errorf("bootstrap command missing %q: %s", want, line)
		}
	}
	// The passphrase goes through the environment, never the command line.
	if strings.Contains(strings.Join(boot.Args, " "), s.Passphrase) || !slices.Contains(boot.Env, "LINX_ROOT_BACKUP_PASSPHRASE="+s.Passphrase) {
		t.Error("passphrase must be passed only in the environment")
	}
	if last := s.Plan[len(s.Plan)-1]; last.Cmd == nil || last.Cmd.String() != "chown -R root:root "+CABackupDir {
		t.Errorf("last step should return the backup to root: %+v", last)
	}
}

func TestPKIPlanExistingCA(t *testing.T) {
	s := PKIPlan(true)
	if s.Passphrase != "" || len(s.Plan) != 4 {
		t.Errorf("existing CA: passphrase %q, %d steps; want the 3 secrets and the certificate permissions", s.Passphrase, len(s.Plan))
	}
	for _, st := range s.Plan[:3] {
		if st.File == nil {
			t.Errorf("unexpected step %q", st.Title)
		}
	}
	if last := s.Plan[len(s.Plan)-1]; last.Cmd == nil || !strings.Contains(last.Cmd.String(), "chmod 0644 /home/step/certs/*.crt") ||
		!strings.Contains(last.Cmd.String(), StepCAVolume+":/home/step") {
		t.Errorf("last step should make the CA's certificates readable: %+v", last)
	}
}

func TestCAExists(t *testing.T) {
	inspect := "docker volume inspect " + StepCAVolume
	probe := "docker run --rm --network none --volume " + StepCAVolume + ":/home/step:ro --entrypoint test " + StepCAImage + " -e /home/step/config/ca.json"
	for name, tt := range map[string]struct {
		answers map[string]string
		want    bool
	}{
		"no docker":    {map[string]string{}, false},
		"no volume":    {map[string]string{inspect: "!no such volume"}, false},
		"empty volume": {map[string]string{inspect: "[]", probe: "!"}, false},
		"initialised":  {map[string]string{inspect: "[]", probe: ""}, true},
	} {
		if got := CAExists(context.Background(), &fakeRunner{answers: tt.answers}); got != tt.want {
			t.Errorf("%s: CAExists = %v, want %v", name, got, tt.want)
		}
	}
}

// TestComposeStepCAImage keeps compose.yaml and the bootstrap on one step-ca.
func TestComposeStepCAImage(t *testing.T) {
	b, err := os.ReadFile("../../deploy/compose/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "image: "+StepCAImage+"\n") {
		t.Errorf("compose.yaml step-ca image differs from StepCAImage %s", StepCAImage)
	}
	if !strings.Contains(string(b), "name: "+StepCAVolume) {
		t.Errorf("compose.yaml doesn't use the %s volume", StepCAVolume)
	}
}
