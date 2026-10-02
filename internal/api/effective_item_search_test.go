package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

type rawItemSearchResult struct {
	Namespace        string          `json:"namespace"`
	Environment      string          `json:"environment"`
	EffectiveVersion *int64          `json:"effectiveVersion"`
	EffectiveItem    json.RawMessage `json:"effectiveItem"`
}

type rawItemSearchResponse struct {
	Name         string                `json:"name"`
	MatchedCount int                   `json:"matchedCount"`
	Results      []rawItemSearchResult `json:"results"`
}

func decodeItemSearch(t *testing.T, recorder *httptest.ResponseRecorder) rawItemSearchResponse {
	t.Helper()
	var body rawItemSearchResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return body
}

func itemSearchPath(query url.Values) string {
	return "/effective-config-item-search?" + query.Encode()
}

func itemSearchQuery(name string) url.Values {
	query := url.Values{}
	query.Set("name", name)
	return query
}

func seedItemSearchScopes(t *testing.T, handler http.Handler) {
	t.Helper()
	publish(t, handler, "payments", "prod", "", map[string]any{"timeout": "30", "keep": 1})
	publish(t, handler, "payments", "prod", "canary", map[string]any{"timeout": "99"})
	publish(t, handler, "payments", "staging", "", map[string]any{"keep": 1})
	publish(t, handler, "billing", "prod", "", map[string]any{"timeout": nil})
	publish(t, handler, "ops", "dev", "gray-only", map[string]any{"timeout": "7"})
}

