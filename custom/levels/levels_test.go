package levels

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/common/testutils"
	"github.com/botlabs-gg/yagpdb/v2/lib/discordgo"
	"github.com/botlabs-gg/yagpdb/v2/lib/dstate"
	"github.com/mediocregopher/radix/v3"
)

func TestMain(m *testing.M) {
	conn, err := testutils.InitPQ([]string{"custom_levels_users"}, DBSchemas)
	if err == nil {
		err = common.InitTestRedis()
	}
	if err != nil {
		if os.Getenv("YAGPDB_TEST_REQUIRE_SERVICES") != "" {
			fmt.Println("Failed connecting to the test services: ", err)
			os.Exit(1)
		}
		fmt.Println("Failed connecting to the test services, not running tests: ", err)
		return
	}

	common.PQ = conn
	os.Exit(m.Run())
}

func TestLevelCurve(t *testing.T) {
	conf := &Config{LevelBase: 100, LevelStep: 50}

	for level, want := range map[int]int64{0: 0, 1: 100, 2: 250, 3: 450, 10: 3250} {
		if got := conf.XPForLevel(level); got != want {
			t.Errorf("XPForLevel(%d) = %d, want %d", level, got, want)
		}
	}
	for xp, want := range map[int64]int{-5: 0, 0: 0, 99: 0, 100: 1, 249: 1, 250: 2, 449: 2, 450: 3} {
		if got := conf.LevelForXP(xp); got != want {
			t.Errorf("LevelForXP(%d) = %d, want %d", xp, got, want)
		}
	}

	// LevelForXP inverts XPForLevel for every level.
	for level := 0; level <= MaxLevel; level++ {
		xp := conf.XPForLevel(level)
		if got := conf.LevelForXP(xp); got != level {
			t.Fatalf("LevelForXP(XPForLevel(%d)) = %d", level, got)
		}
		if level > 0 {
			if got := conf.LevelForXP(xp - 1); got != level-1 {
				t.Fatalf("LevelForXP(XPForLevel(%d)-1) = %d", level, got)
			}
		}
	}
	if got := conf.LevelForXP(1 << 60); got != MaxLevel {
		t.Errorf("huge xp gives level %d, want the cap %d", got, MaxLevel)
	}

	level, in, size := conf.Progress(300)
	if level != 2 || in != 50 || size != 200 {
		t.Errorf("Progress(300) = %d, %d, %d", level, in, size)
	}
	if _, _, size := conf.Progress(conf.XPForLevel(MaxLevel)); size != 0 {
		t.Errorf("max level must report no next level, got size %d", size)
	}

	flat := &Config{LevelBase: 10, LevelStep: 0}
	if got := flat.LevelForXP(95); got != 9 {
		t.Errorf("flat curve: LevelForXP(95) = %d", got)
	}
}

func TestNormalize(t *testing.T) {
	conf := &Config{
		MessageXPMin: 30, MessageXPMax: 10, LevelBase: 0,
		Rewards: []Reward{
			{Level: 10, Role: 3}, {Level: 0, Role: 9}, {Level: 5, Role: 2}, {Level: 5, Role: 2},
			{Level: 7, Role: 0}, {Level: 2000, Role: 4}, {Level: 1, Role: 1},
		},
	}
	conf.Normalize()

	if conf.MessageXPMin != 10 || conf.MessageXPMax != 30 {
		t.Errorf("min/max not swapped: %d %d", conf.MessageXPMin, conf.MessageXPMax)
	}
	if conf.LevelBase != 1 {
		t.Errorf("LevelBase = %d", conf.LevelBase)
	}
	if conf.AnnounceMessage != DefaultAnnounceMessage {
		t.Errorf("empty announcement not defaulted: %q", conf.AnnounceMessage)
	}
	if got := fmt.Sprint(conf.Rewards); got != "[{1 1} {5 2} {10 3}]" {
		t.Errorf("rewards = %s", got)
	}
}

