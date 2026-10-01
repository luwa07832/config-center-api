package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func promotePath(ns, env, version string) string {
	return snapshotPath(ns, env, version) + "/promote"
}

func TestPromoteTurnsGrayVersionIntoEffectiveRelease(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{
		"num":   json.RawMessage(`1`),
		"flag":  true,
		"empty": nil,
	})
	publish(t, handler, "payments", "prod", "canary", map[string]any{
		"num": json.RawMessage(`1.0`),
		"new": "gray-only",
	})

	recorder := doRequest(t, handler, http.MethodPost, promotePath("payments", "prod", "2"), nil)
	if recorder.Code != http.StatusCreated {
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
	if body.Version.Namespace != "payments" || body.Version.Environment != "prod" {
		t.Fatalf("scope metadata = %s/%s", body.Version.Namespace, body.Version.Environment)
	}
	if body.Version.Version != 3 {
		t.Fatalf("version = %d, want 3", body.Version.Version)
	}
	if body.Version.GrayTag != nil {
		t.Fatalf("grayTag = %v, want nil", body.Version.GrayTag)
	}
	if body.Version.RollbackOf != nil {
		t.Fatalf("rollbackOf = %v, want nil", body.Version.RollbackOf)
	}
	if body.Version.PromotionOf == nil || *body.Version.PromotionOf != 2 {
		t.Fatalf("promotionOf = %v, want 2", body.Version.PromotionOf)
	}
	if !body.Version.Effective {
		t.Fatalf("promoted version must be effective")
	}
	if body.Version.CreatedAt == "" {
		t.Fatalf("createdAt must be present")
	}
	want := map[string]string{"num": "1.0", "new": `"gray-only"`}
	if len(body.Items) != len(want) {
		t.Fatalf("items = %v", body.Items)
	}
	for name, value := range want {
		if string(body.Items[name]) != value {
			t.Fatalf("item %s = %s, want %s", name, body.Items[name], value)
		}
	}

	effective := doRequest(t, handler, http.MethodGet,
		"/effective-configs?namespace=payments&environment=prod", nil)
	if effective.Code != http.StatusOK {
		t.Fatalf("effective status = %d", effective.Code)
	}
	effBody := decode(t, effective)
	if effBody["effectiveVersion"].(float64) != 3 {
		t.Fatalf("effectiveVersion = %v, want 3", effBody["effectiveVersion"])
	}
	if effBody["grayTag"] != nil {
		t.Fatalf("effective grayTag = %v, want null", effBody["grayTag"])
	}
}

func TestPromoteKeepsSourceAndHistoryUntouched(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 2})

	doRequest(t, handler, http.MethodPost, promotePath("svc", "dev", "2"), nil)

	source := doRequest(t, handler, http.MethodGet, snapshotPath("svc", "dev", "2"), nil)
	var sourceBody struct {
		Version VersionInfo                `json:"version"`
		Items   map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(source.Body.Bytes(), &sourceBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sourceBody.Version.GrayTag == nil || *sourceBody.Version.GrayTag != "canary" {
		t.Fatalf("source grayTag changed: %v", sourceBody.Version.GrayTag)
	}
	if sourceBody.Version.PromotionOf != nil {
		t.Fatalf("source promotionOf = %v, want nil", sourceBody.Version.PromotionOf)
	}
	if sourceBody.Version.Effective {
		t.Fatalf("source must not become effective")
	}

	history := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace=svc&environment=dev", nil)
	var list struct {
		Versions []VersionInfo `json:"versions"`
	}
	if err := json.Unmarshal(history.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Versions) != 3 {
		t.Fatalf("history length = %d, want 3", len(list.Versions))
	}
	if list.Versions[0].PromotionOf != nil || list.Versions[1].PromotionOf != nil {
		t.Fatalf("older versions must have null promotionOf: %+v", list.Versions)
	}
	if list.Versions[2].PromotionOf == nil || *list.Versions[2].PromotionOf != 2 {
		t.Fatalf("new version promotionOf = %v, want 2", list.Versions[2].PromotionOf)
	}
}

func TestPromoteErrorContract(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 2})
	publish(t, handler, "other", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "other", "dev", "", map[string]any{"a": 2})
	publish(t, handler, "other", "dev", "", map[string]any{"a": 3})

	cases := []struct {
		name       string
		target     string
		wantStatus int
		wantCode   string
	}{
		{"zero", promotePath("svc", "dev", "0"), http.StatusBadRequest, "INVALID_VERSION"},
		{"negative", promotePath("svc", "dev", "-1"), http.StatusBadRequest, "INVALID_VERSION"},
		{"signed", "/namespaces/svc/environments/dev/config-versions/+1/promote", http.StatusBadRequest, "INVALID_VERSION"},
		{"fraction", promotePath("svc", "dev", "1.0"), http.StatusBadRequest, "INVALID_VERSION"},
		{"leading zero", "/namespaces/svc/environments/dev/config-versions/01/promote", http.StatusBadRequest, "INVALID_VERSION"},
		{"missing everywhere", promotePath("svc", "dev", "99"), http.StatusNotFound, "VERSION_NOT_FOUND"},
		{"scope mismatch", promotePath("svc", "dev", "3"), http.StatusConflict, "VERSION_SCOPE_MISMATCH"},
		{"not gray", promotePath("svc", "dev", "1"), http.StatusConflict, "NOT_GRAY_VERSION"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := doRequest(t, handler, http.MethodPost, tc.target, nil)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d body = %s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if code := errorCode(decode(t, recorder)); code != tc.wantCode {
				t.Fatalf("code = %s, want %s", code, tc.wantCode)
			}
		})
	}
}

