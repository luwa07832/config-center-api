package store

import (
	"context"
	"fmt"
	"strings"
)

// ConfigScope summarizes version history of one namespace and environment pair that owns at
// least one version. LatestVersion carries the metadata of the highest version number.
// EffectiveVersion is 0 when the scope has never had a full release; EffectiveItemCount then is 0
// as well. The remaining counters count historical versions carrying a gray tag, a rollbackOf
// source or a promotionOf source respectively.
type ConfigScope struct {
	Namespace             string
	Environment           string
	VersionCount          int64
	LatestVersion         Version
	EffectiveVersion      int64
	EffectiveItemCount    int64
	GrayVersionCount      int64
	RollbackVersionCount  int64
	PromotionVersionCount int64
}

// ConfigScopeFilter narrows a scope listing. Empty Namespace or Environment skips that filter.
// HasEffective is nil when absent, otherwise scopes must have (true) or lack (false) a full
// effective release. A non-empty AfterNamespace/AfterEnvironment pair forms the keyset cursor.
type ConfigScopeFilter struct {
	Namespace        string
	Environment      string
	HasEffective     *bool
	AfterNamespace   string
	AfterEnvironment string
	Limit            int
}

// ConfigScopePage carries one keyset page of scopes plus the total number of scopes that match
// the filter independent of the cursor.
type ConfigScopePage struct {
	Scopes       []ConfigScope
	MatchedCount int64
	HasMore      bool
}

// scopeStatsCTE aggregates per-scope counters. Gray releases keep gray_tag non-null, so the
// effective version is the highest version with gray_tag IS NULL.
const scopeStatsCTE = `
WITH scope_stats AS (
	SELECT namespace, environment,
		COUNT(*) AS version_count,
		MAX(version) AS latest_version,
		COALESCE(MAX(CASE WHEN gray_tag IS NULL THEN version END), 0) AS effective_version,
		SUM(CASE WHEN gray_tag IS NOT NULL THEN 1 ELSE 0 END) AS gray_version_count,
		SUM(CASE WHEN rollback_of IS NOT NULL THEN 1 ELSE 0 END) AS rollback_version_count,
		SUM(CASE WHEN promotion_of IS NOT NULL THEN 1 ELSE 0 END) AS promotion_version_count
	FROM config_versions
	GROUP BY namespace, environment
)`

// ListConfigScopes returns one keyset page of scopes ordered by namespace then environment
// ascending. It is a pure read: it creates no version and changes no effective state.
func (s *Store) ListConfigScopes(ctx context.Context, filter ConfigScopeFilter) (ConfigScopePage, error) {
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if filter.Namespace != "" {
		conditions = append(conditions, "s.namespace = ?")
		args = append(args, filter.Namespace)
	}
	if filter.Environment != "" {
		conditions = append(conditions, "s.environment = ?")
		args = append(args, filter.Environment)
	}
	if filter.HasEffective != nil {
		if *filter.HasEffective {
			conditions = append(conditions, "s.effective_version > 0")
		} else {
			conditions = append(conditions, "s.effective_version = 0")
		}
	}
	where := ""
	if len(conditions) > 0 {
		where = " AND " + strings.Join(conditions, " AND ")
	}

	var matched int64
	countQuery := scopeStatsCTE + `
SELECT COUNT(1) FROM scope_stats AS s WHERE 1 = 1` + where
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&matched); err != nil {
		return ConfigScopePage{}, fmt.Errorf("count config scopes: %w", err)
	}

	pageArgs := make([]any, 0, len(args)+4)
	pageArgs = append(pageArgs, args...)
	pageWhere := where
	if filter.AfterNamespace != "" && filter.AfterEnvironment != "" {
		pageWhere += ` AND (s.namespace > ? OR (s.namespace = ? AND s.environment > ?))`
		pageArgs = append(pageArgs, filter.AfterNamespace, filter.AfterNamespace, filter.AfterEnvironment)
	}
	pageArgs = append(pageArgs, filter.Limit+1)
	pageQuery := scopeStatsCTE + `
SELECT s.namespace, s.environment, s.version_count, s.effective_version,
	(SELECT COUNT(1) FROM config_items AS i
	  WHERE i.namespace = s.namespace AND i.environment = s.environment
		AND i.version = s.effective_version) AS effective_item_count,
	s.gray_version_count, s.rollback_version_count, s.promotion_version_count,
	v.version, v.gray_tag, v.rollback_of, v.promotion_of, v.created_at
FROM scope_stats AS s
JOIN config_versions AS v
  ON v.namespace = s.namespace AND v.environment = s.environment
 AND v.version = s.latest_version
WHERE 1 = 1` + pageWhere + `
ORDER BY s.namespace ASC, s.environment ASC
LIMIT ?`
	rows, err := s.db.QueryContext(ctx, pageQuery, pageArgs...)
	if err != nil {
		return ConfigScopePage{}, fmt.Errorf("list config scopes: %w", err)
	}
	defer rows.Close()

	page := ConfigScopePage{Scopes: []ConfigScope{}, MatchedCount: matched}
	for rows.Next() {
		var scope ConfigScope
		if err := rows.Scan(
			&scope.Namespace, &scope.Environment, &scope.VersionCount,
			&scope.EffectiveVersion, &scope.EffectiveItemCount,
			&scope.GrayVersionCount, &scope.RollbackVersionCount, &scope.PromotionVersionCount,
			&scope.LatestVersion.Version, &scope.LatestVersion.GrayTag,
			&scope.LatestVersion.RollbackOf, &scope.LatestVersion.PromotionOf,
			&scope.LatestVersion.CreatedAt,
		); err != nil {
			return ConfigScopePage{}, fmt.Errorf("scan config scope: %w", err)
		}
		scope.LatestVersion.Namespace = scope.Namespace
		scope.LatestVersion.Environment = scope.Environment
		page.Scopes = append(page.Scopes, scope)
	}
	if err := rows.Err(); err != nil {
		return ConfigScopePage{}, fmt.Errorf("iterate config scopes: %w", err)
	}
	if len(page.Scopes) > filter.Limit {
		page.HasMore = true
		page.Scopes = page.Scopes[:filter.Limit]
	}
	return page, nil
}
