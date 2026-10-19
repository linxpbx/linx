// Package watch turns what `linx doctor` finds into problems worth telling
// an admin about (docs/ROADMAP.md "Last task before production", owner
// 2026-09-25).
//
// Nothing here talks to the host or the database. The host side runs the
// checks and turns them into Findings (cmd/linx), and the control plane
// turns Problems into alerts; both sides share this package, so the control
// plane never links doctor's host-only code.
//
// **Only a new problem is told twice is not.** That rule isn't implemented
// here: a Problem's Key is an alert key, and the alert engine already holds
// a flapping problem back, reminds while it is open, and says when it
// clears. So the whole job here is to give the same problem the same key
// every run.
package watch

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Report is what the host sends the control plane: everything the checks
// found this run. An empty Problems list is meaningful -- it is how the
// control plane learns that problems it had open have cleared.
type Report struct {
	// Trigger is "timer" or "now" (an admin asked), for the audit log.
	Trigger  string    `json:"trigger"`
	Problems []Problem `json:"problems"`
}

// Problem is one thing an admin should know about.
type Problem struct {
	// Key is the alert key: the same problem gets the same key on every
	// run, which is what stops an admin being told about it twice.
	Key string `json:"key"`
	// Severity is alert.SeverityWarning or alert.SeverityCritical.
	Severity string `json:"severity"`
	// Title is one short line, for a phone's notification.
	Title string `json:"title"`
	// Message is the whole of it: what is wrong and what to do.
	Message string `json:"message"`
}

// Finding is one check's outcome, as the host read it (doctor.Result under
// its section). Level is doctor's: 1 warning, 2 problem.
type Finding struct {
	Section string
	Level   int
	Message string
	Fix     string
}

// Levels, matching installer.Level. Copied rather than imported so this
// package stays free of host-only code; a test in cmd/linx holds them
// together.
const (
	LevelOK = iota
	LevelWarning
	LevelProblem
)

// Severities, matching alert.SeverityWarning and alert.SeverityCritical.
// Same reason as the levels above.
const (
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// KeyPrefix starts every key this package makes, so the control plane can
// tell its own alerts from everything else's and resolve the ones that have
// cleared.
const KeyPrefix = "watch:"

// Most is the largest number of problems one report may carry. A server
// that has just lost Docker fails nearly every check at once, and an admin
// woken by forty notifications learns less than one woken by ten. The
// severest are kept.
const Most = 10

// Find turns a run's findings into the problems worth reporting: every
// warning and every failure, worst first, at most Most of them.
func Find(findings []Finding) []Problem {
	var problems []Problem
	for _, level := range []int{LevelProblem, LevelWarning} {
		for _, f := range findings {
			if f.Level != level {
				continue
			}
			problems = append(problems, Problem{
				Key:      Key(f.Section, f.Level, f.Message),
				Severity: severity(f.Level),
				Title:    title(f.Message),
				Message:  message(f),
			})
			if len(problems) == Most {
				return problems
			}
		}
	}
	return problems
}

func severity(level int) string {
	if level == LevelProblem {
		return SeverityCritical
	}
	return SeverityWarning
}

// numbers is what changes between two runs of the same problem: how many
// days a certificate has left, how long something waited, an address, a
// count. Masking them is what lets "3649 days left" and "3648 days left"
// be recognised as one problem rather than a new one every day.
var numbers = regexp.MustCompile(`\d+`)

// Key is the alert key for a problem found by section at level. The same
// problem must produce the same key on every run, or an admin is told about
// it again and again; two different problems must not share one, or the
// second is never mentioned. Both the section and the level are part of it,
// so a warning that becomes a failure is a new problem and says so.
//
// The message is masked and hashed rather than used as it is: it is written
// for a person and carries numbers that move on their own.
func Key(section string, level int, message string) string {
	normal := numbers.ReplaceAllString(strings.ToLower(strings.Join(strings.Fields(message), " ")), "#")
	sum := sha256.Sum256([]byte(strings.ToLower(section) + "\x00" + severity(level) + "\x00" + normal))
	return KeyPrefix + hex.EncodeToString(sum[:])[:16]
}

// title is one short line for a notification: the message's first sentence,
// shortened if even that is long.
func title(message string) string {
	t := strings.Join(strings.Fields(message), " ")
	if cut := strings.IndexAny(t, ".:"); cut > 0 {
		t = t[:cut]
	}
	const most = 80
	if len(t) > most {
		t = strings.TrimSpace(t[:most-1]) + "…"
	}
	return t
}

// message is what is wrong and, where the check says so, what to do about
// it. doctor already writes both for a person to read.
func message(f Finding) string {
	out := strings.Join(strings.Fields(f.Message), " ")
	if fix := strings.Join(strings.Fields(f.Fix), " "); fix != "" {
		out += "\n\n" + fix
	}
	return out
}
