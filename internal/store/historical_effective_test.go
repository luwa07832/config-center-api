package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// seedHistoricalVersions publishes the requested number of versions and then rewrites their
// created_at values so point-in-time queries are deterministic.
func seedHistoricalVersions(t *testing.T, st *Store, ns, env string, fullOrGray ...bool) []Version {
	t.Helper()
	versions := make([]Version, 0, len(fullOrGray))
	for i, full := range fullOrGray {
		tag := "canary"
		if full {
			tag = ""
		}
		v := publishVersion(t, st, ns, env, tag, mustItems(t, "n", intToJSON(i+1)))
		versions = append(versions, v)
	}
	return versions
}

func intToJSON(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func setCreatedAt(t *testing.T, st *Store, ns, env string, version int64, createdAt string) {
	t.Helper()
	if _, err := st.db.Exec(
		`UPDATE config_versions SET created_at = ? WHERE namespace = ? AND environment = ? AND version = ?`,
		createdAt, ns, env, version); err != nil {
		t.Fatalf("set created_at: %v", err)
	}
}

func historicalAt(raw string) time.Time {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		panic(err)
	}
	return t
}

func TestHistoricalEffectiveVersionSelectsLatestFullReleaseAtPointInTime(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	// 1 full, 2 gray, 3 full, 4 gray, 5 full.
	seedHistoricalVersions(t, st, "payments", "prod", true, false, true, false, true)
	setCreatedAt(t, st, "payments", "prod", 1, "2026-10-01T10:00:00Z")
	setCreatedAt(t, st, "payments", "prod", 2, "2026-10-01T10:05:00Z")
	setCreatedAt(t, st, "payments", "prod", 3, "2026-10-01T10:10:00Z")
	setCreatedAt(t, st, "payments", "prod", 4, "2026-10-01T10:15:00Z")
	setCreatedAt(t, st, "payments", "prod", 5, "2026-10-01T10:20:00Z")

	cases := []struct {
		name        string
		asOf        string
		wantVersion int64
		wantValue   string
	}{
		{"before first release", "2026-10-01T09:59:59Z", 0, ""},
		{"exact first full release", "2026-10-01T10:00:00Z", 1, "1"},
		{"during gray only window", "2026-10-01T10:05:30Z", 1, "1"},
		{"after second full release", "2026-10-01T10:10:01Z", 3, "3"},
		{"gray later than full release", "2026-10-01T10:15:00Z", 3, "3"},
		{"after everything", "2027-01-01T00:00:00Z", 5, "5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := st.HistoricalEffectiveVersion(ctx, "payments", "prod", historicalAt(tc.asOf))
			if tc.wantVersion == 0 {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("historical: %v", err)
			}
			if v.Version != tc.wantVersion {
				t.Fatalf("version = %d, want %d", v.Version, tc.wantVersion)
			}
			if v.GrayTag.Valid {
				t.Fatalf("selected version must be a full release: %q", v.GrayTag.String)
			}
			items, err := st.Items(ctx, "payments", "prod", v.Version)
			if err != nil {
				t.Fatalf("items: %v", err)
			}
			if string(items["n"]) != tc.wantValue {
				t.Fatalf("item = %s, want %s", items["n"], tc.wantValue)
			}
		})
	}
}

func TestHistoricalEffectiveVersionSameSecondTieUsesHighestVersion(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	// Full versions 1 and 3 (version 2 is gray) share one creation second.
	seedHistoricalVersions(t, st, "svc", "dev", true, false, true)
	for _, version := range []int64{1, 2, 3} {
		setCreatedAt(t, st, "svc", "dev", version, "2026-10-01T10:00:00Z")
	}

	v, err := st.HistoricalEffectiveVersion(ctx, "svc", "dev", historicalAt("2026-10-01T10:00:00Z"))
	if err != nil {
		t.Fatalf("historical: %v", err)
	}
	if v.Version != 3 {
		t.Fatalf("version = %d, want highest full release 3", v.Version)
	}

	// Fractional asOf within the same second still includes that second.
	v, err = st.HistoricalEffectiveVersion(ctx, "svc", "dev", historicalAt("2026-10-01T10:00:00.250Z"))
	if err != nil {
		t.Fatalf("historical fractional: %v", err)
	}
	if v.Version != 3 {
		t.Fatalf("version = %d, want 3", v.Version)
	}
}

func TestHistoricalEffectiveVersionIsScopedAndGrayOnlyReturnsNotFound(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	seedHistoricalVersions(t, st, "payments", "prod", true)
	setCreatedAt(t, st, "payments", "prod", 1, "2026-10-01T10:00:00Z")

	if _, err := st.HistoricalEffectiveVersion(ctx, "payments", "staging", historicalAt("2027-01-01T00:00:00Z")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other scope err = %v, want ErrNotFound", err)
	}
	if _, err := st.HistoricalEffectiveVersion(ctx, "ghost", "prod", historicalAt("2027-01-01T00:00:00Z")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing namespace err = %v, want ErrNotFound", err)
	}

	publishVersion(t, st, "grayonly", "prod", "canary", mustItems(t, "a", "1"))
	if _, err := st.HistoricalEffectiveVersion(ctx, "grayonly", "prod", historicalAt("2027-01-01T00:00:00Z")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("gray-only scope err = %v, want ErrNotFound", err)
	}
}

func TestHistoricalEffectiveVersionReadsRollbackAndPromotionFullReleases(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", `"v1"`))
	publishVersion(t, st, "payments", "prod", "canary", mustItems(t, "a", `"gray"`))
	promoted, err := st.Promote(ctx, "payments", "prod", 2)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	rolled, err := st.Rollback(ctx, "payments", "prod", 1)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	for _, v := range []int64{1, 2, promoted.Version, rolled.Version} {
		setCreatedAt(t, st, "payments", "prod", v, "2026-10-01T10:00:0"+string(rune('0'+v))+"Z")
	}

	atPromotion := historicalAt("2026-10-01T10:00:03Z")
	v, err := st.HistoricalEffectiveVersion(ctx, "payments", "prod", atPromotion)
	if err != nil {
		t.Fatalf("historical at promotion: %v", err)
	}
	if v.Version != promoted.Version || !v.PromotionOf.Valid || v.PromotionOf.Int64 != 2 {
		t.Fatalf("promoted version = %+v", v)
	}

	atRollback := historicalAt("2026-10-01T10:00:04Z")
	v, err = st.HistoricalEffectiveVersion(ctx, "payments", "prod", atRollback)
	if err != nil {
		t.Fatalf("historical at rollback: %v", err)
	}
	if v.Version != rolled.Version || !v.RollbackOf.Valid || v.RollbackOf.Int64 != 1 {
		t.Fatalf("rollback version = %+v", v)
	}
}
