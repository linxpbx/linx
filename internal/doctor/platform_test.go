package doctor

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/installer"
)

const psqlCmd = "docker exec linx-postgres psql --username linx --dbname linx --no-psqlrc --quiet --tuples-only --no-align --command "

// platformFixture is a healthy Phase 1 install on top of the certificate
// fixture.
func platformFixture(t *testing.T, state string) *fixture {
	f := newFixture(t, true, now.Add(80*day), "*.lab.example.com")
	f.runner[inspect+"linx-postgres"] = "running healthy\n"
	f.runner[inspect+"linx-control-plane"] = "running healthy\n"
	f.runner[psqlCmd+platformQuery] = state + "\n"
	f.env.Stat = secretsStat(nil)
	return f
}

const healthyState = `{"schema": 4, "api_keys": 1, "alert_channels": 2, "open_alerts": []}`

type fakeFile struct {
	size int64
	mode fs.FileMode
}

func (f fakeFile) Name() string       { return "f" }
func (f fakeFile) Size() int64        { return f.size }
func (f fakeFile) Mode() fs.FileMode  { return f.mode }
func (f fakeFile) ModTime() time.Time { return now }
func (f fakeFile) IsDir() bool        { return false }
func (f fakeFile) Sys() any           { return nil }

// secretsStat fakes the secret files, healthy unless override says
// otherwise; every other path (e.g. the CA backup) doesn't exist.
func secretsStat(override map[string]fs.FileInfo) func(string) (fs.FileInfo, error) {
	files := map[string]fs.FileInfo{
		installer.DNSTokenPath:           fakeFile{40, 0o440},
		installer.DBPasswordPath:         fakeFile{32, 0o440},
		installer.DBEncryptionKeyPath:    fakeFile{32, 0o440},
		installer.JWTSigningKeyPath:      fakeFile{32, 0o440},
		installer.AsteriskDBPasswordPath: fakeFile{32, 0o440},
	}
	for k, v := range override {
		files[k] = v
	}
	return func(p string) (fs.FileInfo, error) {
		if fi, ok := files[p]; ok && fi != nil {
			return fi, nil
		}
		return nil, fs.ErrNotExist
	}
}

func all(sections []Section) []Result {
	var rs []Result
	for _, s := range sections {
		rs = append(rs, s.Results...)
	}
	return rs
}

func TestRunAllGreen(t *testing.T) {
	f := platformFixture(t, healthyState)
	secs := Run(context.Background(), f.env, f.cfg)
	rs := all(secs)
	if worst(rs) != installer.OK {
		t.Fatalf("want all ok, got:\n%s", dump(rs))
	}
	var names []string
	for _, s := range secs {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "Services,Certificates,Database, access and alerts,Secrets" {
		t.Errorf("sections = %s", got)
	}
	want(t, rs, installer.OK, "The database is running and answering")
	want(t, rs, installer.OK, "The API service is running and answering")
	want(t, rs, installer.OK, "schema version 4")
	want(t, rs, installer.OK, "1 API key can use the API")
	want(t, rs, installer.OK, "2 alert channels will be told")
	want(t, rs, installer.OK, "No open alerts")
	want(t, rs, installer.OK, "secret files are present")
}

func TestRunDockerDown(t *testing.T) {
	secs := Run(context.Background(), Env{Runner: hostRunner{}}, installer.DefaultConfig())
	if len(secs) != 1 || secs[0].Name != "Docker" || worst(secs[0].Results) != installer.Fail {
		t.Fatalf("got %+v", secs)
	}
}

func TestServices(t *testing.T) {
	tests := []struct {
		name, container, state string
		level                  installer.Level
		text                   string
	}{
		{"database stopped", "linx-postgres", "exited \n", installer.Fail, "isn't running (it's exited)"},
		{"database starting", "linx-postgres", "running starting\n", installer.Warn, "still starting"},
		{"api unhealthy", "linx-control-plane", "running unhealthy\n", installer.Fail, "not answering"},
		{"api without a health check (older compose)", "linx-control-plane", "running \n", installer.OK, "The API service is running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := platformFixture(t, healthyState)
			f.runner[inspect+tt.container] = tt.state
			want(t, Services(context.Background(), f.env), tt.level, tt.text)
		})
	}
	t.Run("not installed", func(t *testing.T) {
		f := platformFixture(t, healthyState)
		delete(f.runner, inspect+"linx-control-plane")
		want(t, Services(context.Background(), f.env), installer.Fail, "isn't installed")
	})
}

