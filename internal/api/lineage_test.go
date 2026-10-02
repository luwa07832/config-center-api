package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luwa07832/config-center-api/internal/store"
)

func lineagePath(ns, env, version string) string {
	return "/namespaces/" + ns + "/environments/" + env + "/config-versions/" + version + "/lineage"
}

func rollback(t *testing.T, handler http.Handler, ns, env, version string) {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodPost,
		"/namespaces/"+ns+"/environments/"+env+"/config-versions/"+version+"/rollback", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("rollback status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func promoteVersion(t *testing.T, handler http.Handler, ns, env, version string) {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodPost, promotePath(ns, env, version), nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("promote status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

type rawLineageResponse struct {
	Namespace       string           `json:"namespace"`
	Environment     string           `json:"environment"`
	Version         VersionInfo      `json:"version"`
	Ancestors       []AncestorNode   `json:"ancestors"`
	Descendants     []DescendantNode `json:"descendants"`
	AncestorCount   int              `json:"ancestorCount"`
	DescendantCount int              `json:"descendantCount"`
}

func getLineage(t *testing.T, handler http.Handler, target string) rawLineageResponse {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodGet, target, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var body rawLineageResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return body
}

func TestVersionLineageTracesMixedAncestorsAndDescendants(t *testing.T) {
	_, handler := newTestRouter(t)
	// v1 full, v2 gray, v3 promotion of v2, v4 rollback of v1, v5 rollback of v4, v6 promotion of v2.
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 2})
	promoteVersion(t, handler, "svc", "dev", "2")
	rollback(t, handler, "svc", "dev", "1")
	rollback(t, handler, "svc", "dev", "4")
	promoteVersion(t, handler, "svc", "dev", "2")

	body := getLineage(t, handler, lineagePath("svc", "dev", "4"))
	if body.Namespace != "svc" || body.Environment != "dev" {
		t.Fatalf("scope = %s/%s", body.Namespace, body.Environment)
	}
	if body.Version.Version != 4 || body.Version.RollbackOf == nil || *body.Version.RollbackOf != 1 {
		t.Fatalf("root metadata = %+v", body.Version)
	}
	if len(body.Ancestors) != 1 || body.AncestorCount != 1 {
		t.Fatalf("ancestors = %+v", body.Ancestors)
	}
	if body.Ancestors[0].Version != 1 || body.Ancestors[0].Depth != 1 ||
		body.Ancestors[0].Relation != lineageRelationRollback {
		t.Fatalf("ancestor = %+v", body.Ancestors[0])
	}

	body = getLineage(t, handler, lineagePath("svc", "dev", "2"))
	if len(body.Ancestors) != 0 || body.AncestorCount != 0 {
		t.Fatalf("gray root must have no ancestors: %+v", body.Ancestors)
	}
	if body.DescendantCount != 2 || len(body.Descendants) != 2 {
		t.Fatalf("descendants = %+v", body.Descendants)
	}
	for _, node := range body.Descendants {
		if node.Depth != 1 || node.ParentVersion != 2 {
			t.Fatalf("descendant = %+v, want depth 1 parent 2", node)
		}
		if node.PromotionOf == nil || *node.PromotionOf != 2 || node.RollbackOf != nil {
			t.Fatalf("descendant %d must be a promotion of 2: %+v", node.Version, node.VersionInfo)
		}
		if node.Relation != lineageRelationPromotion {
			t.Fatalf("descendant %d relation = %q, want promotion", node.Version, node.Relation)
		}
	}
	if body.Descendants[0].Version != 3 || body.Descendants[1].Version != 6 {
		t.Fatalf("descendants not sorted by version: %+v", body.Descendants)
	}
	// v6 is the newest full release at query time; v3 is not. The effective flag is computed
	// per node against the current effective version.
	if !body.Descendants[1].Effective || body.Descendants[0].Effective {
		t.Fatalf("effective flags = %t/%t", body.Descendants[0].Effective, body.Descendants[1].Effective)
	}

	body = getLineage(t, handler, lineagePath("svc", "dev", "1"))
	wantDescendants := []struct {
		version int64
		depth   int
		parent  int64
	}{{4, 1, 1}, {5, 2, 4}}
	if len(body.Descendants) != len(wantDescendants) {
		t.Fatalf("descendants = %+v", body.Descendants)
	}
	for i, want := range wantDescendants {
		node := body.Descendants[i]
		if node.Version != want.version || node.Depth != want.depth || node.ParentVersion != want.parent {
			t.Fatalf("descendant[%d] = %+v, want %+v", i, node, want)
		}
		if node.Relation != lineageRelationRollback {
			t.Fatalf("descendant %d relation = %q, want rollback", node.Version, node.Relation)
		}
	}
	if body.Version.Effective {
		t.Fatalf("v1 must not be effective while v6 is the latest full release")
	}
}