func TestPromoteFailureLeavesNoTrace(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})

	before := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace=svc&environment=dev", nil)
	effectiveBefore := doRequest(t, handler, http.MethodGet,
		"/effective-configs?namespace=svc&environment=dev", nil)

	doRequest(t, handler, http.MethodPost, promotePath("svc", "dev", "1"), nil)

	after := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace=svc&environment=dev", nil)
	effectiveAfter := doRequest(t, handler, http.MethodGet,
		"/effective-configs?namespace=svc&environment=dev", nil)
	if before.Body.String() != after.Body.String() {
		t.Fatalf("failed promote changed history:\nbefore %s\nafter  %s", before.Body.String(), after.Body.String())
	}
	if effectiveBefore.Body.String() != effectiveAfter.Body.String() {
		t.Fatalf("failed promote changed effective config:\nbefore %s\nafter  %s",
			effectiveBefore.Body.String(), effectiveAfter.Body.String())
	}
}

func TestPromotedVersionShowsInDiffAndItemHistory(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": json.RawMessage(`1`)})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": json.RawMessage(`1.0`)})
	doRequest(t, handler, http.MethodPost, promotePath("svc", "dev", "2"), nil)

	diff := doRequest(t, handler, http.MethodGet,
		"/config-version-diffs?namespace=svc&environment=dev&baseVersion=1&targetVersion=3", nil)
	if diff.Code != http.StatusOK {
		t.Fatalf("diff status = %d body = %s", diff.Code, diff.Body.String())
	}
	diffBody := decode(t, diff)
	target := diffBody["targetVersion"].(map[string]any)
	if target["promotionOf"].(float64) != 2 {
		t.Fatalf("target promotionOf = %v, want 2", target["promotionOf"])
	}
	if target["rollbackOf"] != nil || target["grayTag"] != nil {
		t.Fatalf("target metadata = %v", target)
	}
	if target["effective"].(bool) != true {
		t.Fatalf("target effective = %v, want true", target["effective"])
	}
	change := diffBody["changes"].([]any)[0].(map[string]any)
	if change["changeType"] != "modified" || change["newValue"].(float64) != 1 {
		// 1.0 decodes to float64(1); stored canonical JSON keeps 1.0 text before marshal.
		t.Fatalf("change = %v", change)
	}

	// Promotion copies the gray snapshot verbatim, so it never creates an item-level change
	// relative to the immediately preceding version. Every version object still exposes the
	// promotionOf field, even when null.
	itemHistory := doRequest(t, handler, http.MethodGet,
		"/config-item-histories?namespace=svc&environment=dev&name=a", nil)
	if itemHistory.Code != http.StatusOK {
		t.Fatalf("item history status = %d", itemHistory.Code)
	}
	historyBody := decode(t, itemHistory)
	for _, rawChange := range historyBody["changes"].([]any) {
		versionMeta := rawChange.(map[string]any)["version"].(map[string]any)
		if _, present := versionMeta["promotionOf"]; !present {
			t.Fatalf("version metadata must always expose promotionOf: %v", versionMeta)
		}
	}
	if historyBody["effectiveVersion"].(float64) != 3 {
		t.Fatalf("effectiveVersion = %v, want 3", historyBody["effectiveVersion"])
	}
}
