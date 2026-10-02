package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func historicalPath(query url.Values) string {
	return "/historical-effective-configs?" + query.Encode()
}

func historicalQuery(ns, env, asOf string) url.Values {
	query := url.Values{}
	if ns != "" {
		query.Set("namespace", ns)
	}
	if env != "" {
		query.Set("environment", env)
	}
	if asOf != "" {
		query.Set("asOf", asOf)
	}
	return query
}

type historicalResponse struct {
	Namespace        string                     `json:"namespace"`
	Environment      string                     `json:"environment"`
	AsOf             string                     `json:"asOf"`
	EffectiveVersion *int64                     `json:"effectiveVersion"`
	Version          *VersionInfo               `json:"version"`
	Items            map[string]json.RawMessage `json:"items"`
}

func decodeHistorical(t *testing.T, recorder *httptest.ResponseRecorder) historicalResponse {
	t.Helper()
	var body historicalResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return body
}

func listCreatedAt(t *testing.T, handler http.Handler, ns, env string) map[int64]string {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace="+ns+"&environment="+env, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("history status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Versions []VersionInfo `json:"versions"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	created := make(map[int64]string, len(body.Versions))
	for _, version := range body.Versions {
		created[version.Version] = version.CreatedAt
	}
	return created
}

func TestHistoricalEffectiveConfigReturnsSnapshotAtAsOf(t *testing.T) {
	_, handler := newTestRouter(t)
	// payments/prod: one full release followed by a gray one. The gray release never becomes
	// the historical effective config, so asOf at or after the full release always selects it.
	publish(t, handler, "payments", "prod", "", map[string]any{
		"text": "one", "num": json.RawMessage(`1`), "flag": true, "empty": nil,
	})
	publish(t, handler, "payments", "prod", "canary", map[string]any{"text": "gray"})
	created := listCreatedAt(t, handler, "payments", "prod")

	// asOf exactly at the full release creation second selects it (createdAt <= asOf).
	recorder := doRequest(t, handler, http.MethodGet,
		historicalPath(historicalQuery("payments", "prod", created[1])), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeHistorical(t, recorder)
	if body.Namespace != "payments" || body.Environment != "prod" {
		t.Fatalf("scope = %s/%s", body.Namespace, body.Environment)
	}
	if body.AsOf != created[1] {
		t.Fatalf("asOf = %s, want normalized %s", body.AsOf, created[1])
	}
	if body.EffectiveVersion == nil || *body.EffectiveVersion != 1 {
		t.Fatalf("effectiveVersion = %v, want 1", body.EffectiveVersion)
	}
	if body.Version == nil || body.Version.Version != 1 {
		t.Fatalf("version = %+v, want version 1", body.Version)
	}
	if !body.Version.Effective || body.Version.GrayTag != nil ||
		body.Version.RollbackOf != nil || body.Version.PromotionOf != nil {
		t.Fatalf("version metadata = %+v", body.Version)
	}
	if body.Version.CreatedAt != created[1] {
		t.Fatalf("createdAt = %s, want %s", body.Version.CreatedAt, created[1])
	}
	want := map[string]string{"text": `"one"`, "num": "1", "flag": "true", "empty": "null"}
	if len(body.Items) != len(want) {
		t.Fatalf("items = %v", body.Items)
	}
	for name, value := range want {
		if string(body.Items[name]) != value {
			t.Fatalf("item %s = %s, want %s", name, body.Items[name], value)
		}
	}

	// asOf at or after the latest creation time still returns the only full release.
	recorder = doRequest(t, handler, http.MethodGet,
		historicalPath(historicalQuery("payments", "prod", "2099-01-01T00:00:00Z")), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body = decodeHistorical(t, recorder)
	if body.EffectiveVersion == nil || *body.EffectiveVersion != 1 || body.Version == nil || body.Version.Version != 1 {
		t.Fatalf("latest body = %+v", body)
	}

	// orders/prod: a later full release supersedes the earlier one for a late asOf.
	publish(t, handler, "orders", "prod", "", map[string]any{"text": "old"})
	publish(t, handler, "orders", "prod", "canary", map[string]any{"text": "gray"})
	publish(t, handler, "orders", "prod", "", map[string]any{"text": "two", "num": json.RawMessage(`1.0`)})
	ordersCreated := listCreatedAt(t, handler, "orders", "prod")

	recorder = doRequest(t, handler, http.MethodGet,
		historicalPath(historicalQuery("orders", "prod", ordersCreated[3])), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body = decodeHistorical(t, recorder)
	if body.EffectiveVersion == nil || *body.EffectiveVersion != 3 || body.Version == nil || body.Version.Version != 3 {
		t.Fatalf("latest body = %+v", body)
	}
	if string(body.Items["text"]) != `"two"` || string(body.Items["num"]) != "1.0" {
		t.Fatalf("latest items = %v", body.Items)
	}
}

func TestHistoricalEffectiveConfigEmptyResults(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"a": "one"})
	publish(t, handler, "grayonly", "prod", "canary", map[string]any{"a": "gray"})

	assertEmpty := func(ns, env, asOf string) {
		t.Helper()
		recorder := doRequest(t, handler, http.MethodGet,
			historicalPath(historicalQuery(ns, env, asOf)), nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s/%s status = %d body = %s", ns, env, recorder.Code, recorder.Body.String())
		}
		body := decodeHistorical(t, recorder)
		if body.EffectiveVersion != nil || body.Version != nil {
			t.Fatalf("%s/%s asOf %s: got %+v, want nulls", ns, env, asOf, body)
		}
		if len(body.Items) != 0 {
			t.Fatalf("%s/%s asOf %s: items = %v, want empty", ns, env, asOf, body.Items)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
			t.Fatalf("decode raw: %v", err)
		}
		if string(raw["effectiveVersion"]) != "null" || string(raw["version"]) != "null" || string(raw["items"]) != "{}" {
			t.Fatalf("raw body = %s", recorder.Body.String())
		}
	}

	// asOf before the first full release.
	assertEmpty("payments", "prod", "2020-01-01T00:00:00Z")
	// Scope with gray-only history never has a historical effective config.
	assertEmpty("grayonly", "prod", "2099-01-01T00:00:00Z")
	// Scope without any history.
	assertEmpty("unknown", "prod", "2099-01-01T00:00:00Z")
}

func TestHistoricalEffectiveConfigNormalizesAsOfToUTC(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": "one"})
	created := listCreatedAt(t, handler, "svc", "dev")[1]
	moment, err := time.Parse(time.RFC3339, created)
	if err != nil {
		t.Fatalf("parse createdAt %q: %v", created, err)
	}
	zone := time.FixedZone("UTC+8", 8*60*60)
	offsetForm := func(at time.Time) string {
		return at.In(zone).Format("2006-01-02T15:04:05-07:00")
	}

	// The same instant expressed with a +08:00 offset normalizes to the UTC createdAt and hits.
	recorder := doRequest(t, handler, http.MethodGet,
		historicalPath(historicalQuery("svc", "dev", offsetForm(moment))), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeHistorical(t, recorder)
	if body.AsOf != created {
		t.Fatalf("asOf = %s, want normalized %s", body.AsOf, created)
	}
	if body.EffectiveVersion == nil || *body.EffectiveVersion != 1 {
		t.Fatalf("effectiveVersion = %v, want 1", body.EffectiveVersion)
	}

	// One second earlier in the same offset misses the only full release.
	recorder = doRequest(t, handler, http.MethodGet,
		historicalPath(historicalQuery("svc", "dev", offsetForm(moment.Add(-time.Second)))), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body = decodeHistorical(t, recorder)
	if body.EffectiveVersion != nil || body.Version != nil || len(body.Items) != 0 {
		t.Fatalf("body = %+v, want empty result", body)
	}
	if body.AsOf != moment.Add(-time.Second).UTC().Format(time.RFC3339) {
		t.Fatalf("asOf = %s, want normalized UTC", body.AsOf)
	}
}

func TestHistoricalEffectiveConfigValidationErrors(t *testing.T) {
	_, handler := newTestRouter(t)

	cases := []struct {
		name  string
		query url.Values
		code  string
	}{
		{"missing namespace", historicalQuery("", "prod", "2026-10-01T10:00:00Z"), "MISSING_SCOPE"},
		{"missing environment", historicalQuery("payments", "", "2026-10-01T10:00:00Z"), "MISSING_SCOPE"},
		{"missing asOf", historicalQuery("payments", "prod", ""), "MISSING_AS_OF"},
		{"garbage asOf", historicalQuery("payments", "prod", "not-a-time"), "INVALID_TIMESTAMP"},
		{"date only", historicalQuery("payments", "prod", "2026-10-01"), "INVALID_TIMESTAMP"},
		{"no zone", historicalQuery("payments", "prod", "2026-10-01T10:00:00"), "INVALID_TIMESTAMP"},
		{"month out of range", historicalQuery("payments", "prod", "2026-13-01T00:00:00Z"), "INVALID_TIMESTAMP"},
	}
	for _, tc := range cases {
		recorder := doRequest(t, handler, http.MethodGet, historicalPath(tc.query), nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d body = %s", tc.name, recorder.Code, recorder.Body.String())
		}
		body := decode(t, recorder)
		if code := errorCode(body); code != tc.code {
			t.Fatalf("%s: code = %q, want %q body = %s", tc.name, code, tc.code, recorder.Body.String())
		}
		entry := body["error"].(map[string]any)
		if message, ok := entry["message"].(string); !ok || message == "" {
			t.Fatalf("%s: error message missing in %s", tc.name, recorder.Body.String())
		}
	}
}

func TestHistoricalEffectiveConfigSelectsRollbackAndPromotionVersions(t *testing.T) {
	_, handler := newTestRouter(t)

	// A promoted gray version becomes a selectable full release.
	publish(t, handler, "promote-ns", "prod", "canary", map[string]any{"a": "gray"})
	recorder := doRequest(t, handler, http.MethodPost, promotePath("promote-ns", "prod", "1"), nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("promote status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	recorder = doRequest(t, handler, http.MethodGet,
		historicalPath(historicalQuery("promote-ns", "prod", "2099-01-01T00:00:00Z")), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeHistorical(t, recorder)
	if body.EffectiveVersion == nil || *body.EffectiveVersion != 2 {
		t.Fatalf("effectiveVersion = %v, want promoted version 2", body.EffectiveVersion)
	}
	if body.Version.PromotionOf == nil || *body.Version.PromotionOf != 1 {
		t.Fatalf("promotionOf = %v, want 1", body.Version.PromotionOf)
	}
	if string(body.Items["a"]) != `"gray"` {
		t.Fatalf("items = %v", body.Items)
	}

	// A rollback copy is a selectable full release carrying rollbackOf.
	publish(t, handler, "rollback-ns", "prod", "", map[string]any{"a": "one"})
	publish(t, handler, "rollback-ns", "prod", "", map[string]any{"a": "two"})
	recorder = doRequest(t, handler, http.MethodPost,
		"/namespaces/rollback-ns/environments/prod/config-versions/1/rollback", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("rollback status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	recorder = doRequest(t, handler, http.MethodGet,
		historicalPath(historicalQuery("rollback-ns", "prod", "2099-01-01T00:00:00Z")), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body = decodeHistorical(t, recorder)
	if body.EffectiveVersion == nil || *body.EffectiveVersion != 3 {
		t.Fatalf("effectiveVersion = %v, want rollback version 3", body.EffectiveVersion)
	}
	if body.Version.RollbackOf == nil || *body.Version.RollbackOf != 1 {
		t.Fatalf("rollbackOf = %v, want 1", body.Version.RollbackOf)
	}
	if string(body.Items["a"]) != `"one"` {
		t.Fatalf("items = %v", body.Items)
	}
}

func TestHistoricalEffectiveConfigDoesNotMutateState(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": "one"})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": "gray"})

	for _, asOf := range []string{"2020-01-01T00:00:00Z", "2099-01-01T00:00:00Z"} {
		recorder := doRequest(t, handler, http.MethodGet,
			historicalPath(historicalQuery("svc", "dev", asOf)), nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
		}
	}

	recorder := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("history status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var history struct {
		Versions []VersionInfo `json:"versions"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history.Versions) != 2 {
		t.Fatalf("versions = %+v, want the original two", history.Versions)
	}

	recorder = doRequest(t, handler, http.MethodGet, "/effective-configs?namespace=svc&environment=dev", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("effective status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if body["effectiveVersion"].(float64) != 1 {
		t.Fatalf("effectiveVersion = %v, want 1", body["effectiveVersion"])
	}
}
