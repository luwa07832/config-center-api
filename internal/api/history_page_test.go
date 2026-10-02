package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

type rawHistoryPage struct {
	Namespace        string        `json:"namespace"`
	Environment      string        `json:"environment"`
	Versions         []VersionInfo `json:"versions"`
	TotalVersions    int64         `json:"totalVersions"`
	NextAfterVersion int64         `json:"nextAfterVersion"`
	HasMore          bool          `json:"hasMore"`
}

func decodeHistoryPage(t *testing.T, recorder *httptest.ResponseRecorder) rawHistoryPage {
	t.Helper()
	var body rawHistoryPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return body
}

func historyPagePath(ns, env string, query string) string {
	return "/config-versions?namespace=" + ns + "&environment=" + env + query
}

func pageVersions(body rawHistoryPage) []int64 {
	versions := make([]int64, 0, len(body.Versions))
	for _, info := range body.Versions {
		versions = append(versions, info.Version)
	}
	return versions
}

func TestHistoryWithoutPaginationParamsKeepsLegacyShape(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 1})
	publish(t, handler, "payments", "prod", "canary", map[string]any{"a": 2})

	recorder := doRequest(t, handler, http.MethodGet, historyPagePath("payments", "prod", ""), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	for _, key := range []string{"totalVersions", "nextAfterVersion", "hasMore"} {
		if _, exists := body[key]; exists {
			t.Fatalf("legacy response must not contain %q: %s", key, recorder.Body.String())
		}
	}
	versions, ok := body["versions"].([]any)
	if !ok || len(versions) != 2 {
		t.Fatalf("versions = %v", body["versions"])
	}
}

func TestHistoryPaginationWalksAllPagesInOrder(t *testing.T) {
	_, handler := newTestRouter(t)
	for i := 0; i < 5; i++ {
		publish(t, handler, "payments", "prod", "", map[string]any{"n": i})
	}
	publish(t, handler, "payments", "staging", "", map[string]any{"n": 99})

	var walked []int64
	after := ""
	for {
		recorder := doRequest(t, handler, http.MethodGet, historyPagePath("payments", "prod", "&limit=2"+after), nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
		}
		body := decodeHistoryPage(t, recorder)
		if body.Namespace != "payments" || body.Environment != "prod" {
			t.Fatalf("scope = %s/%s", body.Namespace, body.Environment)
		}
		if body.TotalVersions != 5 {
			t.Fatalf("totalVersions = %d, want 5", body.TotalVersions)
		}
		walked = append(walked, pageVersions(body)...)
		if !body.HasMore {
			if len(body.Versions) > 0 && body.NextAfterVersion != body.Versions[len(body.Versions)-1].Version {
				t.Fatalf("nextAfterVersion = %d, want %d", body.NextAfterVersion, body.Versions[len(body.Versions)-1].Version)
			}
			break
		}
		after = fmt.Sprintf("&afterVersion=%d", body.NextAfterVersion)
	}
	want := []int64{1, 2, 3, 4, 5}
	if len(walked) != len(want) {
		t.Fatalf("walked = %v, want %v", walked, want)
	}
	for i := range want {
		if walked[i] != want[i] {
			t.Fatalf("walked = %v, want %v", walked, want)
		}
	}
}

func TestHistoryPaginationDefaultsLimitTo100(t *testing.T) {
	_, handler := newTestRouter(t)
	for i := 0; i < 105; i++ {
		publish(t, handler, "svc", "dev", "", map[string]any{"n": i})
	}

	recorder := doRequest(t, handler, http.MethodGet, historyPagePath("svc", "dev", "&afterVersion=0"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeHistoryPage(t, recorder)
	if len(body.Versions) != 100 || !body.HasMore || body.NextAfterVersion != 100 || body.TotalVersions != 105 {
		t.Fatalf("page = %+v", body)
	}

	recorder = doRequest(t, handler, http.MethodGet, historyPagePath("svc", "dev", "&afterVersion=100"), nil)
	body = decodeHistoryPage(t, recorder)
	if len(body.Versions) != 5 || body.HasMore || body.NextAfterVersion != 105 {
		t.Fatalf("page = %+v", body)
	}
}

func TestHistoryPaginationBeyondMaxReturnsEmptyPage(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 1})
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 2})

	recorder := doRequest(t, handler, http.MethodGet, historyPagePath("payments", "prod", "&limit=10&afterVersion=99"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeHistoryPage(t, recorder)
	if body.Versions == nil || len(body.Versions) != 0 {
		t.Fatalf("versions = %v, want empty array", body.Versions)
	}
	if body.TotalVersions != 2 || body.NextAfterVersion != 99 || body.HasMore {
		t.Fatalf("page = %+v", body)
	}
}

func TestHistoryPaginationEmptyScopeReturnsZeroCursor(t *testing.T) {
	_, handler := newTestRouter(t)

	recorder := doRequest(t, handler, http.MethodGet, historyPagePath("missing", "prod", "&limit=10"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeHistoryPage(t, recorder)
	if body.Versions == nil || len(body.Versions) != 0 {
		t.Fatalf("versions = %v, want empty array", body.Versions)
	}
	if body.TotalVersions != 0 || body.NextAfterVersion != 0 || body.HasMore {
		t.Fatalf("page = %+v", body)
	}
}

func TestHistoryPaginationPageIsStableAcrossNewPublishes(t *testing.T) {
	_, handler := newTestRouter(t)
	for i := 0; i < 3; i++ {
		publish(t, handler, "payments", "prod", "", map[string]any{"n": i})
	}
	first := doRequest(t, handler, http.MethodGet, historyPagePath("payments", "prod", "&limit=2&afterVersion=0"), nil)
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", first.Code, first.Body.String())
	}
	publish(t, handler, "payments", "prod", "", map[string]any{"n": 3})
	publish(t, handler, "payments", "prod", "", map[string]any{"n": 4})
	second := doRequest(t, handler, http.MethodGet, historyPagePath("payments", "prod", "&limit=2&afterVersion=0"), nil)

	before, after := decodeHistoryPage(t, first), decodeHistoryPage(t, second)
	if got := pageVersions(before); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("first page = %v", got)
	}
	if got := pageVersions(after); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("re-read page = %v, want [1 2]", got)
	}
	if before.NextAfterVersion != after.NextAfterVersion || before.HasMore != after.HasMore {
		t.Fatalf("cursor moved: %+v vs %+v", before, after)
	}
	if after.TotalVersions != 5 {
		t.Fatalf("totalVersions = %d, want 5", after.TotalVersions)
	}
}

