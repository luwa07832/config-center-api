package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

type rawScope struct {
	Namespace             string          `json:"namespace"`
	Environment           string          `json:"environment"`
	VersionCount          int64           `json:"versionCount"`
	LatestVersion         json.RawMessage `json:"latestVersion"`
	EffectiveVersion      *int64          `json:"effectiveVersion"`
	EffectiveItemCount    int64           `json:"effectiveItemCount"`
	GrayVersionCount      int64           `json:"grayVersionCount"`
	RollbackVersionCount  int64           `json:"rollbackVersionCount"`
	PromotionVersionCount int64           `json:"promotionVersionCount"`
}

type rawScopesResponse struct {
	MatchedCount         int        `json:"matchedCount"`
	Scopes               []rawScope `json:"scopes"`
	HasMore              bool       `json:"hasMore"`
	NextAfterNamespace   *string    `json:"nextAfterNamespace"`
	NextAfterEnvironment *string    `json:"nextAfterEnvironment"`
}

func configScopesPath(query url.Values) string {
	if len(query) == 0 {
		return "/config-scopes"
	}
	return "/config-scopes?" + query.Encode()
}

func decodeRawScopes(t *testing.T, recorder *httptest.ResponseRecorder) rawScopesResponse {
	t.Helper()
	var body rawScopesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return body
}

type rawLatestVersion struct {
	Version     int64   `json:"version"`
	GrayTag     *string `json:"grayTag"`
	RollbackOf  *int64  `json:"rollbackOf"`
	PromotionOf *int64  `json:"promotionOf"`
	Effective   bool    `json:"effective"`
}

func decodeScopeVersion(t *testing.T, raw json.RawMessage) rawLatestVersion {
	t.Helper()
	var info rawLatestVersion
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("decode latestVersion %q: %v", string(raw), err)
	}
	return info
}

func postVersionAction(t *testing.T, handler http.Handler, ns, env string, version int, action string) {
	t.Helper()
	target := "/namespaces/" + ns + "/environments/" + env + "/config-versions/" +
		strconv.Itoa(version) + "/" + action
	recorder := doRequest(t, handler, http.MethodPost, target, map[string]any{})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("%s status = %d body = %s", action, recorder.Code, recorder.Body.String())
	}
}

