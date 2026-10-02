package api

import (
	"bytes"
	"encoding/json"

	"github.com/luwa07832/config-center-api/internal/store"
)

// EffectiveConfigItemSearchResult reports the state of one named item in the effective snapshot
// of a single scope. It reuses the single-item effective-state semantics: an absent item carries
// no value field, while a stored JSON null still carries value: null.
type EffectiveConfigItemSearchResult struct {
	Namespace        string             `json:"namespace"`
	Environment      string             `json:"environment"`
	EffectiveVersion *int64             `json:"effectiveVersion"`
	EffectiveItem    EffectiveItemState `json:"effectiveItem"`
}

// EffectiveConfigItemSearchResponse is the read-only global search result. Inputs must already be
// ordered by namespace then environment ascending.
type EffectiveConfigItemSearchResponse struct {
	Name         string                            `json:"name"`
	MatchedCount int                               `json:"matchedCount"`
	Results      []EffectiveConfigItemSearchResult `json:"results"`
}

// BuildEffectiveConfigItemSearch applies the presence and value filters to the stored per-scope
// states. presentFilter is nil when no presence filter is supplied; wantValue carries the
// canonical JSON to match when a value filter is supplied, which always requires presence.
func BuildEffectiveConfigItemSearch(name string, scopes []store.EffectiveItemScope,
	presentFilter *bool, wantValue json.RawMessage) EffectiveConfigItemSearchResponse {
	results := make([]EffectiveConfigItemSearchResult, 0, len(scopes))
	for _, scope := range scopes {
		present := scope.HasValue
		if presentFilter != nil && present != *presentFilter {
			continue
		}
		if wantValue != nil && (!present || !bytes.Equal(json.RawMessage(scope.Value), wantValue)) {
			continue
		}
		effectiveItem := EffectiveItemState{Present: present}
		if present {
			value := json.RawMessage(scope.Value)
			effectiveItem.Value = &value
		}
		results = append(results, EffectiveConfigItemSearchResult{
			Namespace:        scope.Namespace,
			Environment:      scope.Environment,
			EffectiveVersion: intPtrOrNil(scope.EffectiveVersion),
			EffectiveItem:    effectiveItem,
		})
	}
	return EffectiveConfigItemSearchResponse{
		Name:         name,
		MatchedCount: len(results),
		Results:      results,
	}
}

// canonicalRawJSON re-encodes raw JSON with the same rules the store applies on write, so a
// value filter matches stored values by semantics: whitespace and object key order are ignored
// while number, boolean, null and string distinctions are preserved.
func canonicalRawJSON(raw string) (json.RawMessage, bool) {
	if !json.Valid([]byte(raw)) {
		return nil, false
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(encoded), true
}
