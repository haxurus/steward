package levels

import (
	"context"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/botlabs-gg/yagpdb/v2/bot"
	"github.com/botlabs-gg/yagpdb/v2/bot/eventsystem"
	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/lib/discordgo"
	"github.com/botlabs-gg/yagpdb/v2/lib/dstate"
	"github.com/mediocregopher/radix/v3"
)

const (
	voiceTick        = time.Minute
	operationTimeout = 30 * time.Second
	// Reaction XP is given once per member and message within this time, and the
	// author of a message is remembered this long for "reaction received" XP.
	reactionMemory = 24 * time.Hour
)

// Daily caps reset at midnight, Italian time.
var capLocation = func() *time.Location {
	if loc, err := time.LoadLocation("Europe/Rome"); err == nil {
		return loc
	}
	return time.UTC
}()

var (
	_ bot.BotInitHandler    = (*Plugin)(nil)
	_ bot.BotStopperHandler = (*Plugin)(nil)

	stopVoice = make(chan *sync.WaitGroup)
)

func (p *Plugin) BotInit() {
	eventsystem.AddHandlerAsyncLastLegacy(p, bot.ConcurrentEventHandler(handleMessageCreate), eventsystem.EventMessageCreate)
	eventsystem.AddHandlerAsyncLastLegacy(p, bot.ConcurrentEventHandler(handleReactionAdd), eventsystem.EventMessageReactionAdd)
	go voiceLoop()
}

func (p *Plugin) StopBot(wg *sync.WaitGroup) {
	stopVoice <- wg
}

func redisKey(parts ...string) string {
	return "custom:levels:" + strings.Join(parts, ":")
}

func id(n int64) string { return discordgo.StrID(n) }

// acquireCooldown returns false while the member is still on cooldown for kind.
func acquireCooldown(guildID, userID int64, kind string, seconds int) bool {
	if seconds <= 0 {
		return true
	}
	var reply string
	err := common.RedisPool.Do(radix.Cmd(&reply, "SET", redisKey("cooldown", kind, id(guildID), id(userID)), "1", "EX", strconv.Itoa(seconds), "NX"))
	if err != nil {
		logger.WithError(err).Error("failed setting cooldown")
		return false
	}
	return reply == "OK"
}

// applyDailyCap counts amount towards today's cap of source and returns the part
// that can still be granted.
func applyDailyCap(guildID, userID int64, source string, amount int64, cap int) int64 {
	if cap <= 0 || amount <= 0 {
		return amount
	}
	day := time.Now().In(capLocation).Format("20060102")
	key := redisKey("daily", day, source, id(guildID), id(userID))

	var used int64
	err := common.RedisPool.Do(radix.Pipeline(
		radix.FlatCmd(&used, "INCRBY", key, amount),
		radix.Cmd(nil, "EXPIRE", key, strconv.Itoa(int((48 * time.Hour).Seconds()))),
	))
	if err != nil {
		logger.WithError(err).Error("failed counting daily experience")
		return 0
	}
	return CapGrant(amount, used, int64(cap))
}

// markActive records that the member wrote or reacted, for the voice anti-AFK check.
func markActive(conf *Config, guildID, userID int64) {
	if conf.VoiceActivityWindow <= 0 || conf.VoiceXPPerMinute <= 0 {
		return
	}
	common.RedisPool.Do(radix.Cmd(nil, "SET", redisKey("active", id(guildID), id(userID)), "1",
		"EX", strconv.Itoa(conf.VoiceActivityWindow*60)))
}

func recentlyActive(guildID, userID int64) bool {
	var exists int
	if err := common.RedisPool.Do(radix.Cmd(&exists, "EXISTS", redisKey("active", id(guildID), id(userID)))); err != nil {
		return false
	}
	return exists > 0
}

func randomXP(min, max int) int64 {
	if max <= min {
		return int64(min)
	}
	return int64(min + rand.Intn(max-min+1))
}

func identityOf(u *discordgo.User, nick string) Identity {
	if u == nil {
		return Identity{}
	}
	name := nick
	if name == "" {
		name = u.Globalname
	}
	if name == "" {
		name = u.Username
	}
	return Identity{Username: name, Avatar: u.AvatarURL("128")}
}

func identityOfState(ms *dstate.MemberState) Identity {
	if ms == nil {
		return Identity{}
	}
	nick := ""
	if ms.Member != nil {
		nick = ms.Member.Nick
	}
	user := ms.User
	return identityOf(&user, nick)
}

func memberRoles(ms *dstate.MemberState) []int64 {
	if ms == nil || ms.Member == nil {
		return nil
	}
	return ms.Member.Roles
}

func parentOf(gs *dstate.GuildSet, channelID int64) int64 {
	if gs == nil {
		return 0
	}
	if cs := gs.GetChannel(channelID); cs != nil {
		return cs.ParentID
	}
	return 0
}

