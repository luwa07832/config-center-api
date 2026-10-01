package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/luwa07832/config-center-api/internal/store"
)

type diffResponseBody struct {
	Namespace   string `json:"namespace"`
	Environment string `json:"environment"`
	BaseVersion struct {
		Version               int64   `json:"version"`
		GrayLabel             *string `json:"grayLabel"`
		RollbackSourceVersion *int64  `json:"rollbackSourceVersion"`
	} `json:"baseVersion"`
	TargetVersion struct {
		Version               int64   `json:"version"`
		GrayLabel             *string `json:"grayLabel"`
		RollbackSourceVersion *int64  `json:"rollbackSourceVersion"`
	} `json:"targetVersion"`
	CurrentVersion *int64 `json:"currentVersion"`
	ChangedCount   int    `json:"changedCount"`
	Diffs          []struct {
		Name           string          `json:"name"`
		Type           string          `json:"type"`
		OldValue       json.RawMessage `json:"oldValue"`
		NewValue       json.RawMessage `json:"newValue"`
		AffectsCurrent bool            `json:"affectsCurrent"`
	} `json:"diffs"`
}

func TestVersionDiffReportsAddedRemovedModifiedChanges(t *testing.T) {
	router, st := newDiffTestRouter(t)
	publishDiffVersion(t, st, "payments", "prod", 3, map[string]json.RawMessage{
		"timeout":  json.RawMessage(`1000`),
		"retries":  json.RawMessage(`true`),
		"currency": json.RawMessage(`"USD"`),
		"note":     json.RawMessage(`null`),
	}, nil, nil)
	publishDiffVersion(t, st, "payments", "prod", 5, map[string]json.RawMessage{
		"timeout":  json.RawMessage(`9007199254740993`),
		"currency": json.RawMessage(`"USD"`),
		"region":   json.RawMessage(`"cn"`),
		"note":     json.RawMessage(`null`),
	}, diffStringPtr("gray-5"), diffInt64Ptr(3))
	if err := st.SetActiveVersion("payments", "prod", 5); err != nil {
		t.Fatalf("set active: %v", err)
	}

	recorder := doDiffRequest(router, "/api/v1/namespaces/payments/environments/prod/versions/diff?baseVersion=3&targetVersion=5")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	var body diffResponseBody
	decodeDiffBody(t, recorder, &body)
	if body.ChangedCount != 3 {
		t.Fatalf("changedCount = %d, want 3", body.ChangedCount)
	}
	if len(body.Diffs) != 3 {
		t.Fatalf("diffs length = %d, want 3", len(body.Diffs))
	}
	if body.CurrentVersion == nil || *body.CurrentVersion != 5 {
		t.Fatalf("currentVersion = %v, want 5", body.CurrentVersion)
	}
	if body.TargetVersion.GrayLabel == nil || *body.TargetVersion.GrayLabel != "gray-5" {
		t.Fatalf("target gray label = %v, want gray-5", body.TargetVersion.GrayLabel)
	}
	if body.TargetVersion.RollbackSourceVersion == nil || *body.TargetVersion.RollbackSourceVersion != 3 {
		t.Fatalf("rollback source = %v, want 3", body.TargetVersion.RollbackSourceVersion)
	}

	wants := []struct {
		name    string
		typ     string
		old     string
		new     string
		current bool
	}{
		{name: "region", typ: "added", new: `"cn"`, current: true},
		{name: "retries", typ: "removed", old: `true`, current: true},
		{name: "timeout", typ: "modified", old: `1000`, new: `9007199254740993`, current: true},
	}
	for index, want := range wants {
		got := body.Diffs[index]
		if got.Name != want.name || got.Type != want.typ {
			t.Fatalf("diff %d = %s/%s, want %s/%s", index, got.Name, got.Type, want.name, want.typ)
		}
		if want.old != "" && string(got.OldValue) != want.old {
			t.Fatalf("%s oldValue = %s, want %s", got.Name, got.OldValue, want.old)
		}
		if want.new != "" && string(got.NewValue) != want.new {
			t.Fatalf("%s newValue = %s, want %s", got.Name, got.NewValue, want.new)
		}
		if got.AffectsCurrent != want.current {
			t.Fatalf("%s affectsCurrent = %v, want %v", got.Name, got.AffectsCurrent, want.current)
		}
	}
}

func TestVersionDiffSameVersionHasEmptyArrayAndZeroCount(t *testing.T) {
	router, st := newDiffTestRouter(t)
	items := map[string]json.RawMessage{"same": json.RawMessage(`"value"`)}
	publishDiffVersion(t, st, "ns", "dev", 2, items, nil, nil)

	recorder := doDiffRequest(router, "/api/v1/config-versions/diff?namespace=ns&environment=dev&baseVersion=2&targetVersion=2")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	var body diffResponseBody
	decodeDiffBody(t, recorder, &body)
	if body.ChangedCount != 0 {
		t.Fatalf("changedCount = %d, want 0", body.ChangedCount)
	}
	if len(body.Diffs) != 0 {
		t.Fatalf("diffs = %v, want empty", body.Diffs)
	}
	if !containsJSON(recorder.Body.String(), `"diffs":[]`) {
		t.Fatalf("empty diffs must serialize as [], body: %s", recorder.Body.String())
	}
}

