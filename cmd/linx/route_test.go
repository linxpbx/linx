package main

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestRunRoute(t *testing.T) {
	var gotArgs []string
	env := func(root bool, code int) apiKeyEnv {
		return apiKeyEnv{isRoot: root, run: func(_ context.Context, _, _ io.Writer, _ string, args ...string) (int, error) {
			gotArgs = args
			return code, nil
		}}
	}
	var out, errb bytes.Buffer
	code := runRoute(t.Context(), []string{"test", "050 123 4567", "--from", "101"}, &out, &errb, env(true, 0))
	want := []string{"exec", "linx-control-plane", "/usr/local/bin/service", "route", "test", "050 123 4567", "--from", "101"}
	if code != 0 || !slices.Equal(gotArgs, want) {
		t.Fatalf("code %d, ran %q", code, gotArgs)
	}
	if code := runRoute(t.Context(), []string{"test", "999"}, &out, &errb, env(false, 0)); code != 1 || !strings.Contains(errb.String(), "root") {
		t.Fatalf("non-root: code %d, %q", code, errb.String())
	}
	errb.Reset()
	if code := runRoute(t.Context(), []string{"test", "999"}, &out, &errb, env(true, 126)); code != 1 || !strings.Contains(errb.String(), "running") {
		t.Fatalf("docker failure: code %d, %q", code, errb.String())
	}
}
