package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/luwa07832/config-center-api/internal/store"
)

func newTestRouter(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "service.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, NewRouter(st)
}

func doRequest(t *testing.T, handler http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

func publish(t *testing.T, handler http.Handler, ns, env, grayTag string, items map[string]any) {
	t.Helper()
	payload := map[string]any{"items": items}
	if grayTag != "" {
		payload["grayTag"] = grayTag
	}
	recorder := doRequest(t, handler, http.MethodPost,
		"/namespaces/"+ns+"/environments/"+env+"/config-versions", payload)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("publish status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func decode(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return body
}

func errorCode(body map[string]any) string {
	entry, ok := body["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := entry["code"].(string)
	return code
}

func TestVersionDiffReportsAddedRemovedModifiedAndEffectiveImpact(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{
		"alpha":   "old",
		"removed": 10,
		"shared":  true,
	})
	publish(t, handler, "payments", "prod", "", map[string]any{
		"alpha":  "new",
		"added":  nil,
		"shared": true,
	})

	recorder := doRequest(t, handler, http.MethodGet,
		"/config-version-diffs?namespace=payments&environment=prod&baseVersion=1&targetVersion=2", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if body["changedCount"].(float64) != 3 {
		t.Fatalf("changedCount = %v, want 3", body["changedCount"])
	}
	changes, _ := body["changes"].([]any)
	if len(changes) != 3 {
		t.Fatalf("changes = %v", body["changes"])
	}
	ordered := []string{"added", "alpha", "removed"}
	for i, entry := range changes {
		change := entry.(map[string]any)
		if change["name"].(string) != ordered[i] {
			t.Fatalf("change %d name = %v, want %s", i, change["name"], ordered[i])
		}
		if change["affectsEffectiveConfig"] != true {
			t.Fatalf("%s should affect the effective config: %v", ordered[i], change)
		}
	}
	added := changes[0].(map[string]any)
	if added["changeType"] != "added" || added["newValue"] != nil {
		t.Fatalf("added change = %v", added)
	}
	if _, present := added["oldValue"]; present {
		t.Fatalf("added change must not carry oldValue: %v", added)
	}
	modified := changes[1].(map[string]any)
	if modified["changeType"] != "modified" || modified["oldValue"] != "old" || modified["newValue"] != "new" {
		t.Fatalf("modified change = %v", modified)
	}
	removed := changes[2].(map[string]any)
	if removed["changeType"] != "removed" || removed["oldValue"].(float64) != 10 {
		t.Fatalf("removed change = %v", removed)
	}
	if _, present := removed["newValue"]; present {
		t.Fatalf("removed change must not carry newValue: %v", removed)
	}

	base := body["baseVersion"].(map[string]any)
	target := body["targetVersion"].(map[string]any)
	if base["version"].(float64) != 1 || target["version"].(float64) != 2 {
		t.Fatalf("version metadata = %v %v", base, target)
	}
	if base["effective"] != false || target["effective"] != true {
		t.Fatalf("effective flags = %v %v", base["effective"], target["effective"])
	}
	if body["effectiveVersion"].(float64) != 2 {
		t.Fatalf("effectiveVersion = %v", body["effectiveVersion"])
	}
}

type rawChange struct {
	Name             string          `json:"name"`
	ChangeType       string          `json:"changeType"`
	OldValue         json.RawMessage `json:"oldValue"`
	NewValue         json.RawMessage `json:"newValue"`
	AffectsEffective bool            `json:"affectsEffectiveConfig"`
}

type rawDiffResponse struct {
	Changes []rawChange `json:"changes"`
}

func TestVersionDiffPreservesRawValueSemantics(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{
		"num":  json.RawMessage(`1`),
		"flag": json.RawMessage(`true`),
		"text": json.RawMessage(`"1"`),
	})
	publish(t, handler, "svc", "dev", "", map[string]any{
		"num":  json.RawMessage(`1.0`),
		"flag": json.RawMessage(`false`),
		"text": json.RawMessage(`"01"`),
	})

	recorder := doRequest(t, handler, http.MethodGet,
		"/config-version-diffs?namespace=svc&environment=dev&baseVersion=1&targetVersion=2", nil)
	var body rawDiffResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byName := map[string]rawChange{}
	for _, change := range body.Changes {
		byName[change.Name] = change
	}
	if len(byName) != 3 {
		t.Fatalf("changes = %v", body.Changes)
	}
	if string(byName["num"].OldValue) != "1" || string(byName["num"].NewValue) != "1.0" {
		t.Fatalf("number semantics changed: %v", byName["num"])
	}
	if string(byName["flag"].OldValue) != "true" || string(byName["flag"].NewValue) != "false" {
		t.Fatalf("boolean semantics changed: %v", byName["flag"])
	}
	if string(byName["text"].OldValue) != `"1"` || string(byName["text"].NewValue) != `"01"` {
		t.Fatalf("string semantics changed: %v", byName["text"])
	}
}

func TestVersionDiffSameVersionsReturnsEmptyChangeSet(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})

	recorder := doRequest(t, handler, http.MethodGet,
		"/config-version-diffs?namespace=svc&environment=dev&baseVersion=1&targetVersion=1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := decode(t, recorder)
	if body["changedCount"].(float64) != 0 {
		t.Fatalf("changedCount = %v", body["changedCount"])
	}
	if changes, ok := body["changes"].([]any); !ok || len(changes) != 0 {
		t.Fatalf("changes = %v", body["changes"])
	}
}

func TestVersionDiffGrayTargetDoesNotAffectEffectiveConfig(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "prod", "", map[string]any{"a": "stable"})
	publish(t, handler, "svc", "prod", "canary", map[string]any{"a": "gray"})

	recorder := doRequest(t, handler, http.MethodGet,
		"/config-version-diffs?namespace=svc&environment=prod&baseVersion=1&targetVersion=2", nil)
	body := decode(t, recorder)
	change := body["changes"].([]any)[0].(map[string]any)
	if change["affectsEffectiveConfig"] != false {
		t.Fatalf("gray target change must not affect effective config: %v", change)
	}
	target := body["targetVersion"].(map[string]any)
	if target["grayTag"] != "canary" || target["effective"] != false {
		t.Fatalf("target metadata = %v", target)
	}
	if body["effectiveVersion"].(float64) != 1 {
		t.Fatalf("effectiveVersion = %v", body["effectiveVersion"])
	}
}

