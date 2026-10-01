// Package store owns the SQLite file and every write the service performs.
package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

var (
	// ErrVersionNotFound reports that no version exists anywhere in the store with that number.
	ErrVersionNotFound = errors.New("version not found")
	// ErrVersionScopeMismatch reports that a version exists, but under another namespace/environment.
	ErrVersionScopeMismatch = errors.New("version belongs to another scope")
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
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable wal: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Ping reports whether the storage layer is usable.
func (s *Store) Ping() error { return s.db.Ping() }

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS service_metadata (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS config_versions (
	id                      INTEGER PRIMARY KEY AUTOINCREMENT,
	namespace               TEXT NOT NULL,
	environment             TEXT NOT NULL,
	version                 INTEGER NOT NULL,
	gray_label              TEXT,
	rollback_source_version INTEGER,
	created_at              TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
	UNIQUE(namespace, environment, version)
);

CREATE TABLE IF NOT EXISTS config_items (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	version_id INTEGER NOT NULL REFERENCES config_versions(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	value      TEXT NOT NULL,
	UNIQUE(version_id, name)
);

CREATE INDEX IF NOT EXISTS idx_config_items_version ON config_items(version_id);

CREATE TABLE IF NOT EXISTS active_versions (
	namespace   TEXT NOT NULL,
	environment TEXT NOT NULL,
	version     INTEGER NOT NULL,
	PRIMARY KEY(namespace, environment),
	FOREIGN KEY(namespace, environment, version)
		REFERENCES config_versions(namespace, environment, version)
);
`

// Version is the stored metadata for one published configuration version.
type Version struct {
	Namespace             string  `json:"namespace"`
	Environment           string  `json:"environment"`
	Version               int64   `json:"version"`
	GrayLabel             *string `json:"grayLabel"`
	RollbackSourceVersion *int64  `json:"rollbackSourceVersion"`
	CreatedAt             string  `json:"createdAt"`
	items                 map[string]json.RawMessage
}

// Item returns the raw JSON value stored for name, without re-encoding numbers, booleans or null.
func (v *Version) Item(name string) (json.RawMessage, bool) {
	value, ok := v.items[name]
	return value, ok
}

// ItemNames returns configuration item names in stable, name-sorted order.
func (v *Version) ItemNames() []string {
	names := make([]string, 0, len(v.items))
	for name := range v.items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// VersionInput carries the fields callers persist when publishing a configuration version.
type VersionInput struct {
	Namespace             string
	NamespaceName         string
	Environment           string
	Env                   string
	Version               int64
	VersionNumber         int64
	GrayLabel             *string
	CanaryLabel           *string
	RollbackSourceVersion *int64
	RollbackFromVersion   *int64
	Items                 map[string]json.RawMessage
	ConfigItems           map[string]json.RawMessage
}

// PublishVersion persists a version and its configuration items without changing active state.
func (s *Store) PublishVersion(input VersionInput) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin publish: %w", err)
	}
	defer tx.Rollback()

	if input.Namespace == "" {
		input.Namespace = input.NamespaceName
	}
	if input.Environment == "" {
		input.Environment = input.Env
	}
	if input.Version == 0 {
		input.Version = input.VersionNumber
	}
	if input.GrayLabel == nil {
		input.GrayLabel = input.CanaryLabel
	}
	if input.RollbackSourceVersion == nil {
		input.RollbackSourceVersion = input.RollbackFromVersion
	}
	if len(input.Items) == 0 {
		input.Items = input.ConfigItems
	}

	result, err := tx.Exec(
		`INSERT INTO config_versions(namespace, environment, version, gray_label, rollback_source_version)
		 VALUES (?, ?, ?, ?, ?)`,
		input.Namespace, input.Environment, input.Version, input.GrayLabel, input.RollbackSourceVersion,
	)
	if err != nil {
		return fmt.Errorf("insert version: %w", err)
	}
	versionID, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("version id: %w", err)
	}

	names := make([]string, 0, len(input.Items))
	for name := range input.Items {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value, err := compactJSON(input.Items[name])
		if err != nil {
			return fmt.Errorf("normalize value for %q: %w", name, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO config_items(version_id, name, value) VALUES (?, ?, ?)`,
			versionID, name, string(value),
		); err != nil {
			return fmt.Errorf("insert item %q: %w", name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit publish: %w", err)
	}
	return nil
}

// SetActiveVersion marks version as the effective version for a namespace/environment.
func (s *Store) SetActiveVersion(namespace, environment string, version int64) error {
	if _, err := s.db.Exec(
		`INSERT INTO active_versions(namespace, environment, version) VALUES (?, ?, ?)
		 ON CONFLICT(namespace, environment) DO UPDATE SET version = excluded.version`,
		namespace, environment, version,
	); err != nil {
		return fmt.Errorf("set active version: %w", err)
	}
	return nil
}

// GetVersion loads one version within the requested scope. When the version number exists only
// under another namespace/environment, ErrVersionScopeMismatch is returned.
func (s *Store) GetVersion(namespace, environment string, number int64) (*Version, error) {
	var existsElsewhere bool
	for _, table := range s.versionTables() {
		version, found, err := s.loadVersionFrom(table, namespace, environment, number)
		if err != nil {
			return nil, err
		}
		if found {
			return version, nil
		}
		if !s.tableExists(table) {
			continue
		}
		columns, err := s.tableColumns(table)
		if err != nil {
			return nil, err
		}
		ns := chooseColumn(columns, "namespace", "namespace_name", "ns")
		env := chooseColumn(columns, "environment", "environment_name", "env")
		ver := chooseColumn(columns, "version", "version_number")
		if ns == "" || env == "" || ver == "" {
			continue
		}
		var exists int
		err = s.db.QueryRow(
			fmt.Sprintf(`SELECT 1 FROM %s WHERE %s = ? AND NOT (%s = ? AND %s = ?) LIMIT 1`,
				quoteIdent(table), quoteIdent(ver), quoteIdent(ns), quoteIdent(env)),
			number, namespace, environment,
		).Scan(&exists)
		if err == nil {
			existsElsewhere = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("probe version scope: %w", err)
		}
	}

	if existsElsewhere {
		return nil, ErrVersionScopeMismatch
	}
	return nil, ErrVersionNotFound
}

type activeTable struct {
	name        string
	namespace   string
	environment string
	version     string
}

// ActiveVersion returns the currently effective version number, if a release has happened.
func (s *Store) ActiveVersion(namespace, environment string) (int64, bool, error) {
	tables := []activeTable{
		{name: "active_versions", namespace: "namespace", environment: "environment", version: "version"},
		{name: "active_configs", namespace: "namespace", environment: "environment", version: "version"},
		{name: "current_versions", namespace: "namespace", environment: "environment", version: "version"},
		{name: "effective_configs", namespace: "namespace", environment: "environment", version: "version"},
	}
	for _, table := range tables {
		if !s.tableExists(table.name) {
			continue
		}
		columns, err := s.tableColumns(table.name)
		if err != nil {
			return 0, false, err
		}
		ns := chooseColumn(columns, "namespace", "namespace_name", "ns")
		env := chooseColumn(columns, "environment", "environment_name", "env")
		ver := chooseColumn(columns, "version", "version_number", "active_version", "current_version")
		if ns == "" || env == "" || ver == "" {
			continue
		}
		var number int64
		err = s.db.QueryRow(
			fmt.Sprintf(`SELECT %s FROM %s WHERE %s = ? AND %s = ? ORDER BY %s DESC LIMIT 1`,
				quoteIdent(ver), quoteIdent(table.name), quoteIdent(ns), quoteIdent(env), quoteIdent(ver)),
			namespace, environment,
		).Scan(&number)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, false, fmt.Errorf("read active version: %w", err)
		}
		return number, true, nil
	}
	return 0, false, nil
}

func (s *Store) versionTables() []string {
	return []string{
		"config_versions",
		"configuration_versions",
		"versions",
		"config_releases",
		"releases",
	}
}

func (s *Store) loadVersionFrom(table, namespace, environment string, number int64) (*Version, bool, error) {
	if !s.tableExists(table) {
		return nil, false, nil
	}
	columns, err := s.tableColumns(table)
	if err != nil {
		return nil, false, err
	}
	ns := chooseColumn(columns, "namespace", "namespace_name", "ns")
	env := chooseColumn(columns, "environment", "environment_name", "env")
	ver := chooseColumn(columns, "version", "version_number")
	if ns == "" || env == "" || ver == "" {
		return nil, false, nil
	}
	gray := chooseColumn(columns, "gray_label", "graylabel", "canary_label", "canary_tag", "gray_tag")
	rollback := chooseColumn(columns, "rollback_source_version", "rollback_source", "rollback_version", "rollback_to_version")
	created := chooseColumn(columns, "created_at", "published_at", "created_time")

	selectColumns := []string{quoteIdent(ver), quoteIdent(ns), quoteIdent(env)}
	if gray != "" {
		selectColumns = append(selectColumns, quoteIdent(gray))
	}
	if rollback != "" {
		selectColumns = append(selectColumns, quoteIdent(rollback))
	}
	if created != "" {
		selectColumns = append(selectColumns, quoteIdent(created))
	}
	query := fmt.Sprintf(
		`SELECT %s FROM %s WHERE %s = ? AND %s = ? AND %s = ? ORDER BY %s DESC LIMIT 1`,
		joinIdent(selectColumns), quoteIdent(table), quoteIdent(ns), quoteIdent(env), quoteIdent(ver), quoteIdent(ver),
	)
	row := s.db.QueryRow(query, namespace, environment, number)

	version := &Version{Namespace: namespace, Environment: environment, items: make(map[string]json.RawMessage)}
	dest := make([]any, 0, 6)
	dest = append(dest, &version.Version, &version.Namespace, &version.Environment)
	var grayValue sql.NullString
	if gray != "" {
		dest = append(dest, &grayValue)
	}
	var rollbackValue sql.NullInt64
	if rollback != "" {
		dest = append(dest, &rollbackValue)
	}
	var createdValue sql.NullString
	if created != "" {
		dest = append(dest, &createdValue)
	}
	if err := row.Scan(dest...); errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, fmt.Errorf("load version: %w", err)
	}
	if grayValue.Valid {
		version.GrayLabel = &grayValue.String
	}
	if rollbackValue.Valid {
		source := rollbackValue.Int64
		version.RollbackSourceVersion = &source
	}
	if createdValue.Valid {
		version.CreatedAt = createdValue.String
	}

	if err := s.loadItems(version, table, columns, number); err != nil {
		return nil, false, err
	}
	return version, true, nil
}

