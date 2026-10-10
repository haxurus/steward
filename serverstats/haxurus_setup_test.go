package serverstats

// Haxurus fork: test setup kept out of serverstats_test.go so upstream changes never
// conflict. The upstream TestMain connects only PostgreSQL, but the daily message
// stats read Redis too (stats.go), which crashed on a nil pool once the tests ran
// with a database. A missing Redis is left to the tests to report.

import (
	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/common/config"
)

func init() {
	config.Load()
	_ = common.InitTestRedis()
}
