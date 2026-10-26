package tempvoice

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/botlabs-gg/yagpdb/v2/bot"
	"github.com/botlabs-gg/yagpdb/v2/bot/eventsystem"
	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/common/pubsub"
	"github.com/botlabs-gg/yagpdb/v2/lib/discordgo"
	"github.com/botlabs-gg/yagpdb/v2/lib/dstate"
	"github.com/mediocregopher/radix/v3"
)

// Custom IDs of the control panel. Every one starts with customIDPrefix, which is
// how this plugin recognises its interactions.
const (
	customIDPrefix = "tempvoice:"

	actionRename         = "rename"
	actionRenameSubmit   = "rename_submit"
	actionLimit          = "limit"
	actionLimitSubmit    = "limit_submit"
	actionLock           = "lock"
	actionHide           = "hide"
	actionInvite         = "invite"
	actionInviteSelect   = "invite_select"
	actionKick           = "kick"
	actionKickSelect     = "kick_select"
	actionTransfer       = "transfer"
	actionTransferSelect = "transfer_select"
	actionClaim          = "claim"
	actionDelete         = "delete"

	// Discord allows 2 renames of a channel every 10 minutes; past that the request
	// would wait in the rate limiter for minutes.
	renamesPerWindow = 2
	renameWindow     = 10 * time.Minute

	panelColor = 0x5865F2
)

// PanelMessage is the control panel posted in the control channel.
func PanelMessage(conf *Config) ([]*discordgo.MessageEmbed, []discordgo.TopLevelComponent) {
	var hubs []string
	for _, id := range conf.HubChannels {
		hubs = append(hubs, "<#"+discordgo.StrID(id)+">")
	}
	join := "un canale **Crea canale**"
	if len(hubs) > 0 {
		join = strings.Join(hubs, ", ")
	}

	embed := &discordgo.MessageEmbed{
		Title: "🎧 Canali vocali temporanei",
		Description: "Entra in " + join + " per avere un canale vocale tutto tuo: viene creato all'istante " +
			"e cancellato quando si svuota. Gestiscilo con i pulsanti qui sotto.\n\n" +
			"✏️ **Rinomina** · 👥 **Limite** utenti · 🔒 **Blocca** / sblocca l'ingresso · 👁️ **Nascondi** / mostra\n" +
			"➕ **Invita** · 👢 **Espelli** · 🔁 **Trasferisci** la proprietà · 🙋 **Rivendica** un canale il cui proprietario è uscito · 🗑️ **Elimina**",
		Color: panelColor,
	}

	button := func(action, label, emoji string, style discordgo.ButtonStyle) discordgo.InteractiveComponent {
		return discordgo.Button{
			Label:    label,
			Style:    style,
			CustomID: customIDPrefix + action,
			Emoji:    &discordgo.ComponentEmoji{Name: emoji},
		}
	}

	components := []discordgo.TopLevelComponent{
		discordgo.ActionsRow{Components: []discordgo.InteractiveComponent{
			button(actionRename, "Rinomina", "✏️", discordgo.SecondaryButton),
			button(actionLimit, "Limite", "👥", discordgo.SecondaryButton),
			button(actionLock, "Blocca/Sblocca", "🔒", discordgo.SecondaryButton),
			button(actionHide, "Nascondi/Mostra", "👁️", discordgo.SecondaryButton),
		}},
		discordgo.ActionsRow{Components: []discordgo.InteractiveComponent{
			button(actionInvite, "Invita", "➕", discordgo.SuccessButton),
			button(actionKick, "Espelli", "👢", discordgo.SecondaryButton),
			button(actionTransfer, "Trasferisci", "🔁", discordgo.SecondaryButton),
			button(actionClaim, "Rivendica", "🙋", discordgo.PrimaryButton),
			button(actionDelete, "Elimina", "🗑️", discordgo.DangerButton),
		}},
	}
	return []*discordgo.MessageEmbed{embed}, components
}

