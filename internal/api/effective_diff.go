package api

import (
	"encoding/json"
	"sort"
)

// EffectiveConfigChange is one item-level difference between snapshots that do not share a single
// environment scope: cross-environment effective configs and cross-environment historical versions.
// It carries no affectsEffectiveConfig flag because that notion only applies to versions inside a
// single environment.
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

// CrossEnvironmentVersionDiffResponse is the complete result of comparing two historical versions
// from two environments in one namespace. Both version objects are always present because each
// referenced version is verified to exist before the comparison runs.
type CrossEnvironmentVersionDiffResponse struct {
	Namespace         string                  `json:"namespace"`
	BaseEnvironment   string                  `json:"baseEnvironment"`
	BaseVersion       VersionInfo             `json:"baseVersion"`
	TargetEnvironment string                  `json:"targetEnvironment"`
	TargetVersion     VersionInfo             `json:"targetVersion"`
	ChangedCount      int                     `json:"changedCount"`
	Changes           []EffectiveConfigChange `json:"changes"`
}

// BuildEffectiveConfigDiff diffs the stored items of two effective snapshots. An empty items
// map represents a scope without an effective release, so every item on the other side becomes
// an added or removed change. Changes are ordered by item name.
func BuildEffectiveConfigDiff(namespace, baseEnvironment, targetEnvironment string,
	baseVersion, targetVersion *VersionInfo,
	baseItems, targetItems map[string]json.RawMessage) EffectiveConfigDiffResponse {
	changes := buildOrderedChanges(baseItems, targetItems)
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

// BuildCrossEnvironmentVersionDiff diffs the stored items of two historical versions living in two
// environments. Version numbers are allocated independently per environment, so any base and
// target numbers are comparable, including baseVersion >= targetVersion. Changes are ordered by
// item name and carry no affectsEffectiveConfig flag.
func BuildCrossEnvironmentVersionDiff(namespace string,
	baseVersion, targetVersion VersionInfo,
	baseItems, targetItems map[string]json.RawMessage) CrossEnvironmentVersionDiffResponse {
	changes := buildOrderedChanges(baseItems, targetItems)
	return CrossEnvironmentVersionDiffResponse{
		Namespace:         namespace,
		BaseEnvironment:   baseVersion.Environment,
		BaseVersion:       baseVersion,
		TargetEnvironment: targetVersion.Environment,
		TargetVersion:     targetVersion,
		ChangedCount:      len(changes),
		Changes:           changes,
	}
}

// buildOrderedChanges merges two item snapshots and returns every difference ordered by item name.
// added changes only carry newValue, removed only oldValue and modified carry both.
func buildOrderedChanges(baseItems, targetItems map[string]json.RawMessage) []EffectiveConfigChange {
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
			Name: change.Name, Type: change.Type, OldValue: change.OldValue, NewValue: change.NewValue,
		})
	}
	return changes
}
