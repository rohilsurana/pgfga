package pgfga

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/lib/pq"
	openfgav1 "github.com/openfga/api/proto/openfga/v1"

	"github.com/rohilsurana/pgfga/parser"
	pgfgasql "github.com/rohilsurana/pgfga/sql"
	"github.com/rohilsurana/pgfga/transform"
)

type Option func(*Client)

// WithSchema places all pgfga objects (authz_model table, check_permission
// functions) into the given Postgres schema instead of public.
// The schema is created automatically by Migrate() if it does not exist.
//
// Your authz_relationship view must be accessible from this schema's
// search_path — either create it in the same schema or in public.
func WithSchema(schema string) Option {
	return func(c *Client) { c.schema = schema }
}

type Client struct {
	db     *sql.DB
	schema string
}

func New(db *sql.DB, opts ...Option) *Client {
	c := &Client{db: db}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func Connect(dsn string, opts ...Option) (*Client, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("pgfga: open: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("pgfga: ping: %w", err)
	}
	c := &Client{db: db}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

func (c *Client) DB() *sql.DB {
	return c.db
}

func (c *Client) Close() error {
	return c.db.Close()
}

// Migrate creates the authz_model table and installs the check_permission
// PL/pgSQL functions. Safe to call multiple times (uses IF NOT EXISTS and
// CREATE OR REPLACE). When WithSchema is configured, the schema is created
// first and all objects are placed in it.
func (c *Client) Migrate(ctx context.Context) error {
	if c.schema != "" {
		if _, err := c.db.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdent(c.schema))); err != nil {
			return fmt.Errorf("pgfga: create schema: %w", err)
		}
	}

	tableSQL := c.qualifyAuthzModelSQL(pgfgasql.AuthzModelSQL)
	if _, err := c.db.ExecContext(ctx, tableSQL); err != nil {
		return fmt.Errorf("pgfga: create authz_model table: %w", err)
	}

	funcSQL := c.qualifyCheckPermissionSQL(pgfgasql.CheckPermissionSQL)
	if _, err := c.db.ExecContext(ctx, funcSQL); err != nil {
		return fmt.Errorf("pgfga: install check_permission: %w", err)
	}
	return nil
}

type CheckRequest struct {
	UserType   string
	UserID     string
	Relation   string
	ObjectType string
	ObjectID   string
}

// Check calls the check_permission PL/pgSQL function using the latest
// schema version.
func (c *Client) Check(ctx context.Context, req CheckRequest) (bool, error) {
	var allowed bool
	q := fmt.Sprintf("SELECT %s($1, $2, $3, $4, $5)", c.qualify("check_permission"))
	err := c.db.QueryRowContext(ctx, q,
		req.UserType, req.UserID, req.Relation, req.ObjectType, req.ObjectID,
	).Scan(&allowed)
	if err != nil {
		return false, fmt.Errorf("pgfga: check: %w", err)
	}
	return allowed, nil
}

// CheckWithVersion calls check_permission with an explicit schema version.
func (c *Client) CheckWithVersion(ctx context.Context, schemaVersion int64, req CheckRequest) (bool, error) {
	var allowed bool
	q := fmt.Sprintf("SELECT %s($1, $2, $3, $4, $5, $6)", c.qualify("check_permission"))
	err := c.db.QueryRowContext(ctx, q,
		schemaVersion, req.UserType, req.UserID, req.Relation, req.ObjectType, req.ObjectID,
	).Scan(&allowed)
	if err != nil {
		return false, fmt.Errorf("pgfga: check: %w", err)
	}
	return allowed, nil
}

// LoadModelDSL parses an OpenFGA DSL string and inserts the resulting
// authorization model at the given schema version. It replaces any existing
// model at that version.
func (c *Client) LoadModelDSL(ctx context.Context, schemaVersion int64, dsl string) error {
	model, err := parser.ParseString(dsl)
	if err != nil {
		return fmt.Errorf("pgfga: parse dsl: %w", err)
	}
	return c.loadModel(ctx, schemaVersion, model)
}

