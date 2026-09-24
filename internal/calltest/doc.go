// Package calltest is the phone engine's automated call suite (docs/PBX.md
// §7): real Asterisk (the linx-asterisk image), real Postgres, SIPp phones
// over TLS with SRTP, and the control plane's ARI app, all run by
// `make test-docker`. It has no non-test code.
package calltest
