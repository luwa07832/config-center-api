package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/luwa07832/config-center-api/internal/store"

	_ "modernc.org/sqlite"
)

func historicalPath(ns, env, asOf string) string {
	query := url.Values{}
	query.Set("namespace", ns)
	query.Set("environment", env)
	query.Set("asOf", asOf)
	return "/historical-effective-configs?" + query.Encode()
}

// newHistoricalTestRouter returns a router backed by a known SQLite file so tests can rewrite
// created_at values through a second connection and make point-in-time queries deterministic.
func newHistoricalTestRouter(t *testing.T) (string, http.Handler) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "service.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return path, NewRouter(st)
}

// setVersionCreatedAt rewrites one version timestamp through another connection to the same file.
func setVersionCreatedAt(t *testing.T, dbPath, ns, env string, version int, createdAt string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open control db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(
		"UPDATE config_versions SET created_at = ? WHERE namespace = ? AND environment = ? AND version = ?",
		createdAt, ns, env, version); err != nil {
		t.Fatalf("rewrite created_at: %v", err)
	}
}

// publishTimed publishes a version and stamps it with the given UTC RFC3339 creation time.
func publishTimed(t *testing.T, handler http.Handler, dbPath, ns, env, grayTag, createdAt string,
	items map[string]any) {
	t.Helper()
	publish(t, handler, ns, env, grayTag, items)
	version := historyLength(t, handler, ns, env)
	setVersionCreatedAt(t, dbPath, ns, env, version, createdAt)
}

