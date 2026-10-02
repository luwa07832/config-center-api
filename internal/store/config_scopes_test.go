package store

import (
	"context"
	"testing"
)

func seedConfigScopes(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	// payments/prod: v1 full, v2 gray, v3 full, v4 rollback of v2 (gray), v5 promotion of v2.
	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", "1", "b", `"x"`))
	publishVersion(t, st, "payments", "prod", "canary", mustItems(t, "c", "true"))
	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", "2"))
	if _, err := st.Rollback(ctx, "payments", "prod", 2); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := st.Promote(ctx, "payments", "prod", 2); err != nil {
		t.Fatalf("promote: %v", err)
	}
	// payments/staging: a single gray release, so no full effective version exists.
	publishVersion(t, st, "payments", "staging", "beta", mustItems(t, "a", "1"))
	// alpha/prod: one full release with two items.
	publishVersion(t, st, "alpha", "prod", "", mustItems(t, "x", "1", "y", "2"))
}

func scopeKey(scope ConfigScope) string {
	return scope.Namespace + "/" + scope.Environment
}

func TestListConfigScopesAggregatesCountersAndEffectiveItems(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	seedConfigScopes(t, st)

	page, err := st.ListConfigScopes(context.Background(), ConfigScopeFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.MatchedCount != 3 {
		t.Fatalf("matched = %d, want 3", page.MatchedCount)
	}
	if page.HasMore {
		t.Fatalf("hasMore = true, want false")
	}
	if len(page.Scopes) != 3 {
		t.Fatalf("scopes = %d, want 3", len(page.Scopes))
	}

	got := map[string]ConfigScope{}
	for _, scope := range page.Scopes {
		got[scopeKey(scope)] = scope
	}

	prod := got["payments/prod"]
	if prod.VersionCount != 5 || prod.LatestVersion.Version != 5 {
		t.Fatalf("prod versions = %d latest = %d", prod.VersionCount, prod.LatestVersion.Version)
	}
	if prod.EffectiveVersion != 5 {
		t.Fatalf("prod effective = %d, want 5", prod.EffectiveVersion)
	}
	if prod.EffectiveItemCount != 1 {
		t.Fatalf("prod effective items = %d, want 1", prod.EffectiveItemCount)
	}
	if prod.GrayVersionCount != 2 {
		t.Fatalf("prod gray = %d, want 2", prod.GrayVersionCount)
	}
	if prod.RollbackVersionCount != 1 {
		t.Fatalf("prod rollback = %d, want 1", prod.RollbackVersionCount)
	}
	if prod.PromotionVersionCount != 1 {
		t.Fatalf("prod promotion = %d, want 1", prod.PromotionVersionCount)
	}
	if !prod.LatestVersion.PromotionOf.Valid || prod.LatestVersion.PromotionOf.Int64 != 2 {
		t.Fatalf("latest metadata = %+v", prod.LatestVersion)
	}

	staging := got["payments/staging"]
	if staging.VersionCount != 1 || staging.LatestVersion.Version != 1 {
		t.Fatalf("staging versions = %d latest = %d", staging.VersionCount, staging.LatestVersion.Version)
	}
	if staging.EffectiveVersion != 0 || staging.EffectiveItemCount != 0 {
		t.Fatalf("staging effective = %d items = %d, want 0/0",
			staging.EffectiveVersion, staging.EffectiveItemCount)
	}
	if staging.GrayVersionCount != 1 || staging.RollbackVersionCount != 0 || staging.PromotionVersionCount != 0 {
		t.Fatalf("staging counters = %+v", staging)
	}
	if !staging.LatestVersion.GrayTag.Valid || staging.LatestVersion.GrayTag.String != "beta" {
		t.Fatalf("staging latest gray tag = %+v", staging.LatestVersion.GrayTag)
	}

	alpha := got["alpha/prod"]
	if alpha.EffectiveVersion != 1 || alpha.EffectiveItemCount != 2 {
		t.Fatalf("alpha effective = %d items = %d, want 1/2",
			alpha.EffectiveVersion, alpha.EffectiveItemCount)
	}
}

func TestListConfigScopesOrdersAscending(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	seedConfigScopes(t, st)

	page, err := st.ListConfigScopes(context.Background(), ConfigScopeFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"alpha/prod", "payments/prod", "payments/staging"}
	for i, scope := range page.Scopes {
		if key := scopeKey(scope); key != want[i] {
			t.Fatalf("position %d = %s, want %s", i, key, want[i])
		}
	}
}

func TestListConfigScopesAppliesFilters(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	seedConfigScopes(t, st)
	ctx := context.Background()

	withEffective, err := st.ListConfigScopes(ctx, ConfigScopeFilter{HasEffective: boolPtr(true), Limit: 100})
	if err != nil {
		t.Fatalf("hasEffective=true: %v", err)
	}
	if withEffective.MatchedCount != 2 || len(withEffective.Scopes) != 2 {
		t.Fatalf("with effective = matched %d page %d", withEffective.MatchedCount, len(withEffective.Scopes))
	}
	for _, scope := range withEffective.Scopes {
		if scope.EffectiveVersion == 0 {
			t.Fatalf("%s/%s should have an effective version", scope.Namespace, scope.Environment)
		}
	}

	withoutEffective, err := st.ListConfigScopes(ctx, ConfigScopeFilter{HasEffective: boolPtr(false), Limit: 100})
	if err != nil {
		t.Fatalf("hasEffective=false: %v", err)
	}
	if withoutEffective.MatchedCount != 1 || len(withoutEffective.Scopes) != 1 ||
		scopeKey(withoutEffective.Scopes[0]) != "payments/staging" {
		t.Fatalf("without effective = %+v", withoutEffective)
	}

	byNamespace, err := st.ListConfigScopes(ctx, ConfigScopeFilter{Namespace: "payments", Limit: 100})
	if err != nil {
		t.Fatalf("namespace filter: %v", err)
	}
	if byNamespace.MatchedCount != 2 {
		t.Fatalf("namespace matched = %d, want 2", byNamespace.MatchedCount)
	}

	byEnvironment, err := st.ListConfigScopes(ctx, ConfigScopeFilter{Environment: "staging", Limit: 100})
	if err != nil {
		t.Fatalf("environment filter: %v", err)
	}
	if byEnvironment.MatchedCount != 1 || scopeKey(byEnvironment.Scopes[0]) != "payments/staging" {
		t.Fatalf("environment matched = %+v", byEnvironment)
	}
}

func TestListConfigScopesPaginatesWithKeysetCursor(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	seedConfigScopes(t, st)
	ctx := context.Background()

	first, err := st.ListConfigScopes(ctx, ConfigScopeFilter{Limit: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.MatchedCount != 3 || !first.HasMore || len(first.Scopes) != 1 ||
		scopeKey(first.Scopes[0]) != "alpha/prod" {
		t.Fatalf("first page = %+v", first)
	}

	second, err := st.ListConfigScopes(ctx, ConfigScopeFilter{
		Limit:            1,
		AfterNamespace:   first.Scopes[0].Namespace,
		AfterEnvironment: first.Scopes[0].Environment,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if second.MatchedCount != 3 || !second.HasMore || len(second.Scopes) != 1 ||
		scopeKey(second.Scopes[0]) != "payments/prod" {
		t.Fatalf("second page = %+v", second)
	}

	third, err := st.ListConfigScopes(ctx, ConfigScopeFilter{
		Limit:            1,
		AfterNamespace:   second.Scopes[0].Namespace,
		AfterEnvironment: second.Scopes[0].Environment,
	})
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if third.MatchedCount != 3 || third.HasMore || len(third.Scopes) != 1 ||
		scopeKey(third.Scopes[0]) != "payments/staging" {
		t.Fatalf("third page = %+v", third)
	}
}

func TestListConfigScopesEmptyStoreReturnsEmptyPage(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	page, err := st.ListConfigScopes(context.Background(), ConfigScopeFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.MatchedCount != 0 || page.HasMore || len(page.Scopes) != 0 {
		t.Fatalf("empty page = %+v", page)
	}
}

func boolPtr(value bool) *bool { return &value }
