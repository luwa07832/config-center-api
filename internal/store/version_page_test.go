package store

import (
	"context"
	"testing"
)

func TestListVersionsPageWalksScopeInAscendingKeysetOrder(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		publishVersion(t, st, "payments", "prod", "", mustItems(t, "n", "1"))
	}
	publishVersion(t, st, "payments", "staging", "", mustItems(t, "n", "1"))

	page, err := st.ListVersionsPage(ctx, "payments", "prod", 0, 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if page.Total != 5 || !page.HasMore || len(page.Versions) != 2 {
		t.Fatalf("first page = %+v", page)
	}
	if page.Versions[0].Version != 1 || page.Versions[1].Version != 2 {
		t.Fatalf("first page versions = %d,%d", page.Versions[0].Version, page.Versions[1].Version)
	}

	page, err = st.ListVersionsPage(ctx, "payments", "prod", 2, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if page.Total != 5 || !page.HasMore || len(page.Versions) != 2 {
		t.Fatalf("second page = %+v", page)
	}
	if page.Versions[0].Version != 3 || page.Versions[1].Version != 4 {
		t.Fatalf("second page versions = %d,%d", page.Versions[0].Version, page.Versions[1].Version)
	}

	page, err = st.ListVersionsPage(ctx, "payments", "prod", 4, 2)
	if err != nil {
		t.Fatalf("last page: %v", err)
	}
	if page.Total != 5 || page.HasMore || len(page.Versions) != 1 || page.Versions[0].Version != 5 {
		t.Fatalf("last page = %+v", page)
	}
}

func TestListVersionsPageBeyondMaxReturnsEmptyPageWithTotal(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	publishVersion(t, st, "payments", "prod", "", mustItems(t, "n", "1"))
	publishVersion(t, st, "payments", "prod", "", mustItems(t, "n", "2"))

	page, err := st.ListVersionsPage(ctx, "payments", "prod", 99, 10)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if page.Total != 2 || page.HasMore || len(page.Versions) != 0 {
		t.Fatalf("page = %+v", page)
	}
}

func TestListVersionsPageEmptyScope(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	page, err := st.ListVersionsPage(ctx, "missing", "prod", 0, 10)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if page.Total != 0 || page.HasMore || len(page.Versions) != 0 {
		t.Fatalf("page = %+v", page)
	}
}

func TestListVersionsPageIsStableAcrossNewPublishes(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		publishVersion(t, st, "payments", "prod", "", mustItems(t, "n", "1"))
	}
	before, err := st.ListVersionsPage(ctx, "payments", "prod", 1, 2)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	publishVersion(t, st, "payments", "prod", "", mustItems(t, "n", "2"))
	publishVersion(t, st, "payments", "prod", "", mustItems(t, "n", "3"))
	after, err := st.ListVersionsPage(ctx, "payments", "prod", 1, 2)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if len(before.Versions) != 2 || len(after.Versions) != 2 {
		t.Fatalf("page sizes changed: %d vs %d", len(before.Versions), len(after.Versions))
	}
	for i := range before.Versions {
		if before.Versions[i].Version != after.Versions[i].Version {
			t.Fatalf("page drifted at %d: %d vs %d", i, before.Versions[i].Version, after.Versions[i].Version)
		}
	}
	if after.Total != 5 {
		t.Fatalf("total = %d, want 5", after.Total)
	}
}