func TestVersionDiffPathStyleUsesSameContract(t *testing.T) {
	router, st := newDiffTestRouter(t)
	publishDiffVersion(t, st, "payments", "prod", 7, map[string]json.RawMessage{"a": json.RawMessage(`1`)}, nil, nil)
	publishDiffVersion(t, st, "payments", "prod", 8, map[string]json.RawMessage{"a": json.RawMessage(`2`)}, nil, nil)

	recorder := doDiffRequest(router, "/api/v1/namespaces/payments/environments/prod/versions/diff?baseVersion=7&targetVersion=8")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", recorder.Code, recorder.Body.String())
	}
	var body diffResponseBody
	decodeDiffBody(t, recorder, &body)
	if body.Namespace != "payments" || body.Environment != "prod" || body.ChangedCount != 1 {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestVersionDiffMarksCurrentEffectsUsingActiveVersion(t *testing.T) {
	router, st := newDiffTestRouter(t)
	publishDiffVersion(t, st, "ns", "prod", 1, map[string]json.RawMessage{
		"shared": json.RawMessage(`"old"`),
		"only":   json.RawMessage(`1`),
	}, nil, nil)
	publishDiffVersion(t, st, "ns", "prod", 2, map[string]json.RawMessage{
		"shared": json.RawMessage(`"new"`),
		"extra":  json.RawMessage(`2`),
	}, nil, nil)
	publishDiffVersion(t, st, "ns", "prod", 3, map[string]json.RawMessage{
		"shared": json.RawMessage(`"new"`),
		"extra":  json.RawMessage(`3`),
	}, nil, nil)
	if err := st.SetActiveVersion("ns", "prod", 3); err != nil {
		t.Fatalf("set active: %v", err)
	}

	recorder := doDiffRequest(router, "/api/v1/config-versions/diff?namespace=ns&environment=prod&baseVersion=1&targetVersion=2")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		AffectsCurrent bool `json:"affectsCurrent"`
		Diffs          []struct {
			Name           string `json:"name"`
			AffectsCurrent bool   `json:"affectsCurrent"`
		} `json:"diffs"`
	}
	decodeDiffBody(t, recorder, &body)
	if !body.AffectsCurrent {
		t.Fatalf("top-level affectsCurrent = false, want true")
	}
	want := map[string]bool{"shared": true, "only": true, "extra": false}
	for _, diff := range body.Diffs {
		if diff.AffectsCurrent != want[diff.Name] {
			t.Fatalf("%s affectsCurrent = %v, want %v", diff.Name, diff.AffectsCurrent, want[diff.Name])
		}
	}
}

func TestVersionDiffValidationErrors(t *testing.T) {
	router, st := newDiffTestRouter(t)
	publishDiffVersion(t, st, "payments", "prod", 3, nil, nil, nil)
	publishDiffVersion(t, st, "billing", "prod", 4, nil, nil, nil)

	cases := []struct {
		name   string
		path   string
		status int
		code   string
	}{
		{name: "missing namespace", path: "/api/v1/config-versions/diff?environment=prod&baseVersion=1&targetVersion=2", status: http.StatusBadRequest, code: "MISSING_SCOPE"},
		{name: "missing environment", path: "/api/v1/config-versions/diff?namespace=payments&baseVersion=1&targetVersion=2", status: http.StatusBadRequest, code: "MISSING_SCOPE"},
		{name: "base not integer", path: "/api/v1/config-versions/diff?namespace=payments&environment=prod&baseVersion=v1&targetVersion=2", status: http.StatusBadRequest, code: "INVALID_VERSION"},
		{name: "target zero", path: "/api/v1/config-versions/diff?namespace=payments&environment=prod&baseVersion=1&targetVersion=0", status: http.StatusBadRequest, code: "INVALID_VERSION"},
		{name: "target negative", path: "/api/v1/config-versions/diff?namespace=payments&environment=prod&baseVersion=2&targetVersion=-1", status: http.StatusBadRequest, code: "INVALID_VERSION"},
		{name: "base decimal", path: "/api/v1/config-versions/diff?namespace=payments&environment=prod&baseVersion=1.5&targetVersion=2", status: http.StatusBadRequest, code: "INVALID_VERSION"},
		{name: "base too large", path: "/api/v1/config-versions/diff?namespace=payments&environment=prod&baseVersion=999999999999999999999999&targetVersion=2", status: http.StatusBadRequest, code: "INVALID_VERSION"},
		{name: "base plus sign", path: "/api/v1/config-versions/diff?namespace=payments&environment=prod&baseVersion=%2B1&targetVersion=2", status: http.StatusBadRequest, code: "INVALID_VERSION"},
		{name: "reversed versions", path: "/api/v1/config-versions/diff?namespace=payments&environment=prod&baseVersion=3&targetVersion=2", status: http.StatusConflict, code: "VERSION_ORDER_CONFLICT"},
		{name: "missing version", path: "/api/v1/config-versions/diff?namespace=payments&environment=prod&baseVersion=3&targetVersion=9", status: http.StatusNotFound, code: "VERSION_NOT_FOUND"},
		{name: "scope mismatch", path: "/api/v1/config-versions/diff?namespace=payments&environment=prod&baseVersion=3&targetVersion=4", status: http.StatusConflict, code: "VERSION_SCOPE_MISMATCH"},
		{name: "base scope mismatch before target lookup", path: "/api/v1/config-versions/diff?namespace=payments&environment=prod&baseVersion=4&targetVersion=99", status: http.StatusConflict, code: "VERSION_SCOPE_MISMATCH"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := doDiffRequest(router, tc.path)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d; body: %s", recorder.Code, tc.status, recorder.Body.String())
			}
			if !containsJSON(recorder.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("body = %s, want error code %s", recorder.Body.String(), tc.code)
			}
		})
	}
}

