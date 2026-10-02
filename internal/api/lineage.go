package api

import (
	"errors"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/config-center-api/internal/store"
)

// Lineage relation kinds derived from the stored source fields.
const (
	relationRollback  = "rollback"
	relationPromotion = "promotion"
)

// LineageAncestor is one source version on the rollback/promotion chain of the queried version.
type LineageAncestor struct {
	Version  VersionInfo `json:"version"`
	Depth    int         `json:"depth"`
	Relation string      `json:"relation"`
}

// LineageDescendant is one version derived from the queried version, directly or transitively.
type LineageDescendant struct {
	Version       VersionInfo `json:"version"`
	Depth         int         `json:"depth"`
	Relation      string      `json:"relation"`
	ParentVersion int64       `json:"parentVersion"`
}

// LineageResponse is the read-only lineage view of one stored version.
type LineageResponse struct {
	Namespace       string              `json:"namespace"`
	Environment     string              `json:"environment"`
	Version         VersionInfo         `json:"version"`
	Ancestors       []LineageAncestor   `json:"ancestors"`
	AncestorCount   int                 `json:"ancestorCount"`
	Descendants     []LineageDescendant `json:"descendants"`
	DescendantCount int                 `json:"descendantCount"`
}

// lineageSource returns the stored source of a version: rollback_of wins over promotion_of,
// mirroring the ancestor walk rule. ok is false for versions without any source.
func lineageSource(v store.Version) (source int64, relation string, ok bool) {
	if v.RollbackOf.Valid {
		return v.RollbackOf.Int64, relationRollback, true
	}
	if v.PromotionOf.Valid {
		return v.PromotionOf.Int64, relationPromotion, true
	}
	return 0, "", false
}

// BuildLineage assembles the ancestor chain and the derived versions of one version from the
// stored rollback_of and promotion_of fields of its scope. versions must contain every version
// of the scope ordered by version ascending. Nothing is inferred from content, timestamps or
// the effective flag.
func BuildLineage(current store.Version, versions []store.Version, effectiveVersion int64) LineageResponse {
	byVersion := make(map[int64]store.Version, len(versions))
	childrenOf := make(map[int64][]store.Version, len(versions))
	for _, candidate := range versions {
		byVersion[candidate.Version] = candidate
		if source, _, ok := lineageSource(candidate); ok {
			childrenOf[source] = append(childrenOf[source], candidate)
		}
	}

	ancestors := []LineageAncestor{}
	seen := map[int64]bool{current.Version: true}
	cursor := current
	for {
		source, relation, ok := lineageSource(cursor)
		if !ok {
			break
		}
		parent, found := byVersion[source]
		if !found || seen[source] {
			break
		}
		seen[source] = true
		ancestors = append(ancestors, LineageAncestor{
			Version:  toVersionInfo(parent, effectiveVersion),
			Depth:    len(ancestors) + 1,
			Relation: relation,
		})
		cursor = parent
	}

	descendants := []LineageDescendant{}
	visited := map[int64]bool{current.Version: true}
	frontier := []store.Version{current}
	for depth := 1; len(frontier) > 0; depth++ {
		var next []store.Version
		for _, parent := range frontier {
			for _, child := range childrenOf[parent.Version] {
				if visited[child.Version] {
					continue
				}
				visited[child.Version] = true
				_, relation, _ := lineageSource(child)
				descendants = append(descendants, LineageDescendant{
					Version:       toVersionInfo(child, effectiveVersion),
					Depth:         depth,
					Relation:      relation,
					ParentVersion: parent.Version,
				})
				next = append(next, child)
			}
		}
		frontier = next
	}
	sort.Slice(descendants, func(i, j int) bool {
		if descendants[i].Depth != descendants[j].Depth {
			return descendants[i].Depth < descendants[j].Depth
		}
		return descendants[i].Version.Version < descendants[j].Version.Version
	})

	return LineageResponse{
		Namespace:       current.Namespace,
		Environment:     current.Environment,
		Version:         toVersionInfo(current, effectiveVersion),
		Ancestors:       ancestors,
		AncestorCount:   len(ancestors),
		Descendants:     descendants,
		DescendantCount: len(descendants),
	}
}

// handleLineage returns the source chain and derived versions of one stored version. It is
// strictly read-only: no version is created and no stored record changes.
func (s *server) handleLineage(c *gin.Context) {
	namespace, environment := c.Param("namespace"), c.Param("environment")
	version, ok := parseVersionParam(c, c.Param("version"))
	if !ok {
		return
	}
	current, err := s.store.GetVersion(c.Request.Context(), namespace, environment, version)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
			return
		}
		exists, existsErr := s.store.VersionExistsAnywhere(c.Request.Context(), version)
		if existsErr != nil {
			writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
			return
		}
		if !exists {
			writeError(c, http.StatusNotFound, "VERSION_NOT_FOUND", "version does not exist")
			return
		}
		writeError(c, http.StatusConflict, "VERSION_SCOPE_MISMATCH", "version belongs to another namespace or environment")
		return
	}
	versions, err := s.store.ListVersions(c.Request.Context(), namespace, environment)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
		return
	}
	effectiveVersion, err := s.store.EffectiveVersion(c.Request.Context(), namespace, environment)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
		return
	}
	c.JSON(http.StatusOK, BuildLineage(current, versions, effectiveVersion))
}
