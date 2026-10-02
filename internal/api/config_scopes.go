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

// ConfigScopeInfo summarizes version history of one scope. LatestVersion reuses the shared
// version metadata shape; EffectiveVersion is null when the scope has never had a full release.
type ConfigScopeInfo struct {
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

// ConfigScopesResponse is the read-only paged scope listing. Next cursor fields are null unless
// another page exists.
type ConfigScopesResponse struct {
	MatchedCount         int               `json:"matchedCount"`
	Scopes               []ConfigScopeInfo `json:"scopes"`
	HasMore              bool              `json:"hasMore"`
	NextAfterNamespace   *string           `json:"nextAfterNamespace"`
	NextAfterEnvironment *string           `json:"nextAfterEnvironment"`
}

// BuildConfigScopes converts one stored page into the public response shape.
func BuildConfigScopes(page store.ConfigScopePage) ConfigScopesResponse {
	scopes := make([]ConfigScopeInfo, 0, len(page.Scopes))
	for _, scope := range page.Scopes {
		scopes = append(scopes, ConfigScopeInfo{
			Namespace:             scope.Namespace,
			Environment:           scope.Environment,
			VersionCount:          scope.VersionCount,
			LatestVersion:         toVersionInfo(scope.LatestVersion, scope.EffectiveVersion),
			EffectiveVersion:      intPtrOrNil(scope.EffectiveVersion),
			EffectiveItemCount:    scope.EffectiveItemCount,
			GrayVersionCount:      scope.GrayVersionCount,
			RollbackVersionCount:  scope.RollbackVersionCount,
			PromotionVersionCount: scope.PromotionVersionCount,
		})
	}
	response := ConfigScopesResponse{
		MatchedCount: int(page.MatchedCount),
		Scopes:       scopes,
		HasMore:      page.HasMore,
	}
	if page.HasMore && len(page.Scopes) > 0 {
		last := page.Scopes[len(page.Scopes)-1]
		response.NextAfterNamespace = &last.Namespace
		response.NextAfterEnvironment = &last.Environment
	}
	return response
}

// handleConfigScopes lists every scope that owns version history. It is strictly read-only: no
// version is created and no effective state changes.
func (s *server) handleConfigScopes(c *gin.Context) {
	limit := defaultConfigScopePageSize
	if rawLimit, hasLimit := c.GetQuery("limit"); hasLimit {
		parsed, ok := parseConfigScopePageSize(rawLimit)
		if !ok {
			writeError(c, http.StatusBadRequest, "INVALID_PAGE_SIZE",
				"limit must be a decimal integer between 1 and 500")
			return
		}
		limit = parsed
	}

	var afterNamespace, afterEnvironment string
	rawAfterNamespace, hasAfterNamespace := c.GetQuery("afterNamespace")
	rawAfterEnvironment, hasAfterEnvironment := c.GetQuery("afterEnvironment")
	if hasAfterNamespace || hasAfterEnvironment {
		if !hasAfterNamespace || !hasAfterEnvironment || rawAfterNamespace == "" || rawAfterEnvironment == "" {
			writeError(c, http.StatusBadRequest, "INVALID_CURSOR",
				"afterNamespace and afterEnvironment must be provided together as non-empty values")
			return
		}
	}
	afterNamespace, afterEnvironment = rawAfterNamespace, rawAfterEnvironment

	var hasEffective *bool
	if rawHasEffective, exists := c.GetQuery("hasEffective"); exists {
		switch rawHasEffective {
		case "true":
			value := true
			hasEffective = &value
		case "false":
			value := false
			hasEffective = &value
		default:
			writeError(c, http.StatusBadRequest, "INVALID_EFFECTIVE_FILTER",
				"hasEffective must be true or false")
			return
		}
	}

	page, err := s.store.ListConfigScopes(c.Request.Context(), store.ConfigScopeFilter{
		Namespace:        c.Query("namespace"),
		Environment:      c.Query("environment"),
		HasEffective:     hasEffective,
		AfterNamespace:   afterNamespace,
		AfterEnvironment: afterEnvironment,
		Limit:            limit,
	})
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
		return
	}
	c.JSON(http.StatusOK, BuildConfigScopes(page))
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
