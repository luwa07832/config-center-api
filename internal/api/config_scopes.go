package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/config-center-api/internal/store"
)

const (
	defaultConfigScopePageSize = 100
	maxConfigScopePageSize     = 500
)

// ConfigScope summarizes the version history of one namespace and environment.
type ConfigScope struct {
	Namespace             string      `json:"namespace"`
	Environment           string      `json:"environment"`
	VersionCount          int64       `json:"versionCount"`
	LatestVersion         VersionInfo `json:"latestVersion"`
	EffectiveVersion      *int64      `json:"effectiveVersion"`
	EffectiveItemCount    int64       `json:"effectiveItemCount"`
	GrayVersionCount      int64       `json:"grayVersionCount"`
	RollbackVersionCount  int64       `json:"rollbackVersionCount"`
	PromotionVersionCount int64       `json:"promotionVersionCount"`
}

// ConfigScopesResponse is the read-only paged scope listing. The cursor fields are null whenever
// the current page is empty.
type ConfigScopesResponse struct {
	MatchedCount         int           `json:"matchedCount"`
	Scopes               []ConfigScope `json:"scopes"`
	HasMore              bool          `json:"hasMore"`
	NextAfterNamespace   *string       `json:"nextAfterNamespace"`
	NextAfterEnvironment *string       `json:"nextAfterEnvironment"`
}

// handleConfigScopes lists every scope that owns version history. It is strictly read-only: no
// version is created and publish, gray, promotion, rollback and effective state never change.
func (s *server) handleConfigScopes(c *gin.Context) {
	filter := store.ScopeFilter{
		Namespace:   c.Query("namespace"),
		Environment: c.Query("environment"),
	}
	limit := defaultConfigScopePageSize
	if rawLimit, hasLimit := c.GetQuery("limit"); hasLimit {
		parsed, ok := parseConfigScopePageSize(rawLimit)
		if !ok {
			writeError(c, http.StatusBadRequest, "INVALID_PAGE_SIZE", "limit must be a decimal integer between 1 and 500")
			return
		}
		limit = parsed
	}
	rawAfterNamespace, hasAfterNamespace := c.GetQuery("afterNamespace")
	rawAfterEnvironment, hasAfterEnvironment := c.GetQuery("afterEnvironment")
	if hasAfterNamespace != hasAfterEnvironment ||
		(hasAfterNamespace && (rawAfterNamespace == "" || rawAfterEnvironment == "")) {
		writeError(c, http.StatusBadRequest, "INVALID_CURSOR", "afterNamespace and afterEnvironment must be supplied together as non-empty values")
		return
	}
	if rawHasEffective, hasEffective := c.GetQuery("hasEffective"); hasEffective {
		switch rawHasEffective {
		case "true":
			filter.HasEffective = true
			filter.HasEffectiveFilter = true
		case "false":
			filter.HasEffective = false
			filter.HasEffectiveFilter = true
		default:
			writeError(c, http.StatusBadRequest, "INVALID_EFFECTIVE_FILTER", "hasEffective must be true or false")
			return
		}
	}

	page, err := s.store.ListScopes(c.Request.Context(), filter, rawAfterNamespace, rawAfterEnvironment, limit)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
		return
	}

	scopes := make([]ConfigScope, 0, len(page.Scopes))
	for _, scope := range page.Scopes {
		scopes = append(scopes, ConfigScope{
			Namespace:             scope.Namespace,
			Environment:           scope.Environment,
			VersionCount:          scope.VersionCount,
			LatestVersion:         toVersionInfo(scope.Latest, scope.EffectiveVersion),
			EffectiveVersion:      intPtrOrNil(scope.EffectiveVersion),
			EffectiveItemCount:    scope.EffectiveItemCount,
			GrayVersionCount:      scope.GrayVersionCount,
			RollbackVersionCount:  scope.RollbackVersionCount,
			PromotionVersionCount: scope.PromotionVersionCount,
		})
	}
	response := ConfigScopesResponse{
		MatchedCount: int(page.Total),
		Scopes:       scopes,
		HasMore:      page.HasMore,
	}
	if len(scopes) > 0 {
		last := scopes[len(scopes)-1]
		response.NextAfterNamespace = &last.Namespace
		response.NextAfterEnvironment = &last.Environment
	}
	c.JSON(http.StatusOK, response)
}

func parseConfigScopePageSize(raw string) (int, bool) {
	if !positiveIntegerPattern.MatchString(raw) {
		return 0, false
	}
	size, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || size > maxConfigScopePageSize {
		return 0, false
	}
	return int(size), true
}
