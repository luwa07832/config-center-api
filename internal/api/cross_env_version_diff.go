package api

import (
	"encoding/json"
	"sort"
)

// CrossEnvironmentVersionDiffResponse compares two historical versions that live in different
// environments of one namespace. Unlike the same-environment version diff, changes carry no
// affectsEffectiveConfig flag because each side belongs to its own environment.
type CrossEnvironmentVersionDiffResponse struct {
	Namespace         string                  `json:"namespace"`
	BaseEnvironment   string                  `json:"baseEnvironment"`
	TargetEnvironment string                  `json:"targetEnvironment"`
	BaseVersion       VersionInfo             `json:"baseVersion"`
	TargetVersion     VersionInfo             `json:"targetVersion"`
	ChangedCount      int                     `json:"changedCount"`
	Changes           []EffectiveConfigChange `json:"changes"`
}

// BuildCrossEnvironmentVersionDiff diffs the stored items of one historical version per
// environment. Item names are merged and changes are ordered by name. Values are compared as the
// stored canonical JSON, so whitespace and object key order are ignored while number, boolean,
// null and string distinctions are preserved.
func BuildCrossEnvironmentVersionDiff(namespace string, baseVersion, targetVersion VersionInfo,
	baseItems, targetItems map[string]json.RawMessage) CrossEnvironmentVersionDiffResponse {
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
	return CrossEnvironmentVersionDiffResponse{
		Namespace:         namespace,
		BaseEnvironment:   baseVersion.Environment,
		TargetEnvironment: targetVersion.Environment,
		BaseVersion:       baseVersion,
		TargetVersion:     targetVersion,
		ChangedCount:      len(changes),
		Changes:           changes,
	}
}
