package pgfga

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

func testDSN() string {
	dsn := os.Getenv("PGFGA_TEST_DSN")
	if dsn == "" {
		dsn = "postgres://pgfga:pgfga@localhost:5435/pgfga_go?sslmode=disable"
	}
	return dsn
}

func setupClient(t *testing.T, opts ...Option) (*Client, func()) {
	t.Helper()
	db, err := sql.Open("postgres", testDSN())
	if err != nil {
		t.Skipf("cannot connect to test db: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Skipf("cannot ping test db: %v", err)
	}

	schema := "pgfga_test"
	allOpts := append([]Option{WithSchema(schema)}, opts...)
	client := New(db, allOpts...)

	ctx := context.Background()
	if err := client.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cleanup := func() {
		db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
		db.Close()
	}
	return client, cleanup
}

const testDSL = `model
  schema 1.1

type user

type organization
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin
    define can_read: member
`

func TestClient_LoadAndCheck(t *testing.T) {
	client, cleanup := setupClient(t)
	defer cleanup()
	ctx := context.Background()

	if err := client.LoadModelDSL(ctx, 1, testDSL); err != nil {
		t.Fatalf("LoadModelDSL: %v", err)
	}

	db := client.DB()
	sqlDB := db.(*sql.DB)
	sqlDB.ExecContext(ctx, `
		CREATE OR REPLACE VIEW pgfga_test.authz_relationship AS
		SELECT 'alice'::text AS user_id, 'user'::text AS user_type,
		       'owner'::text AS relation, 'acme'::text AS object_id,
		       'organization'::text AS object_type
		UNION ALL
		SELECT 'bob', 'user', 'member', 'acme', 'organization'
	`)

	allowed, err := client.CheckWithVersion(ctx, 1, CheckRequest{
		UserType: "user", UserID: "alice",
		Relation: "can_read", ObjectType: "organization", ObjectID: "acme",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !allowed {
		t.Error("expected alice to have can_read on acme")
	}

	denied, err := client.CheckWithVersion(ctx, 1, CheckRequest{
		UserType: "user", UserID: "eve",
		Relation: "can_read", ObjectType: "organization", ObjectID: "acme",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if denied {
		t.Error("expected eve to be denied can_read on acme")
	}
}

func TestClient_WithTx(t *testing.T) {
	client, cleanup := setupClient(t)
	defer cleanup()
	ctx := context.Background()

	if err := client.LoadModelDSL(ctx, 1, testDSL); err != nil {
		t.Fatalf("LoadModelDSL: %v", err)
	}

	sqlDB := client.DB().(*sql.DB)
	sqlDB.ExecContext(ctx, `
		CREATE OR REPLACE VIEW pgfga_test.authz_relationship AS
		SELECT 'alice'::text AS user_id, 'user'::text AS user_type,
		       'owner'::text AS relation, 'acme'::text AS object_id,
		       'organization'::text AS object_type
	`)

	// Start a transaction and use WithTx
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	txClient := client.WithTx(tx)

	// Check permission inside the transaction
	allowed, err := txClient.CheckWithVersion(ctx, 1, CheckRequest{
		UserType: "user", UserID: "alice",
		Relation: "owner", ObjectType: "organization", ObjectID: "acme",
	})
	if err != nil {
		t.Fatalf("Check in tx: %v", err)
	}
	if !allowed {
		t.Error("expected alice to be owner inside tx")
	}

	// GetLatestSchemaVersion inside the tx
	v, err := txClient.GetLatestSchemaVersion(ctx)
	if err != nil {
		t.Fatalf("GetLatestSchemaVersion in tx: %v", err)
	}
	if v != 1 {
		t.Errorf("expected version 1, got %d", v)
	}

	tx.Commit()
}

func TestClient_WithTx_LoadModel(t *testing.T) {
	client, cleanup := setupClient(t)
	defer cleanup()
	ctx := context.Background()

	sqlDB := client.DB().(*sql.DB)

	// Load model inside a user-managed transaction
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	txClient := client.WithTx(tx)
	if err := txClient.LoadModelDSL(ctx, 1, testDSL); err != nil {
		t.Fatalf("LoadModelDSL in tx: %v", err)
	}

	// Verify the model is visible inside the tx before commit
	v, err := txClient.GetLatestSchemaVersion(ctx)
	if err != nil {
		t.Fatalf("GetLatestSchemaVersion in tx: %v", err)
	}
	if v != 1 {
		t.Errorf("expected version 1 inside tx, got %d", v)
	}

	tx.Commit()

	// Verify it persisted after commit
	v2, err := client.GetLatestSchemaVersion(ctx)
	if err != nil {
		t.Fatalf("GetLatestSchemaVersion after commit: %v", err)
	}
	if v2 != 1 {
		t.Errorf("expected version 1 after commit, got %d", v2)
	}
}
