package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

const crossEnvVersionDiffPath = "/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&targetEnvironment=prod"

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
		crossEnvVersionDiffPath+"&baseVersion=1&targetVersion=1", nil)
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
		change := entry.(map[string]any)
		if change["name"].(string) != ordered[i] {
			t.Fatalf("change %d = %v, want name %s", i, change, ordered[i])
		}
		if _, present := change["affectsEffectiveConfig"]; present {
			t.Fatalf("cross-environment change must not carry affectsEffectiveConfig: %v", change)
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

func TestCrossEnvVersionDiffPreservesRawValueSemantics(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{
		"oneDotZero": json.RawMessage(`1.0`),
		"numericStr": json.RawMessage(`"1"`),
		"flag":       json.RawMessage(`true`),
	})
	publish(t, handler, "svc", "prod", "", map[string]any{
		"oneDotZero": json.RawMessage(`1`),
		"numericStr": json.RawMessage(`1`),
		"flag":       json.RawMessage(`false`),
	})

	recorder := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=svc&baseEnvironment=dev&targetEnvironment=prod&baseVersion=1&targetVersion=1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if body["changedCount"].(float64) != 3 {
		t.Fatalf("changedCount = %v, want 3 distinct value pairs", body["changedCount"])
	}
}

func TestCrossEnvVersionDiffIgnoresWhitespaceAndKeyOrder(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{
		"same": json.RawMessage(`{"a": 1, "b": [true, null, "x"]}`),
	})
	publish(t, handler, "svc", "prod", "", map[string]any{
		"same": json.RawMessage("{ \"b\": [ true, null, \"x\" ],\n \"a\": 1 }"),
	})

	recorder := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=svc&baseEnvironment=dev&targetEnvironment=prod&baseVersion=1&targetVersion=1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if body["changedCount"].(float64) != 0 {
		t.Fatalf("changedCount = %v, want 0", body["changedCount"])
	}
	changes, _ := body["changes"].([]any)
	if len(changes) != 0 {
		t.Fatalf("changes = %v, want empty array", body["changes"])
	}
}

func TestCrossEnvVersionDiffAllowsEqualOrDescendingVersionNumbers(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "", map[string]any{"v": "a"})
	publish(t, handler, "payments", "staging", "", map[string]any{"v": "b"})
	publish(t, handler, "payments", "staging", "", map[string]any{"v": "c"})
	publish(t, handler, "payments", "prod", "", map[string]any{"v": "c"})

	recorder := doRequest(t, handler, http.MethodGet,
		crossEnvVersionDiffPath+"&baseVersion=3&targetVersion=1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("descending numbers: status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if body["changedCount"].(float64) != 0 {
		t.Fatalf("changedCount = %v, want 0", body["changedCount"])
	}
	base := body["baseVersion"].(map[string]any)
	if base["version"].(float64) != 3 || base["environment"] != "staging" {
		t.Fatalf("baseVersion = %v", base)
	}
	target := body["targetVersion"].(map[string]any)
	if target["version"].(float64) != 1 || target["environment"] != "prod" {
		t.Fatalf("targetVersion = %v", target)
	}
}

func TestCrossEnvVersionDiffRequiresScope(t *testing.T) {
	_, handler := newTestRouter(t)
	cases := map[string]string{
		"missing namespace":         "/cross-environment-config-version-diffs?baseEnvironment=staging&targetEnvironment=prod&baseVersion=1&targetVersion=1",
		"missing baseEnvironment":   "/cross-environment-config-version-diffs?namespace=payments&targetEnvironment=prod&baseVersion=1&targetVersion=1",
		"missing targetEnvironment": "/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&baseVersion=1&targetVersion=1",
	}
	for name, target := range cases {
		recorder := doRequest(t, handler, http.MethodGet, target, nil)
		if recorder.Code != http.StatusBadRequest || errorCode(decode(t, recorder)) != "MISSING_SCOPE" {
			t.Fatalf("%s: status = %d body = %s", name, recorder.Code, recorder.Body.String())
		}
	}
}

