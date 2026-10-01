package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func itemHistoryPath(ns, env, name string) string {
	query := url.Values{}
	query.Set("namespace", ns)
	query.Set("environment", env)
	query.Set("name", name)
	return "/config-item-histories?" + query.Encode()
}

type rawItemChange struct {
	Version    VersionInfo      `json:"version"`
	ChangeType string           `json:"changeType"`
	OldValue   *json.RawMessage `json:"oldValue"`
	NewValue   *json.RawMessage `json:"newValue"`
}

type rawItemHistory struct {
	Namespace        string          `json:"namespace"`
	Environment      string          `json:"environment"`
	Name             string          `json:"name"`
	EffectiveVersion *int64          `json:"effectiveVersion"`
	EffectiveItem    json.RawMessage `json:"effectiveItem"`
	Changes          []rawItemChange `json:"changes"`
	TotalChanges     int             `json:"totalChanges"`
}

func decodeRawItemHistory(t *testing.T, recorder *httptest.ResponseRecorder) rawItemHistory {
	t.Helper()
	var body rawItemHistory
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return body
}

func TestItemHistoryTracksAddedRemovedModifiedAcrossGrayAndRollback(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"alpha": "v1", "kept": 1})
	publish(t, handler, "payments", "prod", "canary", map[string]any{"alpha": "v2", "kept": 1})
	publish(t, handler, "payments", "prod", "", map[string]any{"kept": 1})
	publish(t, handler, "payments", "prod", "gray2", map[string]any{"alpha": "v4", "kept": 1})
	rb := doRequest(t, handler, http.MethodPost,
		"/namespaces/payments/environments/prod/config-versions/1/rollback", nil)
	if rb.Code != http.StatusCreated {
		t.Fatalf("rollback status = %d body = %s", rb.Code, rb.Body.String())
	}

	recorder := doRequest(t, handler, http.MethodGet, itemHistoryPath("payments", "prod", "alpha"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeRawItemHistory(t, recorder)
	if body.Namespace != "payments" || body.Environment != "prod" || body.Name != "alpha" {
		t.Fatalf("scope/name = %s/%s/%s", body.Namespace, body.Environment, body.Name)
	}
	if body.EffectiveVersion == nil || *body.EffectiveVersion != 5 {
		t.Fatalf("effectiveVersion = %v, want 5", body.EffectiveVersion)
	}
	var effective struct {
		Present bool            `json:"present"`
		Value   json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(body.EffectiveItem, &effective); err != nil {
		t.Fatalf("effectiveItem: %v", err)
	}
	if !effective.Present || string(effective.Value) != `"v1"` {
		t.Fatalf("effectiveItem = %s", body.EffectiveItem)
	}

	want := []struct {
		version int64
		kind    string
		old     string
		new     string
		hasOld  bool
		hasNew  bool
	}{
		{1, "added", "", `"v1"`, false, true},
		{2, "modified", `"v1"`, `"v2"`, true, true},
		{3, "removed", `"v2"`, "", true, false},
		{4, "added", "", `"v4"`, false, true},
		{5, "modified", `"v4"`, `"v1"`, true, true},
	}
	if body.TotalChanges != len(want) || len(body.Changes) != len(want) {
		t.Fatalf("totalChanges = %d changes = %d, want %d", body.TotalChanges, len(body.Changes), len(want))
	}
	for i, wantChange := range want {
		change := body.Changes[i]
		if change.Version.Version != wantChange.version {
			t.Fatalf("change %d version = %d, want %d", i, change.Version.Version, wantChange.version)
		}
		if change.ChangeType != wantChange.kind {
			t.Fatalf("change %d type = %s, want %s", i, change.ChangeType, wantChange.kind)
		}
		if wantChange.hasOld != (change.OldValue != nil) ||
			(change.OldValue != nil && string(*change.OldValue) != wantChange.old) {
			t.Fatalf("change %d oldValue = %v, want %q", i, change.OldValue, wantChange.old)
		}
		if wantChange.hasNew != (change.NewValue != nil) ||
			(change.NewValue != nil && string(*change.NewValue) != wantChange.new) {
			t.Fatalf("change %d newValue = %v, want %q", i, change.NewValue, wantChange.new)
		}
	}
	if body.Changes[0].Version.Effective {
		t.Fatalf("version 1 is rollback source, not effective")
	}
	if !body.Changes[4].Version.Effective {
		t.Fatalf("version 5 must be effective")
	}
	if body.Changes[4].Version.RollbackOf == nil || *body.Changes[4].Version.RollbackOf != 1 {
		t.Fatalf("version 5 rollbackOf = %v, want 1", body.Changes[4].Version.RollbackOf)
	}
	if body.Changes[1].Version.GrayTag == nil || *body.Changes[1].Version.GrayTag != "canary" {
		t.Fatalf("version 2 grayTag = %v", body.Changes[1].Version.GrayTag)
	}
	if body.Changes[2].Version.GrayTag != nil {
		t.Fatalf("version 3 grayTag must be null")
	}
}

func TestItemHistoryUnknownNameReportsAbsentEffectiveItem(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"other": 1})

	recorder := doRequest(t, handler, http.MethodGet, itemHistoryPath("payments", "prod", "alpha"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeRawItemHistory(t, recorder)
	if body.EffectiveVersion == nil || *body.EffectiveVersion != 1 {
		t.Fatalf("effectiveVersion = %v, want 1", body.EffectiveVersion)
	}
	if string(body.EffectiveItem) != `{"present":false}` {
		t.Fatalf("effectiveItem = %s, want absent", body.EffectiveItem)
	}
	if body.TotalChanges != 0 || len(body.Changes) != 0 {
		t.Fatalf("changes = %d, want empty", body.TotalChanges)
	}
}

func TestItemHistoryWithoutAnyVersion(t *testing.T) {
	_, handler := newTestRouter(t)

	recorder := doRequest(t, handler, http.MethodGet, itemHistoryPath("new", "dev", "alpha"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeRawItemHistory(t, recorder)
	if body.EffectiveVersion != nil {
		t.Fatalf("effectiveVersion = %v, want null", body.EffectiveVersion)
	}
	if string(body.EffectiveItem) != `{"present":false}` {
		t.Fatalf("effectiveItem = %s", body.EffectiveItem)
	}
	if body.TotalChanges != 0 || len(body.Changes) != 0 {
		t.Fatalf("changes = %d, want empty", body.TotalChanges)
	}
}

func TestItemHistoryGrayOnlyKeepsHistoryAndNullEffective(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "canary2", map[string]any{"a": 2})

	recorder := doRequest(t, handler, http.MethodGet, itemHistoryPath("svc", "dev", "a"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeRawItemHistory(t, recorder)
	if body.EffectiveVersion != nil {
		t.Fatalf("effectiveVersion = %v, want null", body.EffectiveVersion)
	}
	if string(body.EffectiveItem) != `{"present":false}` {
		t.Fatalf("effectiveItem = %s", body.EffectiveItem)
	}
	if body.TotalChanges != 2 {
		t.Fatalf("totalChanges = %d, want 2", body.TotalChanges)
	}
	if body.Changes[0].ChangeType != "added" || string(*body.Changes[0].NewValue) != "1" {
		t.Fatalf("first change = %v", body.Changes[0])
	}
	if body.Changes[1].ChangeType != "modified" || string(*body.Changes[1].NewValue) != "2" {
		t.Fatalf("second change = %v", body.Changes[1])
	}
}

func TestItemHistoryReflectsCurrentEffectiveRemoval(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1, "b": 9})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 2})
	publish(t, handler, "svc", "dev", "", map[string]any{"b": 9})

	body := decodeRawItemHistory(t, doRequest(t, handler, http.MethodGet,
		itemHistoryPath("svc", "dev", "a"), nil))
	if body.EffectiveVersion == nil || *body.EffectiveVersion != 3 {
		t.Fatalf("effectiveVersion = %v, want 3", body.EffectiveVersion)
	}
	if string(body.EffectiveItem) != `{"present":false}` {
		t.Fatalf("effectiveItem = %s, want absent", body.EffectiveItem)
	}
	kinds := []string{"added", "modified", "removed"}
	if body.TotalChanges != len(kinds) {
		t.Fatalf("totalChanges = %d, want %d", body.TotalChanges, len(kinds))
	}
	for i, kind := range kinds {
		if body.Changes[i].ChangeType != kind {
			t.Fatalf("change %d = %s, want %s", i, body.Changes[i].ChangeType, kind)
		}
	}
	removed := body.Changes[2]
	if removed.OldValue == nil || string(*removed.OldValue) != "2" || removed.NewValue != nil {
		t.Fatalf("removed change = %v", removed)
	}
}

func TestItemHistorySkipsUnchangedAndAppliesCanonicalSemantics(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"obj": json.RawMessage(`{"b": 2, "a": 1}`)})
	publish(t, handler, "svc", "dev", "", map[string]any{"obj": json.RawMessage(`{ "a" : 1 , "b" : 2 }`)})
	publish(t, handler, "svc", "dev", "", map[string]any{"obj": json.RawMessage(`{"a":1,"b":3}`)})

	recorder := doRequest(t, handler, http.MethodGet, itemHistoryPath("svc", "dev", "obj"), nil)
	body := decodeRawItemHistory(t, recorder)
	if body.TotalChanges != 2 || len(body.Changes) != 2 {
		t.Fatalf("totalChanges = %d changes = %d, want added then one modified", body.TotalChanges, len(body.Changes))
	}
	if body.Changes[0].Version.Version != 1 || body.Changes[0].ChangeType != "added" {
		t.Fatalf("first change = %v, want added at version 1", body.Changes[0])
	}
	if string(*body.Changes[0].NewValue) != `{"a":1,"b":2}` {
		t.Fatalf("canonicalized first value = %s", *body.Changes[0].NewValue)
	}
	change := body.Changes[1]
	if change.Version.Version != 3 || change.ChangeType != "modified" {
		t.Fatalf("second change = %v, want modified at version 3", change)
	}
	if string(*change.OldValue) != `{"a":1,"b":2}` || string(*change.NewValue) != `{"a":1,"b":3}` {
		t.Fatalf("values = %s -> %s", *change.OldValue, *change.NewValue)
	}
}

