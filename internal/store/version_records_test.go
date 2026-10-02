package store

import (
	"context"
	"testing"
)

func recordVersions(page VersionPage) []int64 {
	versions := make([]int64, 0, len(page.Versions))
	for _, version := range page.Versions {
		versions = append(versions, version.Version)
	}
	return versions
}

func setupVersionRecords(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	// payments/prod: v1 full, v2 gray canary, v3 rollback of v1 (full), v4 gray beta,
	// v5 promotion of v4 (full and effective).
	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", "1"))
	publishVersion(t, st, "payments", "prod", "canary", mustItems(t, "a", "2"))
	if _, err := st.Rollback(context.Background(), "payments", "prod", 1); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	publishVersion(t, st, "payments", "prod", "beta", mustItems(t, "a", "4"))
	if _, err := st.Promote(context.Background(), "payments", "prod", 4); err != nil {
		t.Fatalf("promote: %v", err)
	}
	// A second scope proves filtering never crosses namespace or environment.
	publishVersion(t, st, "payments", "staging", "", mustItems(t, "a", "9"))
	publishVersion(t, st, "other", "prod", "canary", mustItems(t, "a", "8"))
	return st
}

func TestListVersionRecordsWithoutFilterReturnsAllScopeVersions(t *testing.T) {
	st := setupVersionRecords(t)
	ctx := context.Background()

	page, err := st.ListVersionRecords(ctx, "payments", "prod", VersionRecordFilter{}, 0, 100)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	got := recordVersions(page)
	if page.Total != 5 || page.HasMore || len(got) != 5 {
		t.Fatalf("page = %+v versions = %v", page, got)
	}
	if want := []int64{1, 2, 3, 4, 5}; !equalInt64(recordVersions(page), want) {
		t.Fatalf("versions = %v, want %v", recordVersions(page), want)
	}
}

func TestListVersionRecordsScopeIsRequired(t *testing.T) {
	st := setupVersionRecords(t)
	ctx := context.Background()

	for _, scope := range []struct{ namespace, environment string }{
		{"missing", "prod"}, {"payments", "missing"},
	} {
		page, err := st.ListVersionRecords(ctx, scope.namespace, scope.environment, VersionRecordFilter{}, 0, 100)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		if page.Total != 0 || page.HasMore || len(page.Versions) != 0 {
			t.Fatalf("page = %+v, want empty", page)
		}
	}
}

func TestListVersionRecordsFiltersByGrayTag(t *testing.T) {
	st := setupVersionRecords(t)
	ctx := context.Background()

	page, err := st.ListVersionRecords(ctx, "payments", "prod",
		VersionRecordFilter{GrayTag: "canary", HasGrayTag: true}, 0, 100)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	got := recordVersions(page)
	if page.Total != 1 || len(got) != 1 || got[0] != 2 {
		t.Fatalf("page = %+v versions = %v", page, got)
	}
}

func TestListVersionRecordsFiltersByRollbackOf(t *testing.T) {
	st := setupVersionRecords(t)
	ctx := context.Background()

	page, err := st.ListVersionRecords(ctx, "payments", "prod",
		VersionRecordFilter{RollbackOf: 1, HasRollbackOf: true}, 0, 100)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	got := recordVersions(page)
	if page.Total != 1 || len(got) != 1 || got[0] != 3 {
		t.Fatalf("page = %+v versions = %v", page, got)
	}
	if got := page.Versions[0].PromotionSource(); got != 0 {
		t.Fatalf("promotionOf = %d, want 0", got)
	}

	// rollbackOf must not match promotion records.
	page, err = st.ListVersionRecords(ctx, "payments", "prod",
		VersionRecordFilter{RollbackOf: 4, HasRollbackOf: true}, 0, 100)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if page.Total != 0 || len(page.Versions) != 0 {
		t.Fatalf("page = %+v, want no rollback-of-4 records", page)
	}
}