func TestHistoryPaginationMarksEffectiveVersion(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 1})
	publish(t, handler, "payments", "prod", "canary", map[string]any{"a": 2})

	recorder := doRequest(t, handler, http.MethodGet, historyPagePath("payments", "prod", "&limit=10"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decodeHistoryPage(t, recorder)
	if len(body.Versions) != 2 {
		t.Fatalf("versions = %v", pageVersions(body))
	}
	if !body.Versions[0].Effective || body.Versions[1].Effective {
		t.Fatalf("effective flags = %v,%v", body.Versions[0].Effective, body.Versions[1].Effective)
	}
	if body.Versions[1].GrayTag == nil || *body.Versions[1].GrayTag != "canary" {
		t.Fatalf("grayTag = %v", body.Versions[1].GrayTag)
	}
}

func TestHistoryPaginationValidationErrors(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})

	cases := []struct {
		name   string
		target string
		status int
		code   string
	}{
		{"missing namespace", "/config-versions?environment=dev&limit=5", http.StatusBadRequest, "MISSING_SCOPE"},
		{"missing environment", "/config-versions?namespace=svc&afterVersion=1", http.StatusBadRequest, "MISSING_SCOPE"},
		{"limit zero", historyPagePath("svc", "dev", "&limit=0"), http.StatusBadRequest, "INVALID_PAGE_SIZE"},
		{"limit above max", historyPagePath("svc", "dev", "&limit=101"), http.StatusBadRequest, "INVALID_PAGE_SIZE"},
		{"limit negative", historyPagePath("svc", "dev", "&limit=-1"), http.StatusBadRequest, "INVALID_PAGE_SIZE"},
		{"limit decimal", historyPagePath("svc", "dev", "&limit=1.5"), http.StatusBadRequest, "INVALID_PAGE_SIZE"},
		{"limit text", historyPagePath("svc", "dev", "&limit=abc"), http.StatusBadRequest, "INVALID_PAGE_SIZE"},
		{"limit empty", historyPagePath("svc", "dev", "&limit="), http.StatusBadRequest, "INVALID_PAGE_SIZE"},
		{"limit leading zero", historyPagePath("svc", "dev", "&limit=05"), http.StatusBadRequest, "INVALID_PAGE_SIZE"},
		{"cursor negative", historyPagePath("svc", "dev", "&afterVersion=-1"), http.StatusBadRequest, "INVALID_CURSOR_VERSION"},
		{"cursor decimal", historyPagePath("svc", "dev", "&afterVersion=1.5"), http.StatusBadRequest, "INVALID_CURSOR_VERSION"},
		{"cursor text", historyPagePath("svc", "dev", "&afterVersion=abc"), http.StatusBadRequest, "INVALID_CURSOR_VERSION"},
		{"cursor empty", historyPagePath("svc", "dev", "&afterVersion="), http.StatusBadRequest, "INVALID_CURSOR_VERSION"},
		{"cursor leading zero", historyPagePath("svc", "dev", "&afterVersion=01"), http.StatusBadRequest, "INVALID_CURSOR_VERSION"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := doRequest(t, handler, http.MethodGet, tc.target, nil)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d body = %s", recorder.Code, tc.status, recorder.Body.String())
			}
			if code := errorCode(decode(t, recorder)); code != tc.code {
				t.Fatalf("code = %s, want %s", code, tc.code)
			}
		})
	}
}

func TestHistoryPaginationAcceptsBoundaryValues(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})

	for _, query := range []string{"&limit=1", "&limit=100", "&afterVersion=0", "&limit=1&afterVersion=1"} {
		recorder := doRequest(t, handler, http.MethodGet, historyPagePath("svc", "dev", query), nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body = %s", query, recorder.Code, recorder.Body.String())
		}
	}
}

func TestHistoryPaginationIsReadOnly(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 2})

	before := doRequest(t, handler, http.MethodGet, historyPagePath("svc", "dev", ""), nil)
	for i := 0; i < 3; i++ {
		recorder := doRequest(t, handler, http.MethodGet, historyPagePath("svc", "dev", "&limit=1"), nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
		}
	}
	after := doRequest(t, handler, http.MethodGet, historyPagePath("svc", "dev", ""), nil)
	if before.Body.String() != after.Body.String() {
		t.Fatalf("history changed after paged reads:\nbefore %s\nafter %s", before.Body.String(), after.Body.String())
	}
}

func TestHistoryPaginationStorageUnavailable(t *testing.T) {
	st, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	recorder := doRequest(t, handler, http.MethodGet, historyPagePath("svc", "dev", "&limit=5"), nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if code := errorCode(decode(t, recorder)); code != "storage_unavailable" {
		t.Fatalf("code = %s, want storage_unavailable", code)
	}
}