func (s *Store) loadItems(version *Version, versionTable string, versionColumns map[string]bool, number int64) error {
	for _, table := range []string{"config_items", "configuration_items", "config_entries", "config_version_items", "items", "configurations"} {
		if !s.tableExists(table) {
			continue
		}
		columns, err := s.tableColumns(table)
		if err != nil {
			return err
		}
		name := chooseColumn(columns, "name", "item_name", "config_key", "key_name", "key")
		value := chooseColumn(columns, "value", "config_value", "item_value", "raw_value")
		if name == "" || value == "" {
			continue
		}
		scope, args, err := s.itemJoinScope(table, columns, versionTable, versionColumns, number)
		if err != nil {
			return err
		}
		query := fmt.Sprintf(`SELECT %s, %s FROM %s%s`, quoteIdent(name), quoteIdent(value), quoteIdent(table), scope)
		rows, err := s.db.Query(query, args...)
		if err != nil {
			return fmt.Errorf("load items: %w", err)
		}
		loadedAny := false
		for rows.Next() {
			var itemName string
			var itemValue any
			if err := rows.Scan(&itemName, &itemValue); err != nil {
				return fmt.Errorf("scan item: %w", err)
			}
			raw, err := valueToRawMessage(itemValue)
			if err != nil {
				return err
			}
			version.items[itemName] = raw
			loadedAny = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if loadedAny {
			return nil
		}
	}

	for _, column := range []string{"items_json", "config_items_json", "values_json", "configurations_json", "payload", "items", "config", "configuration"} {
		if !versionColumns[column] {
			continue
		}
		var raw sql.NullString
		query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s = ? AND %s = ? AND %s = ?`,
			quoteIdent(column), quoteIdent(versionTable),
			quoteIdent(chooseColumn(versionColumns, "namespace", "namespace_name", "ns")),
			quoteIdent(chooseColumn(versionColumns, "environment", "environment_name", "env")),
			quoteIdent(chooseColumn(versionColumns, "version", "version_number")),
		)
		if err := s.db.QueryRow(query, version.Namespace, version.Environment, number).Scan(&raw); err != nil {
			return fmt.Errorf("load embedded items: %w", err)
		}
		if raw.Valid {
			return json.Unmarshal([]byte(raw.String), &version.items)
		}
	}
	return nil
}

func (s *Store) itemJoinScope(itemTable string, itemColumns map[string]bool, versionTable string, versionColumns map[string]bool, number int64) (string, []any, error) {
	itemVersion := chooseColumn(itemColumns, "version", "version_number")
	ns := chooseColumn(itemColumns, "namespace", "namespace_name", "ns")
	env := chooseColumn(itemColumns, "environment", "environment_name", "env")
	if itemVersion != "" {
		if ns != "" && env != "" {
			return fmt.Sprintf(` WHERE %s = ? AND %s = ? AND %s = ?`,
				quoteIdent(ns), quoteIdent(env), quoteIdent(itemVersion)), []any{number}, nil
		}
		return fmt.Sprintf(` WHERE %s = ?`, quoteIdent(itemVersion)), []any{number}, nil
	}

	versionID := chooseColumn(versionColumns, "id", "version_id")
	itemVersionID := chooseColumn(itemColumns, "version_id", "version_pk", "release_id")
	if versionID == "" || itemVersionID == "" {
		return "", nil, nil
	}
	versionColumn := chooseColumn(versionColumns, "version", "version_number")
	if versionColumn != "" && s.itemColumnMatchesVersionNumber(itemTable, itemVersionID, number) {
		ns = chooseColumn(itemColumns, "namespace", "namespace_name", "ns")
		env = chooseColumn(itemColumns, "environment", "environment_name", "env")
		if ns != "" && env != "" {
			return fmt.Sprintf(
				` WHERE %s = ? AND %s = ? AND %s = ?`,
				quoteIdent(ns), quoteIdent(env),
				quoteIdent(itemVersionID)), []any{number}, nil
		}
		return fmt.Sprintf(` WHERE %s = ?`, quoteIdent(itemVersionID)), []any{number}, nil
	}
	return fmt.Sprintf(` WHERE %s = (SELECT %s FROM %s WHERE %s = ?)`,
		quoteIdent(itemVersionID), quoteIdent(versionID), quoteIdent(versionTable),
		quoteIdent(versionColumn)), []any{number}, nil
}

func (s *Store) itemColumnMatchesVersionNumber(itemTable, column string, number int64) bool {
	var value int64
	err := s.db.QueryRow(
		fmt.Sprintf(`SELECT %s FROM %s WHERE %s = ? LIMIT 1`, quoteIdent(column), quoteIdent(itemTable), quoteIdent(column)),
		number,
	).Scan(&value)
	return err == nil && value == number
}

func (s *Store) tableExists(name string) bool {
	var value string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type IN ('table','view') AND name = ?`, name).Scan(&value)
	return err == nil
}

func (s *Store) tableColumns(table string) (map[string]bool, error) {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, quoteIdent(table)))
	if err != nil {
		return nil, fmt.Errorf("inspect table %s: %w", table, err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func chooseColumn(columns map[string]bool, candidates ...string) string {
	for _, candidate := range candidates {
		if columns[candidate] {
			return candidate
		}
	}
	return ""
}

func quoteIdent(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func joinIdent(values []string) string { return strings.Join(values, ", ") }

func valueToRawMessage(value any) (json.RawMessage, error) {
	switch typed := value.(type) {
	case nil:
		return json.RawMessage("null"), nil
	case []byte:
		if json.Valid(typed) {
			return json.RawMessage(typed), nil
		}
		return json.Marshal(string(typed))
	case string:
		if json.Valid([]byte(typed)) {
			return json.RawMessage(typed), nil
		}
		return json.Marshal(typed)
	case int64:
		return json.Marshal(typed)
	case float64:
		return json.Marshal(typed)
	case bool:
		return json.Marshal(typed)
	default:
		encoded, err := json.Marshal(typed)
		return encoded, err
	}
}

func compactJSON(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if decoder.More() || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("unexpected trailing data")
	}
	compacted, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return compacted, nil
}