func TestRewardRoles(t *testing.T) {
	conf := &Config{Rewards: []Reward{{Level: 5, Role: 50}, {Level: 10, Role: 100}, {Level: 20, Role: 200}}, StackRewards: true}

	sorted := func(ids []int64) string {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		return fmt.Sprint(ids)
	}

	keep, remove := conf.RewardRoles(12)
	if sorted(keep) != "[50 100]" || sorted(remove) != "[200]" {
		t.Errorf("stacking at 12: keep %v remove %v", keep, remove)
	}

	conf.StackRewards = false
	keep, remove = conf.RewardRoles(12)
	if sorted(keep) != "[100]" || sorted(remove) != "[50 200]" {
		t.Errorf("not stacking at 12: keep %v remove %v", keep, remove)
	}

	keep, remove = conf.RewardRoles(0)
	if len(keep) != 0 || sorted(remove) != "[50 100 200]" {
		t.Errorf("level 0: keep %v remove %v", keep, remove)
	}

	// The same role on two levels is kept, never both kept and removed.
	conf = &Config{Rewards: []Reward{{Level: 1, Role: 7}, {Level: 3, Role: 7}, {Level: 5, Role: 8}}}
	keep, remove = conf.RewardRoles(3)
	if sorted(keep) != "[7]" || sorted(remove) != "[8]" {
		t.Errorf("shared role: keep %v remove %v", keep, remove)
	}
}

func TestIgnores(t *testing.T) {
	conf := &Config{IgnoredChannels: []int64{10}, IgnoredRoles: []int64{5}}
	if !conf.IsIgnoredChannel(10, 0) || !conf.IsIgnoredChannel(99, 10) || conf.IsIgnoredChannel(99, 0) {
		t.Error("IsIgnoredChannel")
	}
	if !conf.HasIgnoredRole([]int64{1, 5}) || conf.HasIgnoredRole([]int64{1, 2}) || conf.HasIgnoredRole(nil) {
		t.Error("HasIgnoredRole")
	}
}

func TestCapGrant(t *testing.T) {
	cases := []struct{ amount, used, cap, want int64 }{
		{10, 10, 0, 10},  // no cap
		{10, 50, 100, 10}, // under the cap
		{10, 105, 100, 5}, // crosses the cap
		{10, 120, 100, 0}, // already over
		{10, 100, 100, 10},
	}
	for _, c := range cases {
		if got := CapGrant(c.amount, c.used, c.cap); got != c.want {
			t.Errorf("CapGrant(%d, %d, %d) = %d, want %d", c.amount, c.used, c.cap, got, c.want)
		}
	}
}

func TestRenderAndProgressBar(t *testing.T) {
	got := RenderAnnouncement("{user} → livello {level} ({xp} XP)", "<@1>", 3, 450)
	if got != "<@1> → livello 3 (450 XP)" {
		t.Errorf("RenderAnnouncement = %q", got)
	}
	if got := RenderAnnouncement("  ", "<@1>", 2, 0); !strings.Contains(got, "<@1>") || !strings.Contains(got, "2") {
		t.Errorf("default announcement = %q", got)
	}

	if got := ProgressBar(5, 10, 10); got != "▰▰▰▰▰▱▱▱▱▱" {
		t.Errorf("half bar = %q", got)
	}
	if got := ProgressBar(50, 10, 4); got != "▰▰▰▰" {
		t.Errorf("overfull bar = %q", got)
	}
	if got := ProgressBar(0, 0, 3); got != "▰▰▰" {
		t.Errorf("max level bar = %q", got)
	}
}

