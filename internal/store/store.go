// Package store owns the SQLite file and every write the service performs.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite handle so callers never touch database/sql directly.
type Store struct {
	db *sql.DB
}

// Open prepares the database file and the schema this service needs.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable wal: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Ping reports whether the storage layer is usable.
func (s *Store) Ping() error { return s.db.Ping() }

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Version is the stored metadata of one published configuration snapshot.
type Version struct {
	Namespace   string         `json:"namespace"`
	Environment string         `json:"environment"`
	Version     int64          `json:"version"`
	GrayTag     sql.NullString `json:"-"`
	RollbackOf  sql.NullInt64  `json:"-"`
	PromotionOf sql.NullInt64  `json:"-"`
	CreatedAt   string         `json:"createdAt"`
}

// EffectiveGrayTag returns the gray tag carried by a version, or "" for a full release.
func (v Version) EffectiveGrayTag() string {
	if v.GrayTag.Valid {
		return v.GrayTag.String
	}
	return ""
}

// RollbackSource returns the version this one was rolled back from, or 0 when it is a new release.
func (v Version) RollbackSource() int64 {
	if v.RollbackOf.Valid {
		return v.RollbackOf.Int64
	}
	return 0
}

// PromotionSource returns the gray version promoted into this one, or 0 when it is not a promotion.
func (v Version) PromotionSource() int64 {
	if v.PromotionOf.Valid {
		return v.PromotionOf.Int64
	}
	return 0
}

// ErrNotFound reports that no stored version matched the lookup.
var ErrNotFound = errors.New("version not found")

// ErrNotGray reports that the source version of a promotion carried no gray tag.
var ErrNotGray = errors.New("version is not a gray release")

// PublishInput describes one new release. Items map item name to its raw JSON value.
type PublishInput struct {
	Namespace   string
	Environment string
	GrayTag     string // empty means a full release
	Items       map[string]json.RawMessage
}

// Publish stores a new snapshot as the next version inside a serialized write transaction.
// A full release becomes the effective snapshot; a gray release keeps the current one effective.
func (s *Store) Publish(ctx context.Context, input PublishInput) (Version, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Version{}, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return Version{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()

	var next int64
	row := conn.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) + 1 FROM config_versions WHERE namespace = ? AND environment = ?",
		input.Namespace, input.Environment)
	if err := row.Scan(&next); err != nil {
		return Version{}, fmt.Errorf("next version: %w", err)
	}
	createdAt, err := storeItems(ctx, conn, input.Namespace, input.Environment, next, input.GrayTag, input.Items)
	if err != nil {
		return Version{}, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Version{}, fmt.Errorf("commit: %w", err)
	}
	return Version{
		Namespace:   input.Namespace,
		Environment: input.Environment,
		Version:     next,
		GrayTag:     nullString(input.GrayTag),
		CreatedAt:   createdAt,
	}, nil
}

// Rollback copies an existing snapshot into a new version and records the source version.
func (s *Store) Rollback(ctx context.Context, namespace, environment string, sourceVersion int64) (Version, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Version{}, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close()

	var grayTag sql.NullString
	var sourceRollbackOf sql.NullInt64
	row := conn.QueryRowContext(ctx,
		"SELECT gray_tag, rollback_of FROM config_versions WHERE namespace = ? AND environment = ? AND version = ?",
		namespace, environment, sourceVersion)
	switch err := row.Scan(&grayTag, &sourceRollbackOf); {
	case errors.Is(err, sql.ErrNoRows):
		return Version{}, ErrNotFound
	case err != nil:
		return Version{}, fmt.Errorf("load source: %w", err)
	}

	items, err := loadItems(ctx, conn, namespace, environment, sourceVersion)
	if err != nil {
		return Version{}, err
	}

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return Version{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()

	var next int64
	row = conn.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) + 1 FROM config_versions WHERE namespace = ? AND environment = ?",
		namespace, environment)
	if err := row.Scan(&next); err != nil {
		return Version{}, fmt.Errorf("next version: %w", err)
	}
	tag := ""
	if grayTag.Valid {
		tag = grayTag.String
	}
	createdAt, err := storeItems(ctx, conn, namespace, environment, next, tag, items)
	if err != nil {
		return Version{}, err
	}
	if _, err := conn.ExecContext(ctx,
		"UPDATE config_versions SET rollback_of = ? WHERE namespace = ? AND environment = ? AND version = ?",
		sourceVersion, namespace, environment, next); err != nil {
		return Version{}, fmt.Errorf("record rollback: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Version{}, fmt.Errorf("commit: %w", err)
	}
	return Version{
		Namespace:   namespace,
		Environment: environment,
		Version:     next,
		GrayTag:     grayTag,
		RollbackOf:  sql.NullInt64{Int64: sourceVersion, Valid: true},
		CreatedAt:   createdAt,
	}, nil
}