func TestConfigScopesAggregatesCountersAndOrders(t *testing.T) {
	_, handler := newTestRouter(t)

	// payments/prod: full v1 (2 items), gray v2 (3 items), rollback of v1 -> v3, promotion of v2 -> v4.
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 1, "b": 2})
	publish(t, handler, "payments", "prod", "canary", map[string]any{"a": 9, "b": 2, "c": 3})
	postVersionAction(t, handler, "payments", "prod", 1, "rollback")
	postVersionAction(t, handler, "payments", "prod", 2, "promote")
	// payments/staging: gray-only, two gray versions.
	publish(t, handler, "payments", "staging", "canary", map[string]any{"a": 1})
	publish(t, handler, "payments", "staging", "beta", map[string]any{"a": 2})
	// alpha/prod: one empty full release.
	publish(t, handler, "alpha", "prod", "", map[string]any{})

	recorder := doRequest(t, handler, http.MethodGet, configScopesPath(url.Values{}), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeRawScopes(t, recorder)
	if body.MatchedCount != 3 || len(body.Scopes) != 3 || body.HasMore {
		t.Fatalf("body = %s", recorder.Body.String())
	}
	wantOrder := [][2]string{{"alpha", "prod"}, {"payments", "prod"}, {"payments", "staging"}}
	for i, want := range wantOrder {
		if got := [2]string{body.Scopes[i].Namespace, body.Scopes[i].Environment}; got != want {
			t.Fatalf("order = %v, want %v", got, wantOrder)
		}
	}

	prod := body.Scopes[1]
	if prod.VersionCount != 4 || prod.EffectiveVersion == nil || *prod.EffectiveVersion != 4 {
		t.Fatalf("prod counters = %+v", prod)
	}
	if prod.EffectiveItemCount != 3 {
		t.Fatalf("effectiveItemCount = %d, want 3", prod.EffectiveItemCount)
	}
	if prod.GrayVersionCount != 1 || prod.RollbackVersionCount != 1 || prod.PromotionVersionCount != 1 {
		t.Fatalf("prod history counters = %+v", prod)
	}
	latest := decodeScopeVersion(t, prod.LatestVersion)
	if latest.Version != 4 || latest.GrayTag != nil || latest.RollbackOf != nil ||
		latest.PromotionOf == nil || *latest.PromotionOf != 2 || !latest.Effective {
		t.Fatalf("prod latest = %+v", latest)
	}
	if body.NextAfterNamespace == nil || *body.NextAfterNamespace != "payments" ||
		body.NextAfterEnvironment == nil || *body.NextAfterEnvironment != "staging" {
		t.Fatalf("next cursor = %+v, %+v", body.NextAfterNamespace, body.NextAfterEnvironment)
	}

	staging := body.Scopes[2]
	if staging.VersionCount != 2 || staging.EffectiveVersion != nil || staging.EffectiveItemCount != 0 {
		t.Fatalf("staging counters = %+v", staging)
	}
	if staging.GrayVersionCount != 2 || staging.RollbackVersionCount != 0 || staging.PromotionVersionCount != 0 {
		t.Fatalf("staging history counters = %+v", staging)
	}
	stagingLatest := decodeScopeVersion(t, staging.LatestVersion)
	if stagingLatest.Version != 2 || stagingLatest.GrayTag == nil || *stagingLatest.GrayTag != "beta" ||
		stagingLatest.Effective {
		t.Fatalf("staging latest = %+v", stagingLatest)
	}

	alpha := body.Scopes[0]
	if alpha.VersionCount != 1 || alpha.EffectiveVersion == nil || *alpha.EffectiveVersion != 1 ||
		alpha.EffectiveItemCount != 0 {
		t.Fatalf("alpha counters = %+v", alpha)
	}
}

func TestConfigScopesFiltersNamespaceEnvironmentAndEmptyParams(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 1})
	publish(t, handler, "payments", "staging", "", map[string]any{"a": 1})
	publish(t, handler, "billing", "prod", "canary", map[string]any{"a": 1})

	query := url.Values{}
	query.Set("namespace", "payments")
	body := decodeRawScopes(t, doRequest(t, handler, http.MethodGet, configScopesPath(query), nil))
	if body.MatchedCount != 2 || len(body.Scopes) != 2 {
		t.Fatalf("body = %+v", body)
	}

	query.Set("environment", "prod")
	body = decodeRawScopes(t, doRequest(t, handler, http.MethodGet, configScopesPath(query), nil))
	if body.MatchedCount != 1 || len(body.Scopes) != 1 || body.Scopes[0].Environment != "prod" {
		t.Fatalf("body = %+v", body)
	}

	// Empty values disable the filter, so all three scopes come back.
	emptyQuery := url.Values{"namespace": {""}, "environment": {""}}
	body = decodeRawScopes(t, doRequest(t, handler, http.MethodGet, configScopesPath(emptyQuery), nil))
	if body.MatchedCount != 3 || len(body.Scopes) != 3 {
		t.Fatalf("body = %+v", body)
	}
}