func TestEffectiveConfigItemSearchCoversEveryScopeInCodePointOrder(t *testing.T) {
	_, handler := newTestRouter(t)
	seedItemSearchScopes(t, handler)

	recorder := doRequest(t, handler, http.MethodGet, itemSearchPath(itemSearchQuery("timeout")), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeItemSearch(t, recorder)
	if body.Name != "timeout" {
		t.Fatalf("name = %q", body.Name)
	}
	type want struct {
		namespace     string
		environment   string
		effective     *int64
		effectiveItem string
	}
	version := func(v int64) *int64 { return &v }
	wants := []want{
		{"billing", "prod", version(1), `{"present":true,"value":null}`},
		{"ops", "dev", nil, `{"present":false}`},
		{"payments", "prod", version(1), `{"present":true,"value":"30"}`},
		{"payments", "staging", version(1), `{"present":false}`},
	}
	if body.MatchedCount != len(wants) || len(body.Results) != len(wants) {
		t.Fatalf("matchedCount = %d results = %d, want %d", body.MatchedCount, len(body.Results), len(wants))
	}
	for i, wantEntry := range wants {
		result := body.Results[i]
		if result.Namespace != wantEntry.namespace || result.Environment != wantEntry.environment {
			t.Fatalf("result %d scope = %s/%s, want %s/%s",
				i, result.Namespace, result.Environment, wantEntry.namespace, wantEntry.environment)
		}
		if (result.EffectiveVersion == nil) != (wantEntry.effective == nil) ||
			(result.EffectiveVersion != nil && *result.EffectiveVersion != *wantEntry.effective) {
			t.Fatalf("result %d effectiveVersion = %v, want %v", i, result.EffectiveVersion, wantEntry.effective)
		}
		if string(result.EffectiveItem) != wantEntry.effectiveItem {
			t.Fatalf("result %d effectiveItem = %s, want %s", i, result.EffectiveItem, wantEntry.effectiveItem)
		}
	}
}

func TestEffectiveConfigItemSearchScopeFilters(t *testing.T) {
	_, handler := newTestRouter(t)
	seedItemSearchScopes(t, handler)

	byNamespace := itemSearchQuery("timeout")
	byNamespace.Set("namespace", "payments")
	body := decodeItemSearch(t, doRequest(t, handler, http.MethodGet, itemSearchPath(byNamespace), nil))
	if body.MatchedCount != 2 || body.Results[0].Environment != "prod" || body.Results[1].Environment != "staging" {
		t.Fatalf("namespace filter = %v", body.Results)
	}

	byEnvironment := itemSearchQuery("timeout")
	byEnvironment.Set("environment", "prod")
	body = decodeItemSearch(t, doRequest(t, handler, http.MethodGet, itemSearchPath(byEnvironment), nil))
	if body.MatchedCount != 2 || body.Results[0].Namespace != "billing" || body.Results[1].Namespace != "payments" {
		t.Fatalf("environment filter = %v", body.Results)
	}

	both := itemSearchQuery("timeout")
	both.Set("namespace", "payments")
	both.Set("environment", "prod")
	body = decodeItemSearch(t, doRequest(t, handler, http.MethodGet, itemSearchPath(both), nil))
	if body.MatchedCount != 1 || body.Results[0].Namespace != "payments" || body.Results[0].Environment != "prod" {
		t.Fatalf("combined filter = %v", body.Results)
	}

	empty := itemSearchQuery("timeout")
	empty.Set("namespace", "")
	empty.Set("environment", "")
	body = decodeItemSearch(t, doRequest(t, handler, http.MethodGet, itemSearchPath(empty), nil))
	if body.MatchedCount != 4 {
		t.Fatalf("empty scope filters must not restrict, matchedCount = %d", body.MatchedCount)
	}
}

func TestEffectiveConfigItemSearchPresenceFilter(t *testing.T) {
	_, handler := newTestRouter(t)
	seedItemSearchScopes(t, handler)

	present := itemSearchQuery("timeout")
	present.Set("present", "true")
	body := decodeItemSearch(t, doRequest(t, handler, http.MethodGet, itemSearchPath(present), nil))
	if body.MatchedCount != 2 || body.Results[0].Namespace != "billing" || body.Results[1].Namespace != "payments" {
		t.Fatalf("present=true results = %v", body.Results)
	}

	absent := itemSearchQuery("timeout")
	absent.Set("present", "false")
	body = decodeItemSearch(t, doRequest(t, handler, http.MethodGet, itemSearchPath(absent), nil))
	if body.MatchedCount != 2 || body.Results[0].Namespace != "ops" || body.Results[1].Namespace != "payments" {
		t.Fatalf("present=false results = %v", body.Results)
	}
	for _, result := range body.Results {
		if string(result.EffectiveItem) != `{"present":false}` {
			t.Fatalf("present=false effectiveItem = %s", result.EffectiveItem)
		}
	}
}

func TestEffectiveConfigItemSearchValueFilterSemantics(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{
		"n":   json.RawMessage(`1`),
		"obj": map[string]any{"a": 1, "b": 2},
		"s":   "1",
		"nil": nil,
	})
	publish(t, handler, "svc", "prod", "", map[string]any{"n": json.RawMessage(`1.0`), "obj": map[string]any{"a": 1, "b": 3}})

	search := func(name, value string) rawItemSearchResponse {
		t.Helper()
		query := itemSearchQuery(name)
		query.Set("value", value)
		recorder := doRequest(t, handler, http.MethodGet, itemSearchPath(query), nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("search %s=%s status = %d body = %s", name, value, recorder.Code, recorder.Body.String())
		}
		return decodeItemSearch(t, recorder)
	}

	if body := search("n", `1`); body.MatchedCount != 1 || body.Results[0].Environment != "dev" {
		t.Fatalf("value 1 results = %v", body.Results)
	}
	if body := search("n", ` 1 `); body.MatchedCount != 1 {
		t.Fatalf("whitespace around value must be ignored, results = %v", body.Results)
	}
	if body := search("n", `1.0`); body.MatchedCount != 1 || body.Results[0].Environment != "prod" {
		t.Fatalf("1 and 1.0 must stay distinct, results = %v", body.Results)
	}
	if body := search("s", `1`); body.MatchedCount != 0 {
		t.Fatalf("number 1 must not match string \"1\", results = %v", body.Results)
	}
	if body := search("s", `"1"`); body.MatchedCount != 1 || body.Results[0].Environment != "dev" {
		t.Fatalf("string value results = %v", body.Results)
	}
	if body := search("obj", `{ "b": 2, "a": 1 }`); body.MatchedCount != 1 || body.Results[0].Environment != "dev" {
		t.Fatalf("object key order and whitespace must be ignored, results = %v", body.Results)
	}
	if body := search("nil", `null`); body.MatchedCount != 1 || body.Results[0].Environment != "dev" {
		t.Fatalf("stored null must match value null, results = %v", body.Results)
	}
	if body := search("obj", `null`); body.MatchedCount != 0 {
		t.Fatalf("absent item must not match value null, results = %v", body.Results)
	}

	combined := itemSearchQuery("n")
	combined.Set("value", `1`)
	combined.Set("present", "true")
	if body := decodeItemSearch(t, doRequest(t, handler, http.MethodGet, itemSearchPath(combined), nil)); body.MatchedCount != 1 {
		t.Fatalf("value with present=true results = %v", body.Results)
	}
}