// Promote copies a gray snapshot into a new full-release version inside one serialized write
// transaction. The copy carries no gray tag, becomes effective immediately and records the source
// version in promotion_of. The source version and every other historical version stay untouched.
func (s *Store) Promote(ctx context.Context, namespace, environment string, sourceVersion int64) (Version, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Version{}, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close()

	var grayTag sql.NullString
	row := conn.QueryRowContext(ctx,
		"SELECT gray_tag FROM config_versions WHERE namespace = ? AND environment = ? AND version = ?",
		namespace, environment, sourceVersion)
	switch err := row.Scan(&grayTag); {
	case errors.Is(err, sql.ErrNoRows):
		return Version{}, ErrNotFound
	case err != nil:
		return Version{}, fmt.Errorf("load source: %w", err)
	}
	if !grayTag.Valid {
		return Version{}, ErrNotGray
	}

	items, err := loadItems(ctx, conn, namespace, environment, sourceVersion)
	if err != nil {
		return Version{}, err
	}

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return Version{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()

	var next int64
	row = conn.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) + 1 FROM config_versions WHERE namespace = ? AND environment = ?",
		namespace, environment)
	if err := row.Scan(&next); err != nil {
		return Version{}, fmt.Errorf("next version: %w", err)
	}
	createdAt, err := storeItems(ctx, conn, namespace, environment, next, "", items)
	if err != nil {
		return Version{}, err
	}
	if _, err := conn.ExecContext(ctx,
		"UPDATE config_versions SET promotion_of = ? WHERE namespace = ? AND environment = ? AND version = ?",
		sourceVersion, namespace, environment, next); err != nil {
		return Version{}, fmt.Errorf("record promotion: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Version{}, fmt.Errorf("commit: %w", err)
	}
	return Version{
		Namespace:   namespace,
		Environment: environment,
		Version:     next,
		PromotionOf: sql.NullInt64{Int64: sourceVersion, Valid: true},
		CreatedAt:   createdAt,
	}, nil
}

// FindVersion loads one version regardless of namespace or environment.
func (s *Store) FindVersion(ctx context.Context, version int64) (Version, error) {
	return s.queryVersion(ctx,
		`SELECT namespace, environment, version, gray_tag, rollback_of, promotion_of, created_at
		 FROM config_versions WHERE version = ?`, version)
}

// GetVersion loads one version in a specific namespace and environment.
func (s *Store) GetVersion(ctx context.Context, namespace, environment string, version int64) (Version, error) {
	return s.queryVersion(ctx,
		`SELECT namespace, environment, version, gray_tag, rollback_of, promotion_of, created_at
		 FROM config_versions WHERE namespace = ? AND environment = ? AND version = ?`,
		namespace, environment, version)
}

// VersionExistsAnywhere reports whether a version with this number exists in any scope. It lets
// handlers distinguish "unknown version" from "version belongs to another scope" even though
// version numbers are allocated independently per scope.
func (s *Store) VersionExistsAnywhere(ctx context.Context, version int64) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM config_versions WHERE version = ?", version).Scan(&count); err != nil {
		return false, fmt.Errorf("check version existence: %w", err)
	}
	return count > 0, nil
}

