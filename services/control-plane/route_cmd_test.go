package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/pbx"
)

// fakeRoutes answers like the database would: classification from the
// library (the Docker test proves the two agree), and "no lines yet".
type fakeRoutes struct {
	tenant uuid.UUID
	ext    pbx.Extension
}

func (f *fakeRoutes) DefaultTenant(context.Context) (uuid.UUID, error) { return f.tenant, nil }
func (f *fakeRoutes) Country(context.Context) (string, error)          { return "AE", nil }
func (f *fakeRoutes) ExtensionByNumber(_ context.Context, _ uuid.UUID, number string) (pbx.Extension, error) {
	if number != f.ext.Number {
		return pbx.Extension{}, pbx.ErrNotFound
	}
	return f.ext, nil
}
func (f *fakeRoutes) Classify(_ context.Context, home, dialled string) (numbering.Result, error) {
	return numbering.Classify(home, dialled), nil
}
func (f *fakeRoutes) Route(_ context.Context, _ uuid.UUID, dialled string) (numbering.Route, error) {
	r := numbering.Route{Result: numbering.Classify("AE", dialled), Reason: numbering.ReasonNoLines}
	switch r.Category {
	case numbering.Emergency:
		r.Allowed, r.Reason = true, numbering.ReasonEmergency
		r.Lines = []numbering.Line{{Trunk: "UCM", Number: r.Dial}}
	case numbering.Invalid:
		r.Reason = numbering.ReasonInvalid
	}
	return r, nil
}

func TestRouteCommand(t *testing.T) {
	st := &fakeRoutes{tenant: uuid.New(), ext: pbx.Extension{ID: uuid.New(), Number: "101"}}
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := routeCommand(t.Context(), st, args, &out, &errb)
		return code, out.String(), errb.String()
	}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"test", "050 123 4567", "--from", "101"}, []string{"Mobile number: +971 50 123 4567.", "no outside line is set up"}},
		{[]string{"test", "--from", "101", "999"}, []string{"Emergency number (police): 999.", "Always allowed", `Goes out on "UCM" as 999.`}},
		{[]string{"test", "901", "--from", "101"}, []string{"Emergency number (police (non-emergency)): 901.", "Always allowed"}},
		{[]string{"test", "0044 20 7946 0958"}, []string{"International number in United Kingdom: +44 20 7946 0958.", "--from"}},
		{[]string{"test", "12345"}, []string{`"12345" isn't a number that can be called from United Arab Emirates.`}},
	} {
		code, out, errOut := run(tc.args...)
		if code != 0 {
			t.Fatalf("%q: code %d, stderr %q", tc.args, code, errOut)
		}
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%q: output %q lacks %q", tc.args, out, w)
			}
		}
	}
	if code, _, errOut := run("test", "999", "--from", "555"); code != 1 || !strings.Contains(errOut, `no extension "555"`) {
		t.Errorf("unknown extension: code %d, %q", code, errOut)
	}
	if code, _, _ := run("test"); code != 2 {
		t.Errorf("no number: code %d", code)
	}
	if code, _, _ := run("test", "999", "998"); code != 2 {
		t.Errorf("two numbers: code %d", code)
	}
	if code, _, _ := run("dial", "999"); code != 2 {
		t.Errorf("unknown command: code %d", code)
	}
}
