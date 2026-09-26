package main

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestRunTrunk(t *testing.T) {
	var gotArgs []string
	env := func(root, terminal bool, code int) apiKeyEnv {
		return apiKeyEnv{isRoot: root, terminal: terminal, run: func(_ context.Context, _, _ io.Writer, _ string, args ...string) (int, error) {
			gotArgs = args
			return code, nil
		}}
	}
	var out, errb bytes.Buffer
	if code := runTrunk(t.Context(), []string{"add"}, &out, &errb, env(true, true, 0)); code != 0 ||
		!slices.Equal(gotArgs, []string{"exec", "-i", "-t", "linx-control-plane", "/usr/local/bin/service", "trunk", "add"}) {
		t.Fatalf("code %d, ran %q", code, gotArgs)
	}
	if code := runTrunk(t.Context(), []string{"list"}, &out, &errb, env(true, false, 0)); code != 0 ||
		!slices.Equal(gotArgs, []string{"exec", "-i", "linx-control-plane", "/usr/local/bin/service", "trunk", "list"}) {
		t.Fatalf("code %d, ran %q", code, gotArgs)
	}
	if code := runTrunk(t.Context(), []string{"list"}, &out, &errb, env(false, true, 0)); code != 1 || !strings.Contains(errb.String(), "root") {
		t.Fatalf("non-root: code %d, %q", code, errb.String())
	}
}