func handleMessageCreate(evt *eventsystem.EventData) {
	m := evt.MessageCreate()
	if m == nil || m.Message == nil || m.GuildID == 0 || m.Author == nil || m.Author.Bot || m.WebhookID != 0 {
		return
	}

	conf, err := CachedConfig(m.GuildID)
	if err != nil || !conf.Enabled {
		return
	}

	if conf.ReactionReceivedXP > 0 {
		common.RedisPool.Do(radix.Cmd(nil, "SET", redisKey("author", id(m.ID)), id(m.Author.ID),
			"EX", strconv.Itoa(int(reactionMemory.Seconds()))))
	}
	markActive(conf, m.GuildID, m.Author.ID)

	if conf.MessageXPMax <= 0 || conf.IsIgnoredChannel(m.ChannelID, eventParent(evt)) {
		return
	}
	var roles []int64
	nick := ""
	if m.Member != nil {
		roles = m.Member.Roles
		nick = m.Member.Nick
	}
	if conf.HasIgnoredRole(roles) {
		return
	}
	if utf8.RuneCountInString(strings.TrimSpace(m.Content)) < conf.MessageMinLength {
		return
	}
	if !acquireCooldown(m.GuildID, m.Author.ID, SourceMessage, conf.MessageCooldown) {
		return
	}

	award(conf, m.GuildID, m.Author.ID, SourceMessage, randomXP(conf.MessageXPMin, conf.MessageXPMax), 0,
		identityOf(m.Author, nick), m.ChannelID)
}

func handleReactionAdd(evt *eventsystem.EventData) {
	r := evt.MessageReactionAdd()
	if r == nil || r.MessageReaction == nil || r.GuildID == 0 {
		return
	}

	conf, err := CachedConfig(r.GuildID)
	if err != nil || !conf.Enabled || (conf.ReactionXP <= 0 && conf.ReactionReceivedXP <= 0) {
		return
	}
	if conf.IsIgnoredChannel(r.ChannelID, eventParent(evt)) {
		return
	}

	reactor, err := bot.GetMember(r.GuildID, r.UserID)
	if err != nil || reactor == nil || reactor.User.Bot {
		return
	}
	markActive(conf, r.GuildID, r.UserID)
	if conf.HasIgnoredRole(memberRoles(reactor)) {
		return
	}

	// One reward per member and message, whatever the number of emojis.
	var first string
	err = common.RedisPool.Do(radix.Cmd(&first, "SET", redisKey("reacted", id(r.UserID), id(r.MessageID)), "1",
		"EX", strconv.Itoa(int(reactionMemory.Seconds())), "NX"))
	if err != nil || first != "OK" {
		return
	}

	if conf.ReactionXP > 0 && acquireCooldown(r.GuildID, r.UserID, SourceReaction, conf.ReactionCooldown) {
		award(conf, r.GuildID, r.UserID, SourceReaction, int64(conf.ReactionXP), 0, identityOfState(reactor), 0)
	}

	if conf.ReactionReceivedXP <= 0 {
		return
	}
	var authorStr string
	if err := common.RedisPool.Do(radix.Cmd(&authorStr, "GET", redisKey("author", id(r.MessageID)))); err != nil || authorStr == "" {
		return
	}
	authorID, err := strconv.ParseInt(authorStr, 10, 64)
	if err != nil || authorID == r.UserID {
		return
	}
	author, err := bot.GetMember(r.GuildID, authorID)
	if err != nil || author == nil || author.User.Bot || conf.HasIgnoredRole(memberRoles(author)) {
		return
	}
	award(conf, r.GuildID, authorID, SourceReaction, int64(conf.ReactionReceivedXP), 0, identityOfState(author), 0)
}

func voiceLoop() {
	ticker := time.NewTicker(voiceTick)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			for _, shard := range bot.ReadyTracker.GetProcessShards() {
				for _, gs := range bot.State.GetShardGuilds(int64(shard)) {
					processVoiceGuild(gs)
				}
			}
		case wg := <-stopVoice:
			wg.Done()
			return
		}
	}
}

type voiceMember struct {
	vs discordgo.VoiceState
	ms *dstate.MemberState
}