func historyLength(t *testing.T, handler http.Handler, ns, env string) int {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace="+ns+"&environment="+env, nil)
	var body struct {
		Versions []struct{} `json:"versions"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	return len(body.Versions)
}

func TestHistoricalEffectiveConfigReturnsFullReleaseAtPointInTime(t *testing.T) {
	dbPath, handler := newHistoricalTestRouter(t)
	publishTimed(t, handler, dbPath, "payments", "prod", "", "2026-10-01T10:00:00Z",
		map[string]any{"num": json.RawMessage(`1`), "flag": true, "empty": nil, "text": "one"})
	publishTimed(t, handler, dbPath, "payments", "prod", "canary", "2026-10-01T10:05:00Z",
		map[string]any{"num": json.RawMessage(`1.0`)})
	publishTimed(t, handler, dbPath, "payments", "prod", "", "2026-10-01T10:10:00Z",
		map[string]any{"num": json.RawMessage(`2`)})

	recorder := doRequest(t, handler, http.MethodGet,
		historicalPath("payments", "prod", "2026-10-01T10:07:00Z"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Namespace        string                     `json:"namespace"`
		Environment      string                     `json:"environment"`
		AsOf             string                     `json:"asOf"`
		EffectiveVersion *int64                     `json:"effectiveVersion"`
		Version          *VersionInfo               `json:"version"`
		Items            map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Namespace != "payments" || body.Environment != "prod" {
		t.Fatalf("scope = %s/%s", body.Namespace, body.Environment)
	}
	if body.AsOf != "2026-10-01T10:07:00Z" {
		t.Fatalf("asOf = %s", body.AsOf)
	}
	if body.EffectiveVersion == nil || *body.EffectiveVersion != 1 {
		t.Fatalf("effectiveVersion = %v, want 1", body.EffectiveVersion)
	}
	if body.Version == nil || body.Version.Version != 1 {
		t.Fatalf("version = %+v, want version 1", body.Version)
	}
	if body.Version.GrayTag != nil || body.Version.RollbackOf != nil || body.Version.PromotionOf != nil {
		t.Fatalf("version metadata = %+v", body.Version)
	}
	if body.Version.CreatedAt != "2026-10-01T10:00:00Z" {
		t.Fatalf("createdAt = %s", body.Version.CreatedAt)
	}
	if body.Version.Effective {
		t.Fatalf("historical version must not be flagged effective when a newer full release exists")
	}
	want := map[string]string{"num": "1", "flag": "true", "empty": "null", "text": `"one"`}
	if len(body.Items) != len(want) {
		t.Fatalf("items = %v", body.Items)
	}
	for name, value := range want {
		if string(body.Items[name]) != value {
			t.Fatalf("item %s = %s, want %s", name, body.Items[name], value)
		}
	}

	// As of the latest creation time the newest full release is selected.
	latest := doRequest(t, handler, http.MethodGet,
		historicalPath("payments", "prod", "2026-10-01T10:10:00Z"), nil)
	var latestBody struct {
		EffectiveVersion *int64      `json:"effectiveVersion"`
		Version          VersionInfo `json:"version"`
	}
	if err := json.Unmarshal(latest.Body.Bytes(), &latestBody); err != nil {
		t.Fatalf("decode latest: %v", err)
	}
	if latestBody.EffectiveVersion == nil || *latestBody.EffectiveVersion != 3 {
		t.Fatalf("latest effectiveVersion = %v, want 3", latestBody.EffectiveVersion)
	}
	if latestBody.Version.Version != 3 || !latestBody.Version.Effective {
		t.Fatalf("latest version metadata = %+v", latestBody.Version)
	}
}

func TestHistoricalEffectiveConfigEmptyCases(t *testing.T) {
	dbPath, handler := newHistoricalTestRouter(t)
	publishTimed(t, handler, dbPath, "payments", "prod", "", "2026-10-01T10:00:00Z",
		map[string]any{"a": 1})
	publishTimed(t, handler, dbPath, "grayonly", "prod", "canary", "2026-10-01T10:00:00Z",
		map[string]any{"a": 1})

	cases := []struct {
		name string
		ns   string
		env  string
		asOf string
	}{
		{"before first full release", "payments", "prod", "2026-10-01T09:59:59Z"},
		{"gray only scope", "grayonly", "prod", "2027-01-01T00:00:00Z"},
		{"scope with no versions", "missing", "prod", "2027-01-01T00:00:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := doRequest(t, handler, http.MethodGet, historicalPath(tc.ns, tc.env, tc.asOf), nil)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
			}
			body := decode(t, recorder)
			if body["effectiveVersion"] != nil {
				t.Fatalf("effectiveVersion = %v, want null", body["effectiveVersion"])
			}
			if body["version"] != nil {
				t.Fatalf("version = %v, want null", body["version"])
			}
			items, ok := body["items"].(map[string]any)
			if !ok || len(items) != 0 {
				t.Fatalf("items = %v, want empty object", body["items"])
			}
		})
	}
}

func TestHistoricalEffectiveConfigSameSecondTieUsesHighestVersion(t *testing.T) {
	dbPath, handler := newHistoricalTestRouter(t)
	// Versions 1 (full), 2 (gray), 3 (full) share one creation second.
	publishTimed(t, handler, dbPath, "svc", "dev", "", "2026-10-01T10:00:00Z",
		map[string]any{"n": 1})
	publishTimed(t, handler, dbPath, "svc", "dev", "canary", "2026-10-01T10:00:00Z",
		map[string]any{"n": 2})
	publishTimed(t, handler, dbPath, "svc", "dev", "", "2026-10-01T10:00:00Z",
		map[string]any{"n": 3})

	recorder := doRequest(t, handler, http.MethodGet,
		historicalPath("svc", "dev", "2026-10-01T10:00:00Z"), nil)
	var body struct {
		EffectiveVersion *int64 `json:"effectiveVersion"`
		Items            map[string]json.RawMessage
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.EffectiveVersion == nil || *body.EffectiveVersion != 3 {
		t.Fatalf("effectiveVersion = %v, want 3", body.EffectiveVersion)
	}
	if string(body.Items["n"]) != "3" {
		t.Fatalf("item = %s, want 3", body.Items["n"])
	}
}

func TestHistoricalEffectiveConfigNormalizesAsOfToUTC(t *testing.T) {
	dbPath, handler := newHistoricalTestRouter(t)
	publishTimed(t, handler, dbPath, "payments", "prod", "", "2026-10-01T00:00:00Z",
		map[string]any{"a": 1})

	// 2026-09-30T19:00:00-05:00 equals 2026-10-01T00:00:00Z and must include the release.
	recorder := doRequest(t, handler, http.MethodGet,
		historicalPath("payments", "prod", "2026-09-30T19:00:00-05:00"), nil)
	body := decode(t, recorder)
	if got := body["asOf"]; got != "2026-10-01T00:00:00Z" {
		t.Fatalf("normalized asOf = %v, want 2026-10-01T00:00:00Z", got)
	}
	if body["effectiveVersion"] == nil {
		t.Fatalf("offset asOf must resolve to the same UTC instant and select version 1: %v", body)
	}

	// Fractional seconds survive normalization.
	fractional := doRequest(t, handler, http.MethodGet,
		historicalPath("payments", "prod", "2026-10-01T00:00:00.5Z"), nil)
	fractionalBody := decode(t, fractional)
	if got := fractionalBody["asOf"]; got != "2026-10-01T00:00:00.5Z" {
		t.Fatalf("fractional asOf = %v, want 2026-10-01T00:00:00.5Z", got)
	}
}

func TestHistoricalEffectiveConfigSelectsRollbackAndPromotionFullReleases(t *testing.T) {
	dbPath, handler := newHistoricalTestRouter(t)
	publishTimed(t, handler, dbPath, "payments", "prod", "", "2026-10-01T10:00:01Z",
		map[string]any{"a": "v1"})
	publishTimed(t, handler, dbPath, "payments", "prod", "canary", "2026-10-01T10:00:02Z",
		map[string]any{"a": "gray"})

	promote := doRequest(t, handler, http.MethodPost,
		"/namespaces/payments/environments/prod/config-versions/2/promote", nil)
	if promote.Code != http.StatusCreated {
		t.Fatalf("promote status = %d body = %s", promote.Code, promote.Body.String())
	}
	setVersionCreatedAt(t, dbPath, "payments", "prod", 3, "2026-10-01T10:00:03Z")

	rollback := doRequest(t, handler, http.MethodPost,
		"/namespaces/payments/environments/prod/config-versions/1/rollback", nil)
	if rollback.Code != http.StatusCreated {
		t.Fatalf("rollback status = %d body = %s", rollback.Code, rollback.Body.String())
	}
	setVersionCreatedAt(t, dbPath, "payments", "prod", 4, "2026-10-01T10:00:04Z")

	atPromotion := doRequest(t, handler, http.MethodGet,
		historicalPath("payments", "prod", "2026-10-01T10:00:03Z"), nil)
	var promotionBody struct {
		EffectiveVersion *int64      `json:"effectiveVersion"`
		Version          VersionInfo `json:"version"`
		Items            map[string]json.RawMessage
	}
	if err := json.Unmarshal(atPromotion.Body.Bytes(), &promotionBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if promotionBody.EffectiveVersion == nil || *promotionBody.EffectiveVersion != 3 {
		t.Fatalf("effectiveVersion = %v, want 3", promotionBody.EffectiveVersion)
	}
	if promotionBody.Version.PromotionOf == nil || *promotionBody.Version.PromotionOf != 2 {
		t.Fatalf("promotionOf = %v, want 2", promotionBody.Version.PromotionOf)
	}
	if promotionBody.Version.GrayTag != nil || promotionBody.Version.RollbackOf != nil {
		t.Fatalf("promoted metadata = %+v", promotionBody.Version)
	}
	if string(promotionBody.Items["a"]) != `"gray"` {
		t.Fatalf("promoted items = %v", promotionBody.Items)
	}

	atRollback := doRequest(t, handler, http.MethodGet,
		historicalPath("payments", "prod", "2026-10-01T10:00:04Z"), nil)
	var rollbackBody struct {
		EffectiveVersion *int64      `json:"effectiveVersion"`
		Version          VersionInfo `json:"version"`
		Items            map[string]json.RawMessage
	}
	if err := json.Unmarshal(atRollback.Body.Bytes(), &rollbackBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rollbackBody.EffectiveVersion == nil || *rollbackBody.EffectiveVersion != 4 {
		t.Fatalf("effectiveVersion = %v, want 4", rollbackBody.EffectiveVersion)
	}
	if rollbackBody.Version.RollbackOf == nil || *rollbackBody.Version.RollbackOf != 1 {
		t.Fatalf("rollbackOf = %v, want 1", rollbackBody.Version.RollbackOf)
	}
	if string(rollbackBody.Items["a"]) != `"v1"` {
		t.Fatalf("rollback items = %v", rollbackBody.Items)
	}
}

func TestHistoricalEffectiveConfigIsReadOnly(t *testing.T) {
	dbPath, handler := newHistoricalTestRouter(t)
	publishTimed(t, handler, dbPath, "svc", "dev", "", "2026-10-01T10:00:00Z", map[string]any{"a": 1})
	publishTimed(t, handler, dbPath, "svc", "dev", "canary", "2026-10-01T10:05:00Z", map[string]any{"a": 2})

	before := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	effectiveBefore := doRequest(t, handler, http.MethodGet, "/effective-configs?namespace=svc&environment=dev", nil)
	doRequest(t, handler, http.MethodGet, historicalPath("svc", "dev", "2026-10-01T10:05:00Z"), nil)
	after := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	effectiveAfter := doRequest(t, handler, http.MethodGet, "/effective-configs?namespace=svc&environment=dev", nil)
	if before.Body.String() != after.Body.String() {
		t.Fatalf("historical query changed history:\nbefore %s\nafter  %s", before.Body.String(), after.Body.String())
	}
	if effectiveBefore.Body.String() != effectiveAfter.Body.String() {
		t.Fatalf("historical query changed effective config")
	}
}

func TestHistoricalEffectiveConfigErrorContract(t *testing.T) {
	_, handler := newTestRouter(t)
	cases := []struct {
		name       string
		target     string
		wantStatus int
		wantCode   string
	}{
		{"missing namespace", "/historical-effective-configs?environment=prod&asOf=2026-10-01T00:00:00Z", http.StatusBadRequest, "MISSING_SCOPE"},
		{"missing environment", "/historical-effective-configs?namespace=svc&asOf=2026-10-01T00:00:00Z", http.StatusBadRequest, "MISSING_SCOPE"},
		{"missing asOf", "/historical-effective-configs?namespace=svc&environment=prod", http.StatusBadRequest, "MISSING_AS_OF"},
		{"empty asOf", "/historical-effective-configs?namespace=svc&environment=prod&asOf=", http.StatusBadRequest, "INVALID_TIMESTAMP"},
		{"date only", historicalPath("svc", "prod", "2026-10-01"), http.StatusBadRequest, "INVALID_TIMESTAMP"},
		{"garbage", historicalPath("svc", "prod", "not-a-time"), http.StatusBadRequest, "INVALID_TIMESTAMP"},
		{"whitespace", historicalPath("svc", "prod", " 2026-10-01T00:00:00Z "), http.StatusBadRequest, "INVALID_TIMESTAMP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := doRequest(t, handler, http.MethodGet, tc.target, nil)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d body = %s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			body := decode(t, recorder)
			if code := errorCode(body); code != tc.wantCode {
				t.Fatalf("code = %s, want %s", code, tc.wantCode)
			}
			errObj := body["error"].(map[string]any)
			if message, _ := errObj["message"].(string); message == "" {
				t.Fatalf("error must carry a non-empty message: %v", errObj)
			}
		})
	}
}

func TestHistoricalEffectiveConfigStorageUnavailable(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "service.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	handler := NewRouter(st)
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	recorder := doRequest(t, handler, http.MethodGet,
		historicalPath("svc", "prod", "2026-10-01T00:00:00Z"), nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body = %s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
	body := decode(t, recorder)
	if code := errorCode(body); code != "storage_unavailable" {
		t.Fatalf("code = %s, want storage_unavailable", code)
	}
}
