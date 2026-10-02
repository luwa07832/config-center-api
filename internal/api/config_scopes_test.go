package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

type rawConfigScope struct {
	Namespace             string      `json:"namespace"`
	Environment           string      `json:"environment"`
	VersionCount          int64       `json:"versionCount"`
	LatestVersion         VersionInfo `json:"latestVersion"`
	EffectiveVersion      *int64      `json:"effectiveVersion"`
	EffectiveItemCount    int64       `json:"effectiveItemCount"`
	GrayVersionCount      int64       `json:"grayVersionCount"`
	RollbackVersionCount  int64       `json:"rollbackVersionCount"`
	PromotionVersionCount int64       `json:"promotionVersionCount"`
}

type rawConfigScopes struct {
	MatchedCount         int              `json:"matchedCount"`
	Scopes               []rawConfigScope `json:"scopes"`
	HasMore              bool             `json:"hasMore"`
	NextAfterNamespace   *string          `json:"nextAfterNamespace"`
	NextAfterEnvironment *string          `json:"nextAfterEnvironment"`
}

func configScopesPath(query url.Values) string {
	if len(query) == 0 {
		return "/config-scopes"
	}
	return "/config-scopes?" + query.Encode()
}

func decodeConfigScopes(t *testing.T, recorder *httptest.ResponseRecorder) rawConfigScopes {
	t.Helper()
	var body rawConfigScopes
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return body
}

func scopeIdentity(scope rawConfigScope) string {
	return scope.Namespace + "/" + scope.Environment
}

func seedScopes(t *testing.T, handler http.Handler) {
	t.Helper()
	// payments/prod: full v1, gray v2, full v3, gray-rollback v4, promoted v5.
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 1, "b": "x"})
	publish(t, handler, "payments", "prod", "canary", map[string]any{"c": true})
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 2})
	rollback(t, handler, "payments", "prod", 2)
	promote(t, handler, "payments", "prod", 2)
	// payments/staging: gray-only scope without a full effective version.
	publish(t, handler, "payments", "staging", "beta", map[string]any{"a": 1})
	// alpha/prod: full release with two items.
	publish(t, handler, "alpha", "prod", "", map[string]any{"x": 1, "y": 2})
}

