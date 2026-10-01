package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func snapshotPath(ns, env, version string) string {
	return "/namespaces/" + ns + "/environments/" + env + "/config-versions/" + version
}

func TestVersionSnapshotReturnsFullStoredItemsAndMetadata(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{
		"num":   json.RawMessage(`1`),
		"flag":  true,
		"empty": nil,
		"text":  "one",
	})
	publish(t, handler, "payments", "prod", "canary", map[string]any{
		"num": json.RawMessage(`1.0`),
	})

	recorder := doRequest(t, handler, http.MethodGet, snapshotPath("payments", "prod", "2"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Namespace   string                     `json:"namespace"`
		Environment string                     `json:"environment"`
		Version     VersionInfo                `json:"version"`
		Items       map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Namespace != "payments" || body.Environment != "prod" {
		t.Fatalf("scope = %s/%s", body.Namespace, body.Environment)
	}
	if body.Version.Version != 2 {
		t.Fatalf("version = %d, want 2", body.Version.Version)
	}
	if body.Version.GrayTag == nil || *body.Version.GrayTag != "canary" {
		t.Fatalf("grayTag = %v", body.Version.GrayTag)
	}
	if body.Version.RollbackOf != nil {
		t.Fatalf("rollbackOf = %v, want nil", body.Version.RollbackOf)
	}
	if body.Version.Effective {
		t.Fatalf("gray snapshot must not be effective")
	}
	if body.Version.CreatedAt == "" {
		t.Fatalf("createdAt must be present")
	}
	if len(body.Items) != 1 || string(body.Items["num"]) != "1.0" {
		t.Fatalf("items = %v", body.Items)
	}

	first := doRequest(t, handler, http.MethodGet, snapshotPath("payments", "prod", "1"), nil)
	var firstBody struct {
		Version VersionInfo                `json:"version"`
		Items   map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !firstBody.Version.Effective || firstBody.Version.GrayTag != nil {
		t.Fatalf("first version metadata = %+v", firstBody.Version)
	}
	want := map[string]string{"num": "1", "flag": "true", "empty": "null", "text": `"one"`}
	if len(firstBody.Items) != len(want) {
		t.Fatalf("items = %v", firstBody.Items)
	}
	for name, value := range want {
		if string(firstBody.Items[name]) != value {
			t.Fatalf("item %s = %s, want %s", name, firstBody.Items[name], value)
		}
	}
}

func TestVersionSnapshotEmptyItemsIsEmptyObject(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{})

	recorder := doRequest(t, handler, http.MethodGet, snapshotPath("svc", "dev", "1"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := decode(t, recorder)
	items, ok := body["items"].(map[string]any)
	if !ok || len(items) != 0 {
		t.Fatalf("items = %v, want empty object", body["items"])
	}
}

func TestVersionSnapshotIncludesRollbackSource(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 2})
	rb := doRequest(t, handler, http.MethodPost,
		"/namespaces/svc/environments/dev/config-versions/1/rollback", nil)
	if rb.Code != http.StatusCreated {
		t.Fatalf("rollback status = %d body = %s", rb.Code, rb.Body.String())
	}

	recorder := doRequest(t, handler, http.MethodGet, snapshotPath("svc", "dev", "3"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Version VersionInfo                `json:"version"`
		Items   map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Version.RollbackOf == nil || *body.Version.RollbackOf != 1 {
		t.Fatalf("rollbackOf = %v, want 1", body.Version.RollbackOf)
	}
	if body.Version.GrayTag == nil || *body.Version.GrayTag != "canary" {
		t.Fatalf("copied grayTag = %v", body.Version.GrayTag)
	}
	if string(body.Items["a"]) != "1" {
		t.Fatalf("items = %v", body.Items)
	}
}

func TestVersionSnapshotErrorContract(t *testing.T) {
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
		{"zero", snapshotPath("svc", "dev", "0"), http.StatusBadRequest, "INVALID_VERSION"},
		{"negative", snapshotPath("svc", "dev", "-1"), http.StatusBadRequest, "INVALID_VERSION"},
		{"signed positive", "/namespaces/svc/environments/dev/config-versions/+1", http.StatusBadRequest, "INVALID_VERSION"},
		{"non numeric", snapshotPath("svc", "dev", "one"), http.StatusBadRequest, "INVALID_VERSION"},
		{"decimal fraction", snapshotPath("svc", "dev", "1.0"), http.StatusBadRequest, "INVALID_VERSION"},
		{"leading zero", "/namespaces/svc/environments/dev/config-versions/01", http.StatusBadRequest, "INVALID_VERSION"},
		{"missing everywhere", snapshotPath("svc", "dev", "99"), http.StatusNotFound, "VERSION_NOT_FOUND"},
		{"scope mismatch", snapshotPath("svc", "dev", "2"), http.StatusConflict, "VERSION_SCOPE_MISMATCH"},
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
			if _, hasMessage := errObj["message"].(string); !hasMessage {
				t.Fatalf("error must carry a string message: %v", errObj)
			}
		})
	}
}

func TestVersionSnapshotIsReadOnly(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 2})

	before := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	effectiveBefore := doRequest(t, handler, http.MethodGet,
		"/effective-configs?namespace=svc&environment=dev", nil)
	doRequest(t, handler, http.MethodGet, snapshotPath("svc", "dev", "2"), nil)
	after := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	effectiveAfter := doRequest(t, handler, http.MethodGet,
		"/effective-configs?namespace=svc&environment=dev", nil)
	if before.Body.String() != after.Body.String() {
		t.Fatalf("snapshot query changed history:\nbefore %s\nafter  %s", before.Body.String(), after.Body.String())
	}
	if effectiveBefore.Body.String() != effectiveAfter.Body.String() {
		t.Fatalf("snapshot query changed effective config:\nbefore %s\nafter  %s",
			effectiveBefore.Body.String(), effectiveAfter.Body.String())
	}
}