func TestConfigScopesHasEffectiveFilter(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "full", "env", "", map[string]any{"a": 1})
	publish(t, handler, "gray", "env", "canary", map[string]any{"a": 1})

	query := url.Values{}
	query.Set("hasEffective", "true")
	body := decodeRawScopes(t, doRequest(t, handler, http.MethodGet, configScopesPath(query), nil))
	if body.MatchedCount != 1 || len(body.Scopes) != 1 || body.Scopes[0].Namespace != "full" {
		t.Fatalf("true body = %+v", body)
	}

	query.Set("hasEffective", "false")
	body = decodeRawScopes(t, doRequest(t, handler, http.MethodGet, configScopesPath(query), nil))
	if body.MatchedCount != 1 || len(body.Scopes) != 1 || body.Scopes[0].Namespace != "gray" {
		t.Fatalf("false body = %+v", body)
	}
	if body.Scopes[0].EffectiveVersion != nil {
		t.Fatalf("gray scope effectiveVersion must be null: %+v", body.Scopes[0])
	}
}

func TestConfigScopesEmptyAndNoMatchShapes(t *testing.T) {
	_, handler := newTestRouter(t)

	// Empty database: scopes array and cursor must be present/null.
	recorder := doRequest(t, handler, http.MethodGet, configScopesPath(url.Values{}), nil)
	body := decodeRawScopes(t, recorder)
	if body.MatchedCount != 0 || len(body.Scopes) != 0 || body.HasMore ||
		body.NextAfterNamespace != nil || body.NextAfterEnvironment != nil {
		t.Fatalf("empty body = %s", recorder.Body.String())
	}

	// Valid query without matches has the same shape.
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 1})
	query := url.Values{}
	query.Set("namespace", "missing")
	recorder = doRequest(t, handler, http.MethodGet, configScopesPath(query), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body = decodeRawScopes(t, recorder)
	if body.MatchedCount != 0 || len(body.Scopes) != 0 || body.HasMore ||
		body.NextAfterNamespace != nil || body.NextAfterEnvironment != nil {
		t.Fatalf("no-match body = %s", recorder.Body.String())
	}
}

func TestConfigScopesPaginatesWithCompositeCursor(t *testing.T) {
	_, handler := newTestRouter(t)
	namespaces := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	for _, ns := range namespaces {
		publish(t, handler, ns, "env", "", map[string]any{"a": 1})
	}

	query := url.Values{}
	query.Set("limit", "2")
	recorder := doRequest(t, handler, http.MethodGet, configScopesPath(query), nil)
	first := decodeRawScopes(t, recorder)
	if first.MatchedCount != 5 || !first.HasMore || len(first.Scopes) != 2 {
		t.Fatalf("first = %s", recorder.Body.String())
	}
	if first.Scopes[0].Namespace != "alpha" || first.Scopes[1].Namespace != "bravo" {
		t.Fatalf("first scopes = %+v", first.Scopes)
	}
	if first.NextAfterNamespace == nil || *first.NextAfterNamespace != "bravo" ||
		first.NextAfterEnvironment == nil || *first.NextAfterEnvironment != "env" {
		t.Fatalf("first cursor = %+v %+v", first.NextAfterNamespace, first.NextAfterEnvironment)
	}

	query.Set("afterNamespace", *first.NextAfterNamespace)
	query.Set("afterEnvironment", *first.NextAfterEnvironment)
	second := decodeRawScopes(t, doRequest(t, handler, http.MethodGet, configScopesPath(query), nil))
	if second.MatchedCount != 5 || !second.HasMore || len(second.Scopes) != 2 ||
		second.Scopes[0].Namespace != "charlie" || second.Scopes[1].Namespace != "delta" {
		t.Fatalf("second = %+v", second)
	}

	query.Set("afterNamespace", *second.NextAfterNamespace)
	query.Set("afterEnvironment", *second.NextAfterEnvironment)
	last := decodeRawScopes(t, doRequest(t, handler, http.MethodGet, configScopesPath(query), nil))
	if last.MatchedCount != 5 || last.HasMore || len(last.Scopes) != 1 ||
		last.Scopes[0].Namespace != "echo" {
		t.Fatalf("last = %+v", last)
	}

	// Walking past the end returns an empty page while matchedCount stays at the total.
	query.Set("afterNamespace", "echo")
	query.Set("afterEnvironment", "env")
	beyond := decodeRawScopes(t, doRequest(t, handler, http.MethodGet, configScopesPath(query), nil))
	if beyond.MatchedCount != 5 || beyond.HasMore || len(beyond.Scopes) != 0 ||
		beyond.NextAfterNamespace != nil || beyond.NextAfterEnvironment != nil {
		t.Fatalf("beyond = %+v", beyond)
	}
}