// EligibleVoiceMembers groups the members of gs earning voice experience this
// minute. Pure apart from the state lookups done by memberOf.
func EligibleVoiceMembers(conf *Config, gs *dstate.GuildSet, memberOf func(userID int64) *dstate.MemberState) []voiceMember {
	byChannel := make(map[int64][]voiceMember)
	for _, vs := range gs.VoiceStates {
		if vs.ChannelID == 0 || vs.ChannelID == gs.AfkChannelID {
			continue
		}
		ms := memberOf(vs.UserID)
		if ms == nil || ms.User.Bot {
			continue
		}
		byChannel[vs.ChannelID] = append(byChannel[vs.ChannelID], voiceMember{vs: vs, ms: ms})
	}

	var result []voiceMember
	for channelID, members := range byChannel {
		if conf.IsIgnoredChannel(channelID, parentOf(gs, channelID)) {
			continue
		}
		others := len(members) - 1
		if others < conf.VoiceMinMembers {
			continue
		}
		for _, m := range members {
			vs := m.vs
			if vs.Suppress ||
				(conf.VoiceIgnoreMuted && (vs.SelfMute || vs.Mute)) ||
				(conf.VoiceIgnoreDeaf && (vs.SelfDeaf || vs.Deaf)) ||
				conf.HasIgnoredRole(memberRoles(m.ms)) {
				continue
			}
			result = append(result, m)
		}
	}
	return result
}

func processVoiceGuild(gs *dstate.GuildSet) {
	if gs == nil || len(gs.VoiceStates) == 0 {
		return
	}
	conf, err := CachedConfig(gs.ID)
	if err != nil || !conf.Enabled || conf.VoiceXPPerMinute <= 0 {
		return
	}

	members := EligibleVoiceMembers(conf, gs, func(userID int64) *dstate.MemberState {
		return bot.State.GetMember(gs.ID, userID)
	})
	for _, m := range members {
		if conf.VoiceActivityWindow > 0 && !recentlyActive(gs.ID, m.vs.UserID) {
			continue
		}
		award(conf, gs.ID, m.vs.UserID, SourceVoice, int64(conf.VoiceXPPerMinute), int64(voiceTick.Seconds()),
			identityOfState(m.ms), 0)
	}
}

// award grants experience (within the daily cap) and handles a level up.
// originChannel is where the experience was earned, for the announcement.
func award(conf *Config, guildID, userID int64, source string, amount, voiceSeconds int64, who Identity, originChannel int64) {
	var cap int
	switch source {
	case SourceMessage:
		cap = conf.MessageDailyCap
	case SourceReaction:
		cap = conf.ReactionDailyCap
	case SourceVoice:
		cap = conf.VoiceDailyCap
	}
	granted := applyDailyCap(guildID, userID, source, amount, cap)
	if granted <= 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()

	xp, stored, err := AddXP(ctx, guildID, userID, source, granted, voiceSeconds, who)
	if err != nil {
		logger.WithError(err).WithField("guild", guildID).WithField("user", userID).Error("failed adding experience")
		return
	}

	level := conf.LevelForXP(xp)
	if level <= stored {
		return
	}
	raised, err := RaiseLevel(ctx, guildID, userID, level)
	if err != nil || !raised {
		return
	}

	SyncRewardRoles(conf, guildID, userID, level)
	announceLevelUp(conf, guildID, userID, level, xp, originChannel)
}

// SyncRewardRoles gives and removes reward roles to match level.
func SyncRewardRoles(conf *Config, guildID, userID int64, level int) {
	if len(conf.Rewards) == 0 {
		return
	}
	ms, err := bot.GetMember(guildID, userID)
	if err != nil || ms == nil {
		return
	}
	has := make(map[int64]bool)
	for _, r := range memberRoles(ms) {
		has[r] = true
	}

	keep, remove := conf.RewardRoles(level)
	for _, role := range keep {
		if !has[role] {
			if err := common.BotSession.GuildMemberRoleAdd(guildID, userID, role); err != nil {
				logger.WithError(err).WithField("guild", guildID).WithField("role", role).Warn("failed giving reward role")
			}
		}
	}
	for _, role := range remove {
		if has[role] {
			if err := common.BotSession.GuildMemberRoleRemove(guildID, userID, role); err != nil {
				logger.WithError(err).WithField("guild", guildID).WithField("role", role).Warn("failed removing reward role")
			}
		}
	}
}

func announceLevelUp(conf *Config, guildID, userID int64, level int, xp int64, originChannel int64) {
	if !conf.AnnounceEnabled {
		return
	}
	channel := conf.AnnounceChannel
	if channel == 0 {
		channel = originChannel
	}
	if channel == 0 {
		return
	}

	content := RenderAnnouncement(conf.AnnounceMessage, "<@"+id(userID)+">", level, xp)
	_, err := common.BotSession.ChannelMessageSendComplex(channel, &discordgo.MessageSend{
		Content:         content,
		AllowedMentions: discordgo.AllowedMentions{Users: []int64{userID}},
	})
	if err != nil {
		logger.WithError(err).WithField("guild", guildID).WithField("channel", channel).Warn("failed announcing level up")
	}
}

func contextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), operationTimeout)
}

// eventParent is the parent of the event's channel: the channel of a thread (or
// the category of a channel), resolved by the event system for threads too.
func eventParent(evt *eventsystem.EventData) int64 {
	if cs := evt.CSOrThread(); cs != nil {
		return cs.ParentID
	}
	return 0
}
