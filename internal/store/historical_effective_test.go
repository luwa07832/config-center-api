package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func seedHistoricalVersion(t *testing.T, st *Store, ns, env string, version int64, grayTag any, createdAt string) {
	t.Helper()
	if _, err := st.db.Exec(
		`INSERT INTO config_versions (namespace, environment, version, gray_tag, rollback_of, promotion_of, created_at)
		 VALUES (?, ?, ?, ?, NULL, NULL, ?)`, ns, env, version, grayTag, createdAt); err != nil {
		t.Fatalf("seed version %s/%s/%d: %v", ns, env, version, err)
	}
}

func openHistoricalStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestEffectiveVersionAtSelectsLatestFullReleaseAtOrBeforeAsOf(t *testing.T) {
	st := openHistoricalStore(t)
	seedHistoricalVersion(t, st, "payments", "prod", 1, nil, "2026-10-01T10:00:00Z")
	seedHistoricalVersion(t, st, "payments", "prod", 2, "canary", "2026-10-01T10:01:00Z")
	seedHistoricalVersion(t, st, "payments", "prod", 3, nil, "2026-10-01T10:02:00Z")
	seedHistoricalVersion(t, st, "payments", "prod", 4, "canary", "2026-10-01T10:03:00Z")
	// Another scope holds a later full release; it must never leak into this scope.
	seedHistoricalVersion(t, st, "payments", "staging", 1, nil, "2026-10-01T11:00:00Z")

	cases := []struct {
		asOf    string
		want    int64
		wantErr error
	}{
		{"2026-10-01T09:59:59Z", 0, ErrNotFound},
		{"2026-10-01T10:00:00Z", 1, nil},
		{"2026-10-01T10:01:00Z", 1, nil}, // gray release at this second never qualifies
		{"2026-10-01T10:01:59Z", 1, nil},
		{"2026-10-01T10:02:00Z", 3, nil},
		{"2026-10-01T10:03:00Z", 3, nil}, // gray release at this second never qualifies
		{"2026-10-01T23:59:59Z", 3, nil},
	}
	for _, tc := range cases {
		version, err := st.EffectiveVersionAt(context.Background(), "payments", "prod", tc.asOf)
		if !errors.Is(err, tc.wantErr) {
			t.Fatalf("asOf %s: err = %v, want %v", tc.asOf, err, tc.wantErr)
		}
		if tc.wantErr != nil {
			continue
		}
		if version.Version != tc.want {
			t.Fatalf("asOf %s: version = %d, want %d", tc.asOf, version.Version, tc.want)
		}
		if version.GrayTag.Valid {
			t.Fatalf("asOf %s: selected a gray version %+v", tc.asOf, version)
		}
		if version.CreatedAt > tc.asOf {
			t.Fatalf("asOf %s: createdAt %s is after asOf", tc.asOf, version.CreatedAt)
		}
	}
}

func TestEffectiveVersionAtSameSecondPicksHighestVersion(t *testing.T) {
	st := openHistoricalStore(t)
	seedHistoricalVersion(t, st, "svc", "dev", 1, nil, "2026-10-01T10:00:00Z")
	seedHistoricalVersion(t, st, "svc", "dev", 2, nil, "2026-10-01T10:00:00Z")
	seedHistoricalVersion(t, st, "svc", "dev", 3, "canary", "2026-10-01T10:00:00Z")

	version, err := st.EffectiveVersionAt(context.Background(), "svc", "dev", "2026-10-01T10:00:00Z")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if version.Version != 2 {
		t.Fatalf("version = %d, want highest full release 2", version.Version)
	}
}

func TestEffectiveVersionAtGrayOnlyScopeIsNotFound(t *testing.T) {
	st := openHistoricalStore(t)
	seedHistoricalVersion(t, st, "svc", "dev", 1, "canary", "2026-10-01T10:00:00Z")
	seedHistoricalVersion(t, st, "svc", "dev", 2, "gray", "2026-10-01T10:01:00Z")

	if _, err := st.EffectiveVersionAt(context.Background(), "svc", "dev", "2026-10-01T23:59:59Z"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := st.EffectiveVersionAt(context.Background(), "svc", "dev", "2026-10-01T10:00:00Z"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
