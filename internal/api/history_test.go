package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

type rawHistoryPage struct {
	Namespace        string        `json:"namespace"`
	Environment      string        `json:"environment"`
	Versions         []VersionInfo `json:"versions"`
	TotalVersions    int           `json:"totalVersions"`
	NextAfterVersion int64         `json:"nextAfterVersion"`
	HasMore          bool          `json:"hasMore"`
}

func historyPath(ns, env, query string) string {
	target := "/config-versions?namespace=" + ns + "&environment=" + env
	if query != "" {
		target += "&" + query
	}
	return target
}

func decodeHistoryPage(t *testing.T, recorder *httptest.ResponseRecorder) rawHistoryPage {
	t.Helper()
	var page rawHistoryPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return page
}

func versionNumbers(infos []VersionInfo) []int64 {
	numbers := make([]int64, len(infos))
	for i, info := range infos {
		numbers[i] = info.Version
	}
	return numbers
}

func publishN(t *testing.T, handler http.Handler, ns, env string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		publish(t, handler, ns, env, "", map[string]any{"index": i})
	}
}

func TestHistoryWithoutPaginationKeepsOriginalShape(t *testing.T) {
	_, handler := newTestRouter(t)
	publishN(t, handler, "svc", "dev", 3)

	recorder := doRequest(t, handler, http.MethodGet, historyPath("svc", "dev", ""), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"totalVersions", "nextAfterVersion", "hasMore"} {
		if _, exists := body[key]; exists {
			t.Fatalf("unpaginated response must not include %s: %s", key, recorder.Body.String())
		}
	}
	if len(body) != 3 {
		t.Fatalf("unpaginated response must keep exactly namespace/environment/versions: %s", recorder.Body.String())
	}
	page := decodeHistoryPage(t, recorder)
	if got := versionNumbers(page.Versions); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("versions = %v, want 1..3", got)
	}
}

func TestHistoryPaginationWalksEveryVersionAscending(t *testing.T) {
	_, handler := newTestRouter(t)
	publishN(t, handler, "svc", "dev", 5)

	var seen []int64
	after := int64(0)
	for {
		page := decodeHistoryPage(t, doRequest(t, handler, http.MethodGet,
			historyPath("svc", "dev", "limit=2&afterVersion="+strconv.FormatInt(after, 10)), nil))
		if page.Namespace != "svc" || page.Environment != "dev" {
			t.Fatalf("scope = %s/%s", page.Namespace, page.Environment)
		}
		if page.TotalVersions != 5 {
			t.Fatalf("totalVersions = %d, want 5", page.TotalVersions)
		}
		numbers := versionNumbers(page.Versions)
		if len(numbers) == 0 {
			t.Fatalf("unexpected empty page after %d", after)
		}
		for i := 1; i < len(numbers); i++ {
			if numbers[i] <= numbers[i-1] {
				t.Fatalf("page not ascending: %v", numbers)
			}
		}
		seen = append(seen, numbers...)
		if page.NextAfterVersion != numbers[len(numbers)-1] {
			t.Fatalf("nextAfterVersion = %d, want %d", page.NextAfterVersion, numbers[len(numbers)-1])
		}
		after = page.NextAfterVersion
		if !page.HasMore {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("walked %v, want 1..5", seen)
	}
	for i, number := range seen {
		if number != int64(i)+1 {
			t.Fatalf("walked %v, want 1..5", seen)
		}
	}

	// One more read past the final version returns the terminal empty page.
	page := decodeHistoryPage(t, doRequest(t, handler, http.MethodGet,
		historyPath("svc", "dev", "limit=2&afterVersion="+strconv.FormatInt(after, 10)), nil))
	if len(page.Versions) != 0 {
		t.Fatalf("versions = %v, want empty", versionNumbers(page.Versions))
	}
	if page.NextAfterVersion != after {
		t.Fatalf("nextAfterVersion = %d, want %d", page.NextAfterVersion, after)
	}
	if page.HasMore {
		t.Fatalf("hasMore = true on terminal page")
	}
	if page.TotalVersions != 5 {
		t.Fatalf("totalVersions = %d, want 5", page.TotalVersions)
	}
}

func TestHistoryPaginationFirstPageOmitsCursor(t *testing.T) {
	_, handler := newTestRouter(t)
	publishN(t, handler, "svc", "dev", 3)

	page := decodeHistoryPage(t, doRequest(t, handler, http.MethodGet,
		historyPath("svc", "dev", "limit=2"), nil))
	if got := versionNumbers(page.Versions); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("versions = %v, want [1 2]", got)
	}
	if page.NextAfterVersion != 2 || !page.HasMore || page.TotalVersions != 3 {
		t.Fatalf("page = %+v", page)
	}
}