func TestVersionLineageEmptyRelationsAreArraysAndCountsZero(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 2})

	recorder := doRequest(t, handler, http.MethodGet, lineagePath("svc", "dev", "1"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	raw := recorder.Body.String()
	for _, key := range []string{`"ancestors":[]`, `"descendants":[]`, `"ancestorCount":0`, `"descendantCount":0`} {
		if !strings.Contains(raw, key) {
			t.Fatalf("response missing %s: %s", key, raw)
		}
	}
}

func TestVersionLineageRollbackTakesPrecedenceInBuilder(t *testing.T) {
	makeVersion := func(number, rollback, promotion int64) store.Version {
		v := store.Version{Namespace: "ns", Environment: "env", Version: number, CreatedAt: "2026-10-01T00:00:00Z"}
		if rollback != 0 {
			v.RollbackOf = sql.NullInt64{Int64: rollback, Valid: true}
		}
		if promotion != 0 {
			v.PromotionOf = sql.NullInt64{Int64: promotion, Valid: true}
		}
		return v
	}
	// v3 records both fields; the ancestor walk must follow rollbackOf (v2), never promotionOf (v1).
	versions := []store.Version{makeVersion(1, 0, 0), makeVersion(2, 0, 0), makeVersion(3, 2, 1)}
	response := BuildLineage("ns", "env", versions[2], versions, 3)
	if len(response.Ancestors) != 1 {
		t.Fatalf("ancestors = %+v", response.Ancestors)
	}
	if response.Ancestors[0].Version != 2 || response.Ancestors[0].Relation != lineageRelationRollback {
		t.Fatalf("ancestor = %+v, want rollback edge to 2", response.Ancestors[0])
	}
	if response.DescendantCount != 0 {
		t.Fatalf("descendants = %+v", response.Descendants)
	}
}

func TestVersionLineageErrorContractAndOrder(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "other", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "other", "dev", "", map[string]any{"a": 2})

	cases := []struct {
		name       string
		target     string
		wantStatus int
		wantCode   string
	}{
		{"zero", lineagePath("svc", "dev", "0"), http.StatusBadRequest, "INVALID_VERSION"},
		{"negative", lineagePath("svc", "dev", "-1"), http.StatusBadRequest, "INVALID_VERSION"},
		{"signed positive", "/namespaces/svc/environments/dev/config-versions/+1/lineage", http.StatusBadRequest, "INVALID_VERSION"},
		{"not decimal", lineagePath("svc", "dev", "1.0"), http.StatusBadRequest, "INVALID_VERSION"},
		{"missing globally", lineagePath("svc", "dev", "99"), http.StatusNotFound, "VERSION_NOT_FOUND"},
		{"scope mismatch", lineagePath("svc", "dev", "2"), http.StatusConflict, "VERSION_SCOPE_MISMATCH"},
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

func TestVersionLineageIsReadOnly(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 2})
	promoteVersion(t, handler, "svc", "dev", "2")

	before := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	effectiveBefore := doRequest(t, handler, http.MethodGet,
		"/effective-configs?namespace=svc&environment=dev", nil)
	doRequest(t, handler, http.MethodGet, lineagePath("svc", "dev", "1"), nil)
	doRequest(t, handler, http.MethodGet, lineagePath("svc", "dev", "3"), nil)
	after := doRequest(t, handler, http.MethodGet, "/config-versions?namespace=svc&environment=dev", nil)
	effectiveAfter := doRequest(t, handler, http.MethodGet,
		"/effective-configs?namespace=svc&environment=dev", nil)
	if before.Body.String() != after.Body.String() {
		t.Fatalf("lineage query changed history:\nbefore %s\nafter  %s", before.Body.String(), after.Body.String())
	}
	if effectiveBefore.Body.String() != effectiveAfter.Body.String() {
		t.Fatalf("lineage query changed effective config:\nbefore %s\nafter  %s",
			effectiveBefore.Body.String(), effectiveAfter.Body.String())
	}
}

func TestVersionLineageStorageUnavailable(t *testing.T) {
	st, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	recorder := doRequest(t, handler, http.MethodGet, lineagePath("svc", "dev", "1"), nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := decode(t, recorder)
	if code := errorCode(body); code != "storage_unavailable" {
		t.Fatalf("code = %s, want storage_unavailable", code)
	}
	message := body["error"].(map[string]any)["message"].(string)
	if strings.Contains(message, "SQL") || strings.Contains(message, "/") || strings.Contains(message, ".go") {
		t.Fatalf("error message leaks internals: %s", message)
	}
}
