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

func (s *server) handlePromote(c *gin.Context) {
	namespace, environment := c.Param("namespace"), c.Param("environment")
	sourceVersion, ok := parseVersionParam(c, c.Param("version"))
	if !ok {
		return
	}
	// Enforce the fixed lookup order: 400 INVALID_VERSION is handled above; the version must
	// exist globally and belong to this scope before the gray-tag check applies.
	if !s.versionAvailable(c, namespace, environment, sourceVersion) {
		return
	}
	version, err := s.store.Promote(c.Request.Context(), namespace, environment, sourceVersion)
	if errors.Is(err, store.ErrNotGray) {
		writeError(c, http.StatusConflict, "NOT_GRAY_VERSION", "the source version is not a gray release")
		return
	}
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
		"namespace":   namespace,
		"environment": environment,
		"version":     toVersionInfo(version, effectiveVersion),
		"items":       rawMap(items),
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

// handleItemHistory returns the change history of one named item across every version of the
// scope. It is strictly read-only and never creates a version or changes effective state.
func (s *server) handleItemHistory(c *gin.Context) {
	namespace, environment := c.Query("namespace"), c.Query("environment")
	if namespace == "" || environment == "" {
		writeError(c, http.StatusBadRequest, "MISSING_SCOPE", "namespace and environment are required")
		return
	}
	name := c.Query("name")
	if name == "" {
		writeError(c, http.StatusBadRequest, "MISSING_ITEM_NAME", "name is required")
		return
	}
	history, err := s.store.ItemHistory(c.Request.Context(), namespace, environment, name)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	effectiveVersion, err := s.store.EffectiveVersion(c.Request.Context(), namespace, environment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	var effectiveValue json.RawMessage
	effectiveHas := false
	if effectiveVersion != 0 {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Version.Version == effectiveVersion {
				effectiveHas = history[i].HasValue
				if effectiveHas {
					effectiveValue = json.RawMessage(history[i].Value)
				}
				break
			}
		}
	}
	c.JSON(http.StatusOK, BuildItemHistory(namespace, environment, name, history, effectiveValue, effectiveHas, effectiveVersion))
}

// handleEffectiveConfigItemSearch reports the state of one exactly-named item across every scope
// that owns version history. It is strictly read-only: no version or item row is created and
// publish, gray, promotion, rollback and other query behavior is unchanged.
func (s *server) handleEffectiveConfigItemSearch(c *gin.Context) {
	name, exists := c.GetQuery("name")
	if !exists || name == "" {
		writeError(c, http.StatusBadRequest, "MISSING_ITEM_NAME", "name is required")
		return
	}
	var presentFilter *bool
	if rawPresent, presentExists := c.GetQuery("present"); presentExists {
		switch rawPresent {
		case "true":
			present := true
			presentFilter = &present
		case "false":
			present := false
			presentFilter = &present
		default:
			writeError(c, http.StatusBadRequest, "INVALID_PRESENCE_FILTER", "present must be true or false")
			return
		}
	}
	var wantValue json.RawMessage
	if rawValue, valueExists := c.GetQuery("value"); valueExists {
		if presentFilter != nil && !*presentFilter {
			writeError(c, http.StatusBadRequest, "INVALID_VALUE_FILTER", "value filter cannot be combined with present=false")
			return
		}
		canonical, ok := canonicalRawJSON(rawValue)
		if !ok {
			writeError(c, http.StatusBadRequest, "INVALID_VALUE_FILTER", "value must be valid JSON")
			return
		}
		wantValue = canonical
	}

	scopes, err := s.store.SearchEffectiveItem(c.Request.Context(),
		c.Query("namespace"), c.Query("environment"), name)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "storage_unavailable", "database is not available")
		return
	}
	c.JSON(http.StatusOK, BuildEffectiveConfigItemSearch(name, scopes, presentFilter, wantValue))
}

