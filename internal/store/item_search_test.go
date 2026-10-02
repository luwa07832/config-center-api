package store

import (
	"context"
	"testing"
)

func TestSearchEffectiveItemEnumeratesScopesIncludingGrayOnly(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	// ns-a/prod: full release carrying the item, then a gray change stays ineffective.
	publishVersion(t, st, "ns-a", "prod", "", mustItems(t, "k", "1"))
	publishVersion(t, st, "ns-a", "prod", "canary", mustItems(t, "k", "2"))
	// ns-b/prod: full release without the item.
	publishVersion(t, st, "ns-b", "prod", "", mustItems(t, "other", "1"))
	// ns-c/prod: gray-only history, no full release.
	publishVersion(t, st, "ns-c", "prod", "canary", mustItems(t, "k", "3"))

	results, err := st.SearchEffectiveItem(ctx, "", "", "k")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results length = %d, want 3 scopes", len(results))
	}
	want := []struct {
		ns       string
		env      string
		version  int64
		hasValue bool
		value    string
	}{
		{"ns-a", "prod", 1, true, "1"},
		{"ns-b", "prod", 1, false, ""},
		{"ns-c", "prod", 0, false, ""},
	}
	for i, expected := range want {
		got := results[i]
		if got.Namespace != expected.ns || got.Environment != expected.env ||
			got.EffectiveVersion != expected.version || got.HasValue != expected.hasValue ||
			got.Value != expected.value {
			t.Fatalf("result %d = %+v, want %+v", i, got, expected)
		}
	}

	// Exact filters narrow the scope set.
	filtered, err := st.SearchEffectiveItem(ctx, "ns-a", "", "k")
	if err != nil || len(filtered) != 1 || filtered[0].Namespace != "ns-a" {
		t.Fatalf("namespace filter = %+v, err = %v", filtered, err)
	}
	filtered, err = st.SearchEffectiveItem(ctx, "ns-a", "staging", "k")
	if err != nil || len(filtered) != 0 {
		t.Fatalf("environment filter = %+v, err = %v", filtered, err)
	}

	// The item name is matched exactly and never trimmed.
	exact, err := st.SearchEffectiveItem(ctx, "", "", " k ")
	if err != nil || len(exact) != 3 {
		t.Fatalf("exact name search = %+v, err = %v", exact, err)
	}
	for _, entry := range exact {
		if entry.HasValue {
			t.Fatalf("whitespace name matched a value: %+v", entry)
		}
	}
}
