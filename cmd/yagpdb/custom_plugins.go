package main

import "github.com/botlabs-gg/yagpdb/v2/custom"

// Plugins of the Haxurus fork register here instead of in main.go, so upstream
// changes to main.go never conflict with the fork (docs/UPSTREAM.md). init runs
// before main: see custom.RegisterPlugins for what registration may do.
func init() {
	custom.RegisterPlugins()
}
