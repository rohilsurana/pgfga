# pgFGA

pgFGA is a pure-Postgres implementation of parts of [OpenFGA](https://openfga.dev/).

This is a Go port of the original [pgFGA](https://github.com/isaacharrisholt/pgfga) project. It replaces the TypeScript/Bun scripts and the OpenFGA CLI dependency with a single Go binary that parses `.fga` files natively using the [openfga/language](https://github.com/openfga/language) package.

## Requirements

- Go 1.25+
- PostgreSQL database

## Getting set up

### Install the CLI

```bash
go install github.com/rohilsurana/pgfga/cmd/pgfga@latest
```

Or build from source:

```bash
go build -o pgfga ./cmd/pgfga
```

### SQL files

The [`pgfga`](./pgfga) directory contains the SQL you need:

- `authz_model.sql` — DDL for the `authz_model` table (auto-created by `migrate`)
- `check_permission.sql` — PL/pgSQL functions for checking permissions

Run `check_permission.sql` in your database after the first migration.

## CLI Usage

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

## `check_permission`

The `check_permission.sql` file provides three PL/pgSQL function overloads:

```sql
-- With explicit schema version (recommended for production)
check_permission(p_schema_version bigint, p_user_type text, p_user_id text,
                 p_relation text, p_object_type text, p_object_id text)
    returns boolean;

-- Uses latest schema version (convenient for development)
check_permission(p_user_type text, p_user_id text,
                 p_relation text, p_object_type text, p_object_id text)
    returns boolean;
```

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