func TestVersionDiffIsReadOnly(t *testing.T) {
	router, st := newDiffTestRouter(t)
	publishDiffVersion(t, st, "payments", "prod", 1, map[string]json.RawMessage{"a": json.RawMessage(`1`)}, nil, nil)
	publishDiffVersion(t, st, "payments", "prod", 2, map[string]json.RawMessage{"a": json.RawMessage(`2`)}, nil, nil)
	before := doDiffRequest(router, "/api/v1/config-versions/diff?namespace=payments&environment=prod&baseVersion=1&targetVersion=2")
	after := doDiffRequest(router, "/api/v1/config-versions/diff?namespace=payments&environment=prod&baseVersion=1&targetVersion=2")
	if before.Body.String() != after.Body.String() {
		t.Fatalf("repeat query changed body:\nbefore %s\nafter %s", before.Body.String(), after.Body.String())
	}
	active, ok, err := st.ActiveVersion("payments", "prod")
	if err != nil || ok {
		t.Fatalf("active version = %d/%v, err %v; want none", active, ok, err)
	}
}

func TestVersionDiffSupportsDirectlyCreatedCandidateSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer raw.Close()
	statements := []string{
		`CREATE TABLE configuration_versions (
			id INTEGER PRIMARY KEY, namespace_name TEXT NOT NULL, environment_name TEXT NOT NULL,
			version_number INTEGER NOT NULL, canary_label TEXT, rollback_version INTEGER)`,
		`CREATE TABLE configuration_items (
			id INTEGER PRIMARY KEY, version_id INTEGER NOT NULL, key TEXT NOT NULL, config_value TEXT NOT NULL)`,
		`INSERT INTO configuration_versions(id, namespace_name, environment_name, version_number, canary_label)
		 VALUES (10, 'payments', 'prod', 1, NULL)`,
		`INSERT INTO configuration_versions(id, namespace_name, environment_name, version_number, canary_label, rollback_version)
		 VALUES (11, 'payments', 'prod', 2, 'gray', 1)`,
		`INSERT INTO configuration_items(version_id, key, config_value) VALUES (10, 'old', '"v1"')`,
		`INSERT INTO configuration_items(version_id, key, config_value) VALUES (10, 'same', 'true')`,
		`INSERT INTO configuration_items(version_id, key, config_value) VALUES (11, 'new', '3')`,
		`INSERT INTO configuration_items(version_id, key, config_value) VALUES (11, 'same', 'true')`,
	}
	for _, statement := range statements {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("seed %q: %v", statement, err)
		}
	}

	recorder := doDiffRequest(NewRouter(st), "/api/v1/config-versions/diff?namespace=payments&environment=prod&baseVersion=1&targetVersion=2")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	bodyText := recorder.Body.String()
	for _, want := range []string{
		`"changedCount":2`,
		`"name":"new"`,
		`"type":"added"`,
		`"newValue":3`,
		`"name":"old"`,
		`"type":"removed"`,
		`"oldValue":"v1"`,
		`"grayLabel":"gray"`,
		`"rollbackSourceVersion":1`,
	} {
		if !stringContains(bodyText, want) {
			t.Fatalf("body missing %s: %s", want, bodyText)
		}
	}
}

func newDiffTestRouter(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewRouter(st), st
}

func publishDiffVersion(t *testing.T, st *store.Store, namespace, environment string, version int64, items map[string]json.RawMessage, gray *string, rollback *int64) {
	t.Helper()
	err := st.PublishVersion(store.VersionInput{
		Namespace:             namespace,
		Environment:           environment,
		Version:               version,
		GrayLabel:             gray,
		RollbackSourceVersion: rollback,
		Items:                 items,
	})
	if err != nil {
		t.Fatalf("publish version %d: %v", version, err)
	}
}

func doDiffRequest(router http.Handler, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func decodeDiffBody(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode body %s: %v", recorder.Body.String(), err)
	}
}

func containsJSON(body, fragment string) bool {
	return len(body) >= len(fragment) && stringContains(body, fragment)
}

func stringContains(body, fragment string) bool {
	for i := 0; i+len(fragment) <= len(body); i++ {
		if body[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}

func diffStringPtr(value string) *string { return &value }
func diffInt64Ptr(value int64) *int64    { return &value }
