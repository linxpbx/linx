package main

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/settings"
)

// The methods below extend fakeStore to also back internal/settings.Service
// in tests (internal/store's real implementation is tested against
// Postgres in make test-docker).

func defaultSettings() *settings.Settings {
	return &settings.Settings{
		Country: "AE", ExtensionDigits: 3, SiteKind: "", SimpleMode: true, SetupStep: 1,
		ExtensionRanges: []settings.Range{
			{Kind: settings.RangePeople, From: 100, To: 599},
			{Kind: settings.RangeGroups, From: 600, To: 699},
			{Kind: settings.RangeReserved, From: 700, To: 899},
		},
	}
}

func (f *fakeStore) Settings(context.Context) (settings.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.settings == nil {
		f.settings = defaultSettings()
	}
	return *f.settings, nil
}

func (f *fakeStore) UpdateSettings(_ context.Context, s settings.Settings, _ auth.AuditEntry) (settings.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.settings == nil {
		f.settings = defaultSettings()
	}
	s.DefaultCallPermissionLevelID = f.settings.DefaultCallPermissionLevelID
	s.SetupStep, s.SetupCompletedAt = f.settings.SetupStep, f.settings.SetupCompletedAt
	f.settings = &s
	return s, nil
}

func (f *fakeStore) SetDefaultCallPermissionLevel(_ context.Context, id uuid.UUID, _ auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.settings == nil {
		f.settings = defaultSettings()
	}
	f.settings.DefaultCallPermissionLevelID = &id
	return nil
}

func (f *fakeStore) SetSetupStep(_ context.Context, step int, completedAt *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.settings == nil {
		f.settings = defaultSettings()
	}
	f.settings.SetupStep = step
	if f.settings.SetupCompletedAt == nil {
		f.settings.SetupCompletedAt = completedAt
	}
	return nil
}

func (f *fakeStore) NextFreeExtensionNumber(_ context.Context, tenant uuid.UUID, _ string, digits, from, to int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	used := map[string]bool{}
	for _, e := range f.extensions {
		if e.TenantID == tenant && e.DeletedAt == nil {
			used[e.Number] = true
		}
	}
	for n := from; n <= to; n++ {
		s := zeroPad(n, digits)
		if !used[s] && reservedNumber(s) == nil {
			return s, nil
		}
	}
	return "", nil
}

// reservedNumbers stands in for numbering_extension_clash, for the same two
// UAE cases fakepbx_test.go's reservedNumber checks.
func reservedInRange(digits, from, to int) (string, string) {
	for n := from; n <= to; n++ {
		s := zeroPad(n, digits)
		if strings.HasPrefix(s, "0") {
			return s, "national_prefix"
		}
	}
	return "", ""
}

func (f *fakeStore) RangeReservedNumber(_ context.Context, _ string, digits, from, to int) (string, string, error) {
	n, reason := reservedInRange(digits, from, to)
	return n, reason, nil
}

func (f *fakeStore) ExtensionsWithOtherLength(_ context.Context, tenant uuid.UUID, digits int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.extensions {
		if e.TenantID == tenant && e.DeletedAt == nil && len(e.Number) != digits {
			out = append(out, e.Number)
		}
	}
	return out, nil
}

func (f *fakeStore) ExtensionsOutsideRanges(_ context.Context, tenant uuid.UUID, ranges []settings.Range) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.extensions {
		if e.TenantID != tenant || e.DeletedAt != nil {
			continue
		}
		v, err := strconv.Atoi(e.Number)
		if err != nil {
			continue
		}
		inside := false
		for _, r := range ranges {
			if v >= r.From && v <= r.To {
				inside = true
				break
			}
		}
		if !inside {
			out = append(out, e.Number)
		}
	}
	return out, nil
}

func zeroPad(n, digits int) string {
	s := strconv.Itoa(n)
	for len(s) < digits {
		s = "0" + s
	}
	return s
}