func TestEligibleVoiceMembers(t *testing.T) {
	const afk, room, lonely, ignored, category = 1, 2, 3, 4, 9
	gs := &dstate.GuildSet{
		GuildState: dstate.GuildState{ID: 100, AfkChannelID: afk},
		Channels: []dstate.ChannelState{
			{ID: room, ParentID: category}, {ID: lonely}, {ID: ignored}, {ID: afk},
		},
		VoiceStates: []discordgo.VoiceState{
			{UserID: 11, ChannelID: room},
			{UserID: 12, ChannelID: room, SelfMute: true},
			{UserID: 13, ChannelID: room, Deaf: true},
			{UserID: 14, ChannelID: room},                 // bot
			{UserID: 15, ChannelID: room},                 // ignored role
			{UserID: 16, ChannelID: room, Suppress: true}, // stage audience
			{UserID: 21, ChannelID: lonely},
			{UserID: 31, ChannelID: ignored},
			{UserID: 32, ChannelID: ignored},
			{UserID: 41, ChannelID: afk},
			{UserID: 42, ChannelID: afk},
			{UserID: 51, ChannelID: 0},
		},
	}
	memberOf := func(userID int64) *dstate.MemberState {
		ms := &dstate.MemberState{User: discordgo.User{ID: userID}, Member: &dstate.MemberFields{}}
		switch userID {
		case 14:
			ms.User.Bot = true
		case 15:
			ms.Member.Roles = []int64{777}
		}
		return ms
	}

	conf := &Config{VoiceMinMembers: 1, VoiceIgnoreMuted: true, VoiceIgnoreDeaf: true,
		IgnoredChannels: []int64{ignored}, IgnoredRoles: []int64{777}}

	ids := func(members []voiceMember) string {
		var list []int64
		for _, m := range members {
			list = append(list, m.vs.UserID)
		}
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		return fmt.Sprint(list)
	}

	if got := ids(EligibleVoiceMembers(conf, gs, memberOf)); got != "[11]" {
		t.Errorf("eligible = %s, want [11]", got)
	}

	conf.VoiceIgnoreMuted, conf.VoiceIgnoreDeaf, conf.VoiceMinMembers = false, false, 0
	if got := ids(EligibleVoiceMembers(conf, gs, memberOf)); got != "[11 12 13 21]" {
		t.Errorf("relaxed rules = %s, want [11 12 13 21]", got)
	}

	conf.VoiceMinMembers = 6
	if got := ids(EligibleVoiceMembers(conf, gs, memberOf)); got != "[]" {
		t.Errorf("bots must not count as company: %s", got)
	}

	conf.VoiceMinMembers, conf.IgnoredChannels = 0, []int64{category}
	if got := ids(EligibleVoiceMembers(conf, gs, memberOf)); got != "[21 31 32]" {
		t.Errorf("ignoring the category of the room = %s", got)
	}
}

