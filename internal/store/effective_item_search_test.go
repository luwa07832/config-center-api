package store

import (
	"context"
	"testing"
)

func TestSearchEffectiveItemsCoversEveryScopeIncludingGrayOnly(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	publishVersion(t, st, "payments", "prod", "", mustItems(t, "timeout", `"30"`, "keep", "1"))
	publishVersion(t, st, "payments", "prod", "canary", mustItems(t, "timeout", `"99"`))
	publishVersion(t, st, "payments", "staging", "", mustItems(t, "keep", "1"))
	publishVersion(t, st, "billing", "prod", "", mustItems(t, "timeout", "null"))
	publishVersion(t, st, "ops", "dev", "gray-only", mustItems(t, "timeout", `"7"`))

	rows, err := st.SearchEffectiveItems(ctx, "", "", "timeout")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	want := []EffectiveItemSearchRow{
		searchRow("billing", "prod", 1, true, "null"),
		searchRow("ops", "dev", 0, false, ""),
		searchRow("payments", "prod", 1, true, `"30"`),
		searchRow("payments", "staging", 1, false, ""),
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d: %v", len(rows), len(want), rows)
	}
	for i, wantRow := range want {
		if rows[i] != wantRow {
			t.Fatalf("row %d = %+v, want %+v", i, rows[i], wantRow)
		}
	}
}

func TestSearchEffectiveItemsFiltersScopeAndIgnoresGrayValues(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	publishVersion(t, st, "svc", "dev", "", mustItems(t, "a", "1"))
	publishVersion(t, st, "svc", "prod", "", mustItems(t, "a", "2"))
	publishVersion(t, st, "svc", "prod", "canary", mustItems(t, "a", "3"))
	publishVersion(t, st, "other", "prod", "", mustItems(t, "a", "4"))

	byNamespace, err := st.SearchEffectiveItems(ctx, "svc", "", "a")
	if err != nil {
		t.Fatalf("search namespace: %v", err)
	}
	if len(byNamespace) != 2 || byNamespace[0].Environment != "dev" || byNamespace[1].Environment != "prod" {
		t.Fatalf("namespace filter rows = %v", byNamespace)
	}
	if byNamespace[1].Value != "2" {
		t.Fatalf("gray value must not leak into effective state, got %q", byNamespace[1].Value)
	}

	byEnvironment, err := st.SearchEffectiveItems(ctx, "", "prod", "a")
	if err != nil {
		t.Fatalf("search environment: %v", err)
	}
	if len(byEnvironment) != 2 || byEnvironment[0].Namespace != "other" || byEnvironment[1].Namespace != "svc" {
		t.Fatalf("environment filter rows = %v", byEnvironment)
	}

	both, err := st.SearchEffectiveItems(ctx, "svc", "prod", "a")
	if err != nil {
		t.Fatalf("search both: %v", err)
	}
	if len(both) != 1 || both[0].EffectiveVersion != 1 || both[0].Value != "2" {
		t.Fatalf("combined filter rows = %v", both)
	}

	none, err := st.SearchEffectiveItems(ctx, "missing", "", "a")
	if err != nil {
		t.Fatalf("search missing namespace: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("unknown namespace must return no rows, got %v", none)
	}
}

func searchRow(namespace, environment string, effectiveVersion int64, hasValue bool, value string) EffectiveItemSearchRow {
	return EffectiveItemSearchRow{
		Namespace:        namespace,
		Environment:      environment,
		EffectiveVersion: effectiveVersion,
		HasValue:         hasValue,
		Value:            value,
	}
}
