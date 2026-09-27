package main

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestRunAdminAccess(t *testing.T) {
	var gotArgs []string
	env := func(root bool) apiKeyEnv {
		return apiKeyEnv{isRoot: root, run: func(_ context.Context, _, _ io.Writer, _ string, args ...string) (int, error) {
			gotArgs = args
			return 0, nil
		}}
	}
	var out, errb bytes.Buffer
	if code := runAdminAccess(t.Context(), []string{"open"}, &out, &errb, env(true)); code != 0 {
		t.Fatalf("code %d, %q", code, errb.String())
	}
	if want := []string{"exec", "linx-control-plane", "/usr/local/bin/service", "admin-access", "open"}; !slices.Equal(gotArgs, want) {
		t.Fatalf("ran docker %q", gotArgs)
	}
	gotArgs = nil
	if code := runAdminAccess(t.Context(), []string{"open"}, &out, &errb, env(false)); code != 1 || gotArgs != nil || !strings.Contains(errb.String(), "root") {
		t.Fatalf("non-root: code %d, ran %q, %q", code, gotArgs, errb.String())
	}
}
