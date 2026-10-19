package main

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/alert"
	"linxpbx.com/linx/internal/watch"
)

type fakeWatchStore struct {
	tenant uuid.UUID
	open   []alert.Alert
}

func (f *fakeWatchStore) DefaultTenant(context.Context) (uuid.UUID, error) { return f.tenant, nil }
func (f *fakeWatchStore) ListAlerts(_ context.Context, _ uuid.UUID, status string, _ *uuid.UUID, _ int) ([]alert.Alert, error) {
	if status != alert.StatusOpen {
		return nil, nil
	}
	return f.open, nil
}

type fakeWatchEngine struct {
	fired    []string
	resolved []string
}

func (f *fakeWatchEngine) Fire(_ context.Context, _ uuid.UUID, key, _, _, _, _ string) error {
	f.fired = append(f.fired, key)
	return nil
}
func (f *fakeWatchEngine) Resolve(_ context.Context, _ uuid.UUID, key string) error {
	f.resolved = append(f.resolved, key)
	return nil
}

func runWatchReport(t *testing.T, st *fakeWatchStore, eng *fakeWatchEngine, rep watch.Report) (int, string) {
	t.Helper()
	in, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	code := watchReport(context.Background(), st, eng, bytes.NewReader(in), &out, &errs)
	return code, errs.String()
}

func TestWatchReportRaisesAnAlertForEachProblem(t *testing.T) {
	st := &fakeWatchStore{tenant: uuid.New()}
	eng := &fakeWatchEngine{}
	a := watch.Key("Database", watch.LevelWarning, "No API key exists yet.")
	b := watch.Key("Services", watch.LevelProblem, "Asterisk isn't running.")
	code, errs := runWatchReport(t, st, eng, watch.Report{Trigger: "timer", Problems: []watch.Problem{
		{Key: a, Severity: watch.SeverityWarning, Title: "No API key", Message: "No API key exists yet."},
		{Key: b, Severity: watch.SeverityCritical, Title: "Asterisk", Message: "Asterisk isn't running."},
	}})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !slices.Contains(eng.fired, a) || !slices.Contains(eng.fired, b) {
		t.Errorf("fired = %v", eng.fired)
	}
}

// A problem that has gone away should say so, or the admin is left thinking
// it is still there.
func TestWatchReportResolvesWhatHasCleared(t *testing.T) {
	gone := watch.Key("Database", watch.LevelWarning, "No API key exists yet.")
	still := watch.Key("Services", watch.LevelProblem, "Asterisk isn't running.")
	st := &fakeWatchStore{tenant: uuid.New(), open: []alert.Alert{{Key: gone}, {Key: still}}}
	eng := &fakeWatchEngine{}
	code, errs := runWatchReport(t, st, eng, watch.Report{Problems: []watch.Problem{
		{Key: still, Severity: watch.SeverityCritical, Title: "Asterisk", Message: "Asterisk isn't running."},
	}})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !slices.Contains(eng.resolved, gone) {
		t.Errorf("the cleared problem wasn't resolved: %v", eng.resolved)
	}
	if slices.Contains(eng.resolved, still) {
		t.Errorf("a problem still being reported was resolved: %v", eng.resolved)
	}
}

// The watch must never close an alert another part of Linx raised -- a
// trunk down, an emergency call, a failing backup.
func TestWatchReportLeavesOtherAlertsAlone(t *testing.T) {
	st := &fakeWatchStore{tenant: uuid.New(), open: []alert.Alert{
		{Key: "trunk:01a1-down"}, {Key: "backup:failed"}, {Key: watch.Key("Database", watch.LevelWarning, "x")},
	}}
	eng := &fakeWatchEngine{}
	if code, errs := runWatchReport(t, st, eng, watch.Report{}); code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	for _, key := range eng.resolved {
		if !strings.HasPrefix(key, watch.KeyPrefix) {
			t.Errorf("resolved an alert that wasn't the watch's: %q", key)
		}
	}
	if len(eng.resolved) != 1 {
		t.Errorf("resolved = %v, wanted only the watch's own", eng.resolved)
	}
}

// A report is read from a host process, so it is not trusted to name any
// alert it likes.
func TestWatchReportRefusesAKeyThatIsNotItsOwn(t *testing.T) {
	st := &fakeWatchStore{tenant: uuid.New()}
	eng := &fakeWatchEngine{}
	code, errs := runWatchReport(t, st, eng, watch.Report{Problems: []watch.Problem{
		{Key: "trunk:01a1-down", Severity: watch.SeverityCritical, Title: "nope", Message: "nope"},
	}})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if len(eng.fired) != 0 {
		t.Errorf("fired %v for a key that wasn't the watch's", eng.fired)
	}
	if !strings.Contains(errs, "isn't the watch's") {
		t.Errorf("nothing was said about it: %q", errs)
	}
}

func TestWatchReportRefusesRubbish(t *testing.T) {
	var out, errs bytes.Buffer
	code := watchReport(context.Background(), &fakeWatchStore{}, &fakeWatchEngine{},
		strings.NewReader("not json"), &out, &errs)
	if code != 2 {
		t.Errorf("exit = %d, wanted 2", code)
	}
}
