package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func rollback(t *testing.T, handler http.Handler, ns, env, version string) {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodPost,
		"/namespaces/"+ns+"/environments/"+env+"/config-versions/"+version+"/rollback", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("rollback status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func itemHistory(t *testing.T, handler http.Handler, target string) map[string]any {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodGet, target, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("history status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	return decode(t, recorder)
}

func changeTypes(t *testing.T, body map[string]any) []string {
	t.Helper()
	changes, ok := body["changes"].([]any)
	if !ok {
		t.Fatalf("changes is not a list: %v", body["changes"])
	}
	types := make([]string, 0, len(changes))
	for _, entry := range changes {
		types = append(types, entry.(map[string]any)["changeType"].(string))
	}
	return types
}

func equalStrings(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestItemHistoryLifecycleAcrossVersions(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{
		"timeout": json.RawMessage(`"30"`),
		"dropped": json.RawMessage(`true`),
	})
	publish(t, handler, "payments", "prod", "", map[string]any{"timeout": json.RawMessage(`"60"`)})
	publish(t, handler, "payments", "prod", "canary", map[string]any{"timeout": json.RawMessage(`"90"`)})
	publish(t, handler, "payments", "prod", "", map[string]any{"other": json.RawMessage(`2`)})
	rollback(t, handler, "payments", "prod", "2")

	body := itemHistory(t, handler, "/config-item-histories?namespace=payments&environment=prod&name=timeout")
	if body["namespace"] != "payments" || body["environment"] != "prod" || body["name"] != "timeout" {
		t.Fatalf("scope echo = %v %v %v", body["namespace"], body["environment"], body["name"])
	}
	if body["effectiveVersion"].(float64) != 5 {
		t.Fatalf("effectiveVersion = %v, want 5", body["effectiveVersion"])
	}
	effectiveItem := body["effectiveItem"].(map[string]any)
	if effectiveItem["present"] != true || effectiveItem["value"] != "60" {
		t.Fatalf("effectiveItem = %v", effectiveItem)
	}

	types := changeTypes(t, body)
	if !equalStrings(types, "added", "modified", "modified", "removed", "added") {
		t.Fatalf("changeTypes = %v", types)
	}
	if body["totalChanges"].(float64) != 5 {
		t.Fatalf("totalChanges = %v, want 5", body["totalChanges"])
	}
	changes := body["changes"].([]any)
	versions := make([]float64, 0, len(changes))
	for _, entry := range changes {
		versions = append(versions, entry.(map[string]any)["version"].(map[string]any)["version"].(float64))
	}
	for i, want := range []float64{1, 2, 3, 4, 5} {
		if versions[i] != want {
			t.Fatalf("change versions = %v", versions)
		}
	}

	added := changes[0].(map[string]any)
	if added["newValue"] != "30" {
		t.Fatalf("added newValue = %v", added["newValue"])
	}
	if _, ok := added["oldValue"]; ok {
		t.Fatalf("added must not carry oldValue: %v", added)
	}
	modified := changes[1].(map[string]any)
	if modified["oldValue"] != "30" || modified["newValue"] != "60" {
		t.Fatalf("modified values = %v", modified)
	}
	gray := changes[2].(map[string]any)
	if gray["version"].(map[string]any)["grayTag"] != "canary" {
		t.Fatalf("gray change version = %v", gray["version"])
	}
	removed := changes[3].(map[string]any)
	if removed["oldValue"] != "90" {
		t.Fatalf("removed oldValue = %v", removed["oldValue"])
	}
	if _, ok := removed["newValue"]; ok {
		t.Fatalf("removed must not carry newValue: %v", removed)
	}
	readded := changes[4].(map[string]any)
	readdedVersion := readded["version"].(map[string]any)
	if readdedVersion["rollbackOf"].(float64) != 2 || readdedVersion["effective"] != true {
		t.Fatalf("rollback change version = %v", readdedVersion)
	}
	if readded["newValue"] != "60" {
		t.Fatalf("re-added newValue = %v", readded["newValue"])
	}
	if readdedVersion["createdAt"].(string) == "" {
		t.Fatalf("createdAt missing: %v", readdedVersion)
	}

	dropped := itemHistory(t, handler, "/config-item-histories?namespace=payments&environment=prod&name=dropped")
	if !equalStrings(changeTypes(t, dropped), "added", "removed") {
		t.Fatalf("dropped changeTypes = %v", changeTypes(t, dropped))
	}
	droppedItem := dropped["effectiveItem"].(map[string]any)
	if droppedItem["present"] != false {
		t.Fatalf("dropped effectiveItem = %v", droppedItem)
	}
	if _, ok := droppedItem["value"]; ok {
		t.Fatalf("absent effectiveItem must not carry value: %v", droppedItem)
	}
	if dropped["effectiveVersion"].(float64) != 5 {
		t.Fatalf("dropped effectiveVersion = %v, want 5", dropped["effectiveVersion"])
	}
}

func TestItemHistoryUnknownNameReturnsEmptyChanges(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": json.RawMessage(`1`)})
	publish(t, handler, "svc", "dev", "gray", map[string]any{"b": json.RawMessage(`2`)})

	body := itemHistory(t, handler, "/config-item-histories?namespace=svc&environment=dev&name=missing")
	if body["effectiveVersion"].(float64) != 1 {
		t.Fatalf("effectiveVersion = %v, want 1", body["effectiveVersion"])
	}
	effectiveItem := body["effectiveItem"].(map[string]any)
	if effectiveItem["present"] != false {
		t.Fatalf("effectiveItem = %v", effectiveItem)
	}
	if _, ok := effectiveItem["value"]; ok {
		t.Fatalf("absent effectiveItem must not carry value: %v", effectiveItem)
	}
	changes, ok := body["changes"].([]any)
	if !ok || len(changes) != 0 {
		t.Fatalf("changes = %v, want empty array", body["changes"])
	}
	if body["totalChanges"].(float64) != 0 {
		t.Fatalf("totalChanges = %v, want 0", body["totalChanges"])
	}

	grayOnly := itemHistory(t, handler, "/config-item-histories?namespace=svc&environment=dev&name=b")
	if !equalStrings(changeTypes(t, grayOnly), "added") {
		t.Fatalf("gray-only changeTypes = %v", changeTypes(t, grayOnly))
	}
	if grayOnly["effectiveItem"].(map[string]any)["present"] != false {
		t.Fatalf("gray-only effectiveItem = %v", grayOnly["effectiveItem"])
	}

	emptyScope := itemHistory(t, handler, "/config-item-histories?namespace=none&environment=dev&name=a")
	if emptyScope["effectiveVersion"] != nil {
		t.Fatalf("effectiveVersion = %v, want null", emptyScope["effectiveVersion"])
	}
	if emptyScope["totalChanges"].(float64) != 0 {
		t.Fatalf("totalChanges = %v, want 0", emptyScope["totalChanges"])
	}
}

func TestItemHistoryErrorContract(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": json.RawMessage(`1`)})

	cases := []struct {
		name       string
		target     string
		wantStatus int
		wantCode   string
	}{
		{"missing namespace", "/config-item-histories?environment=dev&name=a", http.StatusBadRequest, "MISSING_SCOPE"},
		{"missing environment", "/config-item-histories?namespace=svc&name=a", http.StatusBadRequest, "MISSING_SCOPE"},
		{"missing name", "/config-item-histories?namespace=svc&environment=dev", http.StatusBadRequest, "MISSING_ITEM_NAME"},
		{"empty name", "/config-item-histories?namespace=svc&environment=dev&name=", http.StatusBadRequest, "MISSING_ITEM_NAME"},
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
			message := body["error"].(map[string]any)["message"].(string)
			if message == "" || strings.Contains(message, "SELECT") || strings.Contains(message, "/") {
				t.Fatalf("message leaks internals: %q", message)
			}
		})
	}
}

