package migrations

import "embed"

// FS contains all module migrations. Each module keeps its own directory so
// table ownership stays visible in review.
//
//go:embed identity/*.sql ledger/*.sql shared/*.sql
var FS embed.FS
