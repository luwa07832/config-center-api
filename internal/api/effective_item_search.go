package api

import (
	"encoding/json"

	"github.com/luwa07832/config-center-api/internal/store"
)

// EffectiveConfigItemSearchResult is the effective state of the searched item in one scope.
// EffectiveVersion is nil when the scope only has gray releases.
type EffectiveConfigItemSearchResult struct {
	Namespace        string             `json:"namespace"`
	Environment      string             `json:"environment"`
	EffectiveVersion *int64             `json:"effectiveVersion"`
	EffectiveItem    EffectiveItemState `json:"effectiveItem"`
}

// EffectiveConfigItemSearchResponse is the top-level result of the global item search.
type EffectiveConfigItemSearchResponse struct {
	Name         string                            `json:"name"`
	MatchedCount int                               `json:"matchedCount"`
	Results      []EffectiveConfigItemSearchResult `json:"results"`
}

// BuildEffectiveConfigItemSearch filters the stored scopes by the optional presence and value
// filters and assembles the search response. wantValue holds canonical JSON, so a scope only
// matches it when the item is present with an equal stored value. Rows must already be ordered
// by namespace then environment.
func BuildEffectiveConfigItemSearch(name string, rows []store.EffectiveItemSearchRow,
	wantPresent *bool, wantValue *string) EffectiveConfigItemSearchResponse {
	results := make([]EffectiveConfigItemSearchResult, 0, len(rows))
	for _, row := range rows {
		if wantPresent != nil && row.HasValue != *wantPresent {
			continue
		}
		if wantValue != nil && (!row.HasValue || row.Value != *wantValue) {
			continue
		}
		item := EffectiveItemState{Present: false}
		if row.HasValue {
			value := json.RawMessage(row.Value)
			item = EffectiveItemState{Present: true, Value: &value}
		}
		results = append(results, EffectiveConfigItemSearchResult{
			Namespace:        row.Namespace,
			Environment:      row.Environment,
			EffectiveVersion: intPtrOrNil(row.EffectiveVersion),
			EffectiveItem:    item,
		})
	}
	return EffectiveConfigItemSearchResponse{
		Name:         name,
		MatchedCount: len(results),
		Results:      results,
	}
}
