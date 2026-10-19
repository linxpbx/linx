package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/doctor"
	"linxpbx.com/linx/internal/installer"
	"linxpbx.com/linx/internal/watch"
)

// watch keeps its own copies of doctor's levels so the control plane does
// not link doctor's host-only code. If they ever drift, a warning would be
// reported as a failure, or an "ok" as a problem.
func TestWatchLevelsMatchDoctors(t *testing.T) {
	for _, c := range []struct {
		doctor installer.Level
		watch  int
	}{{installer.OK, watch.LevelOK}, {installer.Warn, watch.LevelWarning}, {installer.Fail, watch.LevelProblem}} {
		if int(c.doctor) != c.watch {
			t.Errorf("doctor's %v is %d, watch's is %d", c.doctor, int(c.doctor), c.watch)
		}
	}
}

// Found on a real server (2026-10-19): the watch raises an alert, doctor
// reports that open alert as a warning on the next run, and the watch
// raises an alert about the alert. Two problems became four in one run.
func TestAnAlertIsNotAFindingAboutItself(t *testing.T) {
	got := findings([]doctor.Section{{Name: "Database, access and alerts", Results: []doctor.Result{
		{Level: installer.Warn, Message: doctor.OpenAlertPrefix + "19 Oct 18:13: No API key exists yet"},
		{Level: installer.Warn, Message: "No API key exists yet, so nothing can manage Linx through its API."},
	}}})
	if len(got) != 1 {
		t.Fatalf("wanted the real problem only, got %d: %+v", len(got), got)
	}
	if strings.HasPrefix(got[0].Message, doctor.OpenAlertPrefix) {
		t.Errorf("the mirrored alert was kept: %q", got[0].Message)
	}
}

func watchTestEnv(sent *[]byte, fail error) watchEnv {
	return watchEnv{
		isRoot:      true,
		savedConfig: func() ([]byte, error) { return []byte("version: 1\ndomain:\n  name: pbx.example.com\n"), nil },
		checks: doctor.Env{
			Runner: hostRunner{}, Now: nil,
			Stat: func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist },
		},
		send: func(_ context.Context, payload []byte) ([]byte, error) {
			*sent = payload
			return nil, fail
		},
	}
}

func TestWatchSendsAReportAndSaysNothingByItself(t *testing.T) {
	var sent []byte
	var out, errs bytes.Buffer
	// No --now: this is the timer's run, and a quiet server should stay
	// quiet in the system log.
	if code := runWatch(context.Background(), watchTestEnv(&sent, nil), nil, &out, &errs); code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if out.String() != "" {
		t.Errorf("the timer's run printed %q", out.String())
	}
	var rep watch.Report
	if err := json.Unmarshal(sent, &rep); err != nil {
		t.Fatalf("the report wasn't JSON: %v (%s)", err, sent)
	}
	if rep.Trigger != "timer" {
		t.Errorf("trigger = %q", rep.Trigger)
	}
}

func TestWatchNowSaysWhatItFound(t *testing.T) {
	var sent []byte
	var out, errs bytes.Buffer
	if code := runWatch(context.Background(), watchTestEnv(&sent, nil), []string{"--now"}, &out, &errs); code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if out.String() == "" {
		t.Error("--now printed nothing")
	}
	var rep watch.Report
	if err := json.Unmarshal(sent, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Trigger != "now" {
		t.Errorf("trigger = %q, wanted the admin's own run to be told apart", rep.Trigger)
	}
}

// The checks run on the host and the alerts live in the database: one can
// be there while the other isn't, and the next run tries again.
func TestWatchSaysSoWhenLinxCannotBeReached(t *testing.T) {
	var sent []byte
	var out, errs bytes.Buffer
	code := runWatch(context.Background(), watchTestEnv(&sent, fs.ErrNotExist), nil, &out, &errs)
	if code == 0 {
		t.Error("a failed report looked like success")
	}
	if !strings.Contains(errs.String(), "Can't tell Linx") {
		t.Errorf("stderr = %q", errs.String())
	}
}

func TestWatchNeedsRoot(t *testing.T) {
	var sent []byte
	env := watchTestEnv(&sent, nil)
	env.isRoot = false
	var out, errs bytes.Buffer
	if code := runWatch(context.Background(), env, nil, &out, &errs); code != 1 {
		t.Errorf("exit = %d, wanted 1", code)
	}
	if !strings.Contains(errs.String(), "sudo linx watch") {
		t.Errorf("stderr = %q", errs.String())
	}
}

func TestWatchRefusesAnUnknownOption(t *testing.T) {
	var sent []byte
	var out, errs bytes.Buffer
	if code := runWatch(context.Background(), watchTestEnv(&sent, nil), []string{"--wat"}, &out, &errs); code != 2 {
		t.Errorf("exit = %d, wanted 2", code)
	}
}