func TestItemHistoryKeepsNumberAndNullSemantics(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"n": json.RawMessage(`1`), "x": nil})
	publish(t, handler, "svc", "dev", "", map[string]any{"n": json.RawMessage(`1.0`), "x": nil})

	number := decodeRawItemHistory(t, doRequest(t, handler, http.MethodGet,
		itemHistoryPath("svc", "dev", "n"), nil))
	if number.TotalChanges != 2 {
		t.Fatalf("1 vs 1.0 must differ after the initial add, changes = %d", number.TotalChanges)
	}
	modified := number.Changes[1]
	if modified.ChangeType != "modified" || string(*modified.OldValue) != "1" || string(*modified.NewValue) != "1.0" {
		t.Fatalf("number change = %v", modified)
	}

	nullRecorder := doRequest(t, handler, http.MethodGet, itemHistoryPath("svc", "dev", "x"), nil)
	var rawNull struct {
		EffectiveItem map[string]json.RawMessage   `json:"effectiveItem"`
		Changes       []map[string]json.RawMessage `json:"changes"`
		TotalChanges  int                          `json:"totalChanges"`
	}
	if err := json.Unmarshal(nullRecorder.Body.Bytes(), &rawNull); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rawNull.TotalChanges != 1 {
		t.Fatalf("null must be added once, changes = %d", rawNull.TotalChanges)
	}
	first := rawNull.Changes[0]
	if string(first["changeType"]) != `"added"` || string(first["newValue"]) != "null" {
		t.Fatalf("first change = %v", first)
	}
	if _, hasOld := first["oldValue"]; hasOld {
		t.Fatalf("added change must omit oldValue: %v", first)
	}
	if string(rawNull.EffectiveItem["present"]) != "true" || string(rawNull.EffectiveItem["value"]) != "null" {
		t.Fatalf("effectiveItem = %v, want present with null value", rawNull.EffectiveItem)
	}
}