// handleVersionSnapshot returns the complete stored snapshot of one historical version. It is
// strictly read-only: no version is created and no effective, gray or rollback state changes.
func (s *server) handleVersionSnapshot(c *gin.Context) {
	namespace, environment := c.Param("namespace"), c.Param("environment")
	version, ok := parseVersionParam(c, c.Param("version"))
	if !ok {
		return
	}
	if !s.versionAvailable(c, namespace, environment, version) {
		return
	}
	stored, err := s.store.GetVersion(c.Request.Context(), namespace, environment, version)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	items, err := s.store.Items(c.Request.Context(), namespace, environment, version)
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
		"version":     toVersionInfo(stored, effectiveVersion),
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

// handleEffectiveConfigDiff compares the current effective snapshots of two environments in one
// namespace. It is strictly read-only: no version is created and no effective, gray, rollback or
// history state changes.
func (s *server) handleEffectiveConfigDiff(c *gin.Context) {
	namespace := firstNonEmpty(c.Query("namespace"), c.Param("namespace"))
	baseEnvironment := firstNonEmpty(c.Query("baseEnvironment"), c.Param("baseEnvironment"))
	targetEnvironment := firstNonEmpty(c.Query("targetEnvironment"), c.Param("targetEnvironment"))
	if namespace == "" || baseEnvironment == "" || targetEnvironment == "" {
		writeError(c, http.StatusBadRequest, "MISSING_SCOPE", "namespace, baseEnvironment and targetEnvironment are required")
		return
	}
	if baseEnvironment == targetEnvironment {
		writeError(c, http.StatusBadRequest, "SAME_ENVIRONMENT", "baseEnvironment and targetEnvironment must be different")
		return
	}
	baseVersion, baseItems, err := s.effectiveSnapshot(c, namespace, baseEnvironment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	targetVersion, targetItems, err := s.effectiveSnapshot(c, namespace, targetEnvironment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	c.JSON(http.StatusOK, BuildEffectiveConfigDiff(namespace, baseEnvironment, targetEnvironment,
		baseVersion, targetVersion, baseItems, targetItems))
}

// handleCrossEnvironmentVersionDiff compares two historical versions that live in different
// environments of one namespace. It is strictly read-only: no version, audit or diff record is
// created, and no publish, gray, promotion, rollback, history or effective state changes.
func (s *server) handleCrossEnvironmentVersionDiff(c *gin.Context) {
	namespace := c.Query("namespace")
	baseEnvironment := c.Query("baseEnvironment")
	targetEnvironment := c.Query("targetEnvironment")
	if namespace == "" || baseEnvironment == "" || targetEnvironment == "" {
		writeError(c, http.StatusBadRequest, "MISSING_SCOPE", "namespace, baseEnvironment and targetEnvironment are required")
		return
	}
	if baseEnvironment == targetEnvironment {
		writeError(c, http.StatusBadRequest, "SAME_ENVIRONMENT", "baseEnvironment and targetEnvironment must be different")
		return
	}
	base, ok := parseVersionParam(c, c.Query("baseVersion"))
	if !ok {
		return
	}
	target, ok := parseVersionParam(c, c.Query("targetVersion"))
	if !ok {
		return
	}
	// Lookup order is fixed base first, then target: each version must exist globally and belong
	// to its own side's scope before the other side is inspected.
	if !s.versionAvailable(c, namespace, baseEnvironment, base) {
		return
	}
	if !s.versionAvailable(c, namespace, targetEnvironment, target) {
		return
	}

	baseVersion, err := s.store.GetVersion(c.Request.Context(), namespace, baseEnvironment, base)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	targetVersion, err := s.store.GetVersion(c.Request.Context(), namespace, targetEnvironment, target)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	baseItems, err := s.store.Items(c.Request.Context(), namespace, baseEnvironment, base)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	targetItems, err := s.store.Items(c.Request.Context(), namespace, targetEnvironment, target)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	baseEffective, err := s.store.EffectiveVersion(c.Request.Context(), namespace, baseEnvironment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	targetEffective, err := s.store.EffectiveVersion(c.Request.Context(), namespace, targetEnvironment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	c.JSON(http.StatusOK, BuildCrossEnvironmentVersionDiff(namespace,
		toVersionInfo(baseVersion, baseEffective), toVersionInfo(targetVersion, targetEffective),
		baseItems, targetItems))
}

// effectiveSnapshot loads the effective version metadata and items of one scope. A scope without
// a full release yields a nil version and an empty item map.
func (s *server) effectiveSnapshot(c *gin.Context, namespace, environment string) (*VersionInfo, map[string]json.RawMessage, error) {
	effectiveVersion, err := s.store.EffectiveVersion(c.Request.Context(), namespace, environment)
	if err != nil {
		return nil, nil, err
	}
	items := map[string]json.RawMessage{}
	if effectiveVersion == 0 {
		return nil, items, nil
	}
	version, err := s.store.GetVersion(c.Request.Context(), namespace, environment, effectiveVersion)
	if err != nil {
		return nil, nil, err
	}
	items, err = s.store.Items(c.Request.Context(), namespace, environment, effectiveVersion)
	if err != nil {
		return nil, nil, err
	}
	info := toVersionInfo(version, effectiveVersion)
	return &info, items, nil
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
