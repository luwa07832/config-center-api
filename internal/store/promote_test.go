package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
)

func TestPromoteCreatesFullReleaseFromGray(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	if _, err := st.Publish(ctx, PublishInput{
		Namespace: "payments", Environment: "prod",
		Items: map[string]json.RawMessage{"a": json.RawMessage(`1`)},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := st.Publish(ctx, PublishInput{
		Namespace: "payments", Environment: "prod", GrayTag: "canary",
		Items: map[string]json.RawMessage{
			"a": json.RawMessage(`1.0`),
			"b": json.RawMessage(`true`),
		},
	}); err != nil {
		t.Fatalf("publish gray: %v", err)
	}

	promoted, err := st.Promote(ctx, "payments", "prod", 2)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if promoted.Version != 3 || promoted.GrayTag.Valid || promoted.RollbackOf.Valid {
		t.Fatalf("promoted metadata = %+v", promoted)
	}
	if !promoted.PromotionOf.Valid || promoted.PromotionOf.Int64 != 2 {
		t.Fatalf("promotionOf = %v, want 2", promoted.PromotionOf)
	}
	items, err := st.Items(ctx, "payments", "prod", 3)
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if string(items["a"]) != "1.0" || string(items["b"]) != "true" {
		t.Fatalf("items = %v", items)
	}
	effective, err := st.EffectiveVersion(ctx, "payments", "prod")
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if effective != 3 {
		t.Fatalf("effective = %d, want 3", effective)
	}

	versions, err := st.ListVersions(ctx, "payments", "prod")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("history length = %d, want 3", len(versions))
	}
	for _, v := range versions[:2] {
		if v.PromotionOf.Valid {
			t.Fatalf("historical version %d must have null promotionOf", v.Version)
		}
	}
	if got := versions[2].GrayTag; got.Valid {
		t.Fatalf("promoted version must carry no gray tag")
	}
}

func TestPromoteErrors(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	if _, err := st.Publish(ctx, PublishInput{
		Namespace: "svc", Environment: "dev",
		Items: map[string]json.RawMessage{"a": json.RawMessage(`1`)},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := st.Publish(ctx, PublishInput{
		Namespace: "svc", Environment: "dev", GrayTag: "canary",
		Items: map[string]json.RawMessage{"a": json.RawMessage(`2`)},
	}); err != nil {
		t.Fatalf("publish gray: %v", err)
	}
	if _, err := st.Publish(ctx, PublishInput{
		Namespace: "other", Environment: "dev",
		Items: map[string]json.RawMessage{"a": json.RawMessage(`1`)},
	}); err != nil {
		t.Fatalf("publish other: %v", err)
	}
	if _, err := st.Publish(ctx, PublishInput{
		Namespace: "other", Environment: "dev",
		Items: map[string]json.RawMessage{"a": json.RawMessage(`2`)},
	}); err != nil {
		t.Fatalf("publish other: %v", err)
	}
	if _, err := st.Publish(ctx, PublishInput{
		Namespace: "other", Environment: "dev",
		Items: map[string]json.RawMessage{"a": json.RawMessage(`3`)},
	}); err != nil {
		t.Fatalf("publish other: %v", err)
	}

	if _, err := st.Promote(ctx, "svc", "dev", 99); err != ErrNotFound {
		t.Fatalf("missing version err = %v, want ErrNotFound", err)
	}
	if _, err := st.Promote(ctx, "svc", "dev", 3); err != ErrScopeMismatch {
		t.Fatalf("scope mismatch err = %v, want ErrScopeMismatch", err)
	}
	if _, err := st.Promote(ctx, "svc", "dev", 1); err != ErrNotGray {
		t.Fatalf("full version err = %v, want ErrNotGray", err)
	}

	versions, err := st.ListVersions(ctx, "svc", "dev")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("failed promotions must leave no versions, got %d", len(versions))
	}
}

func TestPromoteAssignsSequentialVersionsUnderConcurrency(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := st.Publish(ctx, PublishInput{
			Namespace: "svc", Environment: "dev", GrayTag: "canary",
			Items: map[string]json.RawMessage{"i": json.RawMessage(string(rune('0' + i)))},
		}); err != nil {
			t.Fatalf("publish gray: %v", err)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	results := make(chan int64, 3)
	for _, source := range []int64{1, 2, 3} {
		wg.Add(1)
		go func(source int64) {
			defer wg.Done()
			v, err := st.Promote(ctx, "svc", "dev", source)
			if err != nil {
				errs <- err
				return
			}
			results <- v.Version
		}(source)
	}
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Fatalf("concurrent promote: %v", err)
	}
	seen := map[int64]bool{}
	for v := range results {
		if v < 4 || v > 6 || seen[v] {
			t.Fatalf("allocated version = %d, want unique 4..6", v)
		}
		seen[v] = true
	}
	if len(seen) != 3 {
		t.Fatalf("allocated versions = %v, want 4,5,6", seen)
	}
	versions, err := st.ListVersions(ctx, "svc", "dev")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(versions) != 6 {
		t.Fatalf("history length = %d, want 6", len(versions))
	}
}

func TestOpenMigratesLegacyDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := legacy.Exec(`CREATE TABLE service_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE config_versions (
  namespace TEXT NOT NULL, environment TEXT NOT NULL, version INTEGER NOT NULL,
  gray_tag TEXT, rollback_of INTEGER, created_at TEXT NOT NULL,
  PRIMARY KEY (namespace, environment, version)
);
CREATE TABLE config_items (
  namespace TEXT NOT NULL, environment TEXT NOT NULL, version INTEGER NOT NULL,
  name TEXT NOT NULL, value_json TEXT NOT NULL,
  PRIMARY KEY (namespace, environment, version, name)
);
CREATE INDEX idx_config_versions_global ON config_versions(version);
INSERT INTO config_versions (namespace, environment, version, gray_tag, rollback_of, created_at)
  VALUES ('svc', 'dev', 1, NULL, NULL, '2026-01-01T00:00:00Z');
INSERT INTO config_items (namespace, environment, version, name, value_json)
  VALUES ('svc', 'dev', 1, 'a', '1');
`); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("migrate open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	v, err := st.GetVersion(ctx, "svc", "dev", 1)
	if err != nil {
		t.Fatalf("get legacy version: %v", err)
	}
	if v.PromotionOf.Valid {
		t.Fatalf("legacy promotionOf = %v, want null", v.PromotionOf)
	}
	items, err := st.Items(ctx, "svc", "dev", 1)
	if err != nil {
		t.Fatalf("legacy items: %v", err)
	}
	if string(items["a"]) != "1" {
		t.Fatalf("legacy item = %q", items["a"])
	}
	versions, err := st.ListVersions(ctx, "svc", "dev")
	if err != nil || len(versions) != 1 || versions[0].PromotionOf.Valid {
		t.Fatalf("legacy history versions=%d err=%v", len(versions), err)
	}
	if _, err := st.Publish(ctx, PublishInput{
		Namespace: "svc", Environment: "dev", GrayTag: "g",
		Items: map[string]json.RawMessage{"a": json.RawMessage(`2`)},
	}); err != nil {
		t.Fatalf("publish after migrate: %v", err)
	}
	if _, err := st.Promote(ctx, "svc", "dev", 2); err != nil {
		t.Fatalf("promote after migrate: %v", err)
	}
}
