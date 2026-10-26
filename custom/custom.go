// Package custom registers the plugins of the Haxurus fork (custom/README.md).
package custom

import (
	"github.com/botlabs-gg/yagpdb/v2/custom/logretention"
	"github.com/botlabs-gg/yagpdb/v2/custom/tempvoice"
)

// RegisterPlugins is called from cmd/yagpdb/custom_plugins.go before main runs.
// A plugin's RegisterPlugin may only call common.RegisterPlugin and
// common.RegisterDBSchemas: configuration, Redis and PostgreSQL are not ready yet.
func RegisterPlugins() {
	logretention.RegisterPlugin()
	tempvoice.RegisterPlugin()
}
