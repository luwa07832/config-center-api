package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func publishRaw(t *testing.T, handler http.Handler, ns, env, body string) {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodPost,
		"/namespaces/"+ns+"/environments/"+env+"/config-versions", json.RawMessage(body))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("publish status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestEffectiveConfigDiffReportsAddedRemovedModified(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "", map[string]any{
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
		"/effective-config-diffs?namespace=payments&baseEnvironment=staging&targetEnvironment=prod", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if body["namespace"] != "payments" || body["baseEnvironment"] != "staging" || body["targetEnvironment"] != "prod" {
		t.Fatalf("scope = %v %v %v", body["namespace"], body["baseEnvironment"], body["targetEnvironment"])
	}
	if body["changedCount"].(float64) != 3 {
		t.Fatalf("changedCount = %v, want 3", body["changedCount"])
	}
	baseVersion := body["baseVersion"].(map[string]any)
	if baseVersion["version"].(float64) != 1 || baseVersion["environment"] != "staging" || baseVersion["effective"] != true {
		t.Fatalf("baseVersion = %v", baseVersion)
	}
	targetVersion := body["targetVersion"].(map[string]any)
	if targetVersion["version"].(float64) != 1 || targetVersion["environment"] != "prod" || targetVersion["effective"] != true {
		t.Fatalf("targetVersion = %v", targetVersion)
	}
	changes, _ := body["changes"].([]any)
	if len(changes) != 3 {
		t.Fatalf("changes = %v", body["changes"])
	}
	ordered := []string{"added", "alpha", "removed"}
	for i, entry := range changes {
		if name := entry.(map[string]any)["name"].(string); name != ordered[i] {
			t.Fatalf("change %d name = %v, want %s", i, name, ordered[i])
		}
	}
	added := changes[0].(map[string]any)
	if added["changeType"] != "added" {
		t.Fatalf("added change = %v", added)
	}
	if value, present := added["newValue"]; !present || value != nil {
		t.Fatalf("added change newValue = %v (present %v), want JSON null", value, present)
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
}

func TestEffectiveConfigDiffPathFormMatchesQueryForm(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "", map[string]any{"alpha": "old"})
	publish(t, handler, "payments", "prod", "", map[string]any{"alpha": "new"})
	query := doRequest(t, handler, http.MethodGet,
		"/effective-config-diffs?namespace=payments&baseEnvironment=staging&targetEnvironment=prod", nil)
	path := doRequest(t, handler, http.MethodGet,
		"/namespaces/payments/effective-config-diffs/staging/prod", nil)
	if query.Code != http.StatusOK || path.Code != http.StatusOK {
		t.Fatalf("statuses = %d %d", query.Code, path.Code)
	}
	if query.Body.String() != path.Body.String() {
		t.Fatalf("path form = %s, query form = %s", path.Body.String(), query.Body.String())
	}
}

func TestEffectiveConfigDiffWithNoEffectiveVersions(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "canary", map[string]any{"alpha": "gray"})
	recorder := doRequest(t, handler, http.MethodGet,
		"/effective-config-diffs?namespace=payments&baseEnvironment=staging&targetEnvironment=prod", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if body["baseVersion"] != nil || body["targetVersion"] != nil {
		t.Fatalf("versions = %v %v, want both null", body["baseVersion"], body["targetVersion"])
	}
	if body["changedCount"].(float64) != 0 {
		t.Fatalf("changedCount = %v, want 0", body["changedCount"])
	}
	changes, ok := body["changes"].([]any)
	if !ok || len(changes) != 0 {
		t.Fatalf("changes = %v, want empty array", body["changes"])
	}
}

func TestEffectiveConfigDiffWithOneSideMissing(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"alpha": "new", "retries": 3})
	recorder := doRequest(t, handler, http.MethodGet,
		"/effective-config-diffs?namespace=payments&baseEnvironment=staging&targetEnvironment=prod", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if body["baseVersion"] != nil {
		t.Fatalf("baseVersion = %v, want null", body["baseVersion"])
	}
	if body["targetVersion"].(map[string]any)["version"].(float64) != 1 {
		t.Fatalf("targetVersion = %v", body["targetVersion"])
	}
	changes, _ := body["changes"].([]any)
	if len(changes) != 2 || body["changedCount"].(float64) != 2 {
		t.Fatalf("changes = %v changedCount = %v", body["changes"], body["changedCount"])
	}
	for _, entry := range changes {
		if change := entry.(map[string]any); change["changeType"] != "added" {
			t.Fatalf("change = %v, want added", change)
		}
	}
	reverse := doRequest(t, handler, http.MethodGet,
		"/effective-config-diffs?namespace=payments&baseEnvironment=prod&targetEnvironment=staging", nil)
	if reverse.Code != http.StatusOK {
		t.Fatalf("reverse status = %d body = %s", reverse.Code, reverse.Body.String())
	}
	reverseBody := decode(t, reverse)
	reverseChanges, _ := reverseBody["changes"].([]any)
	if len(reverseChanges) != 2 {
		t.Fatalf("reverse changes = %v", reverseBody["changes"])
	}
	for _, entry := range reverseChanges {
		if change := entry.(map[string]any); change["changeType"] != "removed" {
			t.Fatalf("reverse change = %v, want removed", change)
		}
	}
}