func rollback(t *testing.T, handler http.Handler, ns, env string, version int) {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodPost,
		"/namespaces/"+ns+"/environments/"+env+"/config-versions/"+itoa(version)+"/rollback", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("rollback status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func promote(t *testing.T, handler http.Handler, ns, env string, version int) {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodPost,
		"/namespaces/"+ns+"/environments/"+env+"/config-versions/"+itoa(version)+"/promote", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("promote status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func TestConfigScopesListsAggregatedScopes(t *testing.T) {
	_, handler := newTestRouter(t)
	seedScopes(t, handler)

	recorder := doRequest(t, handler, http.MethodGet, configScopesPath(nil), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeConfigScopes(t, recorder)
	if body.MatchedCount != 3 || body.HasMore || len(body.Scopes) != 3 {
		t.Fatalf("body = %+v", body)
	}
	if body.NextAfterNamespace != nil || body.NextAfterEnvironment != nil {
		t.Fatalf("cursor must be null on final page: %+v", body)
	}
	want := []string{"alpha/prod", "payments/prod", "payments/staging"}
	for i, scope := range body.Scopes {
		if identity := scopeIdentity(scope); identity != want[i] {
			t.Fatalf("position %d = %s, want %s", i, identity, want[i])
		}
	}

	byIdentity := map[string]rawConfigScope{}
	for _, scope := range body.Scopes {
		byIdentity[scopeIdentity(scope)] = scope
	}
	prod := byIdentity["payments/prod"]
	if prod.VersionCount != 5 || prod.LatestVersion.Version != 5 {
		t.Fatalf("prod = %+v", prod)
	}
	if prod.EffectiveVersion == nil || *prod.EffectiveVersion != 5 {
		t.Fatalf("prod effective = %v, want 5", prod.EffectiveVersion)
	}
	if prod.EffectiveItemCount != 1 || prod.GrayVersionCount != 2 ||
		prod.RollbackVersionCount != 1 || prod.PromotionVersionCount != 1 {
		t.Fatalf("prod counters = %+v", prod)
	}
	if !prod.LatestVersion.Effective {
		t.Fatalf("latest version should be effective: %+v", prod.LatestVersion)
	}
	if prod.LatestVersion.PromotionOf == nil || *prod.LatestVersion.PromotionOf != 2 {
		t.Fatalf("latest promotionOf = %v, want 2", prod.LatestVersion.PromotionOf)
	}

	staging := byIdentity["payments/staging"]
	if staging.EffectiveVersion != nil || staging.EffectiveItemCount != 0 {
		t.Fatalf("staging effective = %v items = %d", staging.EffectiveVersion, staging.EffectiveItemCount)
	}
	if staging.GrayVersionCount != 1 {
		t.Fatalf("staging gray = %d, want 1", staging.GrayVersionCount)
	}
	if staging.LatestVersion.GrayTag == nil || *staging.LatestVersion.GrayTag != "beta" {
		t.Fatalf("staging grayTag = %v", staging.LatestVersion.GrayTag)
	}

	alpha := byIdentity["alpha/prod"]
	if alpha.EffectiveVersion == nil || *alpha.EffectiveVersion != 1 || alpha.EffectiveItemCount != 2 {
		t.Fatalf("alpha = %+v", alpha)
	}
}

func TestConfigScopesFiltersAndWalksPages(t *testing.T) {
	_, handler := newTestRouter(t)
	seedScopes(t, handler)

	effective := url.Values{}
	effective.Set("hasEffective", "true")
	recorder := doRequest(t, handler, http.MethodGet, configScopesPath(effective), nil)
	body := decodeConfigScopes(t, recorder)
	if body.MatchedCount != 2 || len(body.Scopes) != 2 {
		t.Fatalf("hasEffective=true body = %+v", body)
	}

	noEffective := url.Values{}
	noEffective.Set("hasEffective", "false")
	recorder = doRequest(t, handler, http.MethodGet, configScopesPath(noEffective), nil)
	body = decodeConfigScopes(t, recorder)
	if body.MatchedCount != 1 || len(body.Scopes) != 1 || scopeIdentity(body.Scopes[0]) != "payments/staging" {
		t.Fatalf("hasEffective=false body = %+v", body)
	}

	filtered := url.Values{}
	filtered.Set("namespace", "payments")
	filtered.Set("environment", "prod")
	recorder = doRequest(t, handler, http.MethodGet, configScopesPath(filtered), nil)
	body = decodeConfigScopes(t, recorder)
	if body.MatchedCount != 1 || scopeIdentity(body.Scopes[0]) != "payments/prod" {
		t.Fatalf("filtered body = %+v", body)
	}

	// Walk every page with limit=1; matchedCount stays the unfiltered total before the cursor.
	var walked []string
	cursor := url.Values{}
	for page := 0; ; page++ {
		query := url.Values{}
		query.Set("limit", "1")
		if cursor.Get("afterNamespace") != "" {
			query.Set("afterNamespace", cursor.Get("afterNamespace"))
			query.Set("afterEnvironment", cursor.Get("afterEnvironment"))
		}
		recorder = doRequest(t, handler, http.MethodGet, configScopesPath(query), nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("page %d status = %d body = %s", page, recorder.Code, recorder.Body.String())
		}
		pageBody := decodeConfigScopes(t, recorder)
		if pageBody.MatchedCount != 3 {
			t.Fatalf("page %d matched = %d, want 3", page, pageBody.MatchedCount)
		}
		if len(pageBody.Scopes) != 1 {
			t.Fatalf("page %d scopes = %d", page, len(pageBody.Scopes))
		}
		walked = append(walked, scopeIdentity(pageBody.Scopes[0]))
		if !pageBody.HasMore {
			if pageBody.NextAfterNamespace != nil || pageBody.NextAfterEnvironment != nil {
				t.Fatalf("final page cursor must be null: %+v", pageBody)
			}
			break
		}
		if pageBody.NextAfterNamespace == nil || pageBody.NextAfterEnvironment == nil {
			t.Fatalf("page %d missing cursor", page)
		}
		cursor.Set("afterNamespace", *pageBody.NextAfterNamespace)
		cursor.Set("afterEnvironment", *pageBody.NextAfterEnvironment)
		if page > 4 {
			t.Fatalf("pagination did not terminate")
		}
	}
	want := []string{"alpha/prod", "payments/prod", "payments/staging"}
	for i, identity := range walked {
		if identity != want[i] {
			t.Fatalf("walked position %d = %s, want %s", i, identity, want[i])
		}
	}
}

func TestConfigScopesEmptyMatchReturnsEmptyShape(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 1})

	query := url.Values{}
	query.Set("namespace", "missing")
	recorder := doRequest(t, handler, http.MethodGet, configScopesPath(query), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeConfigScopes(t, recorder)
	if body.MatchedCount != 0 || body.HasMore || len(body.Scopes) != 0 {
		t.Fatalf("body = %+v", body)
	}
	if body.NextAfterNamespace != nil || body.NextAfterEnvironment != nil {
		t.Fatalf("cursor must be null without results: %+v", body)
	}

	// A cursor past every scope yields an empty page while matchedCount stays the filtered total.
	beyond := url.Values{}
	beyond.Set("afterNamespace", "zzz")
	beyond.Set("afterEnvironment", "zzz")
	recorder = doRequest(t, handler, http.MethodGet, configScopesPath(beyond), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body = decodeConfigScopes(t, recorder)
	if body.MatchedCount != 1 || body.HasMore || len(body.Scopes) != 0 {
		t.Fatalf("body = %+v", body)
	}
}

func TestConfigScopesRejectsInvalidParams(t *testing.T) {
	_, handler := newTestRouter(t)

	for _, rawLimit := range []string{"0", "-1", "501", "abc", "1.5"} {
		query := url.Values{}
		query.Set("limit", rawLimit)
		recorder := doRequest(t, handler, http.MethodGet, configScopesPath(query), nil)
		if recorder.Code != http.StatusBadRequest || errorCode(decode(t, recorder)) != "INVALID_PAGE_SIZE" {
			t.Fatalf("limit=%s status = %d body = %s", rawLimit, recorder.Code, recorder.Body.String())
		}
	}

	cases := []url.Values{}
	onlyNamespace := url.Values{}
	onlyNamespace.Set("afterNamespace", "payments")
	cases = append(cases, onlyNamespace)
	onlyEnvironment := url.Values{}
	onlyEnvironment.Set("afterEnvironment", "prod")
	cases = append(cases, onlyEnvironment)
	emptyNamespace := url.Values{}
	emptyNamespace.Set("afterNamespace", "")
	emptyNamespace.Set("afterEnvironment", "prod")
	cases = append(cases, emptyNamespace)
	bothEmpty := url.Values{}
	bothEmpty.Set("afterNamespace", "")
	bothEmpty.Set("afterEnvironment", "")
	cases = append(cases, bothEmpty)
	for _, query := range cases {
		recorder := doRequest(t, handler, http.MethodGet, configScopesPath(query), nil)
		if recorder.Code != http.StatusBadRequest || errorCode(decode(t, recorder)) != "INVALID_CURSOR" {
			t.Fatalf("query %s status = %d body = %s", query.Encode(), recorder.Code, recorder.Body.String())
		}
	}

	for _, raw := range []string{"", "TRUE", "yes", "1"} {
		query := url.Values{}
		query.Set("hasEffective", raw)
		recorder := doRequest(t, handler, http.MethodGet, configScopesPath(query), nil)
		if recorder.Code != http.StatusBadRequest || errorCode(decode(t, recorder)) != "INVALID_EFFECTIVE_FILTER" {
			t.Fatalf("hasEffective=%q status = %d body = %s", raw, recorder.Code, recorder.Body.String())
		}
	}
}

func TestConfigScopesEmptyDatabaseReturnsEmptyPage(t *testing.T) {
	_, handler := newTestRouter(t)
	recorder := doRequest(t, handler, http.MethodGet, "/config-scopes", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeConfigScopes(t, recorder)
	if body.MatchedCount != 0 || body.HasMore || len(body.Scopes) != 0 {
		t.Fatalf("body = %+v", body)
	}
}