// EnsurePanel posts, moves or removes the control panel to match the configuration.
// It runs on the bot process, after every settings save.
func EnsurePanel(guildID int64) error {
	conf, err := LoadConfig(guildID)
	if err != nil {
		return err
	}

	wanted := conf.Enabled && conf.ControlChannel != 0
	if conf.PanelMessage != 0 && (!wanted || conf.PanelChannel != conf.ControlChannel) {
		if err := common.BotSession.ChannelMessageDelete(conf.PanelChannel, conf.PanelMessage); err != nil &&
			!common.IsDiscordErr(err, discordgo.ErrCodeUnknownMessage, discordgo.ErrCodeUnknownChannel) {
			logger.WithError(err).WithField("guild", guildID).Warn("failed deleting old control panel")
		}
		conf.PanelChannel, conf.PanelMessage = 0, 0
		if !wanted {
			return SaveConfig(guildID, conf)
		}
	}
	if !wanted {
		return nil
	}

	embeds, components := PanelMessage(conf)
	if conf.PanelMessage != 0 {
		empty := ""
		_, err := common.BotSession.ChannelMessageEditComplex(&discordgo.MessageEdit{
			ID: conf.PanelMessage, Channel: conf.PanelChannel, Content: &empty, Embeds: embeds, Components: components,
		})
		if err == nil {
			return nil
		}
		if !common.IsDiscordErr(err, discordgo.ErrCodeUnknownMessage) {
			return err
		}
	}

	msg, err := common.BotSession.ChannelMessageSendComplex(conf.ControlChannel, &discordgo.MessageSend{
		Embeds: embeds, Components: components,
	})
	if err != nil {
		return err
	}
	conf.PanelChannel, conf.PanelMessage = conf.ControlChannel, msg.ID
	return SaveConfig(guildID, conf)
}

func handlePanelEvent(evt *pubsub.Event) {
	guildID := evt.TargetGuildInt
	if guildID == 0 || !bot.ReadyTracker.IsGuildShardReady(guildID) {
		return
	}
	if err := EnsurePanel(guildID); err != nil {
		logger.WithError(err).WithField("guild", guildID).Error("failed updating the control panel")
	}
}

// interaction is one control panel click, modal submission or menu selection.
type interaction struct {
	ic     *discordgo.InteractionCreate
	gs     *dstate.GuildSet
	conf   *Config
	userID int64
	ctx    context.Context
}

func handleInteractionCreate(evt *eventsystem.EventData) {
	ic := evt.InteractionCreate()
	if ic == nil || ic.GuildID == 0 || ic.Member == nil || ic.Member.User == nil {
		return
	}

	var customID string
	switch ic.Type {
	case discordgo.InteractionMessageComponent:
		customID = ic.MessageComponentData().CustomID
	case discordgo.InteractionModalSubmit:
		customID = ic.ModalSubmitData().CustomID
	default:
		return
	}
	action, ok := strings.CutPrefix(customID, customIDPrefix)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()

	in := &interaction{ic: ic, userID: ic.Member.User.ID, ctx: ctx}
	in.gs = bot.State.GetGuild(ic.GuildID)
	conf, err := CachedConfig(ic.GuildID)
	if in.gs == nil || err != nil {
		in.reply("Riprova tra qualche secondo.")
		return
	}
	if !conf.Enabled {
		in.reply("I canali temporanei sono disattivati in questo server.")
		return
	}
	in.conf = conf

	if err := in.dispatch(action); err != nil {
		logger.WithError(err).WithField("guild", ic.GuildID).WithField("action", action).Error("control panel action failed")
		in.finish("Qualcosa è andato storto, riprova. Se continua, controlla che il bot abbia i permessi *Gestisci canali*, *Gestisci permessi* e *Sposta membri*.")
	}
}

