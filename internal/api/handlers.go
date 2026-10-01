package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/config-center-api/internal/store"
)

var positiveIntegerPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

type publishRequest struct {
	GrayTag string                     `json:"grayTag"`
	Items   map[string]json.RawMessage `json:"items"`
}

func (s *server) handlePublish(c *gin.Context) {
	namespace, environment := c.Param("namespace"), c.Param("environment")
	var body publishRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "request body must be a JSON object with an items map")
		return
	}
	if body.Items == nil {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "items map is required")
		return
	}
	for name, raw := range body.Items {
		if name == "" {
			writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "item name must not be empty")
			return
		}
		if !json.Valid(raw) {
			writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "each item value must be valid JSON")
			return
		}
	}
	version, err := s.store.Publish(c.Request.Context(), store.PublishInput{
		Namespace:   namespace,
		Environment: environment,
		GrayTag:     body.GrayTag,
		Items:       body.Items,
	})
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	effectiveVersion, err := s.store.EffectiveVersion(c.Request.Context(), namespace, environment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"version": toVersionInfo(version, effectiveVersion),
		"items":   rawMap(body.Items),
	})
}

func (s *server) handleRollback(c *gin.Context) {
	namespace, environment := c.Param("namespace"), c.Param("environment")
	sourceVersion, ok := parseVersionParam(c, c.Param("version"))
	if !ok {
		return
	}
	version, err := s.store.Rollback(c.Request.Context(), namespace, environment, sourceVersion)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, http.StatusNotFound, "VERSION_NOT_FOUND", "the source version does not exist in this namespace and environment")
		return
	}
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	effectiveVersion, err := s.store.EffectiveVersion(c.Request.Context(), namespace, environment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	items, err := s.store.Items(c.Request.Context(), namespace, environment, version.Version)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"version": toVersionInfo(version, effectiveVersion),
		"items":   rawMap(items),
	})
}

