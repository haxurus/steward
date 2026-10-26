package tempvoice

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/botlabs-gg/yagpdb/v2/bot"
	"github.com/botlabs-gg/yagpdb/v2/bot/eventsystem"
	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/common/pubsub"
	"github.com/botlabs-gg/yagpdb/v2/lib/discordgo"
	"github.com/botlabs-gg/yagpdb/v2/lib/dstate"
	"github.com/mediocregopher/radix/v3"
)

const (
	// A new channel is not deleted for being empty before this: the owner is
	// still being moved in.
	emptyGrace = 20 * time.Second
	// Joining a hub again within this time does not create another channel.
	createCooldown = 10 * time.Second
	operationTimeout = 30 * time.Second

	pubsubPanelEvent = "custom_tempvoice_panel"
)

var _ bot.BotInitHandler = (*Plugin)(nil)

func (p *Plugin) BotInit() {
	eventsystem.AddHandlerAsyncLastLegacy(p, bot.ConcurrentEventHandler(handleVoiceStateUpdate), eventsystem.EventVoiceStateUpdate)
	eventsystem.AddHandlerAsyncLastLegacy(p, bot.ConcurrentEventHandler(handleInteractionCreate), eventsystem.EventInteractionCreate)
	eventsystem.AddHandlerAsyncLastLegacy(p, bot.ConcurrentEventHandler(handleChannelDelete), eventsystem.EventChannelDelete)
	eventsystem.AddHandlerAsyncLastLegacy(p, bot.ConcurrentEventHandler(handleGuildCreate), eventsystem.EventGuildCreate)
	pubsub.AddHandler(pubsubPanelEvent, handlePanelEvent, nil)
}

// handleVoiceStateUpdate runs after the state tracker applied the update, so the
// guild state already reflects the member's new channel.
func handleVoiceStateUpdate(evt *eventsystem.EventData) {
	vs := evt.VoiceStateUpdate()
	if vs == nil || vs.VoiceState == nil || vs.GuildID == 0 {
		return
	}

	conf, err := CachedConfig(vs.GuildID)
	if err != nil {
		logger.WithError(err).WithField("guild", vs.GuildID).Error("failed loading config")
		return
	}

	// Temporary channels are cleaned up even when the feature has been disabled.
	defer sweepGuild(vs.GuildID)

	if !conf.Enabled || !conf.IsHub(vs.ChannelID) {
		return
	}

	gs := bot.State.GetGuild(vs.GuildID)
	if gs == nil {
		return
	}
	ms, err := bot.GetMember(vs.GuildID, vs.UserID)
	if err != nil || ms == nil || ms.User.Bot {
		return
	}

	if err := createForMember(gs, conf, ms, vs.ChannelID); err != nil {
		logger.WithError(err).WithField("guild", vs.GuildID).WithField("user", vs.UserID).Error("failed creating temporary channel")
	}
}

// createForMember gives the member who joined hub their temporary channel: the one
// they already own, or a new one.
func createForMember(gs *dstate.GuildSet, conf *Config, ms *dstate.MemberState, hub int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()

	userID := ms.User.ID
	existing, err := ChannelByOwner(ctx, gs.ID, userID)
	switch {
	case err == nil && gs.GetChannel(existing.ChannelID) != nil:
		return common.BotSession.GuildMemberMove(gs.ID, userID, existing.ChannelID)
	case err == nil:
		// Deleted while the bot was not looking.
		if err := DeleteChannelRow(ctx, existing.ChannelID); err != nil {
			return err
		}
	case !errors.Is(err, ErrNoChannel):
		return err
	}

	if !acquireCreateCooldown(gs.ID, userID) {
		return nil
	}

	parent := conf.Category
	if parent == 0 {
		if hubChannel := gs.GetChannel(hub); hubChannel != nil {
			parent = hubChannel.ParentID
		}
	}
	var categoryOverwrites []discordgo.PermissionOverwrite
	if category := gs.GetChannel(parent); category != nil {
		categoryOverwrites = category.PermissionOverwrites
	}

	name := RenderChannelName(conf.NameTemplate, DisplayName(ms))
	channel, err := createVoiceChannel(gs.ID, name, parent, conf.DefaultUserLimit,
		ChannelOverwrites(categoryOverwrites, userID, common.BotUser.ID))
	if err != nil {
		return err
	}

	err = InsertChannel(ctx, &TempChannel{ChannelID: channel.ID, GuildID: gs.ID, OwnerID: userID, CreatedAt: time.Now()})
	if err != nil {
		common.BotSession.ChannelDelete(channel.ID)
		return err
	}

	// Picks the channel up if the member left before being moved in.
	time.AfterFunc(emptyGrace+5*time.Second, func() { sweepGuild(gs.ID) })

	return common.BotSession.GuildMemberMove(gs.ID, userID, channel.ID)
}

