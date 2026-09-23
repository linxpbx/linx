package db

import "testing"

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