func (in *interaction) dispatch(action string) error {
	switch action {
	case actionClaim:
		return in.claim()
	}

	channel, err := in.ownedChannel()
	if err != nil {
		return err
	}
	if channel == nil {
		in.reply(in.noChannelMessage())
		return nil
	}

	switch action {
	case actionRename:
		return in.showTextModal(actionRenameSubmit, "Rinomina il canale", "Nuovo nome", in.channelName(channel), 1, MaxNameLength)
	case actionRenameSubmit:
		return in.rename(channel, in.modalValue())
	case actionLimit:
		return in.showTextModal(actionLimitSubmit, "Limite utenti", "Numero massimo di utenti (0 = nessun limite)", "", 1, 2)
	case actionLimitSubmit:
		return in.setLimit(channel, in.modalValue())
	case actionLock:
		return in.toggleEveryone(channel, discordgo.PermissionVoiceConnect, "🔒 Canale bloccato: entra solo chi inviti.", "🔓 Canale sbloccato.")
	case actionHide:
		return in.toggleEveryone(channel, discordgo.PermissionViewChannel, "👁️ Canale nascosto: lo vede solo chi inviti.", "👁️ Canale di nuovo visibile.")
	case actionInvite:
		return in.showUserSelect(actionInviteSelect, "Chi vuoi invitare?", 10)
	case actionInviteSelect:
		return in.invite(channel, in.selectedIDs())
	case actionKick:
		return in.showUserSelect(actionKickSelect, "Chi vuoi espellere?", 10)
	case actionKickSelect:
		return in.kick(channel, in.selectedIDs())
	case actionTransfer:
		return in.showUserSelect(actionTransferSelect, "A chi vuoi passare il canale?", 1)
	case actionTransferSelect:
		return in.transfer(channel, in.selectedIDs())
	case actionDelete:
		in.deferReply()
		if err := deleteTempChannel(in.ctx, channel.ChannelID); err != nil {
			return err
		}
		in.finish("🗑️ Canale eliminato.")
		return nil
	}
	return nil
}

// ownedChannel returns the clicker's temporary channel, or nil if they own none.
func (in *interaction) ownedChannel() (*TempChannel, error) {
	channel, err := ChannelByOwner(in.ctx, in.gs.ID, in.userID)
	if errors.Is(err, ErrNoChannel) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if in.gs.GetChannel(channel.ChannelID) == nil {
		DeleteChannelRow(in.ctx, channel.ChannelID)
		return nil, nil
	}
	return channel, nil
}

func (in *interaction) noChannelMessage() string {
	if len(in.conf.HubChannels) > 0 {
		return "Non hai un canale temporaneo: entra in <#" + discordgo.StrID(in.conf.HubChannels[0]) + "> per crearne uno."
	}
	return "Non hai un canale temporaneo."
}

func (in *interaction) channelName(c *TempChannel) string {
	if ch := in.gs.GetChannel(c.ChannelID); ch != nil {
		return ch.Name
	}
	return ""
}

func (in *interaction) rename(c *TempChannel, name string) error {
	name = TruncateRunes(strings.TrimSpace(name), MaxNameLength)
	if name == "" {
		in.reply("Il nome non può essere vuoto.")
		return nil
	}
	if wait := renameWait(c.ChannelID); wait > 0 {
		in.reply(fmt.Sprintf("Discord permette 2 cambi di nome ogni 10 minuti: riprova tra %d minuti.", int(wait.Minutes())+1))
		return nil
	}

	in.deferReply()
	if _, err := common.BotSession.ChannelEdit(c.ChannelID, name); err != nil {
		return err
	}
	in.finish("✏️ Canale rinominato in **" + name + "**.")
	return nil
}

// renameWait counts a rename of channelID and returns how long to wait when the
// Discord limit has been reached (0 means go ahead).
func renameWait(channelID int64) time.Duration {
	key := "custom:tempvoice:renames:" + discordgo.StrID(channelID)
	var count int
	err := common.RedisPool.Do(radix.Pipeline(
		radix.Cmd(&count, "INCR", key),
		radix.Cmd(nil, "EXPIRE", key, strconv.Itoa(int(renameWindow.Seconds())), "NX"),
	))
	if err != nil {
		logger.WithError(err).Error("failed counting renames")
		return 0
	}
	if count <= renamesPerWindow {
		return 0
	}

	var ttl int
	common.RedisPool.Do(radix.Cmd(&ttl, "TTL", key))
	if ttl <= 0 {
		return time.Minute
	}
	return time.Duration(ttl) * time.Second
}

func (in *interaction) setLimit(c *TempChannel, value string) error {
	limit, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || limit < 0 || limit > MaxUserLimit {
		in.reply(fmt.Sprintf("Scrivi un numero da 0 a %d (0 = nessun limite).", MaxUserLimit))
		return nil
	}

	in.deferReply()
	if err := setUserLimit(c.ChannelID, limit); err != nil {
		return err
	}
	if limit == 0 {
		in.finish("👥 Limite utenti rimosso.")
	} else {
		in.finish(fmt.Sprintf("👥 Limite impostato a **%d** utenti.", limit))
	}
	return nil
}

