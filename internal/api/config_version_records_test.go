package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

type rawVersionRecord struct {
	Version     int64                      `json:"version"`
	GrayTag     *string                    `json:"grayTag"`
	RollbackOf  *int64                     `json:"rollbackOf"`
	PromotionOf *int64                     `json:"promotionOf"`
	CreatedAt   string                     `json:"createdAt"`
	Effective   bool                       `json:"effective"`
	Items       map[string]json.RawMessage `json:"items"`
}

type rawVersionRecords struct {
	Namespace        string             `json:"namespace"`
	Environment      string             `json:"environment"`
	MatchedCount     int64              `json:"matchedCount"`
	Versions         []rawVersionRecord `json:"versions"`
	HasMore          bool               `json:"hasMore"`
	NextAfterVersion int64              `json:"nextAfterVersion"`
}

func recordsPath(query url.Values) string {
	if len(query) == 0 {
		return "/config-version-records"
	}
	return "/config-version-records?" + query.Encode()
}

func getRecords(t *testing.T, handler http.Handler, query url.Values) rawVersionRecords {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodGet, recordsPath(query), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var body rawVersionRecords
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return body
}

func recordVersions(body rawVersionRecords) []int64 {
	versions := make([]int64, 0, len(body.Versions))
	for _, record := range body.Versions {
		versions = append(versions, record.Version)
	}
	return versions
}

func expectRecordsError(t *testing.T, handler http.Handler, query url.Values, status int, code string) {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodGet, recordsPath(query), nil)
	if recorder.Code != status {
		t.Fatalf("status = %d body = %s, want %d", recorder.Code, recorder.Body.String(), status)
	}
	if got := errorCode(decode(t, recorder)); got != code {
		t.Fatalf("code = %q, want %q", got, code)
	}
}

func TestConfigVersionRecordsReturnsScopeAndSnapshots(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"a": "1", "b": true})
	publish(t, handler, "payments", "prod", "canary", map[string]any{"a": "2"})
	promoteVersion(t, handler, "payments", "prod", "2")

	query := url.Values{}
	query.Set("namespace", "payments")
	query.Set("environment", "prod")
	body := getRecords(t, handler, query)
	if body.Namespace != "payments" || body.Environment != "prod" || body.MatchedCount != 3 {
		t.Fatalf("body = %+v", body)
	}
	if got := recordVersions(body); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("versions = %v", got)
	}
	v1 := body.Versions[0]
	if v1.GrayTag != nil || v1.RollbackOf != nil || v1.PromotionOf != nil || v1.Effective || v1.CreatedAt == "" {
		t.Fatalf("v1 = %+v", v1)
	}
	if string(v1.Items["a"]) != `"1"` || string(v1.Items["b"]) != "true" {
		t.Fatalf("v1 items = %v", v1.Items)
	}
	v2 := body.Versions[1]
	if v2.GrayTag == nil || *v2.GrayTag != "canary" || v2.Effective {
		t.Fatalf("v2 = %+v", v2)
	}
	v3 := body.Versions[2]
	if v3.GrayTag != nil || v3.PromotionOf == nil || *v3.PromotionOf != 2 || !v3.Effective {
		t.Fatalf("v3 = %+v", v3)
	}
	if body.HasMore || body.NextAfterVersion != 3 {
		t.Fatalf("paging = hasMore %v next %d", body.HasMore, body.NextAfterVersion)
	}
}

func TestConfigVersionRecordsEmptyScopeAndEmptySnapshot(t *testing.T) {
	_, handler := newTestRouter(t)
	query := url.Values{}
	query.Set("namespace", "missing")
	query.Set("environment", "prod")
	body := getRecords(t, handler, query)
	if body.MatchedCount != 0 || len(body.Versions) != 0 || body.HasMore || body.NextAfterVersion != 0 {
		t.Fatalf("empty scope body = %+v", body)
	}

	publish(t, handler, "svc", "dev", "", map[string]any{})
	query = url.Values{}
	query.Set("namespace", "svc")
	query.Set("environment", "dev")
	body = getRecords(t, handler, query)
	if len(body.Versions) != 1 || body.Versions[0].Items == nil || len(body.Versions[0].Items) != 0 {
		t.Fatalf("empty snapshot = %+v", body.Versions)
	}
}

