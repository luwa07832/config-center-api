package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesLegacyDatabaseAndPromotionOfIsNull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	_, err = legacy.Exec(`
CREATE TABLE service_metadata (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE config_versions (
	namespace   TEXT NOT NULL,
	environment TEXT NOT NULL,
	version     INTEGER NOT NULL,
	gray_tag    TEXT,
	rollback_of INTEGER,
	created_at  TEXT NOT NULL,
	PRIMARY KEY (namespace, environment, version),
	FOREIGN KEY (namespace, environment, rollback_of)
		REFERENCES config_versions(namespace, environment, version)
);
CREATE TABLE config_items (
	namespace   TEXT NOT NULL,
	environment TEXT NOT NULL,
	version     INTEGER NOT NULL,
	name        TEXT NOT NULL,
	value_json  TEXT NOT NULL,
	PRIMARY KEY (namespace, environment, version, name),
	FOREIGN KEY (namespace, environment, version)
		REFERENCES config_versions(namespace, environment, version)
);
CREATE INDEX idx_config_versions_global ON config_versions(version);
INSERT INTO config_versions (namespace, environment, version, gray_tag, rollback_of, created_at)
	VALUES ('legacy', 'prod', 1, NULL, NULL, '2026-01-01T00:00:00Z');
INSERT INTO config_items (namespace, environment, version, name, value_json)
	VALUES ('legacy', 'prod', 1, 'a', '"1"');
`)
	if err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	v, err := st.GetVersion(ctx, "legacy", "prod", 1)
	if err != nil {
		t.Fatalf("read legacy version: %v", err)
	}
	if v.PromotionOf.Valid {
		t.Fatalf("legacy version promotionOf = %d, want null", v.PromotionOf.Int64)
	}
	if v.RollbackOf.Valid {
		t.Fatalf("legacy rollbackOf must stay null")
	}
	items, err := st.Items(ctx, "legacy", "prod", 1)
	if err != nil {
		t.Fatalf("read legacy items: %v", err)
	}
	if string(items["a"]) != `"1"` {
		t.Fatalf("legacy item = %s", items["a"])
	}

	promoted, err := st.Promote(ctx, "legacy", "prod", 1)
	if err == nil {
		t.Fatalf("promoting a legacy full release must fail, got %+v", promoted)
	}
	publishVersion(t, st, "legacy", "prod", "canary", mustItems(t, "a", `"2"`))
	promoted, err = st.Promote(ctx, "legacy", "prod", 2)
	if err != nil {
		t.Fatalf("promote on migrated db: %v", err)
	}
	if promoted.Version != 3 || promoted.PromotionSource() != 2 {
		t.Fatalf("promoted = %d source %d", promoted.Version, promoted.PromotionSource())
	}
	effective, err := st.EffectiveVersion(ctx, "legacy", "prod")
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if effective != 3 {
		t.Fatalf("effective = %d, want 3", effective)
	}
}
