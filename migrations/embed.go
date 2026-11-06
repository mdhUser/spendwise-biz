package migrations

import "embed"

// FS contains all module migrations. Each module keeps its own directory so
// table ownership stays visible in review.
//
//go:embed ledger/*.sql catalog/*.sql conversation/*.sql platform/*.sql
var FS embed.FS
