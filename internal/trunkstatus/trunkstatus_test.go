package trunkstatus

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// Captured from the linx-asterisk image (Asterisk 22).
const registrationsOut = `
 <Registration/ServerURI..............................>  <Auth....................>  <Status.......>
==========================================================================================

 trunk-11111111-1111-1111-1111-111111111111/sip:192.0.2  trunk-11111111-1111-1111-1111-111111111111  Rejected          (exp. 18s ago)
 trunk-33333333-3333-3333-3333-333333333333/sip:provid  trunk-33333333-3333-3333-3333-333333333333  Registered        (exp. 3585s)
 other/sip:example.com  n/a  Registered        (exp. 20s)

Objects found: 3
`

const contactsOut = `
  Contact:  <Aor/ContactUri..............................> <Hash....> <Status> <RTT(ms)..>
==========================================================================================

  Contact:  trunk-11111111-1111-1111-1111-111111111111/sip 6a87682040 Unavail         nan
  Contact:  trunk-22222222-2222-2222-2222-222222222222/sip b0e9390783 Avail         0.497
  Contact:  d_7k2m9x4q/sip:d_7k2m9x4q@192.168.1.20:5061 1234567890 Avail  12.000

Objects found: 3
`

func TestParse(t *testing.T) {
	regs := ParseRegistrations(registrationsOut)
	wantRegs := map[string]string{
		"trunk-11111111-1111-1111-1111-111111111111": RegRejected,
		"trunk-33333333-3333-3333-3333-333333333333": RegRegistered,
	}
	if !reflect.DeepEqual(regs, wantRegs) {
		t.Errorf("registrations = %v, want %v", regs, wantRegs)
	}
	contacts := ParseContacts(contactsOut)
	wantContacts := map[string]string{
		"trunk-11111111-1111-1111-1111-111111111111": ContactUnavail,
		"trunk-22222222-2222-2222-2222-222222222222": ContactAvail,
	}
	if !reflect.DeepEqual(contacts, wantContacts) {
		t.Errorf("contacts = %v, want %v", contacts, wantContacts)
	}
	merged := Merge(regs, contacts)
	if got := merged["trunk-11111111-1111-1111-1111-111111111111"]; got != (Trunk{Registration: RegRejected, Contact: ContactUnavail}) {
		t.Errorf("merged = %+v", got)
	}
	if len(merged) != 3 {
		t.Errorf("merged has %d trunks, want 3", len(merged))
	}
	if len(ParseRegistrations("No objects found.\n")) != 0 || len(ParseContacts("")) != 0 {
		t.Error("empty output should parse to nothing")
	}
}

func TestDecide(t *testing.T) {
	for _, tc := range []struct {
		registers bool
		in        Trunk
		want      string
	}{
		{true, Trunk{RegRegistered, ContactAvail}, StatusRegistered},
		{true, Trunk{RegRegistered, ContactNonQual}, StatusRegistered},
		{true, Trunk{RegRejected, ContactAvail}, StatusRejected},
		{true, Trunk{RegRejected, ContactUnknown}, StatusUnreachable},
		{true, Trunk{RegRegistered, ContactUnavail}, StatusUnreachable},
		{true, Trunk{RegUnregistered, ContactAvail}, StatusUnknown},
		{true, Trunk{}, StatusUnknown},
		{false, Trunk{Contact: ContactAvail}, StatusReachable},
		{false, Trunk{Contact: ContactUnavail}, StatusUnreachable},
		{false, Trunk{Contact: ContactNonQual}, StatusUnknown},
		{false, Trunk{}, StatusUnknown},
	} {
		got, detail := Decide(tc.registers, tc.in)
		if got != tc.want || detail == "" {
			t.Errorf("Decide(%v, %+v) = %q %q, want %q", tc.registers, tc.in, got, detail, tc.want)
		}
	}
}

func TestWriteRead(t *testing.T) {
	dir := t.TempDir()
	if _, err := Read(dir); !errors.Is(err, ErrNoFile) {
		t.Fatalf("Read of an empty dir = %v, want ErrNoFile", err)
	}
	f := File{WrittenAt: time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC),
		Trunks: map[string]Trunk{"trunk-x": {Registration: RegRegistered, Contact: ContactAvail}}}
	if err := Write(dir, f); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.WrittenAt.Equal(f.WrittenAt) || !reflect.DeepEqual(got.Trunks, f.Trunks) {
		t.Errorf("Read = %+v, want %+v", got, f)
	}
}