func TestDatabase(t *testing.T) {
	t.Run("fresh install nudges", func(t *testing.T) {
		f := platformFixture(t, `{"schema": 4, "api_keys": 0, "alert_channels": 0, "open_alerts": []}`)
		rs := Database(context.Background(), f.env)
		want(t, rs, installer.Warn, "No API key exists yet")
		want(t, rs, installer.Warn, "No alert channels are turned on")
		if worst(rs) != installer.Warn {
			t.Errorf("a fresh install should only warn:\n%s", dump(rs))
		}
	})
	t.Run("open alerts by severity", func(t *testing.T) {
		f := platformFixture(t, `{"schema": 4, "api_keys": 1, "alert_channels": 1, "open_alerts": [
			{"severity": "critical", "title": "Certificate renewal is failing", "message": "It expires in 5 days.", "since": "2026-09-23T10:00:00+00:00"},
			{"severity": "warning", "title": "A webhook was turned off", "message": "An admin turned it off.", "since": "2026-09-23T11:00:00.123456+00:00"},
			{"severity": "info", "title": "FYI", "message": "Just so you know.", "since": "2026-09-23T11:30:00+00:00"}]}`)
		rs := Database(context.Background(), f.env)
		want(t, rs, installer.Fail, "Certificate renewal is failing")
		want(t, rs, installer.Warn, "A webhook was turned off")
		want(t, rs, installer.OK, "FYI")
		for _, r := range rs {
			if strings.Contains(r.Message, "Certificate renewal") && !strings.Contains(r.Fix, "It expires in 5 days.") {
				t.Errorf("the alert's own message should be the fix: %q", r.Fix)
			}
		}
	})
	t.Run("query fails", func(t *testing.T) {
		f := platformFixture(t, healthyState)
		delete(f.runner, psqlCmd+platformQuery)
		want(t, Database(context.Background(), f.env), installer.Fail, "Can't read Linx's settings")
	})
	t.Run("garbage output", func(t *testing.T) {
		f := platformFixture(t, "psql: error: something")
		want(t, Database(context.Background(), f.env), installer.Fail, "Can't read Linx's settings")
	})
	t.Run("skipped when the database is down", func(t *testing.T) {
		f := platformFixture(t, healthyState)
		f.runner[inspect+"linx-postgres"] = "exited \n"
		if rs := Database(context.Background(), f.env); len(rs) != 0 {
			t.Errorf("want nothing (Services reports it), got:\n%s", dump(rs))
		}
	})
}

func TestSecrets(t *testing.T) {
	tests := []struct {
		name string
		path string
		file fs.FileInfo
		text string
	}{
		{"missing", installer.JWTSigningKeyPath, nil, "is missing"},
		{"world readable", installer.DBPasswordPath, fakeFile{32, 0o444}, "can be read by anyone"},
		{"wrong key size", installer.DBEncryptionKeyPath, fakeFile{31, 0o440}, "damaged"},
		{"empty token", installer.DNSTokenPath, fakeFile{0, 0o440}, "damaged"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			override := map[string]fs.FileInfo{tt.path: tt.file}
			rs := Secrets(Env{Stat: secretsStat(override)})
			want(t, rs, installer.Fail, tt.text)
			for _, r := range rs {
				if r.Level == installer.OK {
					t.Errorf("an ok line alongside a problem: %q", r.Message)
				}
			}
		})
	}
}
