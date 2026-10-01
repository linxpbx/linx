package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/install"
	"linxpbx.com/linx/internal/installer"
)

func TestWebCertObtain(t *testing.T) {
	staging := "docker " + strings.Join(installer.CertdRun(false, "-bootstrap", "staging"), " ")
	real := "docker " + strings.Join(installer.CertdRun(false, "-bootstrap", "real"), " ")
	w := &webCert{env: setupEnv{runner: hostRunner{
		staging: `{"level":"INFO","msg":"getting the first certificate"}` + "\n" +
			`{"linx_certd_result":{"ok":false,"kind":"connection","detail":"203.0.113.5: Timeout during connect"}}`,
		real: `{"linx_certd_result":{"ok":true}}`,
	}}}
	err := w.Obtain(context.Background(), true)
	var be *certs.BootstrapError
	if !errors.As(err, &be) || be.Kind != certs.ProblemConnection || be.Detail != "203.0.113.5: Timeout during connect" {
		t.Errorf("staging: %v", err)
	}
	if err := w.Obtain(context.Background(), false); err != nil {
		t.Errorf("real: %v", err)
	}
	// certd never got to say: the command's own words.
	w.env.runner = hostRunner{}
	if err := w.Obtain(context.Background(), true); !errors.As(err, &be) || be.Kind != certs.ProblemOther || be.Detail == "" {
		t.Errorf("no result: %v", err)
	}
}

func TestWebCertTokenRefusal(t *testing.T) {
	w := &webCert{env: setupEnv{runner: hostRunner{}, savedConfig: func() ([]byte, error) {
		return []byte("version: 1\ndomain:\n  name: pbx.example.com\n"), nil
	}}}
	refusal, err := w.SaveToken(context.Background(), install.CertView{}, install.DNSKey{Token: "has a space in it and is long enough"})
	if err != nil || !strings.HasPrefix(refusal, "The Cloudflare API token can't contain spaces") {
		t.Errorf("%q %v", refusal, err)
	}
	refusal, err = w.SaveToken(context.Background(), install.CertView{}, install.DNSKey{Provider: "porkbun", Token: "pk1_only-one-of-two"})
	if err != nil || !strings.Contains(refusal, "Porkbun") {
		t.Errorf("porkbun with one value: %q %v", refusal, err)
	}
}
