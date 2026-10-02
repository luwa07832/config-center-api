package api

import (
	"encoding/json"
	"sort"
)

// EffectiveConfigChange is one item-level difference between the effective snapshots of two
// environments. It carries no affectsEffectiveConfig flag because that notion only applies to
// versions inside a single environment.
type EffectiveConfigChange struct {
	Name     string           `json:"name"`
	Type     string           `json:"changeType"`
	OldValue *json.RawMessage `json:"oldValue,omitempty"`
	NewValue *json.RawMessage `json:"newValue,omitempty"`
}

// EffectiveConfigDiffResponse is the complete result of comparing the effective snapshots of
// two environments in one namespace. Version fields are nil when a side has no full release.
type EffectiveConfigDiffResponse struct {
	Namespace         string                  `json:"namespace"`
	BaseEnvironment   string                  `json:"baseEnvironment"`
	TargetEnvironment string                  `json:"targetEnvironment"`
	BaseVersion       *VersionInfo            `json:"baseVersion"`
	TargetVersion     *VersionInfo            `json:"targetVersion"`
	ChangedCount      int                     `json:"changedCount"`
	Changes           []EffectiveConfigChange `json:"changes"`
}

// BuildEffectiveConfigDiff diffs the stored items of two effective snapshots. An empty items
// map represents a scope without an effective release, so every item on the other side becomes
// an added or removed change. Changes are ordered by item name.
func BuildEffectiveConfigDiff(namespace, baseEnvironment, targetEnvironment string,
	baseVersion, targetVersion *VersionInfo,
	baseItems, targetItems map[string]json.RawMessage) EffectiveConfigDiffResponse {
	names := make(map[string]struct{}, len(baseItems)+len(targetItems))
	for name := range baseItems {
		names[name] = struct{}{}
	}
	for name := range targetItems {
		names[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)

	changes := make([]EffectiveConfigChange, 0, len(ordered))
	for _, name := range ordered {
		oldValue, hadOld := baseItems[name]
		newValue, hasNew := targetItems[name]
		change, changed := buildChange(name, oldValue, hadOld, newValue, hasNew)
		if !changed {
			continue
		}
		changes = append(changes, EffectiveConfigChange{
			Name:     change.Name,
			Type:     change.Type,
			OldValue: change.OldValue,
			NewValue: change.NewValue,
		})
	}
	return EffectiveConfigDiffResponse{
		Namespace:         namespace,
		BaseEnvironment:   baseEnvironment,
		TargetEnvironment: targetEnvironment,
		BaseVersion:       baseVersion,
		TargetVersion:     targetVersion,
		ChangedCount:      len(changes),
		Changes:           changes,
	}
}
