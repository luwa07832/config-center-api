package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// VersionRecordFilter narrows the version record listing within one scope. GrayTag only
// participates when HasGrayTag is true and matches non-null tags exactly. RollbackOf only
// participates when HasRollbackOf is true. Effective only participates when HasEffective is
// true, selecting the current effective version or every version other than it.
type VersionRecordFilter struct {
	Namespace     string
	Environment   string
	GrayTag       string
	HasGrayTag    bool
	RollbackOf    int64
	HasRollbackOf bool
	Effective     bool
	HasEffective  bool
}

// VersionRecord is one stored version together with its complete snapshot. A version without
// items carries an empty (never nil) map.
type VersionRecord struct {
	Version
	Items map[string]json.RawMessage
}

// VersionRecordPage carries one keyset page of filtered version records plus the total count
// of versions matching the filter within the scope.
type VersionRecordPage struct {
	Records []VersionRecord
	Total   int64
	HasMore bool
}

// applyVersionRecordFilter builds the WHERE clause shared by the count and list queries. The
// current effective version is the latest full release of the scope, evaluated inside the
// database so the effective predicate composes with the other filters as one intersection.
func applyVersionRecordFilter(filter VersionRecordFilter) (clause string, args []any) {
	clauses := []string{`namespace = ?`, `environment = ?`}
	args = append(args, filter.Namespace, filter.Environment)
	if filter.HasGrayTag {
		clauses = append(clauses, `gray_tag = ?`)
		args = append(args, filter.GrayTag)
	}
	if filter.HasRollbackOf {
		clauses = append(clauses, `rollback_of = ?`)
		args = append(args, filter.RollbackOf)
	}
	if filter.HasEffective {
		effective := `(SELECT COALESCE(MAX(version), 0) FROM config_versions
		              WHERE namespace = ? AND environment = ? AND gray_tag IS NULL)`
		if filter.Effective {
			clauses = append(clauses, `version = `+effective)
		} else {
			clauses = append(clauses, `version <> `+effective)
		}
		args = append(args, filter.Namespace, filter.Environment)
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// ListVersionRecords returns up to limit versions matching the filter with version numbers
// strictly greater than afterVersion, ordered by version ascending, along with the total count
// of matching versions in the scope. Each returned version carries its complete snapshot. The
// read is a pure query: it creates no row and changes no stored record.
func (s *Store) ListVersionRecords(ctx context.Context, filter VersionRecordFilter,
	afterVersion int64, limit int) (VersionRecordPage, error) {
	page := VersionRecordPage{Records: []VersionRecord{}}
	whereClause, filterArgs := applyVersionRecordFilter(filter)

	countQuery := "SELECT COUNT(*) FROM config_versions" + whereClause
	if err := s.db.QueryRowContext(ctx, countQuery, filterArgs...).Scan(&page.Total); err != nil {
		return VersionRecordPage{}, fmt.Errorf("count version records: %w", err)
	}

	listArgs := append(append([]any{}, filterArgs...), afterVersion, limit+1)
	listQuery := `SELECT namespace, environment, version, gray_tag, rollback_of, promotion_of, created_at
	              FROM config_versions` + whereClause + `
	              AND version > ? ORDER BY version ASC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return VersionRecordPage{}, fmt.Errorf("list version records: %w", err)
	}
	defer rows.Close()
	versions := []Version{}
	for rows.Next() {
		v, scanErr := scanVersion(rows)
		if scanErr != nil {
			return VersionRecordPage{}, scanErr
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		return VersionRecordPage{}, fmt.Errorf("iterate version records: %w", err)
	}
	if len(versions) > limit {
		page.HasMore = true
		versions = versions[:limit]
	}

	itemsByVersion, err := s.loadRecordItems(ctx, filter, versions)
	if err != nil {
		return VersionRecordPage{}, err
	}
	page.Records = make([]VersionRecord, 0, len(versions))
	for _, v := range versions {
		items := itemsByVersion[v.Version]
		if items == nil {
			items = map[string]json.RawMessage{}
		}
		page.Records = append(page.Records, VersionRecord{Version: v, Items: items})
	}
	return page, nil
}

// loadRecordItems returns the snapshot items of every listed version in one query. Items of
// versions without rows are absent from the map so callers can substitute an empty object.
func (s *Store) loadRecordItems(ctx context.Context, filter VersionRecordFilter,
	versions []Version) (map[int64]map[string]json.RawMessage, error) {
	itemsByVersion := map[int64]map[string]json.RawMessage{}
	if len(versions) == 0 {
		return itemsByVersion, nil
	}
	placeholders := strings.Repeat("?,", len(versions))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(versions)+2)
	args = append(args, filter.Namespace, filter.Environment)
	for _, v := range versions {
		args = append(args, v.Version)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT version, name, value_json FROM config_items
		 WHERE namespace = ? AND environment = ? AND version IN (`+placeholders+`)
		 ORDER BY version ASC, name ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("load version record items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var version int64
		var name, value string
		if err := rows.Scan(&version, &name, &value); err != nil {
			return nil, fmt.Errorf("scan version record item: %w", err)
		}
		items := itemsByVersion[version]
		if items == nil {
			items = map[string]json.RawMessage{}
			itemsByVersion[version] = items
		}
		items[name] = json.RawMessage(value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate version record items: %w", err)
	}
	return itemsByVersion, nil
}
