package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/config-center-api/internal/store"
)

// VersionRecord is one historical version plus the complete snapshot it carried.
type VersionRecord struct {
	Version     int64                      `json:"version"`
	GrayTag     *string                    `json:"grayTag"`
	RollbackOf  *int64                     `json:"rollbackOf"`
	PromotionOf *int64                     `json:"promotionOf"`
	CreatedAt   string                     `json:"createdAt"`
	Effective   bool                       `json:"effective"`
	Items       map[string]json.RawMessage `json:"items"`
}

// VersionRecordsResponse is the read-only filtered and paged version-record listing.
type VersionRecordsResponse struct {
	Namespace        string          `json:"namespace"`
	Environment      string          `json:"environment"`
	MatchedCount     int             `json:"matchedCount"`
	Versions         []VersionRecord `json:"versions"`
	HasMore          bool            `json:"hasMore"`
	NextAfterVersion int64           `json:"nextAfterVersion"`
}

// handleVersionRecords lists historical versions of a scope with optional intersecting filters.
// It is strictly read-only: no version is created and publish, gray, promotion, rollback,
// effective and other history behavior never changes.
func (s *server) handleVersionRecords(c *gin.Context) {
	namespace, environment := c.Query("namespace"), c.Query("environment")
	if namespace == "" || environment == "" {
		writeError(c, http.StatusBadRequest, "MISSING_SCOPE", "namespace and environment are required")
		return
	}

	filter := store.VersionRecordFilter{}
	if grayTag, present := c.GetQuery("grayTag"); present {
		if grayTag == "" {
			writeError(c, http.StatusBadRequest, "INVALID_GRAY_TAG", "grayTag must be a non-empty value")
			return
		}
		filter.GrayTag = grayTag
		filter.HasGrayTag = true
	}
	if rawRollbackOf, present := c.GetQuery("rollbackOf"); present {
		if !positiveIntegerPattern.MatchString(rawRollbackOf) {
			writeError(c, http.StatusBadRequest, "INVALID_ROLLBACK_SOURCE", "rollbackOf must be a positive integer")
			return
		}
		rollbackOf, err := strconv.ParseInt(rawRollbackOf, 10, 64)
		if err != nil {
			writeError(c, http.StatusBadRequest, "INVALID_ROLLBACK_SOURCE", "rollbackOf must be a positive integer")
			return
		}
		filter.RollbackOf = rollbackOf
		filter.HasRollbackOf = true
	}
	if rawEffective, present := c.GetQuery("effective"); present {
		switch rawEffective {
		case "true":
			filter.Effective = true
		case "false":
			filter.Effective = false
		default:
			writeError(c, http.StatusBadRequest, "INVALID_EFFECTIVE_FILTER", "effective must be true or false")
			return
		}
		filter.HasEffective = true
	}

	limit := defaultHistoryPageSize
	if rawLimit, present := c.GetQuery("limit"); present {
		parsed, ok := parseHistoryPageSize(rawLimit)
		if !ok {
			writeError(c, http.StatusBadRequest, "INVALID_PAGE_SIZE", "limit must be a decimal integer between 1 and 100")
			return
		}
		limit = parsed
	}
	var afterVersion int64
	if rawAfter, present := c.GetQuery("afterVersion"); present {
		parsed, ok := parseHistoryCursor(rawAfter)
		if !ok {
			writeError(c, http.StatusBadRequest, "INVALID_CURSOR_VERSION", "afterVersion must be a non-negative decimal integer")
			return
		}
		afterVersion = parsed
	}

	page, err := s.store.ListVersionRecords(c.Request.Context(), namespace, environment, filter, afterVersion, limit)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
		return
	}
	effectiveVersion, err := s.store.EffectiveVersion(c.Request.Context(), namespace, environment)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
		return
	}

	records := make([]VersionRecord, 0, len(page.Versions))
	for _, version := range page.Versions {
		items, itemsErr := s.store.Items(c.Request.Context(), namespace, environment, version.Version)
		if itemsErr != nil {
			writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
			return
		}
		info := toVersionInfo(version, effectiveVersion)
		records = append(records, VersionRecord{
			Version:     info.Version,
			GrayTag:     info.GrayTag,
			RollbackOf:  info.RollbackOf,
			PromotionOf: info.PromotionOf,
			CreatedAt:   info.CreatedAt,
			Effective:   info.Effective,
			Items:       items,
		})
	}

	nextAfterVersion := afterVersion
	switch {
	case page.Total == 0:
		nextAfterVersion = 0
	case len(records) > 0:
		nextAfterVersion = records[len(records)-1].Version
	}
	c.JSON(http.StatusOK, VersionRecordsResponse{
		Namespace:        namespace,
		Environment:      environment,
		MatchedCount:     int(page.Total),
		Versions:         records,
		HasMore:          page.HasMore,
		NextAfterVersion: nextAfterVersion,
	})
}
