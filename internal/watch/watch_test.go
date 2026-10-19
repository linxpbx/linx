package watch

import (
	"strings"
	"testing"
)

// The real warnings pbx.mym.ae reported on the day this was written, so the
// tests are about messages Linx actually writes rather than invented ones.
const (
	noAPIKey  = "No API key exists yet, so nothing can manage Linx through its API."
	noLines   = "No phone lines to the outside yet: only calls between extensions work, and emergency calls can't go out."
	certLeft  = "Internal intermediate certificate valid until 16 Oct 2036 (3649 days left)."
	certFewer = "Internal intermediate certificate valid until 16 Oct 2036 (3648 days left)."
	notRunnig = "Asterisk (linx-asterisk) isn't running (it's exited)."
)

func TestTheSameProblemKeepsItsKey(t *testing.T) {
	if a, b := Key("Database", LevelWarning, noAPIKey), Key("Database", LevelWarning, noAPIKey); a != b {
		t.Errorf("the same problem got two keys: %q and %q", a, b)
	}
}

// Without this the admin is told about the certificate every single day, as
// the days-left count ticks down.
func TestANumberThatMovesOnItsOwnIsNotANewProblem(t *testing.T) {
	if a, b := Key("Certificates", LevelWarning, certLeft), Key("Certificates", LevelWarning, certFewer); a != b {
		t.Errorf("one day passing made a new problem:\n %q\n %q", a, b)
	}
}

func TestDifferentProblemsGetDifferentKeys(t *testing.T) {
	seen := map[string]string{}
	for _, c := range []struct{ section, message string }{
		{"Database", noAPIKey}, {"Phone lines", noLines}, {"Certificates", certLeft}, {"Services", notRunnig},
	} {
		k := Key(c.section, LevelWarning, c.message)
		if was, clash := seen[k]; clash {
			t.Errorf("%q and %q share a key", was, c.message)
		}
		seen[k] = c.message
	}
}

// The same words under a different heading are a different problem, and a
// warning that becomes a failure is worth saying again.
func TestTheSectionAndTheLevelAreBothPartOfIt(t *testing.T) {
	if Key("Services", LevelWarning, notRunnig) == Key("Phone system", LevelWarning, notRunnig) {
		t.Error("two sections shared a key")
	}
	if Key("Services", LevelWarning, notRunnig) == Key("Services", LevelProblem, notRunnig) {
		t.Error("a warning and a failure shared a key")
	}
}

func TestOnlyWarningsAndFailuresAreReported(t *testing.T) {
	got := Find([]Finding{
		{Section: "Services", Level: LevelOK, Message: "Everything is running."},
		{Section: "Database", Level: LevelWarning, Message: noAPIKey, Fix: "Create one: sudo linx api-key create"},
	})
	if len(got) != 1 {
		t.Fatalf("wanted the one warning, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Message, "Create one") {
		t.Errorf("the fix was dropped: %q", got[0].Message)
	}
	if got[0].Severity != SeverityWarning {
		t.Errorf("severity = %q", got[0].Severity)
	}
}

func TestFailuresComeFirst(t *testing.T) {
	got := Find([]Finding{
		{Section: "Database", Level: LevelWarning, Message: noAPIKey},
		{Section: "Services", Level: LevelProblem, Message: notRunnig},
	})
	if len(got) != 2 || got[0].Severity != SeverityCritical {
		t.Fatalf("wanted the failure first, got %+v", got)
	}
}

// A server that has just lost Docker fails nearly every check at once.
func TestAStormIsCutShort(t *testing.T) {
	var findings []Finding
	for i := range 40 {
		findings = append(findings, Finding{Section: "Services", Level: LevelProblem, Message: string(rune('a'+i%26)) + " is not running"})
	}
	if got := Find(findings); len(got) != Most {
		t.Errorf("wanted at most %d problems, got %d", Most, len(got))
	}
}

func TestTitleIsOneShortLine(t *testing.T) {
	got := Find([]Finding{{Section: "Phone lines", Level: LevelWarning, Message: noLines}})[0].Title
	if strings.ContainsAny(got, "\n") || len(got) > 80 {
		t.Errorf("title is not one short line: %q", got)
	}
	if got != "No phone lines to the outside yet" {
		t.Errorf("title = %q", got)
	}
}

func TestEveryKeyIsRecognisable(t *testing.T) {
	// The control plane tells its own alerts apart by this prefix, and
	// resolves the ones that have cleared; a key without it would be left
	// open for ever.
	for _, p := range Find([]Finding{{Section: "Database", Level: LevelWarning, Message: noAPIKey}}) {
		if !strings.HasPrefix(p.Key, KeyPrefix) {
			t.Errorf("key %q doesn't start with %q", p.Key, KeyPrefix)
		}
	}
}
