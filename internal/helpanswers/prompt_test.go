package helpanswers

import (
	"slices"
	"strings"
	"testing"
)

func TestGuidesFilter(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pieces []string
		text   string
		guides []string
	}{
		{"guides line in pieces", []string{"Do this.\n", "G", "uides: [a], ", "[b]"}, "Do this.\n", []string{"a", "b"}},
		{"a word that starts like it", []string{"Gu", "ests can call.\nGuides: none"}, "Guests can call.\n", nil},
		{"no guides line", []string{"Just ", "this"}, "Just this", nil},
		{"only known guides, once each", []string{"x\nGuides: [a], [zzz], a"}, "x\n", []string{"a"}},
		{"a guides line then more text", []string{"Guides: [a]\nMore."}, "Guides: [a]\nMore.", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			f := &guidesFilter{write: func(s string) error { b.WriteString(s); return nil }}
			for _, p := range tc.pieces {
				if err := f.Write(p); err != nil {
					t.Fatal(err)
				}
			}
			guides, err := f.Close([]string{"a", "b"})
			if err != nil {
				t.Fatal(err)
			}
			if b.String() != tc.text || !slices.Equal(guides, tc.guides) {
				t.Errorf("got %q %v, want %q %v", b.String(), guides, tc.text, tc.guides)
			}
		})
	}
}

// Linx's own bounds, whatever the provider sends (help step 4 review).
func TestGuidesFilterBounds(t *testing.T) {
	var b strings.Builder
	f := &guidesFilter{write: func(s string) error { b.WriteString(s); return nil }}
	long := "Guides: " + strings.Repeat("[a], ", maxGuidesLine/4)
	if err := f.Write(long); err != nil {
		t.Fatal(err)
	}
	if b.String() != long {
		t.Errorf("a guides line past %d bytes was held back", maxGuidesLine)
	}
	var err error
	for i := 0; err == nil && i < maxAnswerBytes; i++ {
		err = f.Write("words ")
	}
	if err != errTooLong {
		t.Errorf("an answer past %d bytes: %v", maxAnswerBytes, err)
	}
}
