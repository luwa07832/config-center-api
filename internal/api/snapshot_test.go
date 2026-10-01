package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type snapshotResponse struct {
	Namespace   string                     `json:"namespace"`
	Environment string                     `json:"environment"`
	Version     map[string]any             `json:"version"`
	Items       map[string]json.RawMessage `json:"items"`
}

func decodeSnapshot(t *testing.T, body string) snapshotResponse {
	t.Helper()
	var snapshot snapshotResponse
	if err := json.Unmarshal([]byte(body), &snapshot); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return snapshot
}

func TestVersionSnapshotReturnsStoredItemsAndMetadata(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"retries": 3})
	publish(t, handler, "payments", "prod", "canary", map[string]any{"retries": 5})
	rollback := doRequest(t, handler, http.MethodPost,
		"/namespaces/payments/environments/prod/config-versions/1/rollback", nil)
	if rollback.Code != http.StatusCreated {
		t.Fatalf("rollback status = %d body = %s", rollback.Code, rollback.Body.String())
	}

	recorder := doRequest(t, handler, http.MethodGet,
		"/namespaces/payments/environments/prod/config-versions/2", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	snapshot := decodeSnapshot(t, recorder.Body.String())
	if snapshot.Namespace != "payments" || snapshot.Environment != "prod" {
		t.Fatalf("scope = %s/%s", snapshot.Namespace, snapshot.Environment)
	}
	version := snapshot.Version
	if version["version"].(float64) != 2 {
		t.Fatalf("version = %v", version["version"])
	}
	if version["grayTag"] != "canary" {
		t.Fatalf("grayTag = %v", version["grayTag"])
	}
	if version["rollbackOf"] != nil {
		t.Fatalf("rollbackOf = %v", version["rollbackOf"])
	}
	if version["effective"] != false {
		t.Fatalf("gray release must not be effective: %v", version)
	}
	if createdAt, _ := version["createdAt"].(string); createdAt == "" {
		t.Fatalf("createdAt missing: %v", version)
	}
	if string(snapshot.Items["retries"]) != "5" {
		t.Fatalf("items = %s", recorder.Body.String())
	}

	rolledBack := doRequest(t, handler, http.MethodGet,
		"/namespaces/payments/environments/prod/config-versions/3", nil)
	rolledBackSnapshot := decodeSnapshot(t, rolledBack.Body.String())
	if rolledBackSnapshot.Version["rollbackOf"].(float64) != 1 {
		t.Fatalf("rollbackOf = %v", rolledBackSnapshot.Version["rollbackOf"])
	}
	if rolledBackSnapshot.Version["effective"] != true {
		t.Fatalf("full release must be effective: %v", rolledBackSnapshot.Version)
	}
	if string(rolledBackSnapshot.Items["retries"]) != "3" {
		t.Fatalf("rolled back items = %s", rolledBack.Body.String())
	}
}

func TestVersionSnapshotPreservesRawValueSemantics(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{
		"int":   json.RawMessage(`1`),
		"float": json.RawMessage(`1.0`),
		"text":  json.RawMessage(`"1"`),
		"flag":  json.RawMessage(`true`),
		"empty": json.RawMessage(`null`),
	})

	recorder := doRequest(t, handler, http.MethodGet,
		"/namespaces/svc/environments/dev/config-versions/1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	snapshot := decodeSnapshot(t, recorder.Body.String())
	want := map[string]string{
		"int":   "1",
		"float": "1.0",
		"text":  `"1"`,
		"flag":  "true",
		"empty": "null",
	}
	if len(snapshot.Items) != len(want) {
		t.Fatalf("items = %s", recorder.Body.String())
	}
	for name, raw := range want {
		if string(snapshot.Items[name]) != raw {
			t.Fatalf("item %s = %s, want %s", name, snapshot.Items[name], raw)
		}
	}
}

func TestVersionSnapshotWithNoItemsReturnsEmptyObject(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{})

	recorder := doRequest(t, handler, http.MethodGet,
		"/namespaces/svc/environments/dev/config-versions/1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"items":{}`) {
		t.Fatalf("items must be an empty object: %s", recorder.Body.String())
	}
}

func TestVersionSnapshotErrorContract(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "other", "prod", "", map[string]any{"a": 1})
	publish(t, handler, "other", "prod", "", map[string]any{"a": 2})

	cases := []struct {
		name       string
		target     string
		wantStatus int
		wantCode   string
	}{
		{"zero", "/namespaces/svc/environments/dev/config-versions/0", http.StatusBadRequest, "INVALID_VERSION"},
		{"negative", "/namespaces/svc/environments/dev/config-versions/-1", http.StatusBadRequest, "INVALID_VERSION"},
		{"signed", "/namespaces/svc/environments/dev/config-versions/+1", http.StatusBadRequest, "INVALID_VERSION"},
		{"decimal", "/namespaces/svc/environments/dev/config-versions/1.0", http.StatusBadRequest, "INVALID_VERSION"},
		{"leading zero", "/namespaces/svc/environments/dev/config-versions/01", http.StatusBadRequest, "INVALID_VERSION"},
		{"non numeric", "/namespaces/svc/environments/dev/config-versions/one", http.StatusBadRequest, "INVALID_VERSION"},
		{"missing everywhere", "/namespaces/svc/environments/dev/config-versions/99", http.StatusNotFound, "VERSION_NOT_FOUND"},
		{"other scope", "/namespaces/svc/environments/dev/config-versions/2", http.StatusConflict, "VERSION_SCOPE_MISMATCH"},
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
			if len(body) != 1 {
				t.Fatalf("error body must only carry the error object: %v", body)
			}
			entry := body["error"].(map[string]any)
			if message, ok := entry["message"].(string); !ok || message == "" {
				t.Fatalf("error message missing: %v", entry)
			}
		})
	}
}

func TestVersionSnapshotIsReadOnly(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 2})

	historyBefore := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	effectiveBefore := doRequest(t, handler, http.MethodGet, "/effective-configs?namespace=svc&environment=dev", nil)
	for _, target := range []string{
		"/namespaces/svc/environments/dev/config-versions/1",
		"/namespaces/svc/environments/dev/config-versions/2",
		"/namespaces/svc/environments/dev/config-versions/99",
	} {
		doRequest(t, handler, http.MethodGet, target, nil)
	}
	historyAfter := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	effectiveAfter := doRequest(t, handler, http.MethodGet, "/effective-configs?namespace=svc&environment=dev", nil)
	if historyBefore.Body.String() != historyAfter.Body.String() {
		t.Fatalf("snapshot query changed history:\nbefore %s\nafter  %s", historyBefore.Body.String(), historyAfter.Body.String())
	}
	if effectiveBefore.Body.String() != effectiveAfter.Body.String() {
		t.Fatalf("snapshot query changed effective config:\nbefore %s\nafter  %s", effectiveBefore.Body.String(), effectiveAfter.Body.String())
	}
}