func TestConfigScopesPaginatesFilteredResults(t *testing.T) {
	_, handler := newTestRouter(t)
	// Two scopes with a full release, one gray-only.
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 1})
	publish(t, handler, "payments", "staging", "", map[string]any{"a": 1})
	publish(t, handler, "billing", "prod", "canary", map[string]any{"a": 1})

	query := url.Values{}
	query.Set("hasEffective", "true")
	query.Set("limit", "1")
	first := decodeRawScopes(t, doRequest(t, handler, http.MethodGet, configScopesPath(query), nil))
	if first.MatchedCount != 2 || !first.HasMore || len(first.Scopes) != 1 ||
		first.Scopes[0].Namespace != "payments" || first.Scopes[0].Environment != "prod" {
		t.Fatalf("first = %+v", first)
	}
	query.Set("afterNamespace", *first.NextAfterNamespace)
	query.Set("afterEnvironment", *first.NextAfterEnvironment)
	second := decodeRawScopes(t, doRequest(t, handler, http.MethodGet, configScopesPath(query), nil))
	if second.MatchedCount != 2 || second.HasMore || len(second.Scopes) != 1 ||
		second.Scopes[0].Namespace != "payments" || second.Scopes[0].Environment != "staging" {
		t.Fatalf("second = %+v", second)
	}
}

func TestConfigScopesErrorContract(t *testing.T) {
	_, handler := newTestRouter(t)
	cases := []struct {
		name  string
		query url.Values
		code  string
	}{
		{"limit zero", url.Values{"limit": {"0"}}, "INVALID_PAGE_SIZE"},
		{"limit negative", url.Values{"limit": {"-1"}}, "INVALID_PAGE_SIZE"},
		{"limit non numeric", url.Values{"limit": {"abc"}}, "INVALID_PAGE_SIZE"},
		{"limit decimal", url.Values{"limit": {"1.5"}}, "INVALID_PAGE_SIZE"},
		{"limit over max", url.Values{"limit": {"501"}}, "INVALID_PAGE_SIZE"},
		{"limit empty", url.Values{"limit": {""}}, "INVALID_PAGE_SIZE"},
		{"cursor only namespace", url.Values{"afterNamespace": {"ns"}}, "INVALID_CURSOR"},
		{"cursor only environment", url.Values{"afterEnvironment": {"env"}}, "INVALID_CURSOR"},
		{"cursor namespace empty", url.Values{"afterNamespace": {""}, "afterEnvironment": {"env"}}, "INVALID_CURSOR"},
		{"cursor environment empty", url.Values{"afterNamespace": {"ns"}, "afterEnvironment": {""}}, "INVALID_CURSOR"},
		{"both cursor values empty", url.Values{"afterNamespace": {""}, "afterEnvironment": {""}}, "INVALID_CURSOR"},
		{"hasEffective garbage", url.Values{"hasEffective": {"yes"}}, "INVALID_EFFECTIVE_FILTER"},
		{"hasEffective empty", url.Values{"hasEffective": {""}}, "INVALID_EFFECTIVE_FILTER"},
		{"hasEffective numeric", url.Values{"hasEffective": {"1"}}, "INVALID_EFFECTIVE_FILTER"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := doRequest(t, handler, http.MethodGet, configScopesPath(tc.query), nil)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d want 400 body = %s", recorder.Code, recorder.Body.String())
			}
			if code := errorCode(decode(t, recorder)); code != tc.code {
				t.Fatalf("code = %q want %s", code, tc.code)
			}
		})
	}
}