// ListVersions returns every version of a scope ordered by version ascending.
func (s *Store) ListVersions(ctx context.Context, namespace, environment string) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT namespace, environment, version, gray_tag, rollback_of, promotion_of, created_at
		 FROM config_versions WHERE namespace = ? AND environment = ? ORDER BY version ASC`,
		namespace, environment)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer rows.Close()
	var versions []Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

// Items loads the configuration items carried by a version keyed by item name.
func (s *Store) Items(ctx context.Context, namespace, environment string, version int64) (map[string]json.RawMessage, error) {
	items, err := loadItems(ctx, s.db, namespace, environment, version)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = map[string]json.RawMessage{}
	}
	return items, nil
}

// ItemVersionValue pairs one stored version with whether a specific item existed in that
// version. Value holds the canonical JSON when HasValue is true.
type ItemVersionValue struct {
	Version  Version
	HasValue bool
	Value    string
}

// ItemHistory returns every version of a scope paired with the state of one named item in it,
// ordered by version ascending. Versions without the item are still returned (HasValue is false),
// so callers can detect removals. It is a single read-only query: gray releases and rollback
// copies participate exactly like full releases.
func (s *Store) ItemHistory(ctx context.Context, namespace, environment, name string) ([]ItemVersionValue, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT v.namespace, v.environment, v.version, v.gray_tag, v.rollback_of, v.promotion_of, v.created_at, i.value_json
		 FROM config_versions AS v
		 LEFT JOIN config_items AS i
		   ON i.namespace = v.namespace AND i.environment = v.environment
		  AND i.version = v.version AND i.name = ?
		 WHERE v.namespace = ? AND v.environment = ?
		 ORDER BY v.version ASC`,
		name, namespace, environment)
	if err != nil {
		return nil, fmt.Errorf("item history: %w", err)
	}
	defer rows.Close()
	history := []ItemVersionValue{}
	for rows.Next() {
		var entry ItemVersionValue
		var value sql.NullString
		if err := rows.Scan(&entry.Version.Namespace, &entry.Version.Environment, &entry.Version.Version,
			&entry.Version.GrayTag, &entry.Version.RollbackOf, &entry.Version.PromotionOf, &entry.Version.CreatedAt, &value); err != nil {
			return nil, fmt.Errorf("scan item history: %w", err)
		}
		entry.HasValue = value.Valid
		entry.Value = value.String
		history = append(history, entry)
	}
	return history, rows.Err()
}

// EffectiveItemSearchRow pairs one stored scope with the effective state of a searched item.
// EffectiveVersion is 0 when the scope only has gray releases. HasValue reports whether the
// item exists in the effective snapshot; Value holds its canonical JSON when it does.
type EffectiveItemSearchRow struct {
	Namespace        string
	Environment      string
	EffectiveVersion int64
	HasValue         bool
	Value            string
}

// SearchEffectiveItems lists every namespace and environment combination that has at least one
// stored version, including scopes with only gray releases, paired with the effective state of
// one named item. Empty namespace or environment filters mean no restriction. Rows are ordered
// by namespace then environment. It is a single read-only query that writes nothing.
func (s *Store) SearchEffectiveItems(ctx context.Context, namespace, environment, name string) ([]EffectiveItemSearchRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.namespace, s.environment, COALESCE(s.effective_version, 0), i.value_json
		 FROM (
		   SELECT namespace, environment,
		          MAX(CASE WHEN gray_tag IS NULL THEN version END) AS effective_version
		   FROM config_versions
		   GROUP BY namespace, environment
		 ) AS s
		 LEFT JOIN config_items AS i
		   ON i.namespace = s.namespace AND i.environment = s.environment
		  AND i.version = s.effective_version AND i.name = ?
		 WHERE (? = '' OR s.namespace = ?) AND (? = '' OR s.environment = ?)
		 ORDER BY s.namespace ASC, s.environment ASC`,
		name, namespace, namespace, environment, environment)
	if err != nil {
		return nil, fmt.Errorf("search effective items: %w", err)
	}
	defer rows.Close()
	matches := []EffectiveItemSearchRow{}
	for rows.Next() {
		var row EffectiveItemSearchRow
		var value sql.NullString
		if err := rows.Scan(&row.Namespace, &row.Environment, &row.EffectiveVersion, &value); err != nil {
			return nil, fmt.Errorf("scan effective item search: %w", err)
		}
		row.HasValue = value.Valid
		row.Value = value.String
		matches = append(matches, row)
	}
	return matches, rows.Err()
}

// EffectiveVersion returns the latest full-release version of a scope. Gray releases never
// become effective. 0 means no effective snapshot exists.
func (s *Store) EffectiveVersion(ctx context.Context, namespace, environment string) (int64, error) {
	var version int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM config_versions
		 WHERE namespace = ? AND environment = ? AND gray_tag IS NULL`,
		namespace, environment).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("effective version: %w", err)
	}
	return version, nil
}