func TestConfigVersionRecordsFilters(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"n": 1})       // v1
	publish(t, handler, "payments", "prod", "canary", map[string]any{"n": 2}) // v2
	rollbackVersion(t, handler, "payments", "prod", "1")                      // v3 rollback of 1, full
	rollbackVersion(t, handler, "payments", "prod", "2")                      // v4 rollback of 2, canary
	publish(t, handler, "payments", "staging", "", map[string]any{"n": 9})    // other scope
	publish(t, handler, "billing", "prod", "canary", map[string]any{"n": 9})  // other scope

	base := url.Values{}
	base.Set("namespace", "payments")
	base.Set("environment", "prod")

	q := cloneQuery(base)
	q.Set("grayTag", "canary")
	if got := recordVersions(getRecords(t, handler, q)); fmt.Sprint(got) != "[2 4]" {
		t.Fatalf("grayTag versions = %v", got)
	}

	q = cloneQuery(base)
	q.Set("rollbackOf", "1")
	if got := recordVersions(getRecords(t, handler, q)); fmt.Sprint(got) != "[3]" {
		t.Fatalf("rollbackOf versions = %v", got)
	}

	q = cloneQuery(base)
	q.Set("effective", "true")
	if got := recordVersions(getRecords(t, handler, q)); fmt.Sprint(got) != "[3]" {
		t.Fatalf("effective=true versions = %v", got)
	}

	q = cloneQuery(base)
	q.Set("effective", "false")
	body := getRecords(t, handler, q)
	if body.MatchedCount != 3 {
		t.Fatalf("effective=false matched = %d", body.MatchedCount)
	}
	if got := recordVersions(body); fmt.Sprint(got) != "[1 2 4]" {
		t.Fatalf("effective=false versions = %v", got)
	}

	q = cloneQuery(base)
	q.Set("grayTag", "canary")
	q.Set("rollbackOf", "2")
	q.Set("effective", "false")
	if got := recordVersions(getRecords(t, handler, q)); fmt.Sprint(got) != "[4]" {
		t.Fatalf("combined versions = %v", got)
	}
}

func cloneQuery(in url.Values) url.Values {
	out := url.Values{}
	for key, values := range in {
		out[key] = append([]string{}, values...)
	}
	return out
}

func TestConfigVersionRecordsPagination(t *testing.T) {
	_, handler := newTestRouter(t)
	for i := 0; i < 5; i++ {
		publish(t, handler, "svc", "dev", "", map[string]any{"n": i})
	}

	query := url.Values{}
	query.Set("namespace", "svc")
	query.Set("environment", "dev")
	query.Set("limit", "2")
	body := getRecords(t, handler, query)
	if body.MatchedCount != 5 || !body.HasMore || body.NextAfterVersion != 2 {
		t.Fatalf("first page = %+v", body)
	}
	if got := recordVersions(body); fmt.Sprint(got) != "[1 2]" {
		t.Fatalf("first page versions = %v", got)
	}

	query.Set("afterVersion", "2")
	body = getRecords(t, handler, query)
	if body.MatchedCount != 5 || !body.HasMore || body.NextAfterVersion != 4 {
		t.Fatalf("second page = %+v", body)
	}
	if got := recordVersions(body); fmt.Sprint(got) != "[3 4]" {
		t.Fatalf("second page versions = %v", got)
	}
	// items still travel on later pages
	if string(body.Versions[1].Items["n"]) != "3" {
		t.Fatalf("v4 items = %v", body.Versions[1].Items)
	}

	query.Set("afterVersion", "4")
	body = getRecords(t, handler, query)
	if body.MatchedCount != 5 || body.HasMore || body.NextAfterVersion != 5 {
		t.Fatalf("last page = %+v", body)
	}

	// Empty page with a filter that matches nothing resets the cursor to 0.
	query.Del("afterVersion")
	query.Set("grayTag", "nope")
	body = getRecords(t, handler, query)
	if body.MatchedCount != 0 || body.HasMore || body.NextAfterVersion != 0 || len(body.Versions) != 0 {
		t.Fatalf("no-match page = %+v", body)
	}

	// Empty page beyond the last match keeps the request cursor.
	query.Del("grayTag")
	query.Set("afterVersion", "99")
	body = getRecords(t, handler, query)
	if body.MatchedCount != 5 || body.HasMore || body.NextAfterVersion != 99 || len(body.Versions) != 0 {
		t.Fatalf("beyond page = %+v", body)
	}
}

