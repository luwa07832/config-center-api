package api

import (
	"errors"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/config-center-api/internal/store"
)

// Lineage relations stored on a version. A rollback edge takes precedence on the ancestor walk.
const (
	lineageRelationRollback  = "rollback"
	lineageRelationPromotion = "promotion"
)

// AncestorNode is one version on the path from the queried version back to its earliest source.
type AncestorNode struct {
	VersionInfo
	Depth    int    `json:"depth"`
	Relation string `json:"relation"`
}

// DescendantNode is one version derived from the queried version. It carries the same metadata
// as an ancestor node plus ParentVersion, the version through which it was first reached during
// the stored-field reverse walk.
type DescendantNode struct {
	VersionInfo
	Depth         int    `json:"depth"`
	Relation      string `json:"relation"`
	ParentVersion int64  `json:"parentVersion"`
}

// LineageResponse is the read-only source and derivation graph of one version.
type LineageResponse struct {
	Namespace       string           `json:"namespace"`
	Environment     string           `json:"environment"`
	Version         VersionInfo      `json:"version"`
	Ancestors       []AncestorNode   `json:"ancestors"`
	Descendants     []DescendantNode `json:"descendants"`
	AncestorCount   int              `json:"ancestorCount"`
	DescendantCount int              `json:"descendantCount"`
}

// BuildLineage derives the lineage graph solely from the stored rollbackOf and promotionOf
// fields of the versions of one scope. It never inspects item contents, timestamps or the
// effective flag to infer relations. The effective flag on every returned node is evaluated
// against effectiveVersion at query time.
func BuildLineage(namespace, environment string, root store.Version, versions []store.Version, effectiveVersion int64) LineageResponse {
	byNumber := make(map[int64]store.Version, len(versions))
	children := make(map[int64][]int64)
	for _, version := range versions {
		byNumber[version.Version] = version
		if parent := lineageParent(version); parent != 0 {
			children[parent] = append(children[parent], version.Version)
		}
	}
	for parent := range children {
		sort.Slice(children[parent], func(i, j int) bool {
			return children[parent][i] < children[parent][j]
		})
	}

	ancestors := make([]AncestorNode, 0)
	visited := map[int64]struct{}{root.Version: {}}
	current, depth := root, 1
	for {
		parentNumber, relation := lineageParentWithRelation(current)
		if parentNumber == 0 {
			break
		}
		if _, seen := visited[parentNumber]; seen {
			break
		}
		parent, ok := byNumber[parentNumber]
		if !ok {
			break
		}
		ancestors = append(ancestors, AncestorNode{
			VersionInfo: toVersionInfo(parent, effectiveVersion),
			Depth:       depth,
			Relation:    relation,
		})
		visited[parentNumber] = struct{}{}
		current = parent
		depth++
	}

	descendants := make([]DescendantNode, 0)
	frontier := []int64{root.Version}
	for level := 1; len(frontier) > 0; level++ {
		levelNumbers := make([]int64, 0)
		for _, parentNumber := range frontier {
			for _, childNumber := range children[parentNumber] {
				if _, seen := visited[childNumber]; seen {
					continue
				}
				visited[childNumber] = struct{}{}
				child := byNumber[childNumber]
				_, relation := lineageParentWithRelation(child)
				descendants = append(descendants, DescendantNode{
					VersionInfo:   toVersionInfo(child, effectiveVersion),
					Depth:         level,
					Relation:      relation,
					ParentVersion: parentNumber,
				})
				levelNumbers = append(levelNumbers, childNumber)
			}
		}
		sort.Slice(levelNumbers, func(i, j int) bool { return levelNumbers[i] < levelNumbers[j] })
		frontier = levelNumbers
	}
	sort.SliceStable(descendants, func(i, j int) bool {
		if descendants[i].Depth != descendants[j].Depth {
			return descendants[i].Depth < descendants[j].Depth
		}
		return descendants[i].Version < descendants[j].Version
	})

	return LineageResponse{
		Namespace:       namespace,
		Environment:     environment,
		Version:         toVersionInfo(root, effectiveVersion),
		Ancestors:       ancestors,
		Descendants:     descendants,
		AncestorCount:   len(ancestors),
		DescendantCount: len(descendants),
	}
}

// lineageParent returns the stored source of a version; rollbackOf takes precedence over
// promotionOf when both are present.
func lineageParent(v store.Version) int64 {
	parent, _ := lineageParentWithRelation(v)
	return parent
}

func lineageParentWithRelation(v store.Version) (int64, string) {
	if source := v.RollbackSource(); source != 0 {
		return source, lineageRelationRollback
	}
	if source := v.PromotionSource(); source != 0 {
		return source, lineageRelationPromotion
	}
	return 0, ""
}

// handleVersionLineage answers the read-only ancestry and derivation query for one version. It
// creates no version and changes no stored state.
func (s *server) handleVersionLineage(c *gin.Context) {
	namespace, environment := c.Param("namespace"), c.Param("environment")
	version, ok := parseVersionParam(c, c.Param("version"))
	if !ok {
		return
	}
	root, err := s.store.GetVersion(c.Request.Context(), namespace, environment, version)
	if err == nil {
		versions, listErr := s.store.ListVersions(c.Request.Context(), namespace, environment)
		if listErr != nil {
			writeStorageUnavailable(c)
			return
		}
		effectiveVersion, effectiveErr := s.store.EffectiveVersion(c.Request.Context(), namespace, environment)
		if effectiveErr != nil {
			writeStorageUnavailable(c)
			return
		}
		c.JSON(http.StatusOK, BuildLineage(namespace, environment, root, versions, effectiveVersion))
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		writeStorageUnavailable(c)
		return
	}
	exists, err := s.store.VersionExistsAnywhere(c.Request.Context(), version)
	if err != nil {
		writeStorageUnavailable(c)
		return
	}
	if !exists {
		writeError(c, http.StatusNotFound, "VERSION_NOT_FOUND", "version does not exist")
		return
	}
	writeError(c, http.StatusConflict, "VERSION_SCOPE_MISMATCH", "version belongs to another namespace or environment")
}

func writeStorageUnavailable(c *gin.Context) {
	writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
}
