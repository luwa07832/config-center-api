package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func promotePath(ns, env, version string) string {
	return "/namespaces/" + ns + "/environments/" + env + "/config-versions/" + version + "/promote"
}

func TestPromoteCreatesEffectiveVersionWithPromotionMetadata(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"a": "old", "n": json.RawMessage("1")})
	publish(t, handler, "payments", "prod", "canary", map[string]any{
		"a": "new", "n": json.RawMessage("1.0"), "flag": true, "empty": nil,
	})

	recorder := doRequest(t, handler, http.MethodPost, promotePath("payments", "prod", "2"), nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Namespace   string      `json:"namespace"`
		Environment string      `json:"environment"`
		Version     VersionInfo `json:"version"`
		Items       map[string]json.RawMessage
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Namespace != "payments" || body.Environment != "prod" {
		t.Fatalf("scope = %s/%s", body.Namespace, body.Environment)
	}
	if body.Version.Version != 3 {
		t.Fatalf("version = %d, want 3", body.Version.Version)
	}
	if body.Version.GrayTag != nil {
		t.Fatalf("grayTag = %v, want null", body.Version.GrayTag)
	}
	if body.Version.RollbackOf != nil {
		t.Fatalf("rollbackOf = %v, want null", body.Version.RollbackOf)
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
	wantItems := map[string]string{"a": `"new"`, "n": "1.0", "flag": "true", "empty": "null"}
	if len(body.Items) != len(wantItems) {
		t.Fatalf("items = %v", body.Items)
	}
	for name, value := range wantItems {
		if string(body.Items[name]) != value {
			t.Fatalf("item %s = %s, want %s", name, body.Items[name], value)
		}
	}

	effective := doRequest(t, handler, http.MethodGet,
		"/effective-configs?namespace=payments&environment=prod", nil)
	var eff struct {
		EffectiveVersion int64                      `json:"effectiveVersion"`
		Items            map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(effective.Body.Bytes(), &eff); err != nil {
		t.Fatalf("decode effective: %v", err)
	}
	if eff.EffectiveVersion != 3 || string(eff.Items["a"]) != `"new"` {
		t.Fatalf("effective config after promotion: %s", effective.Body.String())
	}

	history := doRequest(t, handler, http.MethodGet,
		"/config-versions?namespace=payments&environment=prod", nil)
	var hist struct {
		Versions []VersionInfo `json:"versions"`
	}
	if err := json.Unmarshal(history.Body.Bytes(), &hist); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(hist.Versions) != 3 {
		t.Fatalf("history length = %d, want 3", len(hist.Versions))
	}
	source := hist.Versions[1]
	if source.GrayTag == nil || *source.GrayTag != "canary" || source.PromotionOf != nil {
		t.Fatalf("source version changed: %+v", source)
	}
	if hist.Versions[0].PromotionOf != nil || hist.Versions[0].RollbackOf != nil {
		t.Fatalf("normal release must show null promotionOf: %+v", hist.Versions[0])
	}
}

func TestPromoteShowsPromotionOfInEveryVersionRead(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 2})
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 3})
	pr := doRequest(t, handler, http.MethodPost, promotePath("svc", "dev", "2"), nil)
	if pr.Code != http.StatusCreated {
		t.Fatalf("promote status = %d body = %s", pr.Code, pr.Body.String())
	}

	snapshot := doRequest(t, handler, http.MethodGet, snapshotPath("svc", "dev", "4"), nil)
	var snap struct{ Version VersionInfo }
	if err := json.Unmarshal(snapshot.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.Version.PromotionOf == nil || *snap.Version.PromotionOf != 2 {
		t.Fatalf("snapshot promotionOf = %v", snap.Version.PromotionOf)
	}

	diff := doRequest(t, handler, http.MethodGet,
		"/config-version-diffs?namespace=svc&environment=dev&baseVersion=1&targetVersion=4", nil)
	if diff.Code != http.StatusOK {
		t.Fatalf("diff status = %d body = %s", diff.Code, diff.Body.String())
	}
	decodedDiff := decode(t, diff)
	targetVersion, _ := decodedDiff["targetVersion"].(map[string]any)
	if targetVersion["promotionOf"].(float64) != 2 {
		t.Fatalf("diff target promotionOf = %v", targetVersion["promotionOf"])
	}
	baseVersion, _ := decodedDiff["baseVersion"].(map[string]any)
	if baseVersion["promotionOf"] != nil {
		t.Fatalf("diff base promotionOf = %v, want null", baseVersion["promotionOf"])
	}

	itemHistory := doRequest(t, handler, http.MethodGet,
		"/config-item-histories?namespace=svc&environment=dev&name=a", nil)
	var item struct {
		Changes []struct{ Version VersionInfo } `json:"changes"`
	}
	if err := json.Unmarshal(itemHistory.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode item history: %v", err)
	}
	var promotedEntry *struct{ Version VersionInfo }
	for i := range item.Changes {
		if item.Changes[i].Version.Version == 4 {
			promotedEntry = &item.Changes[i]
		}
	}
	if promotedEntry == nil {
		t.Fatalf("promoted version 4 must appear in item history changes")
	}
	if promotedEntry.Version.PromotionOf == nil || *promotedEntry.Version.PromotionOf != 2 {
		t.Fatalf("item history promoted version = %+v", promotedEntry.Version)
	}
}

