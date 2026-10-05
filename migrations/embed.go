// Package migrations bevat de SQL-migraties van ClusterForge (goose-formaat).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
