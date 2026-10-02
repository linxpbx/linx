package callhistory

import (
	"fmt"
	"strings"
)

// messageHeard is what each of linx-messages' messages says, shortened
// (tools/prompts/prompts.tsv has them in full).
var messageHeard = map[string]string{
	"not-in-use":    `"That number isn't in use"`,
	"not-available": `"Nobody can take your call right now"`,
	"not-permitted": `"This phone isn't allowed to call that number"`,
	"no-lines":      `"Calls outside the company can't be made right now"`,
	"limit":         `"This phone is already on as many outside calls as it's allowed"`,
	"closed":        `"We're closed right now"`,
}

// Words is one step in plain words, for a call's detail: "Rang Sales
// (all at once): Bob (102), Dave (109) · Dave (109) answered". A
// voicemail step's message, if one was left, is the caller's to add.
func (s Step) Words() string {
	switch s.Kind {
	case StepRing:
		if len(s.People) == 0 {
			if s.Group != "" {
				return fmt.Sprintf("Nobody in %s could ring (no phone or browser connected)", s.Group)
			}
			return fmt.Sprintf("%s has no phone or browser connected", s.Target)
		}
		var b strings.Builder
		b.WriteString("Rang ")
		if s.Group != "" {
			how := "all at once"
			if s.InTurn {
				how = "one after another"
			}
			fmt.Fprintf(&b, "%s (%s): ", s.Group, how)
		}
		b.WriteString(strings.Join(s.People, ", "))
		switch {
		case s.AnsweredBy != "" && s.Group != "":
			fmt.Fprintf(&b, " · %s answered", s.AnsweredBy)
		case s.AnsweredBy != "":
			b.WriteString(" · answered")
		case s.Busy:
			b.WriteString(" · busy")
		default:
			fmt.Fprintf(&b, " · nobody answered in %s", seconds(s.Seconds))
		}
		return b.String()
	case StepLine:
		switch s.Outcome {
		case "answered":
			return fmt.Sprintf("Went out on %s · answered", s.Line)
		case "no_answer":
			return fmt.Sprintf("Went out on %s · no answer", s.Line)
		case "busy":
			return fmt.Sprintf("Went out on %s · busy", s.Line)
		}
		return fmt.Sprintf("%s couldn't take it (down or full)", s.Line)
	case StepVoicemail:
		return "Went to the voicemail for " + s.Box
	case StepMessage:
		if m, ok := messageHeard[s.Message]; ok {
			return "Heard " + m
		}
		return "Heard a message"
	case StepEcho:
		return "Echo test"
	}
	return ""
}

func seconds(n int) string {
	if n < 60 {
		return fmt.Sprintf("%d s", n)
	}
	return fmt.Sprintf("%d:%02d", n/60, n%60)
}
