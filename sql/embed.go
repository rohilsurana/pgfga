package sql

import _ "embed"

//go:embed authz_model.sql
var AuthzModelSQL string

//go:embed check_permission.sql
var CheckPermissionSQL string