func acquireCreateCooldown(guildID, userID int64) bool {
	var reply string
	key := "custom:tempvoice:cooldown:" + discordgo.StrID(guildID) + ":" + discordgo.StrID(userID)
	err := common.RedisPool.Do(radix.Cmd(&reply, "SET", key, "1", "EX", strconv.Itoa(int(createCooldown.Seconds())), "NX"))
	if err != nil {
		logger.WithError(err).Error("failed setting create cooldown")
		return true
	}
	return reply == "OK"
}

// createVoiceChannel creates the channel with its user limit in one request
// (GuildChannelCreateWithOverwrites cannot set a user limit).
func createVoiceChannel(guildID int64, name string, parentID int64, userLimit int, overwrites []*discordgo.PermissionOverwrite) (*discordgo.Channel, error) {
	data := struct {
		Name                 string                           `json:"name"`
		Type                 discordgo.ChannelType            `json:"type"`
		ParentID             int64                            `json:"parent_id,string,omitempty"`
		UserLimit            int                              `json:"user_limit,omitempty"`
		PermissionOverwrites []*discordgo.PermissionOverwrite `json:"permission_overwrites"`
	}{name, discordgo.ChannelTypeGuildVoice, parentID, userLimit, overwrites}

	endpoint := discordgo.EndpointGuildChannels(guildID)
	body, err := common.BotSession.RequestWithBucketID("POST", endpoint, data, nil, endpoint)
	if err != nil {
		return nil, err
	}

	var channel discordgo.Channel
	if err := json.Unmarshal(body, &channel); err != nil {
		return nil, err
	}
	return &channel, nil
}

// setUserLimit edits the user limit directly: discordgo.ChannelEdit omits 0, which
// is how "no limit" is expressed.
func setUserLimit(channelID int64, limit int) error {
	endpoint := discordgo.EndpointChannel(channelID)
	_, err := common.BotSession.RequestWithBucketID("PATCH", endpoint, map[string]int{"user_limit": limit}, nil, endpoint)
	return err
}

// occupants counts the members (bots excluded) in each voice channel of gs.
func occupants(gs *dstate.GuildSet) map[int64]int {
	counts := make(map[int64]int)
	for _, vs := range gs.VoiceStates {
		if vs.ChannelID == 0 {
			continue
		}
		if ms := bot.State.GetMember(gs.ID, vs.UserID); ms != nil && ms.User.Bot {
			continue
		}
		counts[vs.ChannelID]++
	}
	return counts
}

// sweepGuild deletes empty temporary channels and forgets the ones removed by
// someone else.
func sweepGuild(guildID int64) {
	gs := bot.State.GetGuild(guildID)
	if gs == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()

	channels, err := GuildChannels(ctx, guildID)
	if err != nil {
		logger.WithError(err).WithField("guild", guildID).Error("failed listing temporary channels")
		return
	}
	if len(channels) == 0 {
		return
	}

	counts := occupants(gs)
	for _, c := range channels {
		if gs.GetChannel(c.ChannelID) == nil {
			DeleteChannelRow(ctx, c.ChannelID)
			continue
		}
		if counts[c.ChannelID] > 0 || time.Since(c.CreatedAt) < emptyGrace {
			continue
		}
		if err := deleteTempChannel(ctx, c.ChannelID); err != nil {
			logger.WithError(err).WithField("channel", c.ChannelID).Error("failed deleting empty temporary channel")
		}
	}
}

// deleteTempChannel deletes the Discord channel and its record; a channel that no
// longer exists only loses its record.
func deleteTempChannel(ctx context.Context, channelID int64) error {
	_, err := common.BotSession.ChannelDelete(channelID)
	if err != nil && !common.IsDiscordErr(err, discordgo.ErrCodeUnknownChannel) {
		return err
	}
	return DeleteChannelRow(ctx, channelID)
}

func handleChannelDelete(evt *eventsystem.EventData) {
	cd := evt.ChannelDelete()
	if cd == nil || cd.Channel == nil || cd.Type != discordgo.ChannelTypeGuildVoice {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	if err := DeleteChannelRow(ctx, cd.ID); err != nil {
		logger.WithError(err).WithField("channel", cd.ID).Error("failed forgetting deleted channel")
	}
}

// handleGuildCreate catches up after downtime: channels emptied or deleted while
// the bot was offline.
func handleGuildCreate(evt *eventsystem.EventData) {
	gc := evt.GuildCreate()
	if gc == nil || gc.Guild == nil || gc.Unavailable {
		return
	}
	sweepGuild(gc.ID)
}
