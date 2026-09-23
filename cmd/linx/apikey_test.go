package main

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestRunAPIKey(t *testing.T) {
	var gotName string
	var gotArgs []string
	env := func(root bool, code int) apiKeyEnv {
		return apiKeyEnv{isRoot: root, run: func(_ context.Context, stdout, _ io.Writer, name string, args ...string) (int, error) {
			gotName, gotArgs = name, args
			io.WriteString(stdout, "ok\n")
			return code, nil
		}}
	}
	var out, errb bytes.Buffer

	code := runAPIKey(t.Context(), []string{"create", "--name", "My laptop; rm -rf /", "--role", "admin"}, &out, &errb, env(true, 0))
	want := []string{"exec", "linx-control-plane", "/usr/local/bin/service", "api-key", "create", "--name", "My laptop; rm -rf /", "--role", "admin"}
	if code != 0 || gotName != "docker" || !slices.Equal(gotArgs, want) {
		t.Fatalf("code %d, ran %s %q", code, gotName, gotArgs)
	}
	if out.String() != "ok\n" {
		t.Fatalf("output not passed through: %q", out.String())
	}

	errb.Reset()
	if code := runAPIKey(t.Context(), []string{"list"}, &out, &errb, env(false, 0)); code != 1 || !strings.Contains(errb.String(), "root") {
		t.Fatalf("non-root: code %d, %q", code, errb.String())
	}

	errb.Reset()
	if code := runAPIKey(t.Context(), []string{"list"}, &out, &errb, env(true, 125)); code != 1 || !strings.Contains(errb.String(), "running") {
		t.Fatalf("docker failure: code %d, %q", code, errb.String())
	}
	if code := runAPIKey(t.Context(), []string{"create"}, &out, &errb, env(true, 2)); code != 2 {
		t.Fatalf("exit code not passed through: %d", code)
	}
}
