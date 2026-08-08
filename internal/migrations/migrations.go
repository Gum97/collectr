// Package migrations embeds the SQL schema so a binary carries everything it
// needs to bring an empty database up to date.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

// Dir is the path within FS holding the migration files.
const Dir = "."