func TestListVersionRecordsFiltersByEffective(t *testing.T) {
	st := setupVersionRecords(t)
	ctx := context.Background()

	page, err := st.ListVersionRecords(ctx, "payments", "prod",
		VersionRecordFilter{Effective: true, HasEffective: true}, 0, 100)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	got := recordVersions(page)
	if page.Total != 1 || len(got) != 1 || got[0] != 5 {
		t.Fatalf("page = %+v versions = %v", page, got)
	}

	page, err = st.ListVersionRecords(ctx, "payments", "prod",
		VersionRecordFilter{Effective: false, HasEffective: true}, 0, 100)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	got = recordVersions(page)
	if page.Total != 4 || !equalInt64(got, []int64{1, 2, 3, 4}) {
		t.Fatalf("page = %+v versions = %v", page, got)
	}
}

func TestListVersionRecordsEffectiveFalseInGrayOnlyScopeMatchesAll(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	publishVersion(t, st, "gray", "dev", "canary", mustItems(t, "a", "1"))
	publishVersion(t, st, "gray", "dev", "beta", mustItems(t, "a", "2"))

	page, err := st.ListVersionRecords(ctx, "gray", "dev",
		VersionRecordFilter{Effective: false, HasEffective: true}, 0, 100)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if page.Total != 2 || len(page.Versions) != 2 {
		t.Fatalf("page = %+v", page)
	}

	page, err = st.ListVersionRecords(ctx, "gray", "dev",
		VersionRecordFilter{Effective: true, HasEffective: true}, 0, 100)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if page.Total != 0 || len(page.Versions) != 0 {
		t.Fatalf("page = %+v, want no effective records", page)
	}
}

func TestListVersionRecordsCombinesFiltersAsIntersection(t *testing.T) {
	st := setupVersionRecords(t)
	ctx := context.Background()

	// Gray canary that is not effective: v2.
	page, err := st.ListVersionRecords(ctx, "payments", "prod", VersionRecordFilter{
		GrayTag: "canary", HasGrayTag: true,
		Effective: false, HasEffective: true,
	}, 0, 100)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	got := recordVersions(page)
	if page.Total != 1 || len(got) != 1 || got[0] != 2 {
		t.Fatalf("page = %+v versions = %v", page, got)
	}

	// rollbackOf=1 plus effective=true intersects to nothing.
	page, err = st.ListVersionRecords(ctx, "payments", "prod", VersionRecordFilter{
		RollbackOf: 1, HasRollbackOf: true,
		Effective: true, HasEffective: true,
	}, 0, 100)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if page.Total != 0 || len(page.Versions) != 0 {
		t.Fatalf("page = %+v, want empty intersection", page)
	}
}

func TestListVersionRecordsPaginatesFilteredMatches(t *testing.T) {
	st := setupVersionRecords(t)
	ctx := context.Background()
	filter := VersionRecordFilter{Effective: false, HasEffective: true}

	page, err := st.ListVersionRecords(ctx, "payments", "prod", filter, 0, 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	got := recordVersions(page)
	if page.Total != 4 || !page.HasMore || !equalInt64(got, []int64{1, 2}) {
		t.Fatalf("first page = %+v versions = %v", page, got)
	}

	page, err = st.ListVersionRecords(ctx, "payments", "prod", filter, 2, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	got = recordVersions(page)
	if page.Total != 4 || page.HasMore || !equalInt64(got, []int64{3, 4}) {
		t.Fatalf("second page = %+v versions = %v", page, got)
	}

	page, err = st.ListVersionRecords(ctx, "payments", "prod", filter, 4, 2)
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if page.Total != 4 || page.HasMore || len(page.Versions) != 0 {
		t.Fatalf("third page = %+v, want empty", page)
	}

	// A page size that does not divide the matched count must signal hasMore on the last full page.
	page, err = st.ListVersionRecords(ctx, "payments", "prod", filter, 0, 3)
	if err != nil {
		t.Fatalf("size-three page: %v", err)
	}
	got = recordVersions(page)
	if page.Total != 4 || !page.HasMore || !equalInt64(got, []int64{1, 2, 3}) {
		t.Fatalf("size-three page = %+v versions = %v", page, got)
	}
	page, err = st.ListVersionRecords(ctx, "payments", "prod", filter, 3, 3)
	if err != nil {
		t.Fatalf("tail page: %v", err)
	}
	got = recordVersions(page)
	if page.Total != 4 || page.HasMore || !equalInt64(got, []int64{4}) {
		t.Fatalf("tail page = %+v versions = %v", page, got)
	}
}

func equalInt64(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
