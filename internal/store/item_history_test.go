package store

import (
	"context"
	"testing"
)

func TestItemHistoryPairsEveryVersionWithItemState(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", `"1"`, "keep", "1"))
	publishVersion(t, st, "payments", "prod", "canary", mustItems(t, "a", "null", "keep", "1"))
	publishVersion(t, st, "payments", "prod", "", mustItems(t, "keep", "1"))
	if _, err := st.Rollback(ctx, "payments", "prod", 1); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	history, err := st.ItemHistory(ctx, "payments", "prod", "a")
	if err != nil {
		t.Fatalf("item history: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("history length = %d, want 4 (every version including gray and rollback)", len(history))
	}
	want := []struct {
		version int64
		has     bool
		value   string
	}{
		{1, true, `"1"`},
		{2, true, "null"},
		{3, false, ""},
		{4, true, `"1"`},
	}
	for i, wantEntry := range want {
		entry := history[i]
		if entry.Version.Version != wantEntry.version || entry.HasValue != wantEntry.has || entry.Value != wantEntry.value {
			t.Fatalf("entry %d = version %d has %v value %q, want %d/%v/%q",
				i, entry.Version.Version, entry.HasValue, entry.Value,
				wantEntry.version, wantEntry.has, wantEntry.value)
		}
	}
	if history[3].Version.RollbackSource() != 1 {
		t.Fatalf("rollback metadata missing on version 4")
	}

	unknown, err := st.ItemHistory(ctx, "payments", "prod", "missing")
	if err != nil {
		t.Fatalf("unknown item: %v", err)
	}
	if len(unknown) != 4 || unknown[0].HasValue {
		t.Fatalf("unknown item history = %v", unknown)
	}

	empty, err := st.ItemHistory(ctx, "new", "dev", "a")
	if err != nil {
		t.Fatalf("empty scope: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty scope must return no entries, got %d", len(empty))
	}
}