func TestVersionDiffIncludesRollbackSource(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 2})
	rb := doRequest(t, handler, http.MethodPost,
		"/namespaces/svc/environments/dev/config-versions/1/rollback", nil)
	if rb.Code != http.StatusCreated {
		t.Fatalf("rollback status = %d body = %s", rb.Code, rb.Body.String())
	}

	recorder := doRequest(t, handler, http.MethodGet,
		"/config-version-diffs?namespace=svc&environment=dev&baseVersion=2&targetVersion=3", nil)
	body := decode(t, recorder)
	target := body["targetVersion"].(map[string]any)
	if target["rollbackOf"].(float64) != 1 {
		t.Fatalf("rollbackOf = %v, want 1", target["rollbackOf"])
	}
	if target["grayTag"] != nil {
		t.Fatalf("full release grayTag must be null: %v", target["grayTag"])
	}
}

func TestVersionDiffErrorContract(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "other", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "other", "dev", "", map[string]any{"a": 2})

	cases := []struct {
		name       string
		target     string
		wantStatus int
		wantCode   string
	}{
		{"missing namespace", "/config-version-diffs?environment=dev&baseVersion=1&targetVersion=2", http.StatusBadRequest, "MISSING_SCOPE"},
		{"missing environment", "/config-version-diffs?namespace=svc&baseVersion=1&targetVersion=2", http.StatusBadRequest, "MISSING_SCOPE"},
		{"base zero", "/config-version-diffs?namespace=svc&environment=dev&baseVersion=0&targetVersion=2", http.StatusBadRequest, "INVALID_VERSION"},
		{"target negative", "/config-version-diffs?namespace=svc&environment=dev&baseVersion=1&targetVersion=-2", http.StatusBadRequest, "INVALID_VERSION"},
		{"target non numeric", "/config-version-diffs?namespace=svc&environment=dev&baseVersion=1&targetVersion=two", http.StatusBadRequest, "INVALID_VERSION"},
		{"target below base", "/config-version-diffs?namespace=svc&environment=dev&baseVersion=2&targetVersion=1", http.StatusConflict, "VERSION_ORDER_CONFLICT"},
		{"missing version", "/config-version-diffs?namespace=svc&environment=dev&baseVersion=1&targetVersion=99", http.StatusNotFound, "VERSION_NOT_FOUND"},
		{"scope mismatch", "/config-version-diffs?namespace=svc&environment=dev&baseVersion=1&targetVersion=2", http.StatusConflict, "VERSION_SCOPE_MISMATCH"},
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
}

func TestVersionDiffCreatesNoHistoryWrite(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 2})

	before := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	doRequest(t, handler, http.MethodGet,
		"/config-version-diffs?namespace=svc&environment=dev&baseVersion=1&targetVersion=2", nil)
	after := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	if before.Body.String() != after.Body.String() {
		t.Fatalf("diff query changed history:\nbefore %s\nafter  %s", before.Body.String(), after.Body.String())
	}

	history := decode(t, after)
	versions := history["versions"].([]any)
	if len(versions) != 2 {
		t.Fatalf("history length = %d, want 2", len(versions))
	}
}

func TestEffectiveAndHistoryEntriesKeepWorking(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "prod", "", map[string]any{"a": "live"})
	publish(t, handler, "svc", "prod", "gray", map[string]any{"a": "canary"})

	effective := doRequest(t, handler, http.MethodGet,
		"/effective-configs?namespace=svc&environment=prod", nil)
	if effective.Code != http.StatusOK {
		t.Fatalf("effective status = %d", effective.Code)
	}
	body := decode(t, effective)
	if body["effectiveVersion"].(float64) != 1 {
		t.Fatalf("effectiveVersion = %v", body["effectiveVersion"])
	}
	items := body["items"].(map[string]any)
	if items["a"] != "live" {
		t.Fatalf("effective items = %v", items)
	}

	missingScope := doRequest(t, handler, http.MethodGet, "/effective-configs?namespace=svc", nil)
	if missingScope.Code != http.StatusBadRequest || errorCode(decode(t, missingScope)) != "MISSING_SCOPE" {
		t.Fatalf("effective scope guard = %d %s", missingScope.Code, missingScope.Body.String())
	}
}
