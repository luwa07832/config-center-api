package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCrossEnvVersionDiffReportsAddedRemovedModifiedSortedByName(t *testing.T) {
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
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=1&targetEnvironment=prod&targetVersion=1", nil)
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
	if _, present := body["environment"]; present {
		t.Fatalf("response must not carry a shared environment: %v", body)
	}
	if _, present := body["effectiveVersion"]; present {
		t.Fatalf("response must not carry a shared effectiveVersion: %v", body)
	}
	base := body["baseVersion"].(map[string]any)
	if base["namespace"] != "payments" || base["environment"] != "staging" ||
		base["version"].(float64) != 1 || base["effective"] != true {
		t.Fatalf("baseVersion = %v", base)
	}
	if base["grayTag"] != nil || base["rollbackOf"] != nil || base["promotionOf"] != nil {
		t.Fatalf("baseVersion metadata = %v", base)
	}
	target := body["targetVersion"].(map[string]any)
	if target["namespace"] != "payments" || target["environment"] != "prod" ||
		target["version"].(float64) != 1 || target["effective"] != true {
		t.Fatalf("targetVersion = %v", target)
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
		if _, present := change["affectsEffectiveConfig"]; present {
			t.Fatalf("cross-environment change must not carry affectsEffectiveConfig: %v", change)
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
}

func TestCrossEnvVersionDiffPreservesRawValueSemantics(t *testing.T) {
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
		"/cross-environment-config-version-diffs?namespace=svc&baseEnvironment=dev&baseVersion=1&targetEnvironment=prod&targetVersion=1", nil)
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

func TestCrossEnvVersionDiffIgnoresWhitespaceAndKeyOrder(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{
		"obj": json.RawMessage(`{ "b": 1, "a": [1, 2] }`),
	})
	publish(t, handler, "svc", "prod", "", map[string]any{
		"obj": json.RawMessage(`{"a":[1,2],"b":1}`),
	})

	recorder := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=svc&baseEnvironment=dev&baseVersion=1&targetEnvironment=prod&targetVersion=1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"changedCount":0`) ||
		!strings.Contains(recorder.Body.String(), `"changes":[]`) {
		t.Fatalf("expected empty diff: %s", recorder.Body.String())
	}
}

func TestCrossEnvVersionDiffAllowsIndependentVersionNumbering(t *testing.T) {
	_, handler := newTestRouter(t)
	// dev has three versions, prod has one; base version 3 compares against target version 1.
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 2})
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 3})
	publish(t, handler, "svc", "prod", "", map[string]any{"a": 3})

	recorder := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=svc&baseEnvironment=dev&baseVersion=3&targetEnvironment=prod&targetVersion=1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("baseVersion > targetVersion must compare: status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if body["changedCount"].(float64) != 0 {
		t.Fatalf("changedCount = %v, want 0", body["changedCount"])
	}
	if body["baseVersion"].(map[string]any)["version"].(float64) != 3 ||
		body["targetVersion"].(map[string]any)["version"].(float64) != 1 {
		t.Fatalf("version fields = %v / %v", body["baseVersion"], body["targetVersion"])
	}

	sameNumbers := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=svc&baseEnvironment=prod&baseVersion=1&targetEnvironment=dev&targetVersion=1", nil)
	if sameNumbers.Code != http.StatusOK {
		t.Fatalf("equal version numbers must compare across environments: status = %d body = %s",
			sameNumbers.Code, sameNumbers.Body.String())
	}
	sameBody := decode(t, sameNumbers)
	if sameBody["changedCount"].(float64) != 1 {
		t.Fatalf("changedCount = %v, want 1", sameBody["changedCount"])
	}
	change := sameBody["changes"].([]any)[0].(map[string]any)
	if change["changeType"] != "modified" || change["oldValue"].(float64) != 3 ||
		change["newValue"].(float64) != 1 {
		t.Fatalf("change = %v", change)
	}
}

func TestCrossEnvVersionDiffReportsMetadataAndEffectiveFlags(t *testing.T) {
	_, handler := newTestRouter(t)
	// staging: v1 full, v2 gray stays non-effective.
	publish(t, handler, "payments", "staging", "", map[string]any{"retries": 3})
	publish(t, handler, "payments", "staging", "canary", map[string]any{"retries": 9})
	// prod: v1 full, v2 gray, v3 promoted from v2.
	publish(t, handler, "payments", "prod", "", map[string]any{"retries": 1})
	publish(t, handler, "payments", "prod", "canary", map[string]any{"retries": 2})
	recorder := doRequest(t, handler, http.MethodPost,
		"/namespaces/payments/environments/prod/config-versions/2/promote", map[string]any{})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("promote status = %d body = %s", recorder.Code, recorder.Body.String())
	}

	diff := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=2&targetEnvironment=prod&targetVersion=3", nil)
	if diff.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", diff.Code, diff.Body.String())
	}
	body := decode(t, diff)
	base := body["baseVersion"].(map[string]any)
	if base["version"].(float64) != 2 || base["grayTag"] != "canary" ||
		base["rollbackOf"] != nil || base["promotionOf"] != nil || base["effective"] != false {
		t.Fatalf("baseVersion metadata = %v", base)
	}
	target := body["targetVersion"].(map[string]any)
	if target["version"].(float64) != 3 || target["grayTag"] != nil ||
		target["rollbackOf"] != nil || target["promotionOf"].(float64) != 2 ||
		target["effective"] != true {
		t.Fatalf("targetVersion metadata = %v", target)
	}
	change := body["changes"].([]any)[0].(map[string]any)
	if change["changeType"] != "modified" || change["oldValue"].(float64) != 9 ||
		change["newValue"].(float64) != 2 {
		t.Fatalf("change = %v", change)
	}
}

func TestCrossEnvVersionDiffRequiresScope(t *testing.T) {
	_, handler := newTestRouter(t)
	targets := []string{
		"/cross-environment-config-version-diffs?baseEnvironment=staging&baseVersion=1&targetEnvironment=prod&targetVersion=1",
		"/cross-environment-config-version-diffs?namespace=payments&baseVersion=1&targetEnvironment=prod&targetVersion=1",
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=1&targetVersion=1",
		"/cross-environment-config-version-diffs",
	}
	for _, target := range targets {
		recorder := doRequest(t, handler, http.MethodGet, target, nil)
		if recorder.Code != http.StatusBadRequest || errorCode(decode(t, recorder)) != "MISSING_SCOPE" {
			t.Fatalf("%s status = %d code = %s, want 400 MISSING_SCOPE",
				target, recorder.Code, errorCode(decode(t, recorder)))
		}
	}
}

func TestCrossEnvVersionDiffRejectsSameEnvironment(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"retries": 3})

	recorder := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=prod&baseVersion=1&targetEnvironment=prod&targetVersion=1", nil)
	if recorder.Code != http.StatusBadRequest || errorCode(decode(t, recorder)) != "SAME_ENVIRONMENT" {
		t.Fatalf("status = %d code = %s, want 400 SAME_ENVIRONMENT",
			recorder.Code, errorCode(decode(t, recorder)))
	}
}

func TestCrossEnvVersionDiffValidatesVersionsBaseFirst(t *testing.T) {
	_, handler := newTestRouter(t)

	for _, target := range []string{
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=0&targetEnvironment=prod&targetVersion=1",
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=-2&targetEnvironment=prod&targetVersion=1",
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=1.5&targetEnvironment=prod&targetVersion=1",
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=01&targetEnvironment=prod&targetVersion=1",
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=abc&targetEnvironment=prod&targetVersion=1",
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=&targetEnvironment=prod&targetVersion=1",
	} {
		recorder := doRequest(t, handler, http.MethodGet, target, nil)
		if recorder.Code != http.StatusBadRequest || errorCode(decode(t, recorder)) != "INVALID_VERSION" {
			t.Fatalf("%s status = %d code = %s, want 400 INVALID_VERSION",
				target, recorder.Code, errorCode(decode(t, recorder)))
		}
	}

	// Base is valid, target invalid -> still INVALID_VERSION after base passes format validation.
	targetInvalid := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=1&targetEnvironment=prod&targetVersion=x", nil)
	if targetInvalid.Code != http.StatusBadRequest || errorCode(decode(t, targetInvalid)) != "INVALID_VERSION" {
		t.Fatalf("status = %d code = %s, want 400 INVALID_VERSION",
			targetInvalid.Code, errorCode(decode(t, targetInvalid)))
	}

	// Both invalid: base must win.
	bothInvalid := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=x&targetEnvironment=prod&targetVersion=y", nil)
	if bothInvalid.Code != http.StatusBadRequest || errorCode(decode(t, bothInvalid)) != "INVALID_VERSION" {
		t.Fatalf("status = %d code = %s, want 400 INVALID_VERSION",
			bothInvalid.Code, errorCode(decode(t, bothInvalid)))
	}
}

func TestCrossEnvVersionDiffResolvesVersionsBaseFirst(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "", map[string]any{"a": 1})
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 2})

	// Base version number exists nowhere.
	baseMissing := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=9&targetEnvironment=prod&targetVersion=99", nil)
	if baseMissing.Code != http.StatusNotFound || errorCode(decode(t, baseMissing)) != "VERSION_NOT_FOUND" {
		t.Fatalf("status = %d code = %s, want 404 VERSION_NOT_FOUND",
			baseMissing.Code, errorCode(decode(t, baseMissing)))
	}

	// Base exists but in another environment; the target is never inspected.
	// Version 1 lives in staging and prod; requesting it from dev is a base-side scope mismatch,
	// reported before the missing target 99 is inspected.
	baseMismatch := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=prod&baseVersion=1&targetEnvironment=dev&targetVersion=99", nil)
	if baseMismatch.Code != http.StatusConflict || errorCode(decode(t, baseMismatch)) != "VERSION_SCOPE_MISMATCH" {
		t.Fatalf("status = %d code = %s, want 409 VERSION_SCOPE_MISMATCH (base first)",
			baseMismatch.Code, errorCode(decode(t, baseMismatch)))
	}

	// Base exists in another namespace: scope mismatch still applies.
	baseOtherNamespace := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=billing&baseEnvironment=staging&baseVersion=1&targetEnvironment=prod&targetVersion=1", nil)
	if baseOtherNamespace.Code != http.StatusConflict || errorCode(decode(t, baseOtherNamespace)) != "VERSION_SCOPE_MISMATCH" {
		t.Fatalf("status = %d code = %s, want 409 VERSION_SCOPE_MISMATCH",
			baseOtherNamespace.Code, errorCode(decode(t, baseOtherNamespace)))
	}

	// Base resolves, target version exists nowhere.
	targetMissing := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=1&targetEnvironment=prod&targetVersion=99", nil)
	if targetMissing.Code != http.StatusNotFound || errorCode(decode(t, targetMissing)) != "VERSION_NOT_FOUND" {
		t.Fatalf("status = %d code = %s, want 404 VERSION_NOT_FOUND",
			targetMissing.Code, errorCode(decode(t, targetMissing)))
	}

	// Base resolves, target exists but outside the target scope.
	targetMismatch := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=1&targetEnvironment=dev&targetVersion=1", nil)
	if targetMismatch.Code != http.StatusConflict || errorCode(decode(t, targetMismatch)) != "VERSION_SCOPE_MISMATCH" {
		t.Fatalf("status = %d code = %s, want 409 VERSION_SCOPE_MISMATCH",
			targetMismatch.Code, errorCode(decode(t, targetMismatch)))
	}
}

func TestCrossEnvVersionDiffIsReadOnly(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "", map[string]any{"retries": 3})
	publish(t, handler, "payments", "prod", "", map[string]any{"retries": 5})

	stagingBefore := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace=payments&environment=staging", nil).Body.String()
	prodBefore := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace=payments&environment=prod", nil).Body.String()

	recorder := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=1&targetEnvironment=prod&targetVersion=1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}

	stagingAfter := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace=payments&environment=staging", nil).Body.String()
	prodAfter := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace=payments&environment=prod", nil).Body.String()
	if stagingBefore != stagingAfter || prodBefore != prodAfter {
		t.Fatalf("diff query must not change version history or effective state")
	}
}