// toggleEveryone flips the deny of perm for @everyone (the role with the guild's
// ID) and reports the new state.
func (in *interaction) toggleEveryone(c *TempChannel, perm int64, deniedMsg, allowedMsg string) error {
	channel := in.gs.GetChannel(c.ChannelID)
	allow, deny := FindOverwrite(channel.PermissionOverwrites, in.gs.ID, discordgo.PermissionOverwriteTypeRole)
	denied := deny&perm == 0

	in.deferReply()
	allow, deny = DenyBit(allow, deny, perm, denied)
	if err := common.BotSession.ChannelPermissionSet(c.ChannelID, in.gs.ID, discordgo.PermissionOverwriteTypeRole, allow, deny); err != nil {
		return err
	}
	if denied {
		in.finish(deniedMsg)
	} else {
		in.finish(allowedMsg)
	}
	return nil
}

func (in *interaction) invite(c *TempChannel, users []int64) error {
	in.deferUpdate()
	var invited []string
	for _, id := range users {
		if id == in.userID || in.isBot(id) {
			continue
		}
		if err := common.BotSession.ChannelPermissionSet(c.ChannelID, id, discordgo.PermissionOverwriteTypeMember, GuestPerms, 0); err != nil {
			return err
		}
		invited = append(invited, "<@"+discordgo.StrID(id)+">")
	}
	if len(invited) == 0 {
		in.finish("Nessuno da invitare.")
		return nil
	}
	in.finish("➕ Ora possono vedere ed entrare in <#" + discordgo.StrID(c.ChannelID) + ">: " + strings.Join(invited, ", "))
	return nil
}

func (in *interaction) kick(c *TempChannel, users []int64) error {
	in.deferUpdate()
	var kicked []string
	for _, id := range users {
		if id == in.userID || id == common.BotUser.ID {
			continue
		}
		if err := common.BotSession.ChannelPermissionSet(c.ChannelID, id, discordgo.PermissionOverwriteTypeMember, 0, GuestPerms); err != nil {
			return err
		}
		if vs := in.gs.GetVoiceState(id); vs != nil && vs.ChannelID == c.ChannelID {
			if err := common.BotSession.GuildMemberMove(in.gs.ID, id, 0); err != nil {
				return err
			}
		}
		kicked = append(kicked, "<@"+discordgo.StrID(id)+">")
	}
	if len(kicked) == 0 {
		in.finish("Nessuno da espellere.")
		return nil
	}
	in.finish("👢 Espulsi e bloccati: " + strings.Join(kicked, ", "))
	return nil
}

func (in *interaction) transfer(c *TempChannel, users []int64) error {
	if len(users) != 1 || users[0] == in.userID || in.isBot(users[0]) {
		in.updateMessage("Scegli un altro membro del server (non un bot).")
		return nil
	}
	in.deferUpdate()
	if err := in.changeOwner(c, users[0]); err != nil {
		if errors.Is(err, ErrOwnsChannel) {
			in.finish("Quel membro ha già un suo canale temporaneo.")
			return nil
		}
		return err
	}
	in.finish("🔁 Ora <#" + discordgo.StrID(c.ChannelID) + "> appartiene a <@" + discordgo.StrID(users[0]) + ">.")
	return nil
}

// claim lets a member in a temporary channel take it over once its owner has left.
func (in *interaction) claim() error {
	vs := in.gs.GetVoiceState(in.userID)
	if vs == nil || vs.ChannelID == 0 {
		in.reply("Entra nel canale temporaneo che vuoi rivendicare.")
		return nil
	}
	channel, err := ChannelByID(in.ctx, vs.ChannelID)
	if errors.Is(err, ErrNoChannel) {
		in.reply("Il canale in cui sei non è un canale temporaneo.")
		return nil
	}
	if err != nil {
		return err
	}
	if channel.OwnerID == in.userID {
		in.reply("Questo canale è già tuo.")
		return nil
	}
	if ownerVS := in.gs.GetVoiceState(channel.OwnerID); ownerVS != nil && ownerVS.ChannelID == channel.ChannelID {
		in.reply("Il proprietario è ancora nel canale.")
		return nil
	}

	in.deferReply()
	if err := in.changeOwner(channel, in.userID); err != nil {
		if errors.Is(err, ErrOwnsChannel) {
			in.finish("Hai già un tuo canale temporaneo: eliminalo prima di rivendicarne un altro.")
			return nil
		}
		return err
	}
	in.finish("🙋 Ora questo canale è tuo.")
	return nil
}

