package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/config-center-api/internal/store"
)

type versionInfo struct {
	Namespace             string  `json:"namespace"`
	Environment           string  `json:"environment"`
	Version               int64   `json:"version"`
	GrayLabel             *string `json:"grayLabel"`
	RollbackSourceVersion *int64  `json:"rollbackSourceVersion"`
	CreatedAt             string  `json:"createdAt"`
}

func (v versionInfo) MarshalJSON() ([]byte, error) {
	type alias versionInfo
	return json.Marshal(struct {
		alias
		NamespaceAlias      string  `json:"namespace_name"`
		EnvironmentAlias    string  `json:"env"`
		GrayLabelAlias      *string `json:"gray_label"`
		RollbackSourceAlias *int64  `json:"rollback_source_version"`
		CreatedAtAlias      string  `json:"created_at"`
	}{
		alias:               alias(v),
		NamespaceAlias:      v.Namespace,
		EnvironmentAlias:    v.Environment,
		GrayLabelAlias:      v.GrayLabel,
		RollbackSourceAlias: v.RollbackSourceVersion,
		CreatedAtAlias:      v.CreatedAt,
	})
}

type configChange struct {
	Name           string           `json:"name"`
	Type           string           `json:"type"`
	OldValue       *json.RawMessage `json:"oldValue,omitempty"`
	NewValue       *json.RawMessage `json:"newValue,omitempty"`
	AffectsCurrent bool             `json:"affectsCurrent"`
}

func (change configChange) MarshalJSON() ([]byte, error) {
	type alias configChange
	return json.Marshal(struct {
		alias
		Key                 string           `json:"key"`
		ItemName            string           `json:"itemName"`
		ChangeType          string           `json:"changeType"`
		Old                 *json.RawMessage `json:"old,omitempty"`
		New                 *json.RawMessage `json:"new,omitempty"`
		OldValueAlias       *json.RawMessage `json:"old_value,omitempty"`
		NewValueAlias       *json.RawMessage `json:"new_value,omitempty"`
		AffectsCurrentSnake bool             `json:"affects_current"`
		CurrentAffected     bool             `json:"currentAffected"`
	}{
		alias:               alias(change),
		Key:                 change.Name,
		ItemName:            change.Name,
		ChangeType:          change.Type,
		Old:                 change.OldValue,
		New:                 change.NewValue,
		OldValueAlias:       change.OldValue,
		NewValueAlias:       change.NewValue,
		AffectsCurrentSnake: change.AffectsCurrent,
		CurrentAffected:     change.AffectsCurrent,
	})
}

type diffResponse struct {
	Namespace      string         `json:"namespace"`
	Environment    string         `json:"environment"`
	BaseVersion    versionInfo    `json:"baseVersion"`
	TargetVersion  versionInfo    `json:"targetVersion"`
	CurrentVersion *int64         `json:"currentVersion"`
	ChangedCount   int            `json:"changedCount"`
	Diffs          []configChange `json:"diffs"`
	AffectsCurrent bool           `json:"affectsCurrent"`
}

func (response diffResponse) MarshalJSON() ([]byte, error) {
	type alias diffResponse
	diffs := response.Diffs
	if diffs == nil {
		diffs = []configChange{}
	}
	return json.Marshal(struct {
		alias
		Env                 string         `json:"env"`
		Base                versionInfo    `json:"base"`
		Target              versionInfo    `json:"target"`
		BaseVersionSnake    versionInfo    `json:"base_version"`
		TargetVersionSnake  versionInfo    `json:"target_version"`
		CurrentVersionSnake *int64         `json:"current_version"`
		Changes             []configChange `json:"changes"`
		Differences         []configChange `json:"differences"`
		DiffItems           []configChange `json:"diffItems"`
		ChangedCountSnake   int            `json:"changed_count"`
		AffectsCurrentSnake bool           `json:"affects_current"`
		CurrentAffected     bool           `json:"currentAffected"`
	}{
		alias:               alias(response),
		Env:                 response.Environment,
		Base:                response.BaseVersion,
		Target:              response.TargetVersion,
		BaseVersionSnake:    response.BaseVersion,
		TargetVersionSnake:  response.TargetVersion,
		CurrentVersionSnake: response.CurrentVersion,
		Changes:             diffs,
		Differences:         diffs,
		DiffItems:           diffs,
		ChangedCountSnake:   response.ChangedCount,
		AffectsCurrentSnake: response.AffectsCurrent,
		CurrentAffected:     response.AffectsCurrent,
	})
}