func TestHistoryDefaultLimitWhenOnlyCursorGiven(t *testing.T) {
	_, handler := newTestRouter(t)
	publishN(t, handler, "svc", "dev", 3)

	page := decodeHistoryPage(t, doRequest(t, handler, http.MethodGet,
		historyPath("svc", "dev", "afterVersion=1"), nil))
	if got := versionNumbers(page.Versions); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("versions = %v, want [2 3]", got)
	}
	if page.HasMore || page.NextAfterVersion != 3 || page.TotalVersions != 3 {
		t.Fatalf("page = %+v", page)
	}
}

func TestHistoryRepeatedReadsAreStable(t *testing.T) {
	_, handler := newTestRouter(t)
	publishN(t, handler, "svc", "dev", 3)

	first := decodeHistoryPage(t, doRequest(t, handler, http.MethodGet,
		historyPath("svc", "dev", "limit=2&afterVersion=0"), nil))
	// A new version appears after the page was first read; the located page content and cursor
	// must stay identical. totalVersions is the live scope total, so it is allowed to grow.
	publishN(t, handler, "svc", "dev", 2)
	second := decodeHistoryPage(t, doRequest(t, handler, http.MethodGet,
		historyPath("svc", "dev", "limit=2&afterVersion=0"), nil))
	wantNumbers := []int64{1, 2}
	if got := versionNumbers(first.Versions); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("first page = %v, want [1 2]", got)
	}
	if got := versionNumbers(second.Versions); !equalInt64(got, wantNumbers) {
		t.Fatalf("second page = %v, want [1 2]", got)
	}
	if first.NextAfterVersion != 2 || second.NextAfterVersion != 2 {
		t.Fatalf("nextAfterVersion = %d/%d, want 2/2", first.NextAfterVersion, second.NextAfterVersion)
	}
	if !first.HasMore || !second.HasMore {
		t.Fatalf("hasMore = %v/%v, want true/true", first.HasMore, second.HasMore)
	}
	if first.TotalVersions != 3 || second.TotalVersions != 5 {
		t.Fatalf("totalVersions = %d/%d, want 3/5", first.TotalVersions, second.TotalVersions)
	}
}