func TestExperienceStore(t *testing.T) {
	ctx := context.Background()
	testutils.ClearTables(common.PQ, "custom_levels_users")

	who := Identity{Username: "Mario", Avatar: "https://cdn/a.png"}
	if _, _, err := AddXP(ctx, 1, 10, SourceMessage, 20, 0, who); err != nil {
		t.Fatalf("AddXP: %v", err)
	}
	if _, _, err := AddXP(ctx, 1, 10, SourceReaction, 3, 0, Identity{}); err != nil {
		t.Fatalf("AddXP: %v", err)
	}
	xp, stored, err := AddXP(ctx, 1, 10, SourceVoice, 5, 60, Identity{Username: "Mario R."})
	if err != nil || xp != 28 || stored != 0 {
		t.Fatalf("AddXP voice = %d, %d, %v", xp, stored, err)
	}
	if _, _, err := AddXP(ctx, 1, 10, SourceAdmin, 1, 0, who); err == nil {
		t.Error("AddXP must refuse sources without a column")
	}

	raised, err := RaiseLevel(ctx, 1, 10, 1)
	if err != nil || !raised {
		t.Fatalf("RaiseLevel = %v, %v", raised, err)
	}
	if raised, _ := RaiseLevel(ctx, 1, 10, 1); raised {
		t.Error("raising to the same level twice must report false")
	}

	stats, rank, err := GetUserStats(ctx, 1, 10)
	if err != nil {
		t.Fatalf("GetUserStats: %v", err)
	}
	if stats.XP != 28 || stats.Level != 1 || stats.MessageXP != 20 || stats.ReactionXP != 3 ||
		stats.VoiceXP != 5 || stats.VoiceSeconds != 60 || rank != 1 {
		t.Errorf("stats = %+v rank %d", stats, rank)
	}
	if stats.Username != "Mario R." || stats.Avatar != "https://cdn/a.png" {
		t.Errorf("identity = %q %q (an empty identity must not erase the stored one)", stats.Username, stats.Avatar)
	}

	if _, _, err := AddXP(ctx, 1, 11, SourceMessage, 100, 0, Identity{Username: "Luigi"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AddXP(ctx, 2, 12, SourceMessage, 999, 0, Identity{}); err != nil {
		t.Fatal(err)
	}
	if _, rank, _ := GetUserStats(ctx, 1, 10); rank != 2 {
		t.Errorf("rank after being overtaken = %d", rank)
	}
	if s, rank, err := GetUserStats(ctx, 1, 99); err != nil || s.XP != 0 || rank != 0 {
		t.Errorf("unknown member = %+v, %d, %v", s, rank, err)
	}

	top, err := TopUsers(ctx, 1, 0, 10)
	if err != nil || len(top) != 2 || top[0].UserID != 11 || top[1].UserID != 10 {
		t.Fatalf("TopUsers = %+v, %v", top, err)
	}
	if n, _ := CountRanked(ctx, 1); n != 2 {
		t.Errorf("CountRanked = %d", n)
	}

	if xp, _, err := AdjustXP(ctx, 1, 10, -1000); err != nil || xp != 0 {
		t.Errorf("AdjustXP below zero = %d, %v", xp, err)
	}
	if xp, _, err := SetXP(ctx, 1, 10, 450); err != nil || xp != 450 {
		t.Errorf("SetXP = %d, %v", xp, err)
	}
	if err := StoreLevel(ctx, 1, 10, 0); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := GetUserStats(ctx, 1, 10); s.Level != 0 {
		t.Errorf("StoreLevel must be able to lower the level, got %d", s.Level)
	}
	if xp, _, err := AdjustXP(ctx, 1, 50, 30); err != nil || xp != 30 {
		t.Errorf("AdjustXP on a new member = %d, %v", xp, err)
	}

	if err := ResetUser(ctx, 1, 10); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := GetUserStats(ctx, 1, 10); s.XP != 0 {
		t.Errorf("reset member still has %d XP", s.XP)
	}
}

func TestConfigStorage(t *testing.T) {
	const guildID = 55501

	conf, err := LoadConfig(guildID)
	if err != nil || conf.Enabled || conf.LevelBase != 100 || conf.VoiceDailyCap != 1200 {
		t.Fatalf("defaults = %+v, %v", conf, err)
	}

	// A configuration saved before an option existed keeps that option's default.
	if err := common.RedisPool.Do(radix.Cmd(nil, "SET", configKey(guildID), `{"Enabled":true,"LevelBase":40}`)); err != nil {
		t.Fatal(err)
	}
	conf, err = LoadConfig(guildID)
	if err != nil || !conf.Enabled || conf.LevelBase != 40 || conf.LevelStep != 50 || conf.MessageXPMax != 25 {
		t.Errorf("partial config = %+v, %v", conf, err)
	}

	conf.Rewards = []Reward{{Level: 3, Role: 30}, {Level: 1, Role: 10}}
	if err := SaveConfig(guildID, conf); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(guildID)
	if err != nil || fmt.Sprint(loaded.Rewards) != "[{1 10} {3 30}]" {
		t.Errorf("rewards after save = %v, %v", loaded.Rewards, err)
	}
	cached, err := CachedConfig(guildID)
	if err != nil || !cached.Enabled {
		t.Errorf("CachedConfig = %+v, %v", cached, err)
	}
}

func TestDailyCapAndCooldown(t *testing.T) {
	const guildID, userID = 777001, 42

	// Fresh keys for this run.
	common.RedisPool.Do(radix.Cmd(nil, "DEL", redisKey("cooldown", SourceMessage, id(guildID), id(userID))))

	if !acquireCooldown(guildID, userID, SourceMessage, 60) {
		t.Fatal("first message must not be on cooldown")
	}
	if acquireCooldown(guildID, userID, SourceMessage, 60) {
		t.Error("second message within the cooldown must be refused")
	}
	if !acquireCooldown(guildID, userID, SourceMessage, 0) {
		t.Error("cooldown 0 must never refuse")
	}

	total := int64(0)
	for i := 0; i < 5; i++ {
		total += applyDailyCap(guildID, userID+int64(1000), SourceVoice, 30, 100)
	}
	if total != 100 {
		t.Errorf("daily cap granted %d, want 100", total)
	}
	if got := applyDailyCap(guildID, userID, SourceVoice, 30, 0); got != 30 {
		t.Errorf("no cap granted %d", got)
	}
}
