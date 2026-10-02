package store

import (
	"context"
	"testing"
)

func TestListVersionsPage(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	// A second scope must never be counted or returned.
	publishVersion(t, st, "other", "env", "", mustItems(t, "k", "1"))
	for i := 0; i < 5; i++ {
		publishVersion(t, st, "svc", "dev", "", mustItems(t, "k", "1"))
	}

	page, err := st.ListVersionsPage(ctx, "svc", "dev", 0, 2)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if page.TotalVersions != 5 || !page.HasMore {
		t.Fatalf("first page = %+v, want total 5 hasMore true", page)
	}
	if got := pageVersions(page.Versions); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("versions = %v, want [1 2]", got)
	}

	page, err = st.ListVersionsPage(ctx, "svc", "dev", 2, 2)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if page.TotalVersions != 5 || !page.HasMore {
		t.Fatalf("second page = %+v, want total 5 hasMore true", page)
	}
	if got := pageVersions(page.Versions); len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("versions = %v, want [3 4]", got)
	}

	page, err = st.ListVersionsPage(ctx, "svc", "dev", 4, 2)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if page.TotalVersions != 5 || page.HasMore {
		t.Fatalf("last page = %+v, want total 5 hasMore false", page)
	}
	if got := pageVersions(page.Versions); len(got) != 1 || got[0] != 5 {
		t.Fatalf("versions = %v, want [5]", got)
	}

	// Cursor past the last version yields an empty terminal page.
	page, err = st.ListVersionsPage(ctx, "svc", "dev", 5, 2)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(page.Versions) != 0 || page.HasMore || page.TotalVersions != 5 {
		t.Fatalf("terminal page = %+v", page)
	}

	// An unknown scope reports zero total and no rows.
	page, err = st.ListVersionsPage(ctx, "missing", "env", 0, 10)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(page.Versions) != 0 || page.TotalVersions != 0 || page.HasMore {
		t.Fatalf("unknown scope page = %+v", page)
	}
}

func pageVersions(versions []Version) []int64 {
	numbers := make([]int64, len(versions))
	for i, version := range versions {
		numbers[i] = version.Version
	}
	return numbers
}
