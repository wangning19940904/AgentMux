package store

import (
	"flag"
	"os"
	"strings"
	"testing"
)

var updatePostgresSchema = flag.Bool("update-postgres-schema", false, "rewrite postgres_schema.sql from the legacy schema translator")

func TestPostgresBaseSchemaGolden(t *testing.T) {
	generated := translatedPostgresBaseSchema()
	if *updatePostgresSchema {
		if err := os.WriteFile("postgres_schema.sql", []byte(generated), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	published, err := os.ReadFile("postgres_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != generated {
		t.Fatal("postgres_schema.sql drifted; run go test ./store -run TestPostgresBaseSchemaGolden -update-postgres-schema")
	}
	if postgresBaseSchema() != generated {
		t.Fatal("embedded PostgreSQL schema differs from its golden file")
	}
}

// translatedPostgresBaseSchema is test-only migration tooling for keeping the
// checked-in PostgreSQL-native schema aligned while the old SQLite reader is
// still supported for offline imports.
func translatedPostgresBaseSchema() string {
	schema := sqliteCoreSchema + "\n" + observationSchema
	schema = strings.ReplaceAll(schema, " BLOB ", " BYTEA ")
	schema = strings.ReplaceAll(schema, " INTEGER", " BIGINT")
	schema = strings.ReplaceAll(schema, " REAL", " DOUBLE PRECISION")
	schema = strings.ReplaceAll(schema, "\tcontroller_id TEXT,\n\tcontroller_id TEXT,\n", "\tcontroller_id TEXT,\n")
	lines := strings.Split(schema, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		if !strings.Contains(line, "json_extract(") {
			filtered = append(filtered, line)
		}
	}
	return strings.Join(filtered, "\n")
}