// LoadModelFile parses an OpenFGA .fga file and inserts the resulting
// authorization model at the given schema version.
func (c *Client) LoadModelFile(ctx context.Context, schemaVersion int64, path string) error {
	model, err := parser.ParseFile(path)
	if err != nil {
		return fmt.Errorf("pgfga: parse file: %w", err)
	}
	return c.loadModel(ctx, schemaVersion, model)
}

func (c *Client) loadModel(ctx context.Context, schemaVersion int64, model *openfgav1.AuthorizationModel) error {
	rows, err := transform.GenerateAuthzModel(schemaVersion, model)
	if err != nil {
		return fmt.Errorf("pgfga: transform: %w", err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("pgfga: no model rows generated")
	}

	table := c.qualify("authz_model")

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pgfga: begin tx: %w", err)
	}
	defer tx.Rollback()

	deleteQ := fmt.Sprintf("DELETE FROM %s WHERE schema_version = $1", table)
	if _, err := tx.ExecContext(ctx, deleteQ, schemaVersion); err != nil {
		return fmt.Errorf("pgfga: delete old version: %w", err)
	}

	insertQ := fmt.Sprintf(`
		INSERT INTO %s (schema_version, entity_type, relation, subject_type, implied_by, parent_relation)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, table)
	stmt, err := tx.PrepareContext(ctx, insertQ)
	if err != nil {
		return fmt.Errorf("pgfga: prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx,
			row.SchemaVersion, row.EntityType, row.Relation,
			row.SubjectType, row.ImpliedBy, row.ParentRelation,
		); err != nil {
			return fmt.Errorf("pgfga: insert %s.%s: %w", row.EntityType, row.Relation, err)
		}
	}

	return tx.Commit()
}

// GetLatestSchemaVersion returns the highest schema_version in the
// authz_model table, or 0 if no models exist.
func (c *Client) GetLatestSchemaVersion(ctx context.Context) (int64, error) {
	var version sql.NullInt64
	q := fmt.Sprintf("SELECT max(schema_version) FROM %s", c.qualify("authz_model"))
	err := c.db.QueryRowContext(ctx, q).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("pgfga: get latest version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return version.Int64, nil
}

// qualify returns a schema-qualified identifier if a schema is configured,
// otherwise returns the bare name.
func (c *Client) qualify(name string) string {
	if c.schema == "" {
		return name
	}
	return quoteIdent(c.schema) + "." + name
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// qualifyAuthzModelSQL rewrites the embedded authz_model DDL to use the
// configured schema.
func (c *Client) qualifyAuthzModelSQL(src string) string {
	if c.schema == "" {
		return src
	}
	q := quoteIdent(c.schema)
	s := src
	// Qualify the table name in CREATE TABLE and ON clauses.
	// Index names are NOT schema-qualified — Postgres places them in the
	// same schema as their table automatically.
	s = strings.ReplaceAll(s, `on "authz_model"`, `on `+q+`."authz_model"`)
	s = strings.ReplaceAll(s, `exists "authz_model"`, `exists `+q+`."authz_model"`)
	return s
}

// qualifyCheckPermissionSQL rewrites the embedded check_permission functions
// to use the configured schema and sets the function search_path so internal
// references to authz_model and authz_relationship resolve correctly.
func (c *Client) qualifyCheckPermissionSQL(src string) string {
	if c.schema == "" {
		return src
	}
	q := quoteIdent(c.schema)
	searchPath := fmt.Sprintf("SET search_path = %s, public", q)

	s := src
	// Qualify function names in CREATE OR REPLACE
	s = strings.ReplaceAll(s, "function check_permission(", "function "+q+".check_permission(")

	// Qualify the authz_model composite type used as a function parameter
	s = strings.ReplaceAll(s, "authz_model[]", q+".authz_model[]")

	// Add search_path to each function so internal references to
	// authz_model (table) and authz_relationship (view) resolve within
	// the configured schema first, then public.
	s = strings.ReplaceAll(s, "$$ language plpgsql;", "$$ language plpgsql\n"+searchPath+";")
	return s
}