func TestCrossEnvVersionDiffRejectsSameEnvironment(t *testing.T) {
	_, handler := newTestRouter(t)
	recorder := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=prod&targetEnvironment=prod&baseVersion=1&targetVersion=2", nil)
	if recorder.Code != http.StatusBadRequest || errorCode(decode(t, recorder)) != "SAME_ENVIRONMENT" {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestCrossEnvVersionDiffValidatesBaseVersionBeforeTarget(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "", map[string]any{"v": "a"})
	publish(t, handler, "payments", "prod", "", map[string]any{"v": "b"})

	bothInvalid := doRequest(t, handler, http.MethodGet,
		crossEnvVersionDiffPath+"&baseVersion=-1&targetVersion=xyz", nil)
	if bothInvalid.Code != http.StatusBadRequest || errorCode(decode(t, bothInvalid)) != "INVALID_VERSION" {
		t.Fatalf("invalid base: status = %d body = %s", bothInvalid.Code, bothInvalid.Body.String())
	}
	targetInvalid := doRequest(t, handler, http.MethodGet,
		crossEnvVersionDiffPath+"&baseVersion=1&targetVersion=0", nil)
	if targetInvalid.Code != http.StatusBadRequest || errorCode(decode(t, targetInvalid)) != "INVALID_VERSION" {
		t.Fatalf("invalid target: status = %d body = %s", targetInvalid.Code, targetInvalid.Body.String())
	}
	missingTarget := doRequest(t, handler, http.MethodGet,
		crossEnvVersionDiffPath+"&baseVersion=1&targetVersion=", nil)
	if missingTarget.Code != http.StatusBadRequest || errorCode(decode(t, missingTarget)) != "INVALID_VERSION" {
		t.Fatalf("empty target: status = %d body = %s", missingTarget.Code, missingTarget.Body.String())
	}
	missingBase := doRequest(t, handler, http.MethodGet,
		crossEnvVersionDiffPath+"&baseVersion=&targetVersion=1", nil)
	if missingBase.Code != http.StatusBadRequest || errorCode(decode(t, missingBase)) != "INVALID_VERSION" {
		t.Fatalf("empty base: status = %d body = %s", missingBase.Code, missingBase.Body.String())
	}
}

func TestCrossEnvVersionDiffErrorContractBaseBeforeTarget(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "", map[string]any{"v": "a"})
	publish(t, handler, "payments", "prod", "", map[string]any{"v": "b"})
	publish(t, handler, "payments", "prod", "", map[string]any{"v": "c"})
	publish(t, handler, "payments", "dev", "", map[string]any{"v": "d"})
	publish(t, handler, "billing", "prod", "", map[string]any{"v": "e"})

	// Base version does not exist anywhere: 404 before target is inspected.
	missingBase := doRequest(t, handler, http.MethodGet,
		crossEnvVersionDiffPath+"&baseVersion=99&targetVersion=99", nil)
	if missingBase.Code != http.StatusNotFound || errorCode(decode(t, missingBase)) != "VERSION_NOT_FOUND" {
		t.Fatalf("missing base: status = %d body = %s", missingBase.Code, missingBase.Body.String())
	}
	// Base is valid; target version exists nowhere.
	missingTarget := doRequest(t, handler, http.MethodGet,
		crossEnvVersionDiffPath+"&baseVersion=1&targetVersion=99", nil)
	if missingTarget.Code != http.StatusNotFound || errorCode(decode(t, missingTarget)) != "VERSION_NOT_FOUND" {
		t.Fatalf("missing target: status = %d body = %s", missingTarget.Code, missingTarget.Body.String())
	}
	// Base v1 is valid for staging; target v2 exists only in another environment of the namespace.
	targetOtherEnvironment := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=payments&baseEnvironment=staging&targetEnvironment=dev&baseVersion=1&targetVersion=2", nil)
	if targetOtherEnvironment.Code != http.StatusConflict || errorCode(decode(t, targetOtherEnvironment)) != "VERSION_SCOPE_MISMATCH" {
		t.Fatalf("target scope mismatch: status = %d body = %s", targetOtherEnvironment.Code, targetOtherEnvironment.Body.String())
	}

	// Version 1 exists in other namespaces, but not in the requested namespace at all.
	crossNamespace := doRequest(t, handler, http.MethodGet,
		"/cross-environment-config-version-diffs?namespace=other&baseEnvironment=staging&targetEnvironment=prod&baseVersion=1&targetVersion=1", nil)
	if crossNamespace.Code != http.StatusConflict || errorCode(decode(t, crossNamespace)) != "VERSION_SCOPE_MISMATCH" {
		t.Fatalf("cross namespace: status = %d body = %s", crossNamespace.Code, crossNamespace.Body.String())
	}
	// Base side mismatch wins over a missing target version because base is looked up first.
	baseFirst := doRequest(t, handler, http.MethodGet,
		crossEnvVersionDiffPath+"&baseVersion=2&targetVersion=99", nil)
	if baseFirst.Code != http.StatusConflict || errorCode(decode(t, baseFirst)) != "VERSION_SCOPE_MISMATCH" {
		t.Fatalf("base checked first: status = %d body = %s", baseFirst.Code, baseFirst.Body.String())
	}
}

func TestCrossEnvVersionDiffBaseVersionInTargetEnvironmentIsScopeMismatch(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "", map[string]any{"v": "a"})
	publish(t, handler, "payments", "prod", "", map[string]any{"v": "b"})
	publish(t, handler, "payments", "prod", "", map[string]any{"v": "c"})

	// Base side staging only has version 1; asking baseVersion=2 finds it only in prod.
	recorder := doRequest(t, handler, http.MethodGet,
		crossEnvVersionDiffPath+"&baseVersion=2&targetVersion=2", nil)
	if recorder.Code != http.StatusConflict || errorCode(decode(t, recorder)) != "VERSION_SCOPE_MISMATCH" {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestCrossEnvVersionDiffIsReadOnly(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "staging", "", map[string]any{"v": "a"})
	publish(t, handler, "payments", "prod", "", map[string]any{"v": "b"})

	attempts := []string{
		crossEnvVersionDiffPath + "&baseVersion=1&targetVersion=1",
		crossEnvVersionDiffPath + "&baseVersion=9&targetVersion=1",
		crossEnvVersionDiffPath + "&baseVersion=1&targetVersion=9",
		crossEnvVersionDiffPath + "&baseVersion=x&targetVersion=1",
	}
	for _, target := range attempts {
		doRequest(t, handler, http.MethodGet, target, nil)
	}

	for _, env := range []string{"staging", "prod"} {
		recorder := doRequest(t, handler, http.MethodGet,
			"/config-versions?namespace=payments&environment="+env, nil)
		body := decode(t, recorder)
		versions, _ := body["versions"].([]any)
		if len(versions) != 1 {
			t.Fatalf("environment %s versions = %v, want exactly one published version", env, body["versions"])
		}
	}
}
