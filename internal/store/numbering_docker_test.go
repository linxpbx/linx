package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nyaruka/phonenumbers"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/pbx"
)

// TestNumberingDocker checks the database's numbering decision (migration
// 0016) against libphonenumber itself (numbering.Classify) on thousands of
// numbers per supported country (ADR-044), then the outgoing-call decision
// and the extension-number checks. It needs Docker: make test-docker.
func TestNumberingDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-numbering-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	data, err := numbering.Build()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if changed, err := s.SyncNumbering(ctx, data, now); err != nil || !changed {
		t.Fatalf("first sync: changed %v, err %v", changed, err)
	}
	if changed, err := s.SyncNumbering(ctx, data, now); err != nil || changed {
		t.Fatalf("second sync with the same data: changed %v, err %v", changed, err)
	}

	for home := range numbering.Countries {
		t.Run(home, func(t *testing.T) {
			corpus := numberCorpus(home)
			rows, err := pool.Query(ctx, `SELECT c.category, coalesce(c.number_type, ''), coalesce(c.region, ''),
					coalesce(c.e164, ''), coalesce(c.dial, ''), coalesce(c.label, '')
				FROM unnest($2::text[]) WITH ORDINALITY AS n(dialled, i), numbering_classify($1, n.dialled) c
				ORDER BY n.i`, home, corpus)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			i, failures := 0, 0
			counts := map[numbering.Category]int{}
			for rows.Next() {
				var got numbering.Result
				var cat string
				if err := rows.Scan(&cat, &got.NumberType, &got.Region, &got.E164, &got.Dial, &got.Label); err != nil {
					t.Fatal(err)
				}
				got.Category = numbering.Category(cat)
				counts[got.Category]++
				if want := numbering.Classify(home, corpus[i]); got != want {
					failures++
					if failures <= 30 {
						t.Errorf("%q: database %+v, libphonenumber %+v", corpus[i], got, want)
					}
				}
				i++
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if i != len(corpus) {
				t.Fatalf("%d results for %d numbers", i, len(corpus))
			}
			if failures > 0 {
				t.Fatalf("%d of %d numbers disagree", failures, len(corpus))
			}
			// Every kind of number must be in the corpus, or agreeing proves little.
			for _, c := range []numbering.Category{numbering.Emergency, numbering.Service, numbering.Landline, numbering.Mobile,
				numbering.National, numbering.SharedCost, numbering.TollFree, numbering.Premium, numbering.International, numbering.Invalid} {
				if counts[c] == 0 {
					t.Errorf("no %s numbers in the corpus", c)
				}
			}
			t.Logf("%d numbers agree: %v", len(corpus), counts)
		})
	}

	// The outgoing-call decision.
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	audit := auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "test", Result: auth.ResultOK}
	newExtension := func(number string, enabled bool) (pbx.Extension, error) {
		e := pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Number: number, DisplayName: "Ext " + number,
			Enabled: enabled, Version: 1, CreatedAt: now, UpdatedAt: now}
		return e, s.CreateExtension(ctx, e, audit)
	}
	ext, err := newExtension("101", true)
	if err != nil {
		t.Fatal(err)
	}
	off, err := newExtension("102", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		from     uuid.UUID
		dialled  string
		category numbering.Category
		allowed  bool
		reason   string
		dial     string
	}{
		{ext.ID, "999", numbering.Emergency, true, numbering.ReasonEmergency, "999"},
		{ext.ID, "901", numbering.Emergency, true, numbering.ReasonEmergency, "901"},
		// ext has no call permission level assigned, so it can only call
		// emergency numbers (migration 0017, fail closed).
		{ext.ID, "050 123 4567", numbering.Mobile, false, numbering.ReasonNotPermitted, "+971501234567"},
		{ext.ID, "+44 20 7946 0958", numbering.International, false, numbering.ReasonNotPermitted, "+442079460958"},
		{ext.ID, "12", numbering.Invalid, false, numbering.ReasonInvalid, ""},
		{off.ID, "0501234567", numbering.Mobile, false, numbering.ReasonUnknownCaller, "+971501234567"},
		{uuid.Nil, "999", numbering.Emergency, false, numbering.ReasonUnknownCaller, "999"},
	} {
		r, err := s.Route(ctx, tc.from, tc.dialled)
		if err != nil {
			t.Fatal(err)
		}
		if r.Category != tc.category || r.Allowed != tc.allowed || r.Reason != tc.reason || r.Dial != tc.dial {
			t.Errorf("Route(%q) = %+v, want %s allowed=%v %s %q", tc.dialled, r, tc.category, tc.allowed, tc.reason, tc.dial)
		}
	}

	// Extension numbers that look like outside or emergency numbers are
	// refused, on create and on rename.
	for number, reason := range map[string]string{
		"0100": numbering.ClashNationalPrefix,
		"999":  numbering.ClashEmergency,
		"901":  numbering.ClashEmergency,
		"112":  numbering.ClashEmergency,
		"4451": numbering.ClashService,
	} {
		_, err := newExtension(number, true)
		var reserved *pbx.ReservedNumberError
		if !errors.As(err, &reserved) || reserved.Reason != reason {
			t.Errorf("creating extension %s: err = %v, want reserved (%s)", number, err, reason)
		}
		e := ext
		e.Number = number
		_, err = s.UpdateExtension(ctx, e, audit)
		if !errors.As(err, &reserved) || reserved.Reason != reason {
			t.Errorf("renaming to %s: err = %v, want reserved (%s)", number, err, reason)
		}
	}
	// An extension that clashes (from before the check, or another country)
	// can still be renamed or turned off; it's listed instead.
	if _, err := pool.Exec(ctx, `ALTER TABLE extension DISABLE TRIGGER extension_number_insert`); err != nil {
		t.Fatal(err)
	}
	old, err := newExtension("998", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE extension ENABLE TRIGGER extension_number_insert`); err != nil {
		t.Fatal(err)
	}
	old.DisplayName = "Renamed"
	if _, err := s.UpdateExtension(ctx, old, audit); err != nil {
		t.Fatalf("renaming a clashing extension's name: %v", err)
	}
	clashes, err := s.ExtensionClashes(ctx, tenant, "AE")
	if err != nil {
		t.Fatal(err)
	}
	if len(clashes) != 1 || clashes[0] != (numbering.Clash{Number: "998", Reason: numbering.ClashEmergency}) {
		t.Errorf("clashes = %+v", clashes)
	}
	if c, err := s.Country(ctx); err != nil || c != numbering.DefaultCountry {
		t.Errorf("country = %q, %v", c, err)
	}
}

// numberCorpus is what the database and libphonenumber are compared on for
// a home country: every example number libphonenumber has, written the
// ways people dial them, slightly wrong versions of them, every number up
// to 4 digits, and random digits after the prefixes that matter.
func numberCorpus(home string) []string {
	rng := rand.New(rand.NewPCG(1, 2))
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	digits := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte('0' + rng.IntN(10))
		}
		return string(b)
	}
	homeCC := strconv.Itoa(phonenumbers.GetCountryCodeForRegion(home))
	coll, _ := phonenumbers.MetadataCollection()
	for _, m := range coll.GetMetadata() {
		cc := strconv.Itoa(int(m.GetCountryCode()))
		isHome := m.GetId() == home
		for _, d := range []*phonenumbers.PhoneNumberDesc{m.GetFixedLine(), m.GetMobile(), m.GetTollFree(),
			m.GetPremiumRate(), m.GetSharedCost(), m.GetVoip(), m.GetPersonalNumber(), m.GetPager(), m.GetUan(), m.GetVoicemail()} {
			ex := d.GetExampleNumber()
			if ex == "" {
				continue
			}
			variants := []string{ex, ex[:len(ex)-1], ex + digits(1), ex[:len(ex)-1] + digits(1), digits(1) + ex[1:]}
			for _, v := range variants {
				add("+" + cc + v)
				add("00" + cc + " " + v)
				if isHome || rng.IntN(4) == 0 {
					add(cc + v)
					add("0" + v)
					add(v)
					add("+" + cc + "0" + v)
					if len(v) > 4 {
						add("(" + v[:2] + ") " + v[2:4] + "-" + v[4:])
					}
				}
			}
		}
	}
	for i := range 10000 {
		add(strconv.Itoa(i))
		add(fmt.Sprintf("%02d", i%100))
		add(fmt.Sprintf("%03d", i%1000))
		add(fmt.Sprintf("%04d", i))
	}
	prefixes := []string{"", "0", "00", "000", "+", "+0", "+00", homeCC, "0" + homeCC, "00" + homeCC, "+" + homeCC,
		"+" + homeCC + "0", "00" + homeCC + "0", "1", "9", "99", "8", "80", "800", "60", "600", "70", "700", "90", "900", "5", "05", "04", "4"}
	for range 4000 {
		add(prefixes[rng.IntN(len(prefixes))] + digits(1+rng.IntN(13)))
	}
	add("")
	add("+")
	add("++971501234567")
	add("050-123-4567")
	add("05o1234567")
	return out
}