func TestItemHistoryPreservesRawValueSemantics(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"x": json.RawMessage(`1`)})
	publish(t, handler, "svc", "dev", "", map[string]any{"x": json.RawMessage(`1.0`)})
	publish(t, handler, "svc", "dev", "", map[string]any{"x": json.RawMessage(`1.0`)})
	publish(t, handler, "svc", "dev", "", map[string]any{"x": json.RawMessage(`{ "a": 1, "b": 2 }`)})
	publish(t, handler, "svc", "dev", "", map[string]any{"x": json.RawMessage(`{"b":2,"a":1}`)})
	publish(t, handler, "svc", "dev", "", map[string]any{"x": json.RawMessage(`null`)})

	body := itemHistory(t, handler, "/config-item-histories?namespace=svc&environment=dev&name=x")
	types := changeTypes(t, body)
	if !equalStrings(types, "added", "modified", "modified", "modified") {
		t.Fatalf("changeTypes = %v", types)
	}
	changes := body["changes"].([]any)
	if changes[0].(map[string]any)["newValue"].(float64) != 1 {
		t.Fatalf("added newValue = %v", changes[0])
	}
	first := changes[1].(map[string]any)
	if first["oldValue"].(float64) != 1 || first["newValue"].(float64) != 1.0 {
		t.Fatalf("1 vs 1.0 must differ: %v", first)
	}
	second := changes[2].(map[string]any)
	if second["version"].(map[string]any)["version"].(float64) != 4 {
		t.Fatalf("whitespace-only and key-order differences must not appear: %v", changes)
	}
	last := changes[3].(map[string]any)
	if value, ok := last["newValue"]; !ok || value != nil {
		t.Fatalf("null value must stay null: %v", last)
	}
	effectiveItem := body["effectiveItem"].(map[string]any)
	if effectiveItem["present"] != true {
		t.Fatalf("effectiveItem = %v", effectiveItem)
	}
	if value, ok := effectiveItem["value"]; !ok || value != nil {
		t.Fatalf("effective null value must be returned as null: %v", effectiveItem)
	}
}

