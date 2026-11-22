// Package migrations embeds the goose SQL migrations so the binary carries its own schema.
// Files are applied in name order by store.Open; never edit one that has shipped, add a new one.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
