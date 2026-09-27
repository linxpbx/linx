package db

import (
	"strings"
	"testing"
)

func TestLoadMigrations(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations found")
	}
	for i, m := range migrations {
		if m.sql == "" {
			t.Errorf("migration %d (%s) has no SQL", m.version, m.name)
		}
		if i > 0 && migrations[i-1].version >= m.version {
			t.Errorf("migrations not strictly ascending: %d then %d", migrations[i-1].version, m.version)
		}
	}
	if migrations[0].version != 1 || migrations[0].name != "tenant_audit_log" {
		t.Errorf("first migration = %+v, want version 1 tenant_audit_log", migrations[0])
	}
}

func TestReferenceScript(t *testing.T) {
	s, err := ReferenceScript(5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "-- 0005_") || strings.Contains(s, "-- 0006_") {
		t.Error("the script doesn't stop at version 5")
	}
	// linx_asterisk already exists on a server running Linx.
	if strings.Contains(s, "\n"+createAsteriskRole) || !strings.Contains(s, "IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'linx_asterisk')") {
		t.Error("0005's role creation isn't guarded")
	}
	if !strings.Contains(s, "INSERT INTO schema_migrations (version, name) VALUES (5,") {
		t.Error("versions aren't recorded")
	}
	if _, err := ReferenceScript(9999); err == nil {
		t.Error("a version this Linx doesn't have was accepted")
	}
}