func TestPromoteErrorContractAndNoSideEffects(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 2})
	publish(t, handler, "other", "dev", "", map[string]any{"a": 9})
	publish(t, handler, "other", "dev", "", map[string]any{"a": 8})
	publish(t, handler, "other", "dev", "", map[string]any{"a": 7})

	cases := []struct {
		name       string
		target     string
		wantStatus int
		wantCode   string
	}{
		{"zero", promotePath("svc", "dev", "0"), http.StatusBadRequest, "INVALID_VERSION"},
		{"negative", promotePath("svc", "dev", "-2"), http.StatusBadRequest, "INVALID_VERSION"},
		{"non numeric", promotePath("svc", "dev", "two"), http.StatusBadRequest, "INVALID_VERSION"},
		{"fraction", promotePath("svc", "dev", "2.0"), http.StatusBadRequest, "INVALID_VERSION"},
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

	history := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	var hist struct {
		Versions []VersionInfo `json:"versions"`
	}
	if err := json.Unmarshal(history.Body.Bytes(), &hist); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(hist.Versions) != 2 {
		t.Fatalf("failed promotions must not create versions, got %d", len(hist.Versions))
	}
	for _, v := range hist.Versions {
		if v.PromotionOf != nil {
			t.Fatalf("failed promotion left promotionOf: %+v", v)
		}
	}
	effective := doRequest(t, handler, http.MethodGet,
		"/effective-configs?namespace=svc&environment=dev", nil)
	var eff struct{ EffectiveVersion *int64 }
	if err := json.Unmarshal(effective.Body.Bytes(), &eff); err != nil {
		t.Fatalf("decode effective: %v", err)
	}
	if eff.EffectiveVersion == nil || *eff.EffectiveVersion != 1 {
		t.Fatalf("effective version must stay 1: %s", effective.Body.String())
	}
}

func TestRollbackAndOldVersionsCarryNullPromotionOf(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 2})
	rb := doRequest(t, handler, http.MethodPost,
		"/namespaces/svc/environments/dev/config-versions/1/rollback", nil)
	if rb.Code != http.StatusCreated {
		t.Fatalf("rollback status = %d body = %s", rb.Code, rb.Body.String())
	}
	var body struct{ Version VersionInfo }
	if err := json.Unmarshal(rb.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode rollback: %v", err)
	}
	if body.Version.RollbackOf == nil || *body.Version.RollbackOf != 1 {
		t.Fatalf("rollbackOf = %v, want 1", body.Version.RollbackOf)
	}
	if body.Version.PromotionOf != nil {
		t.Fatalf("rollback promotionOf = %v, want null", body.Version.PromotionOf)
	}
}