func TestConfigVersionRecordsDefaultLimit100(t *testing.T) {
	_, handler := newTestRouter(t)
	for i := 0; i < 105; i++ {
		publish(t, handler, "svc", "dev", "", map[string]any{"n": i})
	}
	query := url.Values{}
	query.Set("namespace", "svc")
	query.Set("environment", "dev")
	body := getRecords(t, handler, query)
	if body.MatchedCount != 105 || !body.HasMore || len(body.Versions) != 100 || body.NextAfterVersion != 100 {
		t.Fatalf("default page = %+v", body)
	}
}

func TestConfigVersionRecordsValidation(t *testing.T) {
	_, handler := newTestRouter(t)

	missing := url.Values{}
	missing.Set("environment", "prod")
	expectRecordsError(t, handler, missing, http.StatusBadRequest, "MISSING_SCOPE")
	missing = url.Values{}
	missing.Set("namespace", "payments")
	expectRecordsError(t, handler, missing, http.StatusBadRequest, "MISSING_SCOPE")

	q := url.Values{}
	q.Set("namespace", "payments")
	q.Set("environment", "prod")
	q.Set("grayTag", "")
	expectRecordsError(t, handler, q, http.StatusBadRequest, "INVALID_GRAY_TAG")

	for _, bad := range []string{"0", "-1", "1.5", "abc", "1 "} {
		q = url.Values{}
		q.Set("namespace", "payments")
		q.Set("environment", "prod")
		q.Set("rollbackOf", bad)
		expectRecordsError(t, handler, q, http.StatusBadRequest, "INVALID_ROLLBACK_SOURCE")
	}

	for _, bad := range []string{"yes", "TRUE", "1", ""} {
		q = url.Values{}
		q.Set("namespace", "payments")
		q.Set("environment", "prod")
		q.Set("effective", bad)
		expectRecordsError(t, handler, q, http.StatusBadRequest, "INVALID_EFFECTIVE_FILTER")
	}

	for _, bad := range []string{"0", "101", "-1", "1.0", "x"} {
		q = url.Values{}
		q.Set("namespace", "payments")
		q.Set("environment", "prod")
		q.Set("limit", bad)
		expectRecordsError(t, handler, q, http.StatusBadRequest, "INVALID_PAGE_SIZE")
	}

	for _, bad := range []string{"-1", "1.0", "01", "x", ""} {
		q = url.Values{}
		q.Set("namespace", "payments")
		q.Set("environment", "prod")
		q.Set("afterVersion", bad)
		expectRecordsError(t, handler, q, http.StatusBadRequest, "INVALID_CURSOR_VERSION")
	}
}

func TestConfigVersionRecordsStorageUnavailable(t *testing.T) {
	st, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 1})
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	expectRecordsError(t, handler, map[string][]string{
		"namespace":   {"payments"},
		"environment": {"prod"},
	}, http.StatusServiceUnavailable, "storage_unavailable")
}

func TestConfigVersionRecordsIsReadOnly(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 1})
	publish(t, handler, "payments", "prod", "canary", map[string]any{"a": 2})

	query := url.Values{}
	query.Set("namespace", "payments")
	query.Set("environment", "prod")
	first := getRecords(t, handler, query)

	getRecords(t, handler, query)
	second := getRecords(t, handler, query)
	if first.MatchedCount != second.MatchedCount || len(second.Versions) != 2 {
		t.Fatalf("reads changed history: %d vs %d", first.MatchedCount, second.MatchedCount)
	}
	// Effective state stays on v1: the gray v2 is never reported effective.
	if second.Versions[0].Effective != true || second.Versions[1].Effective != false {
		t.Fatalf("effective flags changed: %+v", second.Versions)
	}
}