func (s *server) handleEffective(c *gin.Context) {
	namespace, environment := c.Query("namespace"), c.Query("environment")
	if namespace == "" || environment == "" {
		writeError(c, http.StatusBadRequest, "MISSING_SCOPE", "namespace and environment are required")
		return
	}
	effectiveVersion, err := s.store.EffectiveVersion(c.Request.Context(), namespace, environment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	response := gin.H{
		"namespace":        namespace,
		"environment":      environment,
		"effectiveVersion": intPtrOrNil(effectiveVersion),
		"grayTag":          nil,
		"items":            gin.H{},
	}
	if effectiveVersion != 0 {
		version, err := s.store.GetVersion(c.Request.Context(), namespace, environment, effectiveVersion)
		if err != nil {
			s.handleStorageError(c, err)
			return
		}
		items, err := s.store.Items(c.Request.Context(), namespace, environment, effectiveVersion)
		if err != nil {
			s.handleStorageError(c, err)
			return
		}
		tag := (*string)(nil)
		if version.GrayTag.Valid {
			tag = &version.GrayTag.String
		}
		response["grayTag"] = tag
		response["items"] = rawMap(items)
	}
	c.JSON(http.StatusOK, response)
}

func (s *server) handleHistory(c *gin.Context) {
	namespace, environment := c.Query("namespace"), c.Query("environment")
	if namespace == "" || environment == "" {
		writeError(c, http.StatusBadRequest, "MISSING_SCOPE", "namespace and environment are required")
		return
	}
	versions, err := s.store.ListVersions(c.Request.Context(), namespace, environment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	effectiveVersion, err := s.store.EffectiveVersion(c.Request.Context(), namespace, environment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	infos := make([]VersionInfo, 0, len(versions))
	for _, version := range versions {
		infos = append(infos, toVersionInfo(version, effectiveVersion))
	}
	c.JSON(http.StatusOK, gin.H{
		"namespace":   namespace,
		"environment": environment,
		"versions":    infos,
	})
}

// handleVersionSnapshot returns the full item snapshot stored for one historical version. It is
// read-only: it never creates versions or changes effective, gray, or rollback state.
func (s *server) handleVersionSnapshot(c *gin.Context) {
	namespace, environment := c.Param("namespace"), c.Param("environment")
	versionNumber, ok := parseVersionParam(c, c.Param("version"))
	if !ok {
		return
	}
	if !s.versionAvailable(c, namespace, environment, versionNumber) {
		return
	}
	version, err := s.store.GetVersion(c.Request.Context(), namespace, environment, versionNumber)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	items, err := s.store.Items(c.Request.Context(), namespace, environment, versionNumber)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	effectiveVersion, err := s.store.EffectiveVersion(c.Request.Context(), namespace, environment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"namespace":   namespace,
		"environment": environment,
		"version":     toVersionInfo(version, effectiveVersion),
		"items":       rawMap(items),
	})
}

// handleVersionDiff powers the public diff entry. Scope comes from the query string; base and
// target may come from query string or path parameters. Comparison always runs low -> high.
func (s *server) handleVersionDiff(c *gin.Context) {
	namespace := firstNonEmpty(c.Query("namespace"), c.Param("namespace"))
	environment := firstNonEmpty(c.Query("environment"), c.Param("environment"))
	if namespace == "" || environment == "" {
		writeError(c, http.StatusBadRequest, "MISSING_SCOPE", "namespace and environment are required")
		return
	}
	base, baseOK := parseVersionParam(c, firstNonEmpty(c.Query("baseVersion"), c.Param("base")))
	if !baseOK {
		return
	}
	target, targetOK := parseVersionParam(c, firstNonEmpty(c.Query("targetVersion"), c.Param("target")))
	if !targetOK {
		return
	}
	if target < base {
		writeError(c, http.StatusConflict, "VERSION_ORDER_CONFLICT", "target version must be greater than or equal to base version")
		return
	}
	if base == target {
		if !s.versionAvailable(c, namespace, environment, base) {
			return
		}
	} else {
		if !s.versionAvailable(c, namespace, environment, base) || !s.versionAvailable(c, namespace, environment, target) {
			return
		}
	}

	baseVersion, err := s.store.GetVersion(c.Request.Context(), namespace, environment, base)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	targetVersion, err := s.store.GetVersion(c.Request.Context(), namespace, environment, target)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	baseItems, err := s.store.Items(c.Request.Context(), namespace, environment, base)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	targetItems, err := s.store.Items(c.Request.Context(), namespace, environment, target)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	effectiveVersion, err := s.store.EffectiveVersion(c.Request.Context(), namespace, environment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	effectiveItems := map[string]json.RawMessage{}
	if effectiveVersion != 0 {
		effectiveItems, err = s.store.Items(c.Request.Context(), namespace, environment, effectiveVersion)
		if err != nil {
			s.handleStorageError(c, err)
			return
		}
	}
	c.JSON(http.StatusOK, BuildDiff(baseVersion, targetVersion, baseItems, targetItems, effectiveItems, effectiveVersion))
}

// versionAvailable enforces the fixed lookup order: missing globally -> 404, exists outside the
// requested scope -> 409. A response is only written on failure.
func (s *server) versionAvailable(c *gin.Context, namespace, environment string, version int64) bool {
	_, err := s.store.GetVersion(c.Request.Context(), namespace, environment, version)
	if err == nil {
		return true
	}
	if !errors.Is(err, store.ErrNotFound) {
		s.handleStorageError(c, err)
		return false
	}
	exists, err := s.store.VersionExistsAnywhere(c.Request.Context(), version)
	if err != nil {
		s.handleStorageError(c, err)
		return false
	}
	if !exists {
		writeError(c, http.StatusNotFound, "VERSION_NOT_FOUND", "version does not exist")
		return false
	}
	writeError(c, http.StatusConflict, "VERSION_SCOPE_MISMATCH", "version belongs to another namespace or environment")
	return false
}

func parseVersionParam(c *gin.Context, raw string) (int64, bool) {
	if !positiveIntegerPattern.MatchString(raw) {
		writeError(c, http.StatusBadRequest, "INVALID_VERSION", "version must be a positive integer")
		return 0, false
	}
	version, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_VERSION", "version must be a positive integer")
		return 0, false
	}
	return version, true
}

func (s *server) handleStorageError(c *gin.Context, err error) {
	if errors.Is(err, sql.ErrTxDone) {
		writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
		return
	}
	writeError(c, http.StatusInternalServerError, "internal_error", "the request failed due to a storage problem")
}

func writeError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func rawMap(items map[string]json.RawMessage) gin.H {
	result := make(gin.H, len(items))
	for name, value := range items {
		result[name] = value
	}
	return result
}
