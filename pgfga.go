package pgfga

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/lib/pq"
	openfgav1 "github.com/openfga/api/proto/openfga/v1"

	"github.com/rohilsurana/pgfga/parser"
	pgfgasql "github.com/rohilsurana/pgfga/sql"
	"github.com/rohilsurana/pgfga/transform"
)

type Client struct {
	db *sql.DB
}

func New(db *sql.DB) *Client {
	return &Client{db: db}
}

func Connect(dsn string) (*Client, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("pgfga: open: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("pgfga: ping: %w", err)
	}
	return &Client{db: db}, nil
}

func (c *Client) DB() *sql.DB {
	return c.db
}

func (c *Client) Close() error {
	return c.db.Close()
}

// Migrate creates the authz_model table and installs the check_permission
// PL/pgSQL functions. Safe to call multiple times (uses IF NOT EXISTS and
// CREATE OR REPLACE).
func (c *Client) Migrate(ctx context.Context) error {
	if _, err := c.db.ExecContext(ctx, pgfgasql.AuthzModelSQL); err != nil {
		return fmt.Errorf("pgfga: create authz_model table: %w", err)
	}
	if _, err := c.db.ExecContext(ctx, pgfgasql.CheckPermissionSQL); err != nil {
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
	err := c.db.QueryRowContext(ctx,
		"SELECT check_permission($1, $2, $3, $4, $5)",
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
	err := c.db.QueryRowContext(ctx,
		"SELECT check_permission($1, $2, $3, $4, $5, $6)",
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

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pgfga: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM authz_model WHERE schema_version = $1", schemaVersion); err != nil {
		return fmt.Errorf("pgfga: delete old version: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO authz_model (schema_version, entity_type, relation, subject_type, implied_by, parent_relation)
		VALUES ($1, $2, $3, $4, $5, $6)
	`)
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
	err := c.db.QueryRowContext(ctx, "SELECT max(schema_version) FROM authz_model").Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("pgfga: get latest version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return version.Int64, nil
}
