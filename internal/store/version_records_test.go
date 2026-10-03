package store

import (
	"context"
	"encoding/json"
	"testing"
)

func newRecordFixture(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// v1 full release {a:"1", b:true} -> effective until v4
	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", `"1"`, "b", "true"))
	// v2 gray "canary" {a:"2"}
	publishVersion(t, st, "payments", "prod", "canary", mustItems(t, "a", `"2"`))
	// v3 rollback of v1, copies gray tag (none), does not change effective
	if _, err := st.Rollback(context.Background(), "payments", "prod", 1); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	// v4 rollback of v2, copies gray tag "canary", still gray
	if _, err := st.Rollback(context.Background(), "payments", "prod", 2); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	// v5 promotion of v2 -> full release, effective
	if _, err := st.Promote(context.Background(), "payments", "prod", 2); err != nil {
		t.Fatalf("promote: %v", err)
	}
	// other scope must never leak into payments/prod results
	publishVersion(t, st, "payments", "staging", "", mustItems(t, "a", `"9"`))
	publishVersion(t, st, "billing", "prod", "", mustItems(t, "a", `"9"`))
	return st
}

func recordFilter() VersionRecordFilter {
	return VersionRecordFilter{Namespace: "payments", Environment: "prod"}
}

func recordVersions(page VersionRecordPage) []int64 {
	versions := make([]int64, 0, len(page.Records))
	for _, record := range page.Records {
		versions = append(versions, record.Version.Version)
	}
	return versions
}

