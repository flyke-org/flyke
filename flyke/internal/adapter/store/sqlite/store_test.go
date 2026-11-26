package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// A database created before the retry columns existed is upgraded in place.
func TestOpenAddsColumnsToExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile("testdata/schema_v0.sql") // the schema of the first release
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(old)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO runs (id, namespace, workflow, version, inputs, labels, phase, created_at)
		VALUES ('old-run', 'default', 'W', 'v', '{}', '{}', 'Succeeded', 1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, c := range addedColumns {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, c.table, c.column).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s.%s missing (%v)", c.table, c.column, err)
		}
	}
	var r runRepo = runRepo{s.db}
	if got, err := r.GetByID(t.Context(), "old-run"); err != nil || got.Retry != nil {
		t.Fatalf("existing run unreadable after migration: %v %+v", err, got)
	}
	// Opening again is a no-op.
	s.Close()
	if s2, err := Open(path); err != nil {
		t.Fatal(err)
	} else {
		s2.Close()
	}
}