func (s *Store) queryVersion(ctx context.Context, query string, args ...any) (Version, error) {
	row := s.db.QueryRowContext(ctx, query, args...)
	v, err := scanVersion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	return v, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanVersion(scanner rowScanner) (Version, error) {
	var v Version
	if err := scanner.Scan(&v.Namespace, &v.Environment, &v.Version, &v.GrayTag, &v.RollbackOf, &v.PromotionOf, &v.CreatedAt); err != nil {
		return Version{}, fmt.Errorf("scan version: %w", err)
	}
	return v, nil
}

func storeItems(ctx context.Context, conn *sql.Conn, namespace, environment string, version int64, grayTag string,
	items map[string]json.RawMessage) (string, error) {
	createdAt := nowUTC()
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO config_versions (namespace, environment, version, gray_tag, rollback_of, created_at)
		 VALUES (?, ?, ?, ?, NULL, ?)`,
		namespace, environment, version, nullString(grayTag), createdAt); err != nil {
		return "", fmt.Errorf("insert version: %w", err)
	}
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value, err := CanonicalJSON(items[name])
		if err != nil {
			return "", fmt.Errorf("canonical value of %q: %w", name, err)
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO config_items (namespace, environment, version, name, value_json)
			 VALUES (?, ?, ?, ?, ?)`,
			namespace, environment, version, name, string(value)); err != nil {
			return "", fmt.Errorf("insert item: %w", err)
		}
	}
	return createdAt, nil
}

type itemQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func loadItems(ctx context.Context, querier itemQuerier, namespace, environment string, version int64) (map[string]json.RawMessage, error) {
	rows, err := querier.QueryContext(ctx,
		`SELECT name, value_json FROM config_items
		 WHERE namespace = ? AND environment = ? AND version = ? ORDER BY name ASC`,
		namespace, environment, version)
	if err != nil {
		return nil, fmt.Errorf("load items: %w", err)
	}
	defer rows.Close()
	items := map[string]json.RawMessage{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, fmt.Errorf("scan item: %w", err)
		}
		items[name] = json.RawMessage(value)
	}
	return items, rows.Err()
}

func nullString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

// CanonicalJSON re-encodes raw JSON so whitespace and object key order never cause false diffs,
// while number, boolean, null and string semantics are preserved.
func CanonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytesReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

// migrate upgrades SQLite files created before promotion_of existed. Old files keep every row as
// written; ALTER TABLE cannot attach a composite foreign key, so the added column stays untyped in
// legacy databases while fresh databases create it with the key via the schema.
func migrate(db *sql.DB) error {
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(1) FROM pragma_table_info('config_versions') WHERE name = 'promotion_of'",
	).Scan(&count); err != nil {
		return fmt.Errorf("check schema: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec("ALTER TABLE config_versions ADD COLUMN promotion_of INTEGER"); err != nil {
			return fmt.Errorf("add promotion_of column: %w", err)
		}
	}
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS service_metadata (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS config_versions (
	namespace   TEXT NOT NULL,
	environment TEXT NOT NULL,
	version     INTEGER NOT NULL,
	gray_tag    TEXT,
	rollback_of  INTEGER,
	promotion_of INTEGER,
	created_at   TEXT NOT NULL,
	PRIMARY KEY (namespace, environment, version),
	FOREIGN KEY (namespace, environment, rollback_of)
		REFERENCES config_versions(namespace, environment, version),
	FOREIGN KEY (namespace, environment, promotion_of)
		REFERENCES config_versions(namespace, environment, version)
);

CREATE TABLE IF NOT EXISTS config_items (
	namespace   TEXT NOT NULL,
	environment TEXT NOT NULL,
	version     INTEGER NOT NULL,
	name        TEXT NOT NULL,
	value_json  TEXT NOT NULL,
	PRIMARY KEY (namespace, environment, version, name),
	FOREIGN KEY (namespace, environment, version)
		REFERENCES config_versions(namespace, environment, version)
);

CREATE INDEX IF NOT EXISTS idx_config_versions_global ON config_versions(version);
`