func TestListVersionRecordsWithoutFiltersReturnsAllAscendingWithItems(t *testing.T) {
	st := newRecordFixture(t)
	page, err := st.ListVersionRecords(context.Background(), recordFilter(), 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 5 || page.HasMore {
		t.Fatalf("page total/hasMore = %d/%v", page.Total, page.HasMore)
	}
	if got := recordVersions(page); len(got) != 5 || got[0] != 1 || got[4] != 5 {
		t.Fatalf("versions = %v", got)
	}
	v1 := page.Records[0]
	if v1.GrayTag.Valid || v1.RollbackOf.Valid || v1.PromotionOf.Valid {
		t.Fatalf("v1 metadata = %+v", v1.Version)
	}
	if string(v1.Items["a"]) != `"1"` || string(v1.Items["b"]) != "true" {
		t.Fatalf("v1 items = %v", v1.Items)
	}
	v5 := page.Records[4]
	if v5.PromotionSource() != 2 {
		t.Fatalf("v5 promotionOf = %d", v5.PromotionSource())
	}
	if string(v5.Items["a"]) != `"2"` {
		t.Fatalf("v5 items = %v", v5.Items)
	}
}

func TestListVersionRecordsGrayTagFilterMatchesExactly(t *testing.T) {
	st := newRecordFixture(t)
	filter := recordFilter()
	filter.GrayTag, filter.HasGrayTag = "canary", true
	page, err := st.ListVersionRecords(context.Background(), filter, 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := recordVersions(page); len(got) != 2 || got[0] != 2 || got[1] != 4 {
		t.Fatalf("versions = %v", got)
	}
	for _, record := range page.Records {
		if !record.GrayTag.Valid || record.GrayTag.String != "canary" {
			t.Fatalf("gray tag = %+v", record.GrayTag)
		}
	}

	filter.GrayTag = "missing"
	page, err = st.ListVersionRecords(context.Background(), filter, 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 0 || len(page.Records) != 0 {
		t.Fatalf("page = %+v", page)
	}
}

func TestListVersionRecordsRollbackOfFilter(t *testing.T) {
	st := newRecordFixture(t)
	filter := recordFilter()
	filter.RollbackOf, filter.HasRollbackOf = 2, true
	page, err := st.ListVersionRecords(context.Background(), filter, 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := recordVersions(page); len(got) != 1 || got[0] != 4 {
		t.Fatalf("versions = %v", got)
	}
}

func TestListVersionRecordsEffectiveFilter(t *testing.T) {
	st := newRecordFixture(t)
	filter := recordFilter()
	filter.Effective, filter.HasEffective = true, true
	page, err := st.ListVersionRecords(context.Background(), filter, 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := recordVersions(page); len(got) != 1 || got[0] != 5 {
		t.Fatalf("effective versions = %v", got)
	}

	filter.Effective = false
	page, err = st.ListVersionRecords(context.Background(), filter, 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 4 {
		t.Fatalf("non-effective total = %d", page.Total)
	}
	if got := recordVersions(page); len(got) != 4 {
		t.Fatalf("non-effective versions = %v", got)
	}
}

func TestListVersionRecordsCombinedFiltersIntersect(t *testing.T) {
	st := newRecordFixture(t)
	filter := recordFilter()
	filter.GrayTag, filter.HasGrayTag = "canary", true
	filter.RollbackOf, filter.HasRollbackOf = 2, true
	page, err := st.ListVersionRecords(context.Background(), filter, 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := recordVersions(page); len(got) != 1 || got[0] != 4 {
		t.Fatalf("versions = %v", got)
	}

	// gray canary + rollbackOf=1 intersects to nothing (v3 is a full release)
	filter.RollbackOf = 1
	page, err = st.ListVersionRecords(context.Background(), filter, 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 0 || len(page.Records) != 0 {
		t.Fatalf("page = %+v", page)
	}
}

func TestListVersionRecordsPaginatesAfterCursor(t *testing.T) {
	st := newRecordFixture(t)
	first, err := st.ListVersionRecords(context.Background(), recordFilter(), 0, 2)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	got := recordVersions(first)
	if first.Total != 5 || !first.HasMore || len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("first page = %+v", first)
	}
	second, err := st.ListVersionRecords(context.Background(), recordFilter(), 2, 2)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	got = recordVersions(second)
	if second.Total != 5 || !second.HasMore || len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("second page = %+v", second)
	}
	third, err := st.ListVersionRecords(context.Background(), recordFilter(), 4, 2)
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	got = recordVersions(third)
	if third.Total != 5 || third.HasMore || len(got) != 1 || got[0] != 5 {
		t.Fatalf("third page = %+v", third)
	}

	// Empty page past the last matching version keeps the caller cursor semantics at the API;
	// the store simply reports zero rows and the unchanged filtered total.
	beyond, err := st.ListVersionRecords(context.Background(), recordFilter(), 99, 2)
	if err != nil {
		t.Fatalf("beyond: %v", err)
	}
	if beyond.Total != 5 || beyond.HasMore || len(beyond.Records) != 0 {
		t.Fatalf("beyond page = %+v", beyond)
	}
}

func TestListVersionRecordsPreservesRawJSONAndEmptySnapshots(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	publishVersion(t, st, "svc", "dev", "", mustItems(t, "n", "1", "f", "1.0", "s", `"1"`, "b", "true", "z", "null"))
	publishVersion(t, st, "svc", "dev", "", map[string]json.RawMessage{})

	page, err := st.ListVersionRecords(context.Background(),
		VersionRecordFilter{Namespace: "svc", Environment: "dev"}, 0, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("records = %d", len(page.Records))
	}
	v1 := page.Records[0].Items
	if string(v1["n"]) != "1" || string(v1["f"]) != "1.0" || string(v1["s"]) != `"1"` ||
		string(v1["b"]) != "true" || string(v1["z"]) != "null" {
		t.Fatalf("v1 items = %v", v1)
	}
	if items := page.Records[1].Items; items == nil || len(items) != 0 {
		t.Fatalf("empty snapshot items = %#v", items)
	}
}

func TestListVersionRecordsEffectiveFalseWhenNoFullRelease(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	publishVersion(t, st, "svc", "dev", "canary", mustItems(t, "n", "1"))

	filter := VersionRecordFilter{Namespace: "svc", Environment: "dev"}
	filter.Effective, filter.HasEffective = true, true
	page, err := st.ListVersionRecords(context.Background(), filter, 0, 10)
	if err != nil {
		t.Fatalf("list effective: %v", err)
	}
	if page.Total != 0 || len(page.Records) != 0 {
		t.Fatalf("effective page = %+v", page)
	}
	filter.Effective = false
	page, err = st.ListVersionRecords(context.Background(), filter, 0, 10)
	if err != nil {
		t.Fatalf("list non-effective: %v", err)
	}
	if page.Total != 1 || len(page.Records) != 1 {
		t.Fatalf("non-effective page = %+v", page)
	}
}
