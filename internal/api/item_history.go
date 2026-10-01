package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/config-center-api/internal/store"
)

// EffectiveItem is the item state in the effective snapshot. Value is only sent when Present.
type EffectiveItem struct {
	Present bool             `json:"present"`
	Value   *json.RawMessage `json:"value,omitempty"`
}

// ItemChange is one version-to-version change of a single configuration item.
type ItemChange struct {
	Version  VersionInfo      `json:"version"`
	Type     string           `json:"changeType"`
	OldValue *json.RawMessage `json:"oldValue,omitempty"`
	NewValue *json.RawMessage `json:"newValue,omitempty"`
}

// ItemHistoryResponse is the complete per-item history of one scope.
type ItemHistoryResponse struct {
	Namespace        string        `json:"namespace"`
	Environment      string        `json:"environment"`
	Name             string        `json:"name"`
	EffectiveVersion *int64        `json:"effectiveVersion"`
	EffectiveItem    EffectiveItem `json:"effectiveItem"`
	Changes          []ItemChange  `json:"changes"`
	TotalChanges     int           `json:"totalChanges"`
}

// BuildItemHistory folds the stored values of one item into version-to-version changes. Every
// version of the scope is compared with the immediately previous version, so gray and rollback
// releases take part like any other version and removals surface at the version they happen.
func BuildItemHistory(namespace, environment, name string, versions []store.Version,
	values []store.ItemValue, effectiveVersion int64) ItemHistoryResponse {
	byVersion := make(map[int64]json.RawMessage, len(values))
	for _, stored := range values {
		byVersion[stored.Version] = stored.Value
	}

	changes := make([]ItemChange, 0, len(values))
	var previous json.RawMessage
	hadPrevious := false
	for _, version := range versions {
		current, hasCurrent := byVersion[version.Version]
		switch {
		case hasCurrent && !hadPrevious:
			value := current
			changes = append(changes, ItemChange{
				Version:  toVersionInfo(version, effectiveVersion),
				Type:     changeAdded,
				NewValue: &value,
			})
		case !hasCurrent && hadPrevious:
			value := previous
			changes = append(changes, ItemChange{
				Version:  toVersionInfo(version, effectiveVersion),
				Type:     changeRemoved,
				OldValue: &value,
			})
		case hasCurrent && hadPrevious && !bytes.Equal(previous, current):
			oldValue, newValue := previous, current
			changes = append(changes, ItemChange{
				Version:  toVersionInfo(version, effectiveVersion),
				Type:     changeModified,
				OldValue: &oldValue,
				NewValue: &newValue,
			})
		}
		previous = current
		hadPrevious = hasCurrent
	}

	effectiveItem := EffectiveItem{Present: false}
	if effectiveVersion != 0 {
		if value, ok := byVersion[effectiveVersion]; ok {
			valueCopy := value
			effectiveItem = EffectiveItem{Present: true, Value: &valueCopy}
		}
	}
	return ItemHistoryResponse{
		Namespace:        namespace,
		Environment:      environment,
		Name:             name,
		EffectiveVersion: intPtrOrNil(effectiveVersion),
		EffectiveItem:    effectiveItem,
		Changes:          changes,
		TotalChanges:     len(changes),
	}
}

// handleItemHistory powers the read-only per-item history entry. It never writes to storage.
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
	versions, err := s.store.ListVersions(c.Request.Context(), namespace, environment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	values, err := s.store.ItemValues(c.Request.Context(), namespace, environment, name)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	effectiveVersion, err := s.store.EffectiveVersion(c.Request.Context(), namespace, environment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	c.JSON(http.StatusOK, BuildItemHistory(namespace, environment, name, versions, values, effectiveVersion))
}
