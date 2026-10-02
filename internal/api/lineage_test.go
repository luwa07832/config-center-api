package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func lineagePath(ns, env, version string) string {
	return "/namespaces/" + ns + "/environments/" + env + "/config-versions/" + version + "/lineage"
}

type rawLineageNode struct {
	Version       VersionInfo `json:"version"`
	Depth         int         `json:"depth"`
	Relation      string      `json:"relation"`
	ParentVersion *int64      `json:"parentVersion"`
}

type rawLineage struct {
	Namespace       string           `json:"namespace"`
	Environment     string           `json:"environment"`
	Version         VersionInfo      `json:"version"`
	Ancestors       []rawLineageNode `json:"ancestors"`
	AncestorCount   int              `json:"ancestorCount"`
	Descendants     []rawLineageNode `json:"descendants"`
	DescendantCount int              `json:"descendantCount"`
}

func promoteVersion(t *testing.T, handler http.Handler, ns, env, version string) {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodPost,
		"/namespaces/"+ns+"/environments/"+env+"/config-versions/"+version+"/promote", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("promote status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func rollbackVersion(t *testing.T, handler http.Handler, ns, env, version string) {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodPost,
		"/namespaces/"+ns+"/environments/"+env+"/config-versions/"+version+"/rollback", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("rollback status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func getLineage(t *testing.T, handler http.Handler, ns, env, version string) rawLineage {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodGet, lineagePath(ns, env, version), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var body rawLineage
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestLineageTracesMixedRollbackAndPromotionChain(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 1})
	publish(t, handler, "payments", "prod", "canary", map[string]any{"a": 2})
	promoteVersion(t, handler, "payments", "prod", "2")
	rollbackVersion(t, handler, "payments", "prod", "1")
	rollbackVersion(t, handler, "payments", "prod", "3")

	body := getLineage(t, handler, "payments", "prod", "5")
	if body.Namespace != "payments" || body.Environment != "prod" {
		t.Fatalf("scope = %s/%s", body.Namespace, body.Environment)
	}
	if body.Version.Version != 5 || !body.Version.Effective {
		t.Fatalf("version = %+v, want effective v5", body.Version)
	}
	if body.Version.RollbackOf == nil || *body.Version.RollbackOf != 3 {
		t.Fatalf("rollbackOf = %v, want 3", body.Version.RollbackOf)
	}
	if body.AncestorCount != 2 || len(body.Ancestors) != 2 {
		t.Fatalf("ancestors = %+v count = %d", body.Ancestors, body.AncestorCount)
	}
	near := body.Ancestors[0]
	if near.Version.Version != 3 || near.Depth != 1 || near.Relation != "rollback" {
		t.Fatalf("ancestors[0] = %+v", near)
	}
	if near.Version.PromotionOf == nil || *near.Version.PromotionOf != 2 {
		t.Fatalf("ancestors[0].promotionOf = %v, want 2", near.Version.PromotionOf)
	}
	if near.Version.Effective {
		t.Fatalf("v3 must not be effective after v5")
	}
	far := body.Ancestors[1]
	if far.Version.Version != 2 || far.Depth != 2 || far.Relation != "promotion" {
		t.Fatalf("ancestors[1] = %+v", far)
	}
	if far.Version.GrayTag == nil || *far.Version.GrayTag != "canary" {
		t.Fatalf("ancestors[1].grayTag = %v, want canary", far.Version.GrayTag)
	}
	if body.DescendantCount != 0 || len(body.Descendants) != 0 {
		t.Fatalf("descendants = %+v count = %d", body.Descendants, body.DescendantCount)
	}

	gray := getLineage(t, handler, "payments", "prod", "2")
	if gray.AncestorCount != 0 || len(gray.Ancestors) != 0 {
		t.Fatalf("gray ancestors = %+v", gray.Ancestors)
	}
	if gray.DescendantCount != 2 || len(gray.Descendants) != 2 {
		t.Fatalf("gray descendants = %+v count = %d", gray.Descendants, gray.DescendantCount)
	}
	direct := gray.Descendants[0]
	if direct.Version.Version != 3 || direct.Depth != 1 || direct.Relation != "promotion" {
		t.Fatalf("descendants[0] = %+v", direct)
	}
	if direct.ParentVersion == nil || *direct.ParentVersion != 2 {
		t.Fatalf("descendants[0].parentVersion = %v, want 2", direct.ParentVersion)
	}
	indirect := gray.Descendants[1]
	if indirect.Version.Version != 5 || indirect.Depth != 2 || indirect.Relation != "rollback" {
		t.Fatalf("descendants[1] = %+v", indirect)
	}
	if indirect.ParentVersion == nil || *indirect.ParentVersion != 3 {
		t.Fatalf("descendants[1].parentVersion = %v, want 3", indirect.ParentVersion)
	}
	if !indirect.Version.Effective {
		t.Fatalf("v5 must be effective at query time")
	}

	root := getLineage(t, handler, "payments", "prod", "1")
	if root.AncestorCount != 0 || root.DescendantCount != 1 {
		t.Fatalf("root lineage = ancestors %+v descendants %+v", root.Ancestors, root.Descendants)
	}
	child := root.Descendants[0]
	if child.Version.Version != 4 || child.Depth != 1 || child.Relation != "rollback" {
		t.Fatalf("root descendants[0] = %+v", child)
	}
	if child.ParentVersion == nil || *child.ParentVersion != 1 {
		t.Fatalf("root descendants[0].parentVersion = %v, want 1", child.ParentVersion)
	}
}
func TestLineageDescendantsOrderedByDepthThenVersion(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	rollbackVersion(t, handler, "svc", "dev", "1")
	rollbackVersion(t, handler, "svc", "dev", "1")
	rollbackVersion(t, handler, "svc", "dev", "2")

	body := getLineage(t, handler, "svc", "dev", "1")
	if body.DescendantCount != 3 || len(body.Descendants) != 3 {
		t.Fatalf("descendants = %+v count = %d", body.Descendants, body.DescendantCount)
	}
	wantVersions := []int64{2, 3, 4}
	wantDepths := []int{1, 1, 2}
	wantParents := []int64{1, 1, 2}
	for i, node := range body.Descendants {
		if node.Version.Version != wantVersions[i] || node.Depth != wantDepths[i] {
			t.Fatalf("descendants[%d] = %+v, want version %d depth %d", i, node, wantVersions[i], wantDepths[i])
		}
		if node.Relation != "rollback" {
			t.Fatalf("descendants[%d].relation = %s", i, node.Relation)
		}
		if node.ParentVersion == nil || *node.ParentVersion != wantParents[i] {
			t.Fatalf("descendants[%d].parentVersion = %v, want %d", i, node.ParentVersion, wantParents[i])
		}
	}
}

func TestLineageWithoutRelationsReturnsEmptyArrays(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})

	recorder := doRequest(t, handler, http.MethodGet, lineagePath("svc", "dev", "1"), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"ancestors":[]`) ||
		!strings.Contains(recorder.Body.String(), `"descendants":[]`) {
		t.Fatalf("relations must be empty arrays, body = %s", recorder.Body.String())
	}
	var body rawLineage
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.AncestorCount != 0 || body.DescendantCount != 0 {
		t.Fatalf("counts = %d/%d, want 0/0", body.AncestorCount, body.DescendantCount)
	}
	if body.Ancestors == nil || body.Descendants == nil {
		t.Fatalf("relation arrays must not be null: %+v", body)
	}
	if !body.Version.Effective || body.Version.GrayTag != nil ||
		body.Version.RollbackOf != nil || body.Version.PromotionOf != nil {
		t.Fatalf("version = %+v", body.Version)
	}
}

func TestLineageRejectsInvalidVersion(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})

	for _, raw := range []string{"abc", "0", "-1", "1.5", "01"} {
		recorder := doRequest(t, handler, http.MethodGet, lineagePath("svc", "dev", raw), nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("version %q: status = %d body = %s", raw, recorder.Code, recorder.Body.String())
		}
		if code := errorCode(decode(t, recorder)); code != "INVALID_VERSION" {
			t.Fatalf("version %q: code = %s, want INVALID_VERSION", raw, code)
		}
	}
}

func TestLineageVersionNotFound(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})

	recorder := doRequest(t, handler, http.MethodGet, lineagePath("svc", "dev", "7"), nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if code := errorCode(decode(t, recorder)); code != "VERSION_NOT_FOUND" {
		t.Fatalf("code = %s, want VERSION_NOT_FOUND", code)
	}
}

func TestLineageVersionScopeMismatch(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "payments", "prod", "", map[string]any{"a": 1})

	recorder := doRequest(t, handler, http.MethodGet, lineagePath("payments", "staging", "1"), nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if code := errorCode(decode(t, recorder)); code != "VERSION_SCOPE_MISMATCH" {
		t.Fatalf("code = %s, want VERSION_SCOPE_MISMATCH", code)
	}
}

func TestLineageStorageUnavailable(t *testing.T) {
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
	message, _ := body["error"].(map[string]any)["message"].(string)
	if strings.Contains(message, "sql") || strings.Contains(message, "/") || strings.Contains(message, ".db") {
		t.Fatalf("message leaks internals: %q", message)
	}
}

func TestLineageIsReadOnly(t *testing.T) {
	_, handler := newTestRouter(t)
	publish(t, handler, "svc", "dev", "", map[string]any{"a": 1})
	publish(t, handler, "svc", "dev", "canary", map[string]any{"a": 2})
	promoteVersion(t, handler, "svc", "dev", "2")
	rollbackVersion(t, handler, "svc", "dev", "1")

	historyPath := "/config-versions?namespace=svc&environment=dev"
	before := doRequest(t, handler, http.MethodGet, historyPath, nil)
	for _, version := range []string{"1", "2", "3", "4"} {
		getLineage(t, handler, "svc", "dev", version)
	}
	after := doRequest(t, handler, http.MethodGet, historyPath, nil)
	if before.Body.String() != after.Body.String() {
		t.Fatalf("history changed after lineage reads:\nbefore %s\nafter %s", before.Body.String(), after.Body.String())
	}
}