func TestConfigScopesValidationOrder(t *testing.T) {
	_, handler := newTestRouter(t)
	// Bad page size is reported before a malformed cursor or hasEffective value.
	query := url.Values{}
	query.Set("limit", "0")
	query.Set("afterNamespace", "ns")
	query.Set("hasEffective", "bad")
	recorder := doRequest(t, handler, http.MethodGet, configScopesPath(query), nil)
	if code := errorCode(decode(t, recorder)); code != "INVALID_PAGE_SIZE" {
		t.Fatalf("code = %q want INVALID_PAGE_SIZE", code)
	}

	// Bad cursor is reported before a malformed hasEffective value.
	query.Del("limit")
	recorder = doRequest(t, handler, http.MethodGet, configScopesPath(query), nil)
	if code := errorCode(decode(t, recorder)); code != "INVALID_CURSOR" {
		t.Fatalf("code = %q want INVALID_CURSOR", code)
	}
}

func TestConfigScopesDefaultLimitAllowsUpTo500(t *testing.T) {
	_, handler := newTestRouter(t)
	for i := 0; i < 105; i++ {
		ns := "ns" + strconv.Itoa(i)
		publish(t, handler, ns, "env", "", map[string]any{"a": 1})
	}
	// Default limit 100 pages the 105 scopes.
	body := decodeRawScopes(t, doRequest(t, handler, http.MethodGet, configScopesPath(url.Values{}), nil))
	if body.MatchedCount != 105 || !body.HasMore || len(body.Scopes) != 100 {
		t.Fatalf("default page = matched %d hasMore %v len %d", body.MatchedCount, body.HasMore, len(body.Scopes))
	}
	query := url.Values{}
	query.Set("limit", "500")
	body = decodeRawScopes(t, doRequest(t, handler, http.MethodGet, configScopesPath(query), nil))
	if body.MatchedCount != 105 || body.HasMore || len(body.Scopes) != 105 {
		t.Fatalf("limit 500 page = matched %d hasMore %v len %d", body.MatchedCount, body.HasMore, len(body.Scopes))
	}
}

func TestConfigScopesStorageUnavailable(t *testing.T) {
	st, handler := newTestRouter(t)
	publish(t, handler, "ns", "env", "", map[string]any{"a": 1})
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	recorder := doRequest(t, handler, http.MethodGet, configScopesPath(url.Values{}), nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if code := errorCode(decode(t, recorder)); code != "storage_unavailable" {
		t.Fatalf("code = %q want storage_unavailable", code)
	}
}

func TestConfigScopesIsReadOnly(t *testing.T) {
	st, handler := newTestRouter(t)
	publish(t, handler, "ns", "env1", "", map[string]any{"a": 1})
	publish(t, handler, "ns", "env2", "canary", map[string]any{"a": 2})

	countVersions := func() int {
		t.Helper()
		versions1, err := st.ListVersions(t.Context(), "ns", "env1")
		if err != nil {
			t.Fatalf("list env1: %v", err)
		}
		versions2, err := st.ListVersions(t.Context(), "ns", "env2")
		if err != nil {
			t.Fatalf("list env2: %v", err)
		}
		return len(versions1) + len(versions2)
	}
	before := countVersions()
	for _, target := range []string{
		configScopesPath(url.Values{}),
		configScopesPath(url.Values{"hasEffective": {"true"}}),
		configScopesPath(url.Values{"hasEffective": {"false"}}),
		configScopesPath(url.Values{"namespace": {"ns"}, "environment": {"env1"}, "limit": {"1"}}),
	} {
		recorder := doRequest(t, handler, http.MethodGet, target, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("target %s status = %d body = %s", target, recorder.Code, recorder.Body.String())
		}
	}
	if after := countVersions(); after != before {
		t.Fatalf("version count changed: before=%d after=%d", before, after)
	}
}