func TestItemHistoryDoesNotTrimItemName(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{" spaced ": 1, "trimmed": 2})

	recorder := doRequest(t, handler, http.MethodGet, itemHistoryPath("svc", "dev", " spaced "), nil)
	body := decodeRawItemHistory(t, recorder)
	if body.Name != " spaced " {
		t.Fatalf("name = %q", body.Name)
	}
	if body.TotalChanges != 1 || body.Changes[0].ChangeType != "added" {
		t.Fatalf("changes = %v", body.Changes)
	}

	trimmed := doRequest(t, handler, http.MethodGet, itemHistoryPath("svc", "dev", "trimmed"), nil)
	if decodeRawItemHistory(t, trimmed).TotalChanges != 1 {
		t.Fatalf("untrimmed name lookup must still work")
	}

	empty := doRequest(t, handler, http.MethodGet,
		"/config-item-histories?namespace=svc&environment=dev&name=%20%20", nil)
	if empty.Code != http.StatusOK || decodeRawItemHistory(t, empty).TotalChanges != 0 {
		t.Fatalf("whitespace-only name is a distinct unknown name, got %d %s", empty.Code, empty.Body.String())
	}
}

func TestItemHistoryErrorContract(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})

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
			if code := errorCode(decode(t, recorder)); code != tc.wantCode {
				t.Fatalf("code = %s, want %s", code, tc.wantCode)
			}
		})
	}
}

func TestItemHistoryCreatesNoHistoryWrite(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 2})

	before := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	doRequest(t, handler, http.MethodGet, itemHistoryPath("svc", "dev", "a"), nil)
	after := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	if before.Body.String() != after.Body.String() {
		t.Fatalf("item history query changed history:\nbefore %s\nafter  %s", before.Body.String(), after.Body.String())
	}
}
