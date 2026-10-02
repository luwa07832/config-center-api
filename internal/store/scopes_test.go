package store

import (
	"context"
	"testing"
)

func scopeMap(page ScopePage) map[[2]string]ScopeSummary {
	result := map[[2]string]ScopeSummary{}
	for _, scope := range page.Scopes {
		result[[2]string{scope.Namespace, scope.Environment}] = scope
	}
	return result
}

func TestListScopesAggregatesCountersAcrossScopes(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	// payments/prod: full v1 (2 items), gray v2 (3 items), rollback of v1 as v3, promotion of v2 as v4.
	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", "1", "b", "2"))
	publishVersion(t, st, "payments", "prod", "canary", mustItems(t, "a", "9", "b", "2", "c", "3"))
	if _, err := st.Rollback(ctx, "payments", "prod", 1); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := st.Promote(ctx, "payments", "prod", 2); err != nil {
		t.Fatalf("promote: %v", err)
	}
	// payments/staging: gray-only history, no full release.
	publishVersion(t, st, "payments", "staging", "canary", mustItems(t, "a", "1"))
	publishVersion(t, st, "payments", "staging", "beta", mustItems(t, "a", "2"))
	// alpha/prod: single empty full release.
	publishVersion(t, st, "alpha", "prod", "", mustItems(t))

	page, err := st.ListScopes(ctx, ScopeFilter{}, "", "", 100)
	if err != nil {
		t.Fatalf("list scopes: %v", err)
	}
	if page.Total != 3 || len(page.Scopes) != 3 || page.HasMore {
		t.Fatalf("page = %+v", page)
	}
	gotOrder := [][2]string{}
	for _, scope := range page.Scopes {
		gotOrder = append(gotOrder, [2]string{scope.Namespace, scope.Environment})
	}
	wantOrder := [][2]string{
		{"alpha", "prod"},
		{"payments", "prod"},
		{"payments", "staging"},
	}
	for i, want := range wantOrder {
		if gotOrder[i] != want {
			t.Fatalf("order = %v, want %v", gotOrder, wantOrder)
		}
	}

	byScope := scopeMap(page)
	prod := byScope[[2]string{"payments", "prod"}]
	if prod.VersionCount != 4 || prod.LatestVersion != 4 || prod.EffectiveVersion != 4 {
		t.Fatalf("prod counters = %+v", prod)
	}
	if prod.EffectiveItemCount != 3 {
		t.Fatalf("effective item count = %d, want items of promoted snapshot (3)", prod.EffectiveItemCount)
	}
	if prod.GrayVersionCount != 1 || prod.RollbackVersionCount != 1 || prod.PromotionVersionCount != 1 {
		t.Fatalf("history counters = %+v", prod)
	}
	if prod.Latest.Version != 4 || prod.Latest.PromotionSource() != 2 || prod.Latest.EffectiveGrayTag() != "" {
		t.Fatalf("latest metadata = %+v", prod.Latest)
	}

	staging := byScope[[2]string{"payments", "staging"}]
	if staging.VersionCount != 2 || staging.LatestVersion != 2 || staging.EffectiveVersion != 0 {
		t.Fatalf("staging counters = %+v", staging)
	}
	if staging.EffectiveItemCount != 0 || staging.GrayVersionCount != 2 ||
		staging.RollbackVersionCount != 0 || staging.PromotionVersionCount != 0 {
		t.Fatalf("staging counters = %+v", staging)
	}
	if staging.Latest.Version != 2 || staging.Latest.EffectiveGrayTag() != "beta" {
		t.Fatalf("staging latest metadata = %+v", staging.Latest)
	}

	alpha := byScope[[2]string{"alpha", "prod"}]
	if alpha.VersionCount != 1 || alpha.LatestVersion != 1 || alpha.EffectiveVersion != 1 ||
		alpha.EffectiveItemCount != 0 {
		t.Fatalf("alpha counters = %+v", alpha)
	}
}

