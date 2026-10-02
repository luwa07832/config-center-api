package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/config-center-api/internal/store"
)

// handleHistoricalEffective returns the full-release snapshot that was effective for a namespace
// and environment at a given point in time. It is strictly read-only: no version is created and
// no effective, gray tag, rollback, promotion or history state changes.
func (s *server) handleHistoricalEffective(c *gin.Context) {
	namespace, environment := c.Query("namespace"), c.Query("environment")
	if namespace == "" || environment == "" {
		writeError(c, http.StatusBadRequest, "MISSING_SCOPE", "namespace and environment are required")
		return
	}
	rawAsOf, exists := c.GetQuery("asOf")
	if !exists {
		writeError(c, http.StatusBadRequest, "MISSING_AS_OF", "asOf is required")
		return
	}
	asOf, err := time.Parse(time.RFC3339, rawAsOf)
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_TIMESTAMP", "asOf must be an RFC3339 timestamp")
		return
	}
	asOf = asOf.UTC()
	normalizedAsOf := asOf.Format(time.RFC3339Nano)

	version, err := s.store.HistoricalEffectiveVersion(c.Request.Context(), namespace, environment, asOf)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"namespace":        namespace,
			"environment":      environment,
			"asOf":             normalizedAsOf,
			"effectiveVersion": nil,
			"version":          nil,
			"items":            gin.H{},
		})
		return
	}
	items, err := s.store.Items(c.Request.Context(), namespace, environment, version.Version)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
		return
	}
	effectiveVersion, err := s.store.EffectiveVersion(c.Request.Context(), namespace, environment)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"namespace":        namespace,
		"environment":      environment,
		"asOf":             normalizedAsOf,
		"effectiveVersion": intPtrOrNil(version.Version),
		"version":          toVersionInfo(version, effectiveVersion),
		"items":            rawMap(items),
	})
}
