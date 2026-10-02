package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestEffectiveConfigDiffReportsAddedRemovedModifiedSortedByName(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "", map[string]any{
		"alpha":   "old",
		"removed": 10,
		"shared":  true,
	})
	publish(t, handler, "payments", "prod", "", map[string]any{
		"added":  nil,
		"alpha":  "new",
		"shared": true,
	})

	recorder := doRequest(t, handler, http.MethodGet,
		"/effective-config-diffs?namespace=payments&baseEnvironment=staging&targetEnvironment=prod", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if body["namespace"] != "payments" || body["baseEnvironment"] != "staging" || body["targetEnvironment"] != "prod" {
		t.Fatalf("scope fields = %v", body)
	}
	if body["changedCount"].(float64) != 3 {
		t.Fatalf("changedCount = %v, want 3", body["changedCount"])
	}
	base := body["baseVersion"].(map[string]any)
	if base["environment"] != "staging" || base["version"].(float64) != 1 || base["effective"] != true {
		t.Fatalf("baseVersion = %v", base)
	}
	target := body["targetVersion"].(map[string]any)
	if target["environment"] != "prod" || target["version"].(float64) != 1 || target["effective"] != true {
		t.Fatalf("targetVersion = %v", target)
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
		t.Fatalf("added change must carry newValue JSON null: %v", added)
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

func TestEffectiveConfigDiffPreservesRawValueSemantics(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{
		"num":  json.RawMessage(`1`),
		"text": json.RawMessage(`"1"`),
	})
	publish(t, handler, "svc", "prod", "", map[string]any{
		"num":  json.RawMessage(`1.0`),
		"text": json.RawMessage(`1`),
	})

	recorder := doRequest(t, handler, http.MethodGet,
		"/effective-config-diffs?namespace=svc&baseEnvironment=dev&targetEnvironment=prod", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		ChangedCount int `json:"changedCount"`
		Changes      []struct {
			Name     string          `json:"name"`
			Type     string          `json:"changeType"`
			OldValue json.RawMessage `json:"oldValue"`
			NewValue json.RawMessage `json:"newValue"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ChangedCount != 2 || len(body.Changes) != 2 {
		t.Fatalf("changes = %+v", body)
	}
	num, text := body.Changes[0], body.Changes[1]
	if num.Name != "num" || num.Type != "modified" ||
		string(num.OldValue) != "1" || string(num.NewValue) != "1.0" {
		t.Fatalf("number semantics changed: %+v", num)
	}
	if text.Name != "text" || text.Type != "modified" ||
		string(text.OldValue) != `"1"` || string(text.NewValue) != `1` {
		t.Fatalf("string semantics changed: %+v", text)
	}
}

func TestEffectiveConfigDiffIgnoresWhitespaceAndKeyOrder(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{
		"obj": json.RawMessage(`{ "b": 1, "a": [1, 2] }`),
	})
	publish(t, handler, "svc", "prod", "", map[string]any{
		"obj": json.RawMessage(`{"a":[1,2],"b":1}`),
	})

	recorder := doRequest(t, handler, http.MethodGet,
		"/effective-config-diffs?namespace=svc&baseEnvironment=dev&targetEnvironment=prod", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if body["changedCount"].(float64) != 0 {
		t.Fatalf("changedCount = %v, want 0", body["changedCount"])
	}
	if !strings.Contains(recorder.Body.String(), `"changes":[]`) {
		t.Fatalf("changes must be an empty array: %s", recorder.Body.String())
	}
}

func TestEffectiveConfigDiffWithNeitherSideEffective(t *testing.T) {
	_, handler := newTestRouter(t)

	recorder := doRequest(t, handler, http.MethodGet,
		"/effective-config-diffs?namespace=payments&baseEnvironment=staging&targetEnvironment=prod", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if body["baseVersion"] != nil || body["targetVersion"] != nil {
		t.Fatalf("version fields = %v %v, want both null", body["baseVersion"], body["targetVersion"])
	}
	if body["changedCount"].(float64) != 0 {
		t.Fatalf("changedCount = %v, want 0", body["changedCount"])
	}
	changes, ok := body["changes"].([]any)
	if !ok || len(changes) != 0 {
		t.Fatalf("changes = %v, want empty array", body["changes"])
	}
}

func TestEffectiveConfigDiffWithOnlyOneSideEffective(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "canary", map[string]any{"gray": "only"})
	publish(t, handler, "payments", "prod", "", map[string]any{
		"retries": 3,
		"timeout": "30",
	})

	recorder := doRequest(t, handler, http.MethodGet,
		"/effective-config-diffs?namespace=payments&baseEnvironment=staging&targetEnvironment=prod", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if body["baseVersion"] != nil {
		t.Fatalf("gray-only environment must report baseVersion null: %v", body["baseVersion"])
	}
	if body["targetVersion"].(map[string]any)["version"].(float64) != 1 {
		t.Fatalf("targetVersion = %v", body["targetVersion"])
	}
	changes, _ := body["changes"].([]any)
	if len(changes) != 2 || body["changedCount"].(float64) != 2 {
		t.Fatalf("changes = %v", body["changes"])
	}
	for _, entry := range changes {
		change := entry.(map[string]any)
		if change["changeType"] != "added" {
			t.Fatalf("change against empty base must be added: %v", change)
		}
		if _, present := change["oldValue"]; present {
			t.Fatalf("added change must not carry oldValue: %v", change)
		}
	}

	reverse := doRequest(t, handler, http.MethodGet,
		"/effective-config-diffs?namespace=payments&baseEnvironment=prod&targetEnvironment=staging", nil)
	if reverse.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", reverse.Code, reverse.Body.String())
	}
	reverseBody := decode(t, reverse)
	reverseChanges, _ := reverseBody["changes"].([]any)
	if len(reverseChanges) != 2 {
		t.Fatalf("changes = %v", reverseBody["changes"])
	}
	for _, entry := range reverseChanges {
		change := entry.(map[string]any)
		if change["changeType"] != "removed" {
			t.Fatalf("change against empty target must be removed: %v", change)
		}
		if _, present := change["newValue"]; present {
			t.Fatalf("removed change must not carry newValue: %v", change)
		}
	}
}

func TestEffectiveConfigDiffPathEntryMatchesQueryEntry(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "", map[string]any{"retries": 3})
	publish(t, handler, "payments", "prod", "", map[string]any{"retries": 5})

	query := doRequest(t, handler, http.MethodGet,
		"/effective-config-diffs?namespace=payments&baseEnvironment=staging&targetEnvironment=prod", nil)
	path := doRequest(t, handler, http.MethodGet,
		"/namespaces/payments/effective-config-diffs/staging/prod", nil)
	if query.Code != http.StatusOK || path.Code != http.StatusOK {
		t.Fatalf("status = %d / %d", query.Code, path.Code)
	}
	if query.Body.String() != path.Body.String() {
		t.Fatalf("path entry body = %s, want %s", path.Body.String(), query.Body.String())
	}
}

func TestEffectiveConfigDiffRequiresScope(t *testing.T) {
	_, handler := newTestRouter(t)
	targets := []string{
		"/effective-config-diffs?baseEnvironment=staging&targetEnvironment=prod",
		"/effective-config-diffs?namespace=payments&targetEnvironment=prod",
		"/effective-config-diffs?namespace=payments&baseEnvironment=staging",
		"/effective-config-diffs",
	}
	for _, target := range targets {
		recorder := doRequest(t, handler, http.MethodGet, target, nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want %d", target, recorder.Code, http.StatusBadRequest)
		}
		if code := errorCode(decode(t, recorder)); code != "MISSING_SCOPE" {
			t.Fatalf("%s code = %s, want MISSING_SCOPE", target, code)
		}
	}
}

func TestEffectiveConfigDiffRejectsSameEnvironment(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"retries": 3})

	for _, target := range []string{
		"/effective-config-diffs?namespace=payments&baseEnvironment=prod&targetEnvironment=prod",
		"/namespaces/payments/effective-config-diffs/prod/prod",
	} {
		recorder := doRequest(t, handler, http.MethodGet, target, nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want %d", target, recorder.Code, http.StatusBadRequest)
		}
		if code := errorCode(decode(t, recorder)); code != "SAME_ENVIRONMENT" {
			t.Fatalf("%s code = %s, want SAME_ENVIRONMENT", target, code)
		}
	}
}

func TestEffectiveConfigDiffIsReadOnly(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "", map[string]any{"retries": 3})
	publish(t, handler, "payments", "prod", "", map[string]any{"retries": 5})

	historyBefore := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace=payments&environment=staging", nil).Body.String()
	prodBefore := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace=payments&environment=prod", nil).Body.String()

	recorder := doRequest(t, handler, http.MethodGet,
		"/effective-config-diffs?namespace=payments&baseEnvironment=staging&targetEnvironment=prod", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}

	historyAfter := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace=payments&environment=staging", nil).Body.String()
	prodAfter := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace=payments&environment=prod", nil).Body.String()
	if historyBefore != historyAfter || prodBefore != prodAfter {
		t.Fatalf("diff query must not change version history or effective state")
	}
}
