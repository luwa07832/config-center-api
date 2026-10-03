package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/config-center-api/internal/store"
)

// VersionRecord is one historical version plus its complete stored snapshot.
type VersionRecord struct {
	Version     int64                      `json:"version"`
	GrayTag     *string                    `json:"grayTag"`
	RollbackOf  *int64                     `json:"rollbackOf"`
	PromotionOf *int64                     `json:"promotionOf"`
	CreatedAt   string                     `json:"createdAt"`
	Effective   bool                       `json:"effective"`
	Items       map[string]json.RawMessage `json:"items"`
}

// VersionRecordsResponse is the read-only filtered and paged version record listing.
type VersionRecordsResponse struct {
	Namespace        string          `json:"namespace"`
	Environment      string          `json:"environment"`
	MatchedCount     int64           `json:"matchedCount"`
	Versions         []VersionRecord `json:"versions"`
	HasMore          bool            `json:"hasMore"`
	NextAfterVersion int64           `json:"nextAfterVersion"`
}

// handleConfigVersionRecords lists filtered historical versions of one scope, each carrying its
// complete snapshot. It is strictly read-only: no version is created and effective, gray,
// promotion, rollback and history state never change.
func (s *server) handleConfigVersionRecords(c *gin.Context) {
	namespace, environment := c.Query("namespace"), c.Query("environment")
	if namespace == "" || environment == "" {
		writeError(c, http.StatusBadRequest, "MISSING_SCOPE", "namespace and environment are required")
		return
	}

	filter := store.VersionRecordFilter{Namespace: namespace, Environment: environment}
	if rawGrayTag, hasGrayTag := c.GetQuery("grayTag"); hasGrayTag {
		if rawGrayTag == "" {
			writeError(c, http.StatusBadRequest, "INVALID_GRAY_TAG", "grayTag must be a non-empty value")
			return
		}
		filter.GrayTag = rawGrayTag
		filter.HasGrayTag = true
	}
	if rawRollbackOf, hasRollbackOf := c.GetQuery("rollbackOf"); hasRollbackOf {
		if !positiveIntegerPattern.MatchString(rawRollbackOf) {
			writeError(c, http.StatusBadRequest, "INVALID_ROLLBACK_SOURCE", "rollbackOf must be a positive integer")
			return
		}
		rollbackSource, parseErr := strconv.ParseInt(rawRollbackOf, 10, 64)
		if parseErr != nil {
			writeError(c, http.StatusBadRequest, "INVALID_ROLLBACK_SOURCE", "rollbackOf must be a positive integer")
			return
		}
		filter.RollbackOf = rollbackSource
		filter.HasRollbackOf = true
	}
	if rawEffective, hasEffective := c.GetQuery("effective"); hasEffective {
		switch rawEffective {
		case "true":
			filter.Effective = true
			filter.HasEffective = true
		case "false":
			filter.Effective = false
			filter.HasEffective = true
		default:
			writeError(c, http.StatusBadRequest, "INVALID_EFFECTIVE_FILTER", "effective must be true or false")
			return
		}
	}

	limit := defaultHistoryPageSize
	if rawLimit, hasLimit := c.GetQuery("limit"); hasLimit {
		parsed, ok := parseHistoryPageSize(rawLimit)
		if !ok {
			writeError(c, http.StatusBadRequest, "INVALID_PAGE_SIZE", "limit must be a decimal integer between 1 and 100")
			return
		}
		limit = parsed
	}
	var afterVersion int64
	if rawAfter, hasAfter := c.GetQuery("afterVersion"); hasAfter {
		parsed, ok := parseHistoryCursor(rawAfter)
		if !ok {
			writeError(c, http.StatusBadRequest, "INVALID_CURSOR_VERSION", "afterVersion must be a non-negative decimal integer")
			return
		}
		afterVersion = parsed
	}

	effectiveVersion, err := s.store.EffectiveVersion(c.Request.Context(), namespace, environment)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
		return
	}
	page, err := s.store.ListVersionRecords(c.Request.Context(), filter, afterVersion, limit)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
		return
	}

	records := make([]VersionRecord, 0, len(page.Records))
	for _, record := range page.Records {
		info := toVersionInfo(record.Version, effectiveVersion)
		if record.Items == nil {
			record.Items = map[string]json.RawMessage{}
		}
		records = append(records, VersionRecord{
			Version:     info.Version,
			GrayTag:     info.GrayTag,
			RollbackOf:  info.RollbackOf,
			PromotionOf: info.PromotionOf,
			CreatedAt:   info.CreatedAt,
			Effective:   info.Effective,
			Items:       record.Items,
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
		MatchedCount:     page.Total,
		Versions:         records,
		HasMore:          page.HasMore,
		NextAfterVersion: nextAfterVersion,
	})
}