func TestListScopesAppliesNamespaceEnvironmentFilters(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", "1"))
	publishVersion(t, st, "payments", "staging", "", mustItems(t, "a", "1"))
	publishVersion(t, st, "billing", "prod", "", mustItems(t, "a", "1"))

	page, err := st.ListScopes(ctx, ScopeFilter{Namespace: "payments"}, "", "", 100)
	if err != nil {
		t.Fatalf("namespace filter: %v", err)
	}
	if page.Total != 2 || len(page.Scopes) != 2 {
		t.Fatalf("page = %+v", page)
	}

	page, err = st.ListScopes(ctx, ScopeFilter{Namespace: "payments", Environment: "prod"}, "", "", 100)
	if err != nil {
		t.Fatalf("scope filter: %v", err)
	}
	if page.Total != 1 || len(page.Scopes) != 1 {
		t.Fatalf("page = %+v", page)
	}
	if page.Scopes[0].Namespace != "payments" || page.Scopes[0].Environment != "prod" {
		t.Fatalf("scope = %+v", page.Scopes[0])
	}

	page, err = st.ListScopes(ctx, ScopeFilter{Namespace: "missing"}, "", "", 100)
	if err != nil {
		t.Fatalf("missing filter: %v", err)
	}
	if page.Total != 0 || len(page.Scopes) != 0 || page.HasMore {
		t.Fatalf("missing page = %+v", page)
	}
}

func TestListScopesFiltersByEffectiveState(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	publishVersion(t, st, "full", "env", "", mustItems(t, "a", "1"))
	publishVersion(t, st, "gray", "env", "canary", mustItems(t, "a", "1"))

	withEffective, err := st.ListScopes(ctx, ScopeFilter{HasEffective: true, HasEffectiveFilter: true}, "", "", 100)
	if err != nil {
		t.Fatalf("has effective: %v", err)
	}
	if withEffective.Total != 1 || len(withEffective.Scopes) != 1 ||
		withEffective.Scopes[0].Namespace != "full" {
		t.Fatalf("withEffective = %+v", withEffective)
	}

	withoutEffective, err := st.ListScopes(ctx, ScopeFilter{HasEffective: false, HasEffectiveFilter: true}, "", "", 100)
	if err != nil {
		t.Fatalf("without effective: %v", err)
	}
	if withoutEffective.Total != 1 || len(withoutEffective.Scopes) != 1 ||
		withoutEffective.Scopes[0].Namespace != "gray" {
		t.Fatalf("withoutEffective = %+v", withoutEffective)
	}
}

func TestListScopesPaginatesWithCompositeKeysetCursor(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	scopes := [][2]string{
		{"alpha", "env"},
		{"bravo", "env"},
		{"charlie", "env"},
		{"delta", "env"},
		{"echo", "env"},
	}
	for _, scope := range scopes {
		publishVersion(t, st, scope[0], scope[1], "", mustItems(t, "a", "1"))
	}

	first, err := st.ListScopes(ctx, ScopeFilter{}, "", "", 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.Total != 5 || !first.HasMore || len(first.Scopes) != 2 {
		t.Fatalf("first = %+v", first)
	}
	if first.Scopes[0].Namespace != "alpha" || first.Scopes[1].Namespace != "bravo" {
		t.Fatalf("first scopes = %+v", first.Scopes)
	}

	second, err := st.ListScopes(ctx, ScopeFilter{}, "bravo", "env", 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if second.Total != 5 || !second.HasMore || len(second.Scopes) != 2 {
		t.Fatalf("second = %+v", second)
	}
	if second.Scopes[0].Namespace != "charlie" || second.Scopes[1].Namespace != "delta" {
		t.Fatalf("second scopes = %+v", second.Scopes)
	}

	last, err := st.ListScopes(ctx, ScopeFilter{}, "delta", "env", 2)
	if err != nil {
		t.Fatalf("last page: %v", err)
	}
	if last.Total != 5 || last.HasMore || len(last.Scopes) != 1 || last.Scopes[0].Namespace != "echo" {
		t.Fatalf("last = %+v", last)
	}

	beyond, err := st.ListScopes(ctx, ScopeFilter{}, "echo", "env", 2)
	if err != nil {
		t.Fatalf("beyond page: %v", err)
	}
	if beyond.Total != 5 || beyond.HasMore || len(beyond.Scopes) != 0 {
		t.Fatalf("beyond = %+v", beyond)
	}
}

func TestListScopesEmptyDatabaseReturnsEmptyPage(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	page, err := st.ListScopes(context.Background(), ScopeFilter{}, "", "", 100)
	if err != nil {
		t.Fatalf("list scopes: %v", err)
	}
	if page.Total != 0 || len(page.Scopes) != 0 || page.HasMore {
		t.Fatalf("page = %+v", page)
	}
}
