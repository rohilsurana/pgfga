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

// DBTX is the interface for database operations. It is satisfied by *sql.DB,
// *sql.Tx, *sqlx.DB, *sqlx.Tx, pgx stdlib pools, and pgx stdlib transactions.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// txBeginner is satisfied by connection-level types (*sql.DB, *sqlx.DB, pgx
// pools) but NOT transaction types (*sql.Tx, *sqlx.Tx). Used to detect
// whether loadModel should wrap operations in a transaction.
type txBeginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

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
	db     DBTX
	rawDB  *sql.DB // only set by Connect(); used by Close()
	schema string
}

// New creates a Client from any DBTX — *sql.DB, *sql.Tx, *sqlx.DB, *sqlx.Tx,
// pgx stdlib pool, etc.
func New(db DBTX, opts ...Option) *Client {
	c := &Client{db: db}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Connect opens a new Postgres connection and returns a Client.
// Call Close() when done to release the connection.
func Connect(dsn string, opts ...Option) (*Client, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("pgfga: open: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("pgfga: ping: %w", err)
	}
	c := &Client{db: db, rawDB: db}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// DB returns the underlying DBTX.
func (c *Client) DB() DBTX {
	return c.db
}

// Close closes the underlying connection. Only valid on clients created
// via Connect(). For clients created via New() or WithTx(), this is a no-op.
func (c *Client) Close() error {
	if c.rawDB != nil {
		return c.rawDB.Close()
	}
	return nil
}

// WithTx returns a new Client bound to the given transaction (or any DBTX).
// All operations on the returned client execute within that transaction.
// The caller is responsible for committing or rolling back.
//
//	tx, _ := db.BeginTx(ctx, nil)
//	defer tx.Rollback()
//	txClient := client.WithTx(tx)
//	allowed, _ := txClient.Check(ctx, req)
//	tx.Commit()
func (c *Client) WithTx(tx DBTX) *Client {
	return &Client{
		db:     tx,
		schema: c.schema,
	}
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
	q := fmt.Sprintf("SELECT %s($1::text, $2::text, $3::text, $4::text, $5::text)", c.qualify("check_permission"))
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
	q := fmt.Sprintf("SELECT %s($1::bigint, $2::text, $3::text, $4::text, $5::text, $6::text)", c.qualify("check_permission"))
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

	// If the underlying DBTX supports BeginTx (e.g. *sql.DB, *sqlx.DB),
	// wrap in a transaction. Otherwise assume we are already inside one
	// (e.g. *sql.Tx passed via WithTx) and execute directly.
	if beginner, ok := c.db.(txBeginner); ok {
		tx, err := beginner.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("pgfga: begin tx: %w", err)
		}
		defer tx.Rollback()
		if err := c.insertModel(ctx, tx, schemaVersion, rows); err != nil {
			return err
		}
		return tx.Commit()
	}

	return c.insertModel(ctx, c.db, schemaVersion, rows)
}

func (c *Client) insertModel(ctx context.Context, db DBTX, schemaVersion int64, rows []transform.AuthzModelRow) error {
	table := c.qualify("authz_model")

	deleteQ := fmt.Sprintf("DELETE FROM %s WHERE schema_version = $1", table)
	if _, err := db.ExecContext(ctx, deleteQ, schemaVersion); err != nil {
		return fmt.Errorf("pgfga: delete old version: %w", err)
	}

	insertQ := fmt.Sprintf(`
		INSERT INTO %s (schema_version, entity_type, relation, subject_type, implied_by, parent_relation)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, table)
	for _, row := range rows {
		if _, err := db.ExecContext(ctx, insertQ,
			row.SchemaVersion, row.EntityType, row.Relation,
			row.SubjectType, row.ImpliedBy, row.ParentRelation,
		); err != nil {
			return fmt.Errorf("pgfga: insert %s.%s: %w", row.EntityType, row.Relation, err)
		}
	}

	return nil
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

func (c *Client) qualify(name string) string {
	if c.schema == "" {
		return name
	}
	return quoteIdent(c.schema) + "." + name
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func (c *Client) qualifyAuthzModelSQL(src string) string {
	if c.schema == "" {
		return src
	}
	q := quoteIdent(c.schema)
	s := src
	s = strings.ReplaceAll(s, `on "authz_model"`, `on `+q+`."authz_model"`)
	s = strings.ReplaceAll(s, `exists "authz_model"`, `exists `+q+`."authz_model"`)
	return s
}

func (c *Client) qualifyCheckPermissionSQL(src string) string {
	if c.schema == "" {
		return src
	}
	q := quoteIdent(c.schema)
	searchPath := fmt.Sprintf("SET search_path = %s, public", q)

	s := src
	s = strings.ReplaceAll(s, "function check_permission(", "function "+q+".check_permission(")
	s = strings.ReplaceAll(s, "authz_model[]", q+".authz_model[]")
	s = strings.ReplaceAll(s, "$$ language plpgsql;", "$$ language plpgsql\n"+searchPath+";")
	return s
}
