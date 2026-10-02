package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func itemSearchPath(query url.Values) string {
	return "/effective-config-item-search?" + query.Encode()
}

type rawSearchResult struct {
	Namespace        string          `json:"namespace"`
	Environment      string          `json:"environment"`
	EffectiveVersion *int64          `json:"effectiveVersion"`
	EffectiveItem    json.RawMessage `json:"effectiveItem"`
}

type rawItemSearch struct {
	Name         string            `json:"name"`
	MatchedCount int               `json:"matchedCount"`
	Results      []rawSearchResult `json:"results"`
}

func decodeRawItemSearch(t *testing.T, recorder *httptest.ResponseRecorder) rawItemSearch {
	t.Helper()
	var body rawItemSearch
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return body
}

func decodeEffectiveItem(t *testing.T, raw json.RawMessage) (bool, json.RawMessage) {
	t.Helper()
	var state struct {
		Present bool            `json:"present"`
		Value   json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode effectiveItem %q: %v", string(raw), err)
	}
	return state.Present, state.Value
}

func TestEffectiveConfigItemSearchScopesOrderingAndGrayOnlyHistory(t *testing.T) {
	_, handler := newTestRouter(t)
	// beta/zeta: gray-only history, the name appears only in a gray snapshot.
	publish(t, handler, "beta", "zeta", "canary", map[string]any{"flag": true, "other": 1})
	// payments/prod: full release carries the item.
	publish(t, handler, "payments", "prod", "", map[string]any{"flag": true})
	// payments/staging: full release without the item.
	publish(t, handler, "payments", "staging", "", map[string]any{"other": 2})
	// alpha/prod: full release carries the item with JSON null value.
	publish(t, handler, "alpha", "prod", "", map[string]any{"flag": nil})

	query := url.Values{}
	query.Set("name", "flag")
	recorder := doRequest(t, handler, http.MethodGet, itemSearchPath(query), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeRawItemSearch(t, recorder)
	if body.Name != "flag" || body.MatchedCount != 4 || len(body.Results) != 4 {
		t.Fatalf("body = %s", recorder.Body.String())
	}

	got := make([][2]string, 0, len(body.Results))
	for _, result := range body.Results {
		got = append(got, [2]string{result.Namespace, result.Environment})
	}
	wantOrder := [][2]string{
		{"alpha", "prod"},
		{"beta", "zeta"},
		{"payments", "prod"},
		{"payments", "staging"},
	}
	for i := range wantOrder {
		if got[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v", got, wantOrder)
		}
	}

	alpha := body.Results[0]
	present, value := decodeEffectiveItem(t, alpha.EffectiveItem)
	if alpha.EffectiveVersion == nil || *alpha.EffectiveVersion != 1 || !present || string(value) != "null" {
		t.Fatalf("alpha/prod result = %+v item = %s", alpha, alpha.EffectiveItem)
	}

	grayOnly := body.Results[1]
	present, _ = decodeEffectiveItem(t, grayOnly.EffectiveItem)
	if grayOnly.EffectiveVersion != nil || present || string(grayOnly.EffectiveItem) != `{"present":false}` {
		t.Fatalf("gray-only result = %+v item = %s", grayOnly, grayOnly.EffectiveItem)
	}

	staging := body.Results[3]
	present, _ = decodeEffectiveItem(t, staging.EffectiveItem)
	if staging.EffectiveVersion == nil || *staging.EffectiveVersion != 1 || present {
		t.Fatalf("staging result = %+v item = %s", staging, staging.EffectiveItem)
	}
}

func TestEffectiveConfigItemSearchExactNameAndFilters(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "ns", "env1", "", map[string]any{"flag": true, "n": 1})
	publish(t, handler, "ns", "env2", "", map[string]any{"flag": false})
	publish(t, handler, "ns", "env3", "canary", map[string]any{"flag": true})
	publish(t, handler, "other", "env1", "", map[string]any{"flag": true})

	search := func(extra url.Values) *httptest.ResponseRecorder {
		t.Helper()
		query := url.Values{}
		query.Set("name", "flag")
		for key, values := range extra {
			query[key] = values
		}
		return doRequest(t, handler, http.MethodGet, itemSearchPath(query), nil)
	}

	// Exact full-name match: no trimming and no substring matching; pure whitespace is a valid name.
	query := url.Values{}
	query.Set("name", " flag ")
	recorder := doRequest(t, handler, http.MethodGet, itemSearchPath(query), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeRawItemSearch(t, recorder)
	if body.Name != " flag " || body.MatchedCount != 4 {
		t.Fatalf("whitespace name body = %s", recorder.Body.String())
	}
	for _, result := range body.Results {
		present, _ := decodeEffectiveItem(t, result.EffectiveItem)
		if present {
			t.Fatalf("whitespace name matched stored item %q: %+v", " flag ", result)
		}
	}
	// present=true on the non-existent exact name yields no matches.
	query.Set("present", "true")
	recorder = doRequest(t, handler, http.MethodGet, itemSearchPath(query), nil)
	if body := decodeRawItemSearch(t, recorder); body.MatchedCount != 0 || len(body.Results) != 0 {
		t.Fatalf("whitespace name present=true matched: %s", recorder.Body.String())
	}

	// Namespace filter.
	recorder = search(url.Values{"namespace": {"ns"}})
	if body := decodeRawItemSearch(t, recorder); body.MatchedCount != 3 {
		t.Fatalf("namespace filter body = %s", recorder.Body.String())
	}

	// Namespace + environment filter.
	recorder = search(url.Values{"namespace": {"ns"}, "environment": {"env1"}})
	body = decodeRawItemSearch(t, recorder)
	if body.MatchedCount != 1 || body.Results[0].Environment != "env1" {
		t.Fatalf("scope filter body = %s", recorder.Body.String())
	}

	// Empty filter values do not restrict the search.
	recorder = search(url.Values{"namespace": {""}, "environment": {""}})
	if body := decodeRawItemSearch(t, recorder); body.MatchedCount != 4 {
		t.Fatalf("empty filters body = %s", recorder.Body.String())
	}

	// present=true excludes the gray-only scope.
	recorder = search(url.Values{"present": {"true"}})
	body = decodeRawItemSearch(t, recorder)
	if body.MatchedCount != 3 {
		t.Fatalf("present=true body = %s", recorder.Body.String())
	}
	for _, result := range body.Results {
		present, _ := decodeEffectiveItem(t, result.EffectiveItem)
		if !present {
			t.Fatalf("present=true returned absent scope: %+v", result)
		}
	}

	// present=false keeps every non-present scope, including the gray-only history.
	recorder = search(url.Values{"present": {"false"}})
	body = decodeRawItemSearch(t, recorder)
	if body.MatchedCount != 1 || body.Results[0].Namespace != "ns" || body.Results[0].Environment != "env3" {
		t.Fatalf("present=false body = %s", recorder.Body.String())
	}
}

func TestEffectiveConfigItemSearchValueSemantics(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "ns", "int", "", map[string]any{"v": json.RawMessage(`1`)})
	publish(t, handler, "ns", "float", "", map[string]any{"v": json.RawMessage(`1.0`)})
	publish(t, handler, "ns", "str", "", map[string]any{"v": json.RawMessage(`"1"`)})
	publish(t, handler, "ns", "obj", "", map[string]any{"v": json.RawMessage(`{"a":1,"b":[1,2]}`)})
	publish(t, handler, "ns", "absent", "", map[string]any{"other": 9})
	publish(t, handler, "ns", "nullenv", "", map[string]any{"v": json.RawMessage(`null`)})

	search := func(value string) rawItemSearch {
		t.Helper()
		query := url.Values{}
		query.Set("name", "v")
		if value != "" {
			query.Set("value", value)
		}
		recorder := doRequest(t, handler, http.MethodGet, itemSearchPath(query), nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("value %q status = %d body = %s", value, recorder.Code, recorder.Body.String())
		}
		return decodeRawItemSearch(t, recorder)
	}

	body := search("1")
	if body.MatchedCount != 1 || body.Results[0].Environment != "int" {
		t.Fatalf("value=1 matched %d scopes", body.MatchedCount)
	}
	body = search("1.0")
	if body.MatchedCount != 1 || body.Results[0].Environment != "float" {
		t.Fatalf("value=1.0 matched %d scopes", body.MatchedCount)
	}
	body = search(`"1"`)
	if body.MatchedCount != 1 || body.Results[0].Environment != "str" {
		t.Fatalf(`value="1" matched %d scopes`, body.MatchedCount)
	}
	// Whitespace and object key order are ignored.
	body = search(` { "b" : [  1 , 2 ] , "a" : 1 } `)
	if body.MatchedCount != 1 || body.Results[0].Environment != "obj" {
		t.Fatalf("object value matched %d scopes", body.MatchedCount)
	}
	// JSON null matches only the stored null, never an absent item.
	body = search("null")
	if body.MatchedCount != 1 || body.Results[0].Environment != "nullenv" {
		t.Fatalf("value=null matched %d scopes", body.MatchedCount)
	}
	// A value that exists nowhere still reports 200 with no matches.
	body = search("2")
	if body.MatchedCount != 0 {
		t.Fatalf("value=2 matched %d scopes", body.MatchedCount)
	}
	// The value filter inherently requires presence even without present=true.
	query := url.Values{}
	query.Set("name", "v")
	query.Set("present", "true")
	query.Set("value", "1")
	recorder := doRequest(t, handler, http.MethodGet, itemSearchPath(query), nil)
	if body := decodeRawItemSearch(t, recorder); body.MatchedCount != 1 || body.Results[0].Environment != "int" {
		t.Fatalf("present=true+value body = %s", recorder.Body.String())
	}
}