func TestEffectiveConfigItemSearchEmptyStoreAndUnknownName(t *testing.T) {
	_, handler := newTestRouter(t)

	recorder := doRequest(t, handler, http.MethodGet, itemSearchPath(itemSearchQuery("anything")), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeItemSearch(t, recorder)
	if body.MatchedCount != 0 || len(body.Results) != 0 {
		t.Fatalf("empty store results = %v", body.Results)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if string(raw["results"]) != `[]` {
		t.Fatalf("results must be an empty array, got %s", raw["results"])
	}

	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	unknown := decodeItemSearch(t, doRequest(t, handler, http.MethodGet, itemSearchPath(itemSearchQuery("missing")), nil))
	if unknown.MatchedCount != 1 || string(unknown.Results[0].EffectiveItem) != `{"present":false}` {
		t.Fatalf("unknown name results = %v", unknown.Results)
	}
}

func TestEffectiveConfigItemSearchErrorContract(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})

	cases := []struct {
		name       string
		target     string
		wantStatus int
		wantCode   string
	}{
		{"missing name", "/effective-config-item-search", http.StatusBadRequest, "MISSING_ITEM_NAME"},
		{"empty name", "/effective-config-item-search?name=", http.StatusBadRequest, "MISSING_ITEM_NAME"},
		{"name checked first", "/effective-config-item-search?present=maybe", http.StatusBadRequest, "MISSING_ITEM_NAME"},
		{"invalid present", "/effective-config-item-search?name=a&present=maybe", http.StatusBadRequest, "INVALID_PRESENCE_FILTER"},
		{"empty present", "/effective-config-item-search?name=a&present=", http.StatusBadRequest, "INVALID_PRESENCE_FILTER"},
		{"invalid value json", "/effective-config-item-search?name=a&value=not-json", http.StatusBadRequest, "INVALID_VALUE_FILTER"},
		{"empty value", "/effective-config-item-search?name=a&value=", http.StatusBadRequest, "INVALID_VALUE_FILTER"},
		{"value with present false", "/effective-config-item-search?name=a&value=1&present=false", http.StatusBadRequest, "INVALID_VALUE_FILTER"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := doRequest(t, handler, http.MethodGet, tc.target, nil)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d body = %s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if code := errorCode(decode(t, recorder)); code != tc.wantCode {
				t.Fatalf("code = %s, want %s", code, tc.wantCode)
			}
		})
	}

	whitespace := doRequest(t, handler, http.MethodGet, "/effective-config-item-search?name=%20%20", nil)
	if whitespace.Code != http.StatusOK {
		t.Fatalf("whitespace-only name is valid, got %d %s", whitespace.Code, whitespace.Body.String())
	}
	if body := decodeItemSearch(t, whitespace); body.Name != "  " || body.MatchedCount != 1 {
		t.Fatalf("whitespace name search = %q count %d", body.Name, body.MatchedCount)
	}
}

func TestEffectiveConfigItemSearchCreatesNoHistoryWrite(t *testing.T) {
	_, handler := newTestRouter(t)
	seedItemSearchScopes(t, handler)

	targets := []string{
		"/config-versions?namespace=payments&environment=prod",
		"/config-versions?namespace=ops&environment=dev",
	}
	before := make([]string, len(targets))
	for i, target := range targets {
		before[i] = doRequest(t, handler, http.MethodGet, target, nil).Body.String()
	}
	query := itemSearchQuery("timeout")
	query.Set("value", `"30"`)
	doRequest(t, handler, http.MethodGet, itemSearchPath(query), nil)
	for i, target := range targets {
		if after := doRequest(t, handler, http.MethodGet, target, nil).Body.String(); after != before[i] {
			t.Fatalf("search changed history for %s:\nbefore %s\nafter  %s", target, before[i], after)
		}
	}
}
