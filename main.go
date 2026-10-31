// Ledger is a small self-hosted household budget app. The app itself is in
// internal/app; the plugins compiled in are imported here.
package main

import (
	"github.com/staal0/ledger/internal/app"
	_ "github.com/staal0/ledger/plugins/bank"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() { app.Main(version) }
