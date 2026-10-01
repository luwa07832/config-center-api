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
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite writes are serialized by BEGIN IMMEDIATE. A single connection makes that
	// serialization explicit so concurrent publish/rollback/promote calls queue and block
	// instead of racing for the write lock or observing SQLITE_BUSY.
	db.SetMaxOpenConns(1)
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
	if err := migrateSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
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

// PromotionSource returns the gray version this one was promoted from, or 0 for releases and rollbacks.
func (v Version) PromotionSource() int64 {
	if v.PromotionOf.Valid {
		return v.PromotionOf.Int64
	}
	return 0
}

// ErrNotFound reports that no stored version matched the lookup.
var ErrNotFound = errors.New("version not found")

// ErrScopeMismatch reports that a version exists in another namespace or environment.
var ErrScopeMismatch = errors.New("version belongs to another scope")

// ErrNotGray reports that promotion was requested for a version without a gray tag.
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
	createdAt, err := storeItems(ctx, conn, input.Namespace, input.Environment, next, input.GrayTag,
		sql.NullInt64{}, sql.NullInt64{}, input.Items)
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
	createdAt, err := storeItems(ctx, conn, namespace, environment, next, tag,
		sql.NullInt64{Int64: sourceVersion, Valid: true}, sql.NullInt64{}, items)
	if err != nil {
		return Version{}, err
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

// Promote copies a gray release into a new full-release version and records the source gray
// version in promotion_of. The new version carries no gray tag and becomes effective immediately.
// It returns ErrNotFound when the version exists in no scope, ErrScopeMismatch when it belongs to
// another namespace or environment, and ErrNotGray when the source version has no gray tag.
func (s *Store) Promote(ctx context.Context, namespace, environment string, sourceVersion int64) (Version, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Version{}, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return Version{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()

	var grayTag sql.NullString
	err = conn.QueryRowContext(ctx,
		"SELECT gray_tag FROM config_versions WHERE namespace = ? AND environment = ? AND version = ?",
		namespace, environment, sourceVersion).Scan(&grayTag)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var exists int
		if lookupErr := conn.QueryRowContext(ctx,
			"SELECT 1 FROM config_versions WHERE version = ? LIMIT 1", sourceVersion).Scan(&exists); lookupErr != nil {
			if errors.Is(lookupErr, sql.ErrNoRows) {
				return Version{}, ErrNotFound
			}
			return Version{}, fmt.Errorf("check version existence: %w", lookupErr)
		}
		return Version{}, ErrScopeMismatch
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

	var next int64
	if err := conn.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) + 1 FROM config_versions WHERE namespace = ? AND environment = ?",
		namespace, environment).Scan(&next); err != nil {
		return Version{}, fmt.Errorf("next version: %w", err)
	}
	createdAt, err := storeItems(ctx, conn, namespace, environment, next, "",
		sql.NullInt64{}, sql.NullInt64{Int64: sourceVersion, Valid: true}, items)
	if err != nil {
		return Version{}, err
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
			&entry.Version.GrayTag, &entry.Version.RollbackOf, &entry.Version.PromotionOf,
			&entry.Version.CreatedAt, &value); err != nil {
			return nil, fmt.Errorf("scan item history: %w", err)
		}
		entry.HasValue = value.Valid
		entry.Value = value.String
		history = append(history, entry)
	}
	return history, rows.Err()
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
	if err := scanner.Scan(&v.Namespace, &v.Environment, &v.Version, &v.GrayTag,
		&v.RollbackOf, &v.PromotionOf, &v.CreatedAt); err != nil {
		return Version{}, fmt.Errorf("scan version: %w", err)
	}
	return v, nil
}

func storeItems(ctx context.Context, conn *sql.Conn, namespace, environment string, version int64,
	grayTag string, rollbackOf, promotionOf sql.NullInt64, items map[string]json.RawMessage) (string, error) {
	createdAt := nowUTC()
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO config_versions (namespace, environment, version, gray_tag, rollback_of, promotion_of, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		namespace, environment, version, nullString(grayTag),
		nullableInt64(rollbackOf), nullableInt64(promotionOf), createdAt); err != nil {
		return "", fmt.Errorf("insert version: %w", err)
	}
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value, err := canonicalJSON(items[name])
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

// nullableInt64 converts a NullInt64 into a value/NULL bind argument.
func nullableInt64(value sql.NullInt64) any {
	if value.Valid {
		return value.Int64
	}
	return nil
}

// canonicalJSON re-encodes raw JSON so whitespace and object key order never cause false diffs,
// while number, boolean, null and string semantics are preserved.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
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
	rollback_of INTEGER,
	promotion_of INTEGER,
	created_at  TEXT NOT NULL,
	PRIMARY KEY (namespace, environment, version),
	FOREIGN KEY (namespace, environment, rollback_of)
		REFERENCES config_versions(namespace, environment, version)
	,
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

// migrateSchema upgrades databases created before promotion_of existed. The column is nullable,
// so every historical version reads promotionOf as null without rewriting existing rows.
func migrateSchema(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(config_versions)")
	if err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}
	hasPromotion := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("scan schema: %w", err)
		}
		if name == "promotion_of" {
			hasPromotion = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read schema: %w", err)
	}
	rows.Close()
	if !hasPromotion {
		if _, err := db.Exec("ALTER TABLE config_versions ADD COLUMN promotion_of INTEGER"); err != nil {
			return fmt.Errorf("add promotion_of: %w", err)
		}
	}
	return nil
}
