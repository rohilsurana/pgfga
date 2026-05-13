# pgFGA

pgFGA is a pure-Postgres implementation of parts of [OpenFGA](https://openfga.dev/).

This is a Go port of the original [pgFGA](https://github.com/isaacharrisholt/pgfga) project. It replaces the TypeScript/Bun scripts and the OpenFGA CLI dependency with a Go library and CLI that parses `.fga` files natively using the [openfga/language](https://github.com/openfga/language) package.

## Requirements

- Go 1.25+
- PostgreSQL database

## Library Usage

```go
import "github.com/rohilsurana/pgfga"
```

### Initialize

```go
// From an existing *sql.DB
client := pgfga.New(db)

// Or connect directly
client, err := pgfga.Connect("postgres://user:pass@localhost:5432/mydb?sslmode=disable")
if err != nil {
    log.Fatal(err)
}
defer client.Close()
```

### Migrate (create tables + install PL/pgSQL functions)

```go
if err := client.Migrate(ctx); err != nil {
    log.Fatal(err)
}
```

This creates the `authz_model` table and installs the `check_permission` functions. Safe to call multiple times.

### Load an authorization model

```go
// From a DSL string
dsl := `model
  schema 1.1

type user

type document
  relations
    define viewer: [user]
    define editor: [user] or viewer
`
err := client.LoadModelDSL(ctx, 1, dsl)

// Or from a .fga file
err := client.LoadModelFile(ctx, 1, "schemas/v001/schema.fga")
```

### Check permissions

```go
allowed, err := client.Check(ctx, pgfga.CheckRequest{
    UserType:   "user",
    UserID:     "alice",
    Relation:   "viewer",
    ObjectType: "document",
    ObjectID:   "doc-1",
})

// With explicit schema version
allowed, err := client.CheckWithVersion(ctx, 1, pgfga.CheckRequest{
    UserType:   "user",
    UserID:     "alice",
    Relation:   "viewer",
    ObjectType: "document",
    ObjectID:   "doc-1",
})
```

### Sub-packages

For advanced use, the parser and transform packages are importable directly:

```go
import (
    "github.com/rohilsurana/pgfga/parser"
    "github.com/rohilsurana/pgfga/transform"
)

model, err := parser.ParseFile("schema.fga")
rows, err := transform.GenerateAuthzModel(1, model)
```

## CLI Usage

### Install

```bash
go install github.com/rohilsurana/pgfga/cmd/pgfga@latest
```

### Commands

```
pgfga validate              Validate all .fga schemas
pgfga new                   Create a new WIP schema from the latest version
pgfga finalize              Promote WIP schema to a versioned schema
pgfga migrate [--dsn=...]   Migrate the database to the latest schema version
```

### Environment variables

| Variable | Description |
|---|---|
| `PGFGA_DSN` | Postgres connection string (for `migrate`) |
| `PGFGA_LOCAL` | Set to `true` to include WIP schemas in migration |

### Workflow

1. `pgfga new` — copies the latest schema into `schemas/wip/`
2. Edit `schemas/wip/schema.fga`
3. `pgfga validate` — check all schemas parse correctly
4. `pgfga finalize` — promotes WIP to next version number
5. `pgfga migrate --dsn=...` — applies the schema to the database

## `authz_relationship` view

You must create an `authz_relationship` view in your database that maps your application's data to authorization tuples. The view must have these columns:

| Column | Type | Description |
|---|---|---|
| `user_id` | text | The subject's ID |
| `user_type` | text | The subject's type |
| `relation` | text | The relation name |
| `object_id` | text | The object's ID |
| `object_type` | text | The object's type |

Example:

```sql
CREATE VIEW authz_relationship AS
SELECT user_id, 'user' AS user_type, role AS relation,
       org_id AS object_id, 'organization' AS object_type
FROM org_memberships
UNION ALL
SELECT id AS user_id, 'repository' AS user_type, 'organization' AS relation,
       org_id AS object_id, 'organization' AS object_type
FROM repositories;
```
