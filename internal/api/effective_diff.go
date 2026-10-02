package api

import (
	"encoding/json"
	"sort"

	"github.com/luwa07832/config-center-api/internal/store"
)

// EffectiveConfigChange is one item-level difference between the effective configurations of
// two environments in the same namespace.
type EffectiveConfigChange struct {
	Name     string           `json:"name"`
	Type     string           `json:"changeType"`
	OldValue *json.RawMessage `json:"oldValue,omitempty"`
	NewValue *json.RawMessage `json:"newValue,omitempty"`
}

// EffectiveConfigDiffResponse is the complete result of comparing the effective configurations
// of two environments. Version fields are null when an environment has no full release yet.
type EffectiveConfigDiffResponse struct {
	Namespace         string                  `json:"namespace"`
	BaseEnvironment   string                  `json:"baseEnvironment"`
	TargetEnvironment string                  `json:"targetEnvironment"`
	BaseVersion       *VersionInfo            `json:"baseVersion"`
	TargetVersion     *VersionInfo            `json:"targetVersion"`
	ChangedCount      int                     `json:"changedCount"`
	Changes           []EffectiveConfigChange `json:"changes"`
}

// BuildEffectiveConfigDiff compares the effective snapshots of two environments. Item names are
// merged and ordered lexicographically; values keep their stored raw JSON semantics, so numbers,
// booleans, null and strings are never converted. A nil version means that environment has no
// full release and its side compares as an empty configuration.
func BuildEffectiveConfigDiff(namespace, baseEnvironment, targetEnvironment string,
	baseVersion *store.Version, baseItems map[string]json.RawMessage,
	targetVersion *store.Version, targetItems map[string]json.RawMessage) EffectiveConfigDiffResponse {
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
		BaseVersion:       effectiveVersionInfo(baseVersion),
		TargetVersion:     effectiveVersionInfo(targetVersion),
		ChangedCount:      len(changes),
		Changes:           changes,
	}
}

// effectiveVersionInfo renders the stored metadata of an effective version, or nil when the
// environment has no full release. The returned version is the effective one by definition.
func effectiveVersionInfo(version *store.Version) *VersionInfo {
	if version == nil {
		return nil
	}
	info := toVersionInfo(*version, version.Version)
	return &info
}
