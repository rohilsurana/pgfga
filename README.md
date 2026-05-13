# pgFGA

pgFGA is a pure-Postgres fine-grained authorization library for Go, implementing parts of [OpenFGA](https://openfga.dev/). It parses `.fga` schema files natively using the [openfga/language](https://github.com/openfga/language) package — no external CLI or sidecar needed.

## Credits

This project is a Go rewrite of [pgFGA](https://github.com/isaacharrisholt/pgfga) by [Isaac Harris-Holt](https://github.com/isaacharrisholt). The original project implemented pgFGA using TypeScript/Bun and the OpenFGA CLI. This fork replaces those with a pure Go library using the [openfga/language](https://github.com/openfga/language) package for native DSL parsing. The core SQL — the `authz_model` table design and the recursive `check_permission` PL/pgSQL function — originates from the original project.

Licensed under MIT. See [LICENSE](./LICENSE) for the full text.

## Requirements

- Go 1.25+
- PostgreSQL 14+

## Install

```bash
go get github.com/rohilsurana/pgfga
```

## Quick Start

Here is a complete working example — from schema definition to permission checks.

### 1. Define your authorization model

Create a `schema.fga` file using [OpenFGA DSL](https://openfga.dev/docs/configuration-language):

```
model
  schema 1.1

type user

type organization
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin
    define can_read: member
    define can_edit: admin
    define can_delete: owner

type project
  relations
    define organization: [organization]
    define editor: [user]
    define viewer: [user] or editor
    define can_read: member from organization
    define can_edit: editor
    define can_delete: can_edit
```

This defines: users can be owners/admins/members of organizations, and projects inherit read access from org membership while having their own editor/viewer roles.

### 2. Set up pgfga and load the model

```go
package main

import (
    "context"
    "database/sql"
    "fmt"
    "log"

    _ "github.com/lib/pq"
    "github.com/rohilsurana/pgfga"
)

func main() {
    ctx := context.Background()
    db, _ := sql.Open("postgres", "postgres://user:pass@localhost:5432/mydb?sslmode=disable")

    // Create a pgfga client. Use WithSchema to isolate pgfga objects
    // into a dedicated Postgres schema instead of public.
    client := pgfga.New(db, pgfga.WithSchema("authz"))

    // Migrate creates the authz_model table and installs the
    // check_permission PL/pgSQL functions. Idempotent — safe to call
    // on every application startup.
    if err := client.Migrate(ctx); err != nil {
        log.Fatal(err)
    }

    // Load your authorization model from a DSL string.
    // The first argument is the schema version number.
    dsl := `model
  schema 1.1

type user

type organization
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin
    define can_read: member
    define can_edit: admin
    define can_delete: owner

type project
  relations
    define organization: [organization]
    define editor: [user]
    define viewer: [user] or editor
    define can_read: member from organization
    define can_edit: editor
    define can_delete: can_edit
`
    if err := client.LoadModelDSL(ctx, 1, dsl); err != nil {
        log.Fatal(err)
    }

    // Or load from a file:
    // client.LoadModelFile(ctx, 1, "schemas/v001/schema.fga")
}
```

### 3. Create the `authz_relationship` view

pgfga checks permissions by querying a view called `authz_relationship` that maps your application data to authorization tuples. You define this view yourself, pointing at your existing tables.

```sql
-- If using WithSchema("authz"), create the view in the same schema.
CREATE VIEW authz.authz_relationship AS

-- Organization memberships (direct role assignments)
SELECT
    user_id,
    'user' AS user_type,
    role AS relation,           -- 'owner', 'admin', or 'member'
    org_id AS object_id,
    'organization' AS object_type
FROM org_memberships

UNION ALL

-- Project -> Organization parent link (for inherited permissions)
SELECT
    id AS user_id,
    'project' AS user_type,
    'organization' AS relation,
    org_id AS object_id,
    'organization' AS object_type
FROM projects

UNION ALL

-- Project memberships (direct role assignments)
SELECT
    user_id,
    'user' AS user_type,
    role AS relation,           -- 'editor' or 'viewer'
    project_id AS object_id,
    'project' AS object_type
FROM project_memberships;
```

The view must have exactly these columns:

| Column | Type | Description |
|---|---|---|
| `user_id` | text | The subject (user or parent resource ID) |
| `user_type` | text | The subject type (`user`, `project`, etc.) |
| `relation` | text | The relation name (`owner`, `member`, `organization`, etc.) |
| `object_id` | text | The target object ID |
| `object_type` | text | The target object type |

### 4. Check permissions

```go
// Can alice read the organization?
allowed, err := client.Check(ctx, pgfga.CheckRequest{
    UserType:   "user",
    UserID:     "alice",
    Relation:   "can_read",
    ObjectType: "organization",
    ObjectID:   "acme",
})
// allowed == true (if alice is owner, admin, or member of acme)

// Can bob edit the project? (checks direct editor role)
allowed, err = client.Check(ctx, pgfga.CheckRequest{
    UserType:   "user",
    UserID:     "bob",
    Relation:   "can_edit",
    ObjectType: "project",
    ObjectID:   "proj-1",
})

// Can carol read the project? (traverses project -> org -> member)
allowed, err = client.Check(ctx, pgfga.CheckRequest{
    UserType:   "user",
    UserID:     "carol",
    Relation:   "can_read",
    ObjectType: "project",
    ObjectID:   "proj-1",
})

// Pin to a specific schema version (recommended for production)
allowed, err = client.CheckWithVersion(ctx, 1, pgfga.CheckRequest{
    UserType:   "user",
    UserID:     "alice",
    Relation:   "can_delete",
    ObjectType: "organization",
    ObjectID:   "acme",
})
```

## How it works

pgfga stores your authorization model in a Postgres table and evaluates permissions using a recursive PL/pgSQL function — no external service needed.

```
Your .fga schema          pgfga library          Postgres
─────────────────     ─────────────────────     ──────────────────────
                      parser.ParseString()
  model               ──────────────────>       authz_model table
    schema 1.1                                  (stores the schema)
  type user            transform.Generate()
  type org             ──────────────────>
    define owner                                check_permission()
    define admin                                (recursive PL/pgSQL)
                       client.Check()
                       ──────────────────>       authz_relationship
                                                (your view, your data)
```

1. **Parse**: `.fga` DSL is parsed into protobuf types via `openfga/language`
2. **Transform**: Protobuf model is converted to `authz_model` rows (entity_type, relation, subject_type, implied_by, parent_relation)
3. **Check**: `check_permission()` recursively walks the model, querying your `authz_relationship` view to resolve direct assignments, role hierarchies, and parent-child inheritance

## API Reference

### Client

```go
// Create from existing *sql.DB
client := pgfga.New(db)
client := pgfga.New(db, pgfga.WithSchema("authz"))

// Or connect directly
client, err := pgfga.Connect(dsn)
client, err := pgfga.Connect(dsn, pgfga.WithSchema("authz"))

client.DB()    // access underlying *sql.DB
client.Close() // close the connection (only if created via Connect)
```

### Options

| Option | Description |
|---|---|
| `pgfga.WithSchema(name)` | Place all pgfga objects in a dedicated Postgres schema. Created automatically by `Migrate()`. |

### Methods

| Method | Description |
|---|---|
| `Migrate(ctx)` | Create `authz_model` table + install `check_permission` functions. Idempotent. |
| `LoadModelDSL(ctx, version, dsl)` | Parse DSL string and upsert authorization model at given version. |
| `LoadModelFile(ctx, version, path)` | Parse `.fga` file and upsert authorization model at given version. |
| `Check(ctx, req)` | Check permission using the latest schema version. |
| `CheckWithVersion(ctx, version, req)` | Check permission using a specific schema version. |
| `GetLatestSchemaVersion(ctx)` | Get the highest schema version number, or 0. |

### Sub-packages

| Package | Import | Use |
|---|---|---|
| `parser` | `github.com/rohilsurana/pgfga/parser` | Parse/validate `.fga` files without a database |
| `transform` | `github.com/rohilsurana/pgfga/transform` | Convert parsed models to `authz_model` rows |
| `sql` | `github.com/rohilsurana/pgfga/sql` | Access embedded SQL (table DDL, function definitions) |

## CLI

A thin CLI wrapper is included for schema management workflows.

```bash
go install github.com/rohilsurana/pgfga/cmd/pgfga@latest
```

```
pgfga validate                             Validate all .fga schemas
pgfga new                                  Create WIP schema from latest version
pgfga finalize                             Promote WIP to next version number
pgfga migrate [--dsn=...] [--schema=...]   Apply schema to database
```

| Variable | Flag | Description |
|---|---|---|
| `PGFGA_DSN` | `--dsn` | Postgres connection string |
| `PGFGA_SCHEMA` | `--schema` | Postgres schema for pgfga objects |
| `PGFGA_LOCAL` | | Set `true` to include WIP schemas |

### Schema versioning workflow

```bash
pgfga new                    # copies latest schema to schemas/wip/
vim schemas/wip/schema.fga   # edit the model
pgfga validate               # check all schemas parse correctly
pgfga finalize               # schemas/wip/ -> schemas/v001/
pgfga migrate --dsn=...      # apply to database
```
