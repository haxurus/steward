package mqueue

// Haxurus fork: test setup kept out of mqueue_test.go so upstream changes never
// conflict. Without loaded configuration yagpdb.mqueue.max_concurrent_sends reads 0
// and findWork never hands out work, so TestMqueuePubsub waited forever.

import "github.com/botlabs-gg/yagpdb/v2/common/config"

func init() {
	config.Load()
}