// changeOwner gives the channel to newOwner; the previous owner keeps guest access.
func (in *interaction) changeOwner(c *TempChannel, newOwner int64) error {
	if err := SetOwner(in.ctx, c.ChannelID, in.gs.ID, newOwner); err != nil {
		return err
	}
	if err := common.BotSession.ChannelPermissionSet(c.ChannelID, newOwner, discordgo.PermissionOverwriteTypeMember, OwnerPerms, 0); err != nil {
		return err
	}
	return common.BotSession.ChannelPermissionSet(c.ChannelID, c.OwnerID, discordgo.PermissionOverwriteTypeMember, GuestPerms, 0)
}

func (in *interaction) isBot(userID int64) bool {
	if userID == common.BotUser.ID {
		return true
	}
	ms, err := bot.GetMember(in.gs.ID, userID)
	return err == nil && ms != nil && ms.User.Bot
}

func (in *interaction) modalValue() string {
	if in.ic.Type != discordgo.InteractionModalSubmit {
		return ""
	}
	for _, component := range in.ic.ModalSubmitData().Components {
		switch c := component.(type) {
		case *discordgo.Label:
			if input, ok := c.Component.(*discordgo.TextInput); ok {
				return input.Value
			}
		case *discordgo.ActionsRow:
			for _, inner := range c.Components {
				if input, ok := inner.(*discordgo.TextInput); ok {
					return input.Value
				}
			}
		}
	}
	return ""
}

func (in *interaction) selectedIDs() []int64 {
	if in.ic.Type != discordgo.InteractionMessageComponent {
		return nil
	}
	return ParseIDs(in.ic.MessageComponentData().Values)
}

// Responses. Every one is ephemeral: only the clicker sees it.

func (in *interaction) respond(resp *discordgo.InteractionResponse) {
	if err := common.BotSession.CreateInteractionResponse(in.ic.ID, in.ic.Token, resp); err != nil {
		logger.WithError(err).WithField("guild", in.ic.GuildID).Warn("failed responding to interaction")
	}
}

func (in *interaction) reply(content string) {
	in.respond(&discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content, Flags: discordgo.MessageFlagsEphemeral},
	})
}

// deferReply acknowledges a slow action; finish then fills in the answer.
func (in *interaction) deferReply() {
	in.respond(&discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	})
}

// deferUpdate acknowledges a menu selection; finish then replaces the menu message.
func (in *interaction) deferUpdate() {
	in.respond(&discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate})
}

func (in *interaction) updateMessage(content string) {
	in.respond(&discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{Content: content, Components: []discordgo.TopLevelComponent{}},
	})
}

// finish writes the answer of a deferred interaction (or of a failed one). It falls
// back to a new ephemeral reply when nothing was deferred yet.
func (in *interaction) finish(content string) {
	_, err := common.BotSession.EditOriginalInteractionResponse(in.ic.ApplicationID, in.ic.Token, &discordgo.WebhookParams{
		Content: content, Components: []discordgo.TopLevelComponent{},
	})
	if err != nil {
		in.reply(content)
	}
}

func (in *interaction) showTextModal(action, title, label, value string, minLength, maxLength int) error {
	in.respond(&discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: customIDPrefix + action,
			Title:    title,
			Components: []discordgo.TopLevelComponent{
				discordgo.Label{Label: label, Component: discordgo.TextInput{
					CustomID:  "value",
					Style:     discordgo.TextInputShort,
					Value:     value,
					Required:  true,
					MinLength: minLength,
					MaxLength: maxLength,
				}},
			},
		},
	})
	return nil
}

func (in *interaction) showUserSelect(action, placeholder string, maxValues int) error {
	minValues := 1
	in.respond(&discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
			Components: []discordgo.TopLevelComponent{
				discordgo.ActionsRow{Components: []discordgo.InteractiveComponent{
					discordgo.SelectMenu{
						MenuType:    discordgo.UserSelectMenu,
						CustomID:    customIDPrefix + action,
						Placeholder: placeholder,
						MinValues:   &minValues,
						MaxValues:   maxValues,
					},
				}},
			},
		},
	})
	return nil
}