func TestEffectiveConfigItemSearchErrors(t *testing.T) {
	_, handler := newTestRouter(t)

	assertError := func(target string, wantStatus int, wantCode string) {
		t.Helper()
		recorder := doRequest(t, handler, http.MethodGet, target, nil)
		if recorder.Code != wantStatus {
			t.Fatalf("target %s status = %d body = %s, want %d", target, recorder.Code, recorder.Body.String(), wantStatus)
		}
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %q: %v", recorder.Body.String(), err)
		}
		if wantCode != "" && body.Error.Code != wantCode {
			t.Fatalf("code = %q, want %q (body %s)", body.Error.Code, wantCode, recorder.Body.String())
		}
	}

	assertError("/effective-config-item-search", http.StatusBadRequest, "MISSING_ITEM_NAME")
	assertError("/effective-config-item-search?name=", http.StatusBadRequest, "MISSING_ITEM_NAME")
	assertError("/effective-config-item-search?name=%20%20", http.StatusOK, "")
	assertError("/effective-config-item-search?name=x&present=", http.StatusBadRequest, "INVALID_PRESENCE_FILTER")
	assertError("/effective-config-item-search?name=x&present=yes", http.StatusBadRequest, "INVALID_PRESENCE_FILTER")
	assertError("/effective-config-item-search?name=x&present=1", http.StatusBadRequest, "INVALID_PRESENCE_FILTER")
	assertError("/effective-config-item-search?name=x&value=", http.StatusBadRequest, "INVALID_VALUE_FILTER")
	assertError("/effective-config-item-search?name=x&present=false&value=1", http.StatusBadRequest, "INVALID_VALUE_FILTER")
	assertError("/effective-config-item-search?name=x&value={bad", http.StatusBadRequest, "INVALID_VALUE_FILTER")
	assertError("/effective-config-item-search?name=x&value=1garbage", http.StatusBadRequest, "INVALID_VALUE_FILTER")
	// Name is validated before present and value.
	assertError("/effective-config-item-search?present=yes&value=x", http.StatusBadRequest, "MISSING_ITEM_NAME")
	assertError("/effective-config-item-search?name=&present=yes", http.StatusBadRequest, "MISSING_ITEM_NAME")
	// present is validated before value.
	assertError("/effective-config-item-search?name=x&present=yes&value={bad", http.StatusBadRequest, "INVALID_PRESENCE_FILTER")
	assertError("/effective-config-item-search?name=x&present=false&value={bad", http.StatusBadRequest, "INVALID_VALUE_FILTER")
}

func TestEffectiveConfigItemSearchStorageUnavailable(t *testing.T) {
	st, handler := newTestRouter(t)
	publish(t, handler, "ns", "env", "", map[string]any{"flag": true})
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	recorder := doRequest(t, handler, http.MethodGet, "/effective-config-item-search?name=flag", nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	if body.Error.Code != "storage_unavailable" {
		t.Fatalf("code = %q body = %s", body.Error.Code, recorder.Body.String())
	}
}

func TestEffectiveConfigItemSearchIsReadOnly(t *testing.T) {
	st, handler := newTestRouter(t)
	publish(t, handler, "ns", "env1", "", map[string]any{"flag": true})
	publish(t, handler, "ns", "env2", "canary", map[string]any{"flag": false})

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
		"/effective-config-item-search?name=flag",
		"/effective-config-item-search?name=flag&present=false",
		"/effective-config-item-search?name=flag&present=true&value=true",
		"/effective-config-item-search?name=missing",
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