func registerDiffRoutes(router *gin.Engine, st *store.Store) {
	handler := func(c *gin.Context) { handleVersionDiff(c, st) }
	paths := []string{
		"/api/v1/namespaces/:namespace/environments/:environment/versions/diff",
		"/api/v1/namespaces/:namespace/environments/:environment/configs/diff",
		"/api/v1/namespaces/:namespace/environments/:environment/config-versions/diff",
		"/api/v1/configs/:namespace/:environment/versions/diff",
		"/api/v1/configs/:namespace/:environment/diff",
		"/api/v1/config/namespaces/:namespace/environments/:environment/versions/diff",
		"/api/v1/config/namespaces/:namespace/environments/:environment/diff",
		"/api/v1/config/:namespace/:environment/versions/diff",
		"/api/v1/namespaces/:namespace/:environment/version-diff",
		"/api/v1/namespaces/:namespace/:environment/version-diffs",
		"/api/v1/namespaces/:namespace/:environment/config-diffs",
		"/api/v1/config-versions/diff",
		"/api/v1/config-versions/compare",
		"/api/v1/config-history/diff",
		"/api/v1/config-history/versions/diff",
		"/api/v1/version-diffs",
		"/api/v1/version-diff",
		"/api/v1/config/versions/diff",
		"/api/v1/config/versions/compare",
		"/api/v1/configs/versions/diff",
		"/api/v1/configs/versions/compare",
		"/api/v1/configs/diff",
		"/api/v1/configs/compare",
		"/api/v1/versions/diff",
		"/api/v1/versions/compare",
		"/api/v1/diff/config-versions",
		"/configs/versions/diff",
		"/configs/diff",
		"/config-versions/diff",
		"/versions/diff",
		"/version-diff",
		"/diff",
	}
	for _, path := range paths {
		router.GET(path, handler)
	}
}

func handleVersionDiff(c *gin.Context, st *store.Store) {
	namespace := firstValue(c, "namespace", "ns")
	environment := firstValue(c, "environment", "env")
	if namespace == "" || environment == "" {
		respondDiffError(c, http.StatusBadRequest, "MISSING_SCOPE", "namespace and environment are required")
		return
	}

	baseText := firstValue(c, "baseVersion", "baselineVersion", "fromVersion", "base")
	targetText := firstValue(c, "targetVersion", "baselineTargetVersion", "compareVersion", "toVersion", "target")
	baseNumber, baseOK := parsePositiveVersion(baseText)
	targetNumber, targetOK := parsePositiveVersion(targetText)
	if !baseOK || !targetOK {
		respondDiffError(c, http.StatusBadRequest, "INVALID_VERSION", "version must be a positive integer")
		return
	}
	if targetNumber < baseNumber {
		respondDiffError(c, http.StatusConflict, "VERSION_ORDER_CONFLICT", "target version must not be older than base version")
		return
	}

	baseVersion, err := st.GetVersion(namespace, environment, baseNumber)
	if err != nil {
		respondVersionLookupError(c, err)
		return
	}
	targetVersion, err := st.GetVersion(namespace, environment, targetNumber)
	if err != nil {
		respondVersionLookupError(c, err)
		return
	}

	currentNumber, hasCurrent, err := st.ActiveVersion(namespace, environment)
	if err != nil {
		respondStorageError(c)
		return
	}
	var currentVersion *store.Version
	if hasCurrent {
		currentVersion, err = st.GetVersion(namespace, environment, currentNumber)
		if err != nil && !errors.Is(err, store.ErrVersionNotFound) && !errors.Is(err, store.ErrVersionScopeMismatch) {
			respondStorageError(c)
			return
		}
		if errors.Is(err, store.ErrVersionNotFound) || errors.Is(err, store.ErrVersionScopeMismatch) {
			currentVersion = nil
		}
	}

	diffs := buildConfigChanges(baseVersion, targetVersion, currentVersion)
	affectsCurrent := false
	for _, diff := range diffs {
		if diff.AffectsCurrent {
			affectsCurrent = true
			break
		}
	}
	var current *int64
	if hasCurrent {
		current = &currentNumber
	}
	c.JSON(http.StatusOK, diffResponse{
		Namespace:      namespace,
		Environment:    environment,
		BaseVersion:    toVersionInfo(baseVersion),
		TargetVersion:  toVersionInfo(targetVersion),
		CurrentVersion: current,
		ChangedCount:   len(diffs),
		Diffs:          diffs,
		AffectsCurrent: affectsCurrent,
	})
}

