package store

import (
	"context"
	"fmt"
)

// ScopeFilter narrows the scope listing. Empty namespace and environment values disable that
// filter. HasEffective only participates when HasEffectiveFilter is true, selecting scopes that
// respectively have, or have never had, a full release.
type ScopeFilter struct {
	Namespace          string
	Environment        string
	HasEffective       bool
	HasEffectiveFilter bool
}

// ScopeSummary aggregates the version counters of one namespace and environment. LatestVersion
// is 0 when the scope has no versions; such scopes are never listed because scopes are derived
// from stored versions. EffectiveVersion is 0 when no full release exists, and EffectiveItemCount
// only counts items of the effective snapshot in that case.
type ScopeSummary struct {
	Namespace             string
	Environment           string
	VersionCount          int64
	LatestVersion         int64
	Latest                Version
	EffectiveVersion      int64
	EffectiveItemCount    int64
	GrayVersionCount      int64
	RollbackVersionCount  int64
	PromotionVersionCount int64
}

// ScopePage carries one keyset page of scopes plus the total count matching the filter.
type ScopePage struct {
	Scopes  []ScopeSummary
	Total   int64
	HasMore bool
}

// scopeAggregateSQL groups every version by scope. The effective snapshot item count comes from
// a correlated scalar subquery, so version rows are never multiplied by item rows. The outer
// query is where the hasEffective predicate and cursor predicates apply.
const scopeAggregateSQL = `
SELECT v.namespace, v.environment,
       COUNT(*)                              AS version_count,
       MAX(v.version)                        AS latest_version,
       COALESCE(MAX(CASE WHEN v.gray_tag IS NULL THEN v.version END), 0) AS effective_version,
       COALESCE((
               SELECT COUNT(*)
               FROM config_items AS i
               JOIN (
                       SELECT namespace, environment, MAX(version) AS version
                       FROM config_versions
                       WHERE gray_tag IS NULL
                       GROUP BY namespace, environment
               ) AS e
                 ON i.namespace = e.namespace AND i.environment = e.environment
                AND i.version = e.version
               WHERE e.namespace = v.namespace AND e.environment = v.environment
       ), 0)                                  AS effective_item_count,
       SUM(CASE WHEN v.gray_tag IS NOT NULL THEN 1 ELSE 0 END) AS gray_version_count,
       SUM(CASE WHEN v.rollback_of IS NOT NULL THEN 1 ELSE 0 END) AS rollback_version_count,
       SUM(CASE WHEN v.promotion_of IS NOT NULL THEN 1 ELSE 0 END) AS promotion_version_count
FROM config_versions AS v
WHERE 1 = 1`

// scopeLatestJoinSQL attaches the metadata of each scope's highest-versioned snapshot to an
// aggregated scope row. %s holds the inner scope-filter clauses, which must live inside the
// grouped subquery where the version table alias is visible.
const scopeLatestJoinSQL = `
SELECT s.namespace, s.environment, s.version_count, s.latest_version,
       lv.gray_tag, lv.rollback_of, lv.promotion_of, lv.created_at,
       s.effective_version, s.effective_item_count,
       s.gray_version_count, s.rollback_version_count, s.promotion_version_count
FROM (` + scopeAggregateSQL + `%s
GROUP BY v.namespace, v.environment) AS s
JOIN config_versions AS lv
  ON lv.namespace = s.namespace AND lv.environment = s.environment
 AND lv.version = s.latest_version`

// scopeCountSQL counts the grouped scopes. The inner filter clauses live inside the subquery.
const scopeCountSQL = `SELECT COUNT(*) FROM (` + scopeAggregateSQL + `%s
GROUP BY v.namespace, v.environment) AS s`

func applyScopeFilter(filter ScopeFilter) (innerClause string, innerArgs []any, outerClause string, outerArgs []any) {
	if filter.Namespace != "" {
		innerClause += ` AND v.namespace = ?`
		innerArgs = append(innerArgs, filter.Namespace)
	}
	if filter.Environment != "" {
		innerClause += ` AND v.environment = ?`
		innerArgs = append(innerArgs, filter.Environment)
	}
	if filter.HasEffectiveFilter {
		if filter.HasEffective {
			outerClause += ` WHERE s.effective_version > 0`
		} else {
			outerClause += ` WHERE s.effective_version = 0`
		}
	}
	return innerClause, innerArgs, outerClause, outerArgs
}

// ListScopes returns one page of scopes that own at least one version, ordered by namespace then
// environment ascending, along with the total count matching the filter. AfterNamespace and
// afterEnvironment must be supplied together and select combinations strictly after that cursor.
// The read is a pure query: it creates no row and changes no stored record.
func (s *Store) ListScopes(ctx context.Context, filter ScopeFilter,
	afterNamespace, afterEnvironment string, limit int) (ScopePage, error) {
	page := ScopePage{Scopes: []ScopeSummary{}}
	innerClause, innerArgs, outerClause, outerArgs := applyScopeFilter(filter)

	countQuery := fmt.Sprintf(scopeCountSQL, innerClause) + outerClause
	countArgs := append(append([]any{}, innerArgs...), outerArgs...)
	if err := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&page.Total); err != nil {
		return ScopePage{}, fmt.Errorf("count scopes: %w", err)
	}

	cursorClause := ""
	queryArgs := append(append([]any{}, innerArgs...), outerArgs...)
	if afterNamespace != "" || afterEnvironment != "" {
		cursorClause = ` WHERE (s.namespace > ? OR (s.namespace = ? AND s.environment > ?))`
		if filter.HasEffectiveFilter {
			cursorClause = ` AND (s.namespace > ? OR (s.namespace = ? AND s.environment > ?))`
		}
		queryArgs = append(queryArgs, afterNamespace, afterNamespace, afterEnvironment)
	}
	query := fmt.Sprintf(scopeLatestJoinSQL, innerClause) + outerClause + cursorClause +
		` ORDER BY s.namespace ASC, s.environment ASC LIMIT ?`
	queryArgs = append(queryArgs, limit+1)

	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return ScopePage{}, fmt.Errorf("list scopes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var scope ScopeSummary
		if err := rows.Scan(
			&scope.Namespace, &scope.Environment, &scope.VersionCount, &scope.LatestVersion,
			&scope.Latest.GrayTag, &scope.Latest.RollbackOf, &scope.Latest.PromotionOf, &scope.Latest.CreatedAt,
			&scope.EffectiveVersion, &scope.EffectiveItemCount,
			&scope.GrayVersionCount, &scope.RollbackVersionCount, &scope.PromotionVersionCount,
		); err != nil {
			return ScopePage{}, fmt.Errorf("scan scope: %w", err)
		}
		scope.Latest.Namespace = scope.Namespace
		scope.Latest.Environment = scope.Environment
		scope.Latest.Version = scope.LatestVersion
		page.Scopes = append(page.Scopes, scope)
	}
	if err := rows.Err(); err != nil {
		return ScopePage{}, fmt.Errorf("iterate scopes: %w", err)
	}
	if len(page.Scopes) > limit {
		page.HasMore = true
		page.Scopes = page.Scopes[:limit]
	}
	return page, nil
}
