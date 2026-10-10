package serverstats

// Haxurus fork: replaces the upstream TestDailyStatsMessages and TestDailyMisc
// (stats_test.go), which still insert hourly rows into PostgreSQL while
// RetrieveDailyStats now reads the day's counters from Redis. The CI skips those two
// (.github/scripts/go-test.sh) and runs these instead.

import (
	"testing"
	"time"

	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/serverstats/messagestatscollector"
	"github.com/mediocregopher/radix/v3"
)

func TestDailyStatsFromRedis(t *testing.T) {
	if common.RedisPool == nil {
		t.Fatal("redis is not available")
	}

	const guildID = 7701
	now := time.Now()
	year, day := now.Year(), now.YearDay()

	keys := []string{
		messagestatscollector.KeyMessageStats(guildID, year, day),
		keyTotalMembers(year, day),
		keyJoinedMembers(year, day),
		keyLeftMembers(year, day),
	}
	cleanup := func() {
		for _, k := range keys {
			common.RedisPool.Do(radix.Cmd(nil, "DEL", k))
		}
	}
	cleanup()
	defer cleanup()

	cmds := []radix.CmdAction{
		radix.FlatCmd(nil, "ZADD", keys[0], 120, 2, 5, 3),
		radix.FlatCmd(nil, "ZADD", keys[1], 60, guildID),
		radix.FlatCmd(nil, "ZADD", keys[2], 30, guildID),
		radix.FlatCmd(nil, "ZADD", keys[3], 15, guildID),
		// Another guild on the same day must not leak into the result.
		radix.FlatCmd(nil, "ZADD", keys[1], 999, guildID+1),
	}
	if err := common.RedisPool.Do(radix.Pipeline(cmds...)); err != nil {
		t.Fatalf("seeding redis: %v", err)
	}

	daily, err := RetrieveDailyStats(now, guildID)
	if err != nil {
		t.Fatalf("RetrieveDailyStats: %v", err)
	}

	if len(daily.ChannelMessages) != 2 {
		t.Fatalf("expected 2 channels, got %+v", daily.ChannelMessages)
	}
	for channel, want := range map[string]int64{"2": 120, "3": 5} {
		got := daily.ChannelMessages[channel]
		if got == nil || got.Count != want || got.Name != channel {
			t.Errorf("channel %s: got %+v, want count %d", channel, got, want)
		}
	}
	if daily.TotalMembers != 60 || daily.JoinedDay != 30 || daily.LeftDay != 15 {
		t.Errorf("members/joins/leaves = %d/%d/%d, want 60/30/15", daily.TotalMembers, daily.JoinedDay, daily.LeftDay)
	}
	// The online count comes from the bot's internal API, absent in tests.
	if daily.Online != 0 {
		t.Errorf("online = %d without a bot, want 0", daily.Online)
	}
}

func TestDailyStatsEmptyDay(t *testing.T) {
	if common.RedisPool == nil {
		t.Fatal("redis is not available")
	}

	daily, err := RetrieveDailyStats(time.Date(2001, 1, 1, 12, 0, 0, 0, time.UTC), 7702)
	if err != nil {
		t.Fatalf("RetrieveDailyStats: %v", err)
	}
	if len(daily.ChannelMessages) != 0 || daily.TotalMembers != 0 || daily.JoinedDay != 0 || daily.LeftDay != 0 {
		t.Errorf("a day without data must be empty, got %+v", daily)
	}
}