func TestEffectiveConfigDiffPreservesRawJSONSemantics(t *testing.T) {
	_, handler := newTestRouter(t)
	publishRaw(t, handler, "payments", "staging",
		`{"items": {"count": 1, "label": "1", "obj": {"a": 1, "b": 2}, "flag": true}}`)
	publishRaw(t, handler, "payments", "prod",
		`{"items": {"count": 1.0, "label": 1, "obj": { "b": 2,  "a": 1 }, "flag": true}}`)
	recorder := doRequest(t, handler, http.MethodGet,
		"/effective-config-diffs?namespace=payments&baseEnvironment=staging&targetEnvironment=prod", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if body["changedCount"].(float64) != 2 {
		t.Fatalf("changedCount = %v, want 2 (whitespace and key order ignored): %v", body["changedCount"], body["changes"])
	}
	changes, _ := body["changes"].([]any)
	count := changes[0].(map[string]any)
	if count["name"] != "count" || count["changeType"] != "modified" {
		t.Fatalf("count change = %v", count)
	}
	if count["oldValue"].(float64) != 1 || count["newValue"].(float64) != 1 {
		t.Fatalf("count change values = %v", count)
	}
	label := changes[1].(map[string]any)
	if label["name"] != "label" || label["changeType"] != "modified" {
		t.Fatalf("label change = %v", label)
	}
	if label["oldValue"] != "1" || label["newValue"].(float64) != 1 {
		t.Fatalf("label change values = %v, want string \"1\" vs number 1", label)
	}
}

func TestEffectiveConfigDiffValidation(t *testing.T) {
	_, handler := newTestRouter(t)
	cases := []struct {
		name   string
		target string
		status int
		code   string
	}{
		{"missing namespace", "/effective-config-diffs?baseEnvironment=staging&targetEnvironment=prod", http.StatusBadRequest, "MISSING_SCOPE"},
		{"missing base", "/effective-config-diffs?namespace=payments&targetEnvironment=prod", http.StatusBadRequest, "MISSING_SCOPE"},
		{"missing target", "/effective-config-diffs?namespace=payments&baseEnvironment=staging", http.StatusBadRequest, "MISSING_SCOPE"},
		{"same environment", "/effective-config-diffs?namespace=payments&baseEnvironment=prod&targetEnvironment=prod", http.StatusBadRequest, "SAME_ENVIRONMENT"},
		{"same environment path", "/namespaces/payments/effective-config-diffs/prod/prod", http.StatusBadRequest, "SAME_ENVIRONMENT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := doRequest(t, handler, http.MethodGet, tc.target, nil)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
			}
			if code := errorCode(decode(t, recorder)); code != tc.code {
				t.Fatalf("code = %q, want %q", code, tc.code)
			}
		})
	}
}

func TestEffectiveConfigDiffIsReadOnly(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "", map[string]any{"alpha": "old"})
	publish(t, handler, "payments", "staging", "canary", map[string]any{"alpha": "gray"})
	publish(t, handler, "payments", "prod", "", map[string]any{"alpha": "new"})
	historyBefore := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace=payments&environment=staging", nil).Body.String()
	effectiveBefore := doRequest(t, handler, http.MethodGet,
		"/effective-configs?namespace=payments&environment=staging", nil).Body.String()
	recorder := doRequest(t, handler, http.MethodGet,
		"/effective-config-diffs?namespace=payments&baseEnvironment=staging&targetEnvironment=prod", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	historyAfter := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace=payments&environment=staging", nil).Body.String()
	effectiveAfter := doRequest(t, handler, http.MethodGet,
		"/effective-configs?namespace=payments&environment=staging", nil).Body.String()
	if historyBefore != historyAfter {
		t.Fatalf("history changed: %s -> %s", historyBefore, historyAfter)
	}
	if effectiveBefore != effectiveAfter {
		t.Fatalf("effective config changed: %s -> %s", effectiveBefore, effectiveAfter)
	}
}
