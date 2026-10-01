package api

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/luwa07832/config-center-api/internal/store"
)

// Change kinds returned by a version diff.
const (
	changeAdded    = "added"
	changeRemoved  = "removed"
	changeModified = "modified"
)

// Change is one item-level difference, grouped by item name.
type Change struct {
	Name                   string           `json:"name"`
	Type                   string           `json:"changeType"`
	OldValue               *json.RawMessage `json:"oldValue,omitempty"`
	NewValue               *json.RawMessage `json:"newValue,omitempty"`
	AffectsEffectiveConfig bool             `json:"affectsEffectiveConfig"`
}

// VersionInfo is the version metadata shared by history and diff responses.
type VersionInfo struct {
	Namespace   string  `json:"namespace"`
	Environment string  `json:"environment"`
	Version     int64   `json:"version"`
	GrayTag     *string `json:"grayTag"`
	RollbackOf  *int64  `json:"rollbackOf"`
	PromotionOf *int64  `json:"promotionOf"`
	CreatedAt   string  `json:"createdAt"`
	Effective   bool    `json:"effective"`
}

// DiffResponse is the complete result of comparing two versions in one scope.
type DiffResponse struct {
	Namespace        string      `json:"namespace"`
	Environment      string      `json:"environment"`
	BaseVersion      VersionInfo `json:"baseVersion"`
	TargetVersion    VersionInfo `json:"targetVersion"`
	EffectiveVersion *int64      `json:"effectiveVersion"`
	ChangedCount     int         `json:"changedCount"`
	Changes          []Change    `json:"changes"`
}

// BuildDiff compares two snapshots from the same scope. Item names are merged and the result is
// ordered by name. A change reports whether the effective snapshot currently carries that change.
func BuildDiff(base, target store.Version, baseItems, targetItems,
	effectiveItems map[string]json.RawMessage, effectiveVersion int64) DiffResponse {
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

	changes := make([]Change, 0, len(ordered))
	for _, name := range ordered {
		oldValue, hadOld := baseItems[name]
		newValue, hasNew := targetItems[name]
		change, changed := buildChange(name, oldValue, hadOld, newValue, hasNew)
		if !changed {
			continue
		}
		change.AffectsEffectiveConfig = affectsEffective(change.Type, name, oldValue, newValue, effectiveItems)
		changes = append(changes, change)
	}
	return DiffResponse{
		Namespace:        target.Namespace,
		Environment:      target.Environment,
		BaseVersion:      toVersionInfo(base, effectiveVersion),
		TargetVersion:    toVersionInfo(target, effectiveVersion),
		EffectiveVersion: intPtrOrNil(effectiveVersion),
		ChangedCount:     len(changes),
		Changes:          changes,
	}
}

func buildChange(name string, oldValue json.RawMessage, hadOld bool,
	newValue json.RawMessage, hasNew bool) (Change, bool) {
	switch {
	case hasNew && !hadOld:
		value := newValue
		return Change{Name: name, Type: changeAdded, NewValue: &value}, true
	case hadOld && !hasNew:
		value := oldValue
		return Change{Name: name, Type: changeRemoved, OldValue: &value}, true
	case hadOld && hasNew && !bytes.Equal(oldValue, newValue):
		oldCopy, newCopy := oldValue, newValue
		return Change{Name: name, Type: changeModified, OldValue: &oldCopy, NewValue: &newCopy}, true
	default:
		return Change{}, false
	}
}

// affectsEffective reports whether the effective snapshot currently shows the target side of a
// change, so callers can tell if a historical release still shapes the live configuration.
func affectsEffective(kind, name string, oldValue, newValue json.RawMessage,
	effectiveItems map[string]json.RawMessage) bool {
	effectiveValue, effectiveHas := effectiveItems[name]
	switch kind {
	case changeAdded:
		return effectiveHas && bytes.Equal(effectiveValue, newValue)
	case changeRemoved:
		return !effectiveHas
	case changeModified:
		if !effectiveHas {
			return false
		}
		return !bytes.Equal(effectiveValue, oldValue) && bytes.Equal(effectiveValue, newValue)
	default:
		return false
	}
}

func toVersionInfo(v store.Version, effectiveVersion int64) VersionInfo {
	info := VersionInfo{
		Namespace:   v.Namespace,
		Environment: v.Environment,
		Version:     v.Version,
		RollbackOf:  intPtrOrNil(v.RollbackSource()),
		PromotionOf: intPtrOrNil(v.PromotionSource()),
		CreatedAt:   v.CreatedAt,
		Effective:   effectiveVersion != 0 && v.Version == effectiveVersion,
	}
	if v.GrayTag.Valid {
		tag := v.GrayTag.String
		info.GrayTag = &tag
	}
	return info
}

func intPtrOrNil(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}
