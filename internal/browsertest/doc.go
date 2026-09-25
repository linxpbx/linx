// Package browsertest is the web client's automated call suite (docs/WEB.md
// §8 step 5): the real compose stack (control plane with the web client,
// Asterisk, coturn, Postgres, the internal CA), two headless Chromium
// browsers in Playwright's container calling each other, one of them
// unable to use UDP so its audio must go through the relay over TLS, plus
// a browser calling a SIPp softphone and back, and the Opus echo test. Run by
// `make test-browser` (needs the control-plane, asterisk and coturn images).
// It has no non-test code.
package browsertest