func equalInt64(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestHistoryPaginationEmptyScopes(t *testing.T) {
	_, handler := newTestRouter(t)

	// Scope with no versions at all: nextAfterVersion is 0.
	page := decodeHistoryPage(t, doRequest(t, handler, http.MethodGet,
		historyPath("svc", "dev", "limit=10"), nil))
	if len(page.Versions) != 0 || page.TotalVersions != 0 || page.NextAfterVersion != 0 || page.HasMore {
		t.Fatalf("empty scope page = %+v", page)
	}

	publishN(t, handler, "svc", "dev", 2)
	// Cursor beyond every existing version: HTTP 200 empty page echoing the cursor.
	recorder := doRequest(t, handler, http.MethodGet,
		historyPath("svc", "dev", "limit=10&afterVersion=99"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	page = decodeHistoryPage(t, recorder)
	if len(page.Versions) != 0 || page.TotalVersions != 2 || page.NextAfterVersion != 99 || page.HasMore {
		t.Fatalf("past-end page = %+v", page)
	}
}

func TestHistoryPaginationMarksEffective(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 2})
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 3})

	page := decodeHistoryPage(t, doRequest(t, handler, http.MethodGet,
		historyPath("svc", "dev", "limit=100"), nil))
	if len(page.Versions) != 3 {
		t.Fatalf("versions = %d, want 3", len(page.Versions))
	}
	effective := map[int64]bool{}
	for _, info := range page.Versions {
		effective[info.Version] = info.Effective
	}
	if effective[1] || effective[2] || !effective[3] {
		t.Fatalf("effective flags = %v, want only version 3", effective)
	}
	if page.Versions[1].GrayTag == nil || *page.Versions[1].GrayTag != "canary" {
		t.Fatalf("gray tag not preserved: %+v", page.Versions[1])
	}
	if page.Versions[0].RollbackOf != nil || page.Versions[0].PromotionOf != nil {
		t.Fatalf("ordinary release metadata changed: %+v", page.Versions[0])
	}
}

func TestHistoryPaginationValidation(t *testing.T) {
	_, handler := newTestRouter(t)
	assertError := func(target string, wantStatus int, wantCode string) {
		t.Helper()
		recorder := doRequest(t, handler, http.MethodGet, target, nil)
		if recorder.Code != wantStatus {
			t.Fatalf("%s status = %d body = %s", target, recorder.Code, recorder.Body.String())
		}
		if code := errorCode(decode(t, recorder)); code != wantCode {
			t.Fatalf("%s code = %s, want %s", target, code, wantCode)
		}
	}
	base := historyPath("svc", "dev", "")
	assertError(base+"&limit=0", http.StatusBadRequest, "INVALID_PAGE_SIZE")
	assertError(base+"&limit=101", http.StatusBadRequest, "INVALID_PAGE_SIZE")
	assertError(base+"&limit=-1", http.StatusBadRequest, "INVALID_PAGE_SIZE")
	assertError(base+"&limit=1.5", http.StatusBadRequest, "INVALID_PAGE_SIZE")
	assertError(base+"&limit=01", http.StatusBadRequest, "INVALID_PAGE_SIZE")
	assertError(base+"&limit=", http.StatusBadRequest, "INVALID_PAGE_SIZE")
	assertError(base+"&limit=abc", http.StatusBadRequest, "INVALID_PAGE_SIZE")
	assertError(base+"&afterVersion=-1", http.StatusBadRequest, "INVALID_CURSOR_VERSION")
	assertError(base+"&afterVersion=01", http.StatusBadRequest, "INVALID_CURSOR_VERSION")
	assertError(base+"&afterVersion=1.5", http.StatusBadRequest, "INVALID_CURSOR_VERSION")
	assertError(base+"&afterVersion=abc", http.StatusBadRequest, "INVALID_CURSOR_VERSION")
	assertError(base+"&afterVersion=", http.StatusBadRequest, "INVALID_CURSOR_VERSION")
	// Limit is validated before cursor.
	assertError(base+"&limit=0&afterVersion=x", http.StatusBadRequest, "INVALID_PAGE_SIZE")
	// Scope is validated before paging parameters.
	assertError("/config-versions?namespace=svc&limit=1", http.StatusBadRequest, "MISSING_SCOPE")
}

func TestHistoryPaginationStorageUnavailable(t *testing.T) {
	st, handler := newTestRouter(t)
	publishN(t, handler, "svc", "dev", 2)
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	recorder := doRequest(t, handler, http.MethodGet,
		historyPath("svc", "dev", "limit=10"), nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if code := errorCode(decode(t, recorder)); code != "storage_unavailable" {
		t.Fatalf("code = %s, want storage_unavailable", code)
	}
}
