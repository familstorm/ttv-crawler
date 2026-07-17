package migrations

import "embed"

// FS contains every forward-only database migration.
//
//go:embed *.sql
var FS embed.FS