func TestItemHistoryDoesNotTrimName(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{
		"odd":   json.RawMessage(`1`),
		" odd ": json.RawMessage(`2`),
	})

	plain := itemHistory(t, handler, "/config-item-histories?namespace=svc&environment=dev&name=odd")
	if plain["effectiveItem"].(map[string]any)["present"] != true {
		t.Fatalf("plain effectiveItem = %v", plain["effectiveItem"])
	}
	if plain["effectiveItem"].(map[string]any)["value"].(float64) != 1 {
		t.Fatalf("plain value = %v", plain["effectiveItem"])
	}
	padded := itemHistory(t, handler, "/config-item-histories?namespace=svc&environment=dev&name=%20odd%20")
	if padded["name"] != " odd " {
		t.Fatalf("name echo = %q", padded["name"])
	}
	if padded["effectiveItem"].(map[string]any)["value"].(float64) != 2 {
		t.Fatalf("padded value = %v", padded["effectiveItem"])
	}
}

func TestItemHistoryIsReadOnly(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": json.RawMessage(`1`)})
	publish(t, handler, "svc", "dev", "", map[string]any{"a": json.RawMessage(`2`)})

	before := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	doRequest(t, handler, http.MethodGet, "/config-item-histories?namespace=svc&environment=dev&name=a", nil)
	doRequest(t, handler, http.MethodGet, "/config-item-histories?namespace=svc&environment=dev&name=missing", nil)
	after := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	if before.Body.String() != after.Body.String() {
		t.Fatalf("item history query changed history:\nbefore %s\nafter  %s", before.Body.String(), after.Body.String())
	}
}