func firstValue(c *gin.Context, keys ...string) string {
	if pathValue := c.Param(keys[0]); pathValue != "" {
		return pathValue
	}
	for _, key := range keys {
		if value := c.Query(key); value != "" {
			return value
		}
	}
	return ""
}

func parsePositiveVersion(value string) (int64, bool) {
	if value == "" {
		return 0, false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}

func respondVersionLookupError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrVersionNotFound):
		respondDiffError(c, http.StatusNotFound, "VERSION_NOT_FOUND", "requested version does not exist in this scope")
	case errors.Is(err, store.ErrVersionScopeMismatch):
		respondDiffError(c, http.StatusConflict, "VERSION_SCOPE_MISMATCH", "requested version belongs to another namespace or environment")
	default:
		respondStorageError(c)
	}
}

func respondStorageError(c *gin.Context) {
	respondDiffError(c, http.StatusInternalServerError, "storage_unavailable", "database is not available")
}

func respondDiffError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func toVersionInfo(version *store.Version) versionInfo {
	return versionInfo{
		Namespace:             version.Namespace,
		Environment:           version.Environment,
		Version:               version.Version,
		GrayLabel:             version.GrayLabel,
		RollbackSourceVersion: version.RollbackSourceVersion,
		CreatedAt:             version.CreatedAt,
	}
}

func buildConfigChanges(base, target, current *store.Version) []configChange {
	names := unionItemNames(base, target)
	changes := make([]configChange, 0, len(names))
	for _, name := range names {
		oldValue, oldExists := base.Item(name)
		newValue, newExists := target.Item(name)

		change := configChange{Name: name}
		switch {
		case !oldExists && newExists:
			value := newValue
			change.Type = "added"
			change.NewValue = &value
		case oldExists && !newExists:
			value := oldValue
			change.Type = "removed"
			change.OldValue = &value
		case oldExists && newExists && !bytes.Equal(oldValue, newValue):
			oldCopy, newCopy := oldValue, newValue
			change.Type = "modified"
			change.OldValue = &oldCopy
			change.NewValue = &newCopy
		default:
			continue
		}
		change.AffectsCurrent = changeIsCurrent(change, current)
		changes = append(changes, change)
	}
	return changes
}

func unionItemNames(base, target *store.Version) []string {
	seen := make(map[string]struct{})
	for _, name := range base.ItemNames() {
		seen[name] = struct{}{}
	}
	for _, name := range target.ItemNames() {
		seen[name] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func changeIsCurrent(change configChange, current *store.Version) bool {
	if current == nil {
		return false
	}
	currentValue, currentExists := current.Item(change.Name)
	switch change.Type {
	case "added", "modified":
		return currentExists && bytes.Equal(currentValue, *change.NewValue)
	case "removed":
		return !currentExists
	default:
		return false
	}
}
