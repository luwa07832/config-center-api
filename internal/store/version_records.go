package store

import (
	"context"
	"fmt"
)

// VersionRecordFilter narrows a version-record listing inside one scope. The zero value applies
// no optional filter. HasGrayTag, HasRollbackOf and HasEffective mark the presence of each
// predicate so that, for example, rollbackOf = 0 never leaks into an unfiltered query.
type VersionRecordFilter struct {
	GrayTag       string
	HasGrayTag    bool
	RollbackOf    int64
	HasRollbackOf bool
	Effective     bool
	HasEffective  bool
}

// applyVersionRecordFilter builds the WHERE clause and arguments shared by the count and page
// queries. Namespace and environment always bound the scope. The effective predicates compare
// against the scope's latest full release inside a scalar subquery: an effective=true match is
// impossible without a full release, while effective=false then matches every version.
func applyVersionRecordFilter(namespace, environment string, filter VersionRecordFilter) (string, []any) {
	clause := "namespace = ? AND environment = ?"
	args := []any{namespace, environment}
	if filter.HasGrayTag {
		clause += " AND gray_tag = ?"
		args = append(args, filter.GrayTag)
	}
	if filter.HasRollbackOf {
		clause += " AND rollback_of = ?"
		args = append(args, filter.RollbackOf)
	}
	if filter.HasEffective {
		if filter.Effective {
			clause += ` AND version = (
				SELECT MAX(version) FROM config_versions
				WHERE namespace = ? AND environment = ? AND gray_tag IS NULL)`
		} else {
			clause += ` AND version <> (
				SELECT COALESCE(MAX(version), 0) FROM config_versions
				WHERE namespace = ? AND environment = ? AND gray_tag IS NULL)`
		}
		args = append(args, namespace, environment)
	}
	return clause, args
}

// ListVersionRecords returns one ascending keyset page of the versions of a scope that match the
// filter, with version numbers greater than afterVersion, along with the total matched count. It
// is a pure read: no version is created and no stored record changes.
func (s *Store) ListVersionRecords(ctx context.Context, namespace, environment string,
	filter VersionRecordFilter, afterVersion int64, limit int) (VersionPage, error) {
	page := VersionPage{Versions: []Version{}}
	clause, args := applyVersionRecordFilter(namespace, environment, filter)

	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM config_versions WHERE "+clause, args...).Scan(&page.Total); err != nil {
		return VersionPage{}, fmt.Errorf("count version records: %w", err)
	}

	queryArgs := append(append([]any{}, args...), afterVersion, limit+1)
	rows, err := s.db.QueryContext(ctx,
		`SELECT namespace, environment, version, gray_tag, rollback_of, promotion_of, created_at
		 FROM config_versions WHERE `+clause+` AND version > ?
		 ORDER BY version ASC LIMIT ?`,
		queryArgs...)
	if err != nil {
		return VersionPage{}, fmt.Errorf("list version records: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		version, scanErr := scanVersion(rows)
		if scanErr != nil {
			return VersionPage{}, scanErr
		}
		page.Versions = append(page.Versions, version)
	}
	if err := rows.Err(); err != nil {
		return VersionPage{}, err
	}
	if len(page.Versions) > limit {
		page.HasMore = true
		page.Versions = page.Versions[:limit]
	}
	return page, nil
}
