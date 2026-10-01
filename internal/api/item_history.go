package api

import (
	"encoding/json"

	"github.com/luwa07832/config-center-api/internal/store"
)

// ItemChange is one change of a single item between two consecutive stored versions.
type ItemChange struct {
	Version  VersionInfo      `json:"version"`
	Type     string           `json:"changeType"`
	OldValue *json.RawMessage `json:"oldValue,omitempty"`
	NewValue *json.RawMessage `json:"newValue,omitempty"`
}

// EffectiveItemState reports whether the item exists in the latest full release and, when it
// does, its stored raw JSON value.
type EffectiveItemState struct {
	Present bool             `json:"present"`
	Value   *json.RawMessage `json:"value,omitempty"`
}

// ItemHistoryResponse is the read-only per-item history.
type ItemHistoryResponse struct {
	Namespace        string             `json:"namespace"`
	Environment      string             `json:"environment"`
	Name             string             `json:"name"`
	EffectiveVersion *int64             `json:"effectiveVersion"`
	EffectiveItem    EffectiveItemState `json:"effectiveItem"`
	Changes          []ItemChange       `json:"changes"`
	TotalChanges     int                `json:"totalChanges"`
}

// BuildItemHistory compares one item's value in each version with the immediately preceding
// version of the same scope. Gray releases and rollback copies participate; unchanged versions
// are skipped. Inputs must be ordered by version ascending.
func BuildItemHistory(namespace, environment, name string,
	history []store.ItemVersionValue, effectiveValue json.RawMessage, effectiveHas bool, effectiveVersion int64) ItemHistoryResponse {
	changes := make([]ItemChange, 0)
	var previous string
	hadPrevious := false
	for _, entry := range history {
		switch {
		case entry.HasValue && !hadPrevious:
			newValue := json.RawMessage(entry.Value)
			changes = append(changes, ItemChange{
				Version:  toVersionInfo(entry.Version, effectiveVersion),
				Type:     changeAdded,
				NewValue: &newValue,
			})
		case hadPrevious && !entry.HasValue:
			oldValue := json.RawMessage(previous)
			changes = append(changes, ItemChange{
				Version:  toVersionInfo(entry.Version, effectiveVersion),
				Type:     changeRemoved,
				OldValue: &oldValue,
			})
		case hadPrevious && entry.HasValue && entry.Value != previous:
			oldValue := json.RawMessage(previous)
			newValue := json.RawMessage(entry.Value)
			changes = append(changes, ItemChange{
				Version:  toVersionInfo(entry.Version, effectiveVersion),
				Type:     changeModified,
				OldValue: &oldValue,
				NewValue: &newValue,
			})
		}
		if entry.HasValue {
			previous = entry.Value
			hadPrevious = true
		} else {
			hadPrevious = false
		}
	}

	effectiveItem := EffectiveItemState{Present: false}
	if effectiveHas {
		value := effectiveValue
		effectiveItem = EffectiveItemState{Present: true, Value: &value}
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
