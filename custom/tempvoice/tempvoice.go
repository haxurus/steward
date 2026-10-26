// Package tempvoice creates a private voice channel for every member who joins one
// of the configured "hub" voice channels, and deletes it once it is empty. Owners
// manage their channel from a single control panel message (buttons, menus and
// modals) that the bot keeps in a configured text channel.
//
// Settings live in Redis (custom:tempvoice:<guild>), the temporary channels in
// PostgreSQL (custom_tempvoice_channels).
package tempvoice

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/lib/discordgo"
	"github.com/botlabs-gg/yagpdb/v2/lib/dstate"
)

const (
	DefaultNameTemplate = "Canale di {user}"
	MaxNameLength       = 100
	MaxUserLimit        = 99
)

// Permissions granted on a temporary channel.
const (
	// The owner: see, join and talk; everything else goes through the control panel,
	// so the owner cannot delete the channel or edit its permissions directly.
	OwnerPerms = discordgo.PermissionViewChannel | discordgo.PermissionVoiceConnect |
		discordgo.PermissionVoiceSpeak | discordgo.PermissionStream | discordgo.PermissionVoiceUseVAD
	// Members invited by the owner.
	GuestPerms = discordgo.PermissionViewChannel | discordgo.PermissionVoiceConnect
	// The bot itself, so locking or hiding a channel never locks the bot out.
	BotPerms = discordgo.PermissionViewChannel | discordgo.PermissionVoiceConnect |
		discordgo.PermissionManageChannels | discordgo.PermissionVoiceMoveMembers
)

var logger = common.GetPluginLogger(&Plugin{})

type Plugin struct{}

func RegisterPlugin() {
	common.RegisterPlugin(&Plugin{})
	common.RegisterDBSchemas("custom_tempvoice", DBSchemas...)
}

func (p *Plugin) PluginInfo() *common.PluginInfo {
	return &common.PluginInfo{
		Name:     "Temporary voice channels",
		SysName:  "custom_tempvoice",
		Category: common.PluginCategoryMisc,
	}
}

// Config is the per-server configuration, edited from the control panel.
type Config struct {
	Enabled bool
	// Voice channels that create a temporary channel when joined.
	HubChannels []int64 `valid:"channel,true"`
	// Category for new channels; 0 uses the category of the hub that was joined.
	Category int64 `json:",string" valid:"channel,true"`
	// Text channel holding the control panel message.
	ControlChannel int64 `json:",string" valid:"channel,true"`
	// Name of new channels; {user} is the owner's display name.
	NameTemplate string `valid:",100"`
	// User limit of new channels, 0 for none.
	DefaultUserLimit int `valid:"0,99"`

	// Managed by the bot, never by the form.
	PanelChannel int64 `json:",string"`
	PanelMessage int64 `json:",string"`
}

// IsHub reports whether channelID is one of the configured hub channels.
func (c *Config) IsHub(channelID int64) bool {
	if channelID == 0 {
		return false
	}
	for _, id := range c.HubChannels {
		if id == channelID {
			return true
		}
	}
	return false
}

// RenderChannelName fills the name template with the owner's display name and
// trims the result to Discord's channel name limit.
func RenderChannelName(template, displayName string) string {
	template = strings.TrimSpace(template)
	if template == "" {
		template = DefaultNameTemplate
	}

	name := strings.TrimSpace(strings.ReplaceAll(template, "{user}", displayName))
	if name == "" {
		name = strings.TrimSpace(strings.ReplaceAll(DefaultNameTemplate, "{user}", displayName))
	}
	return TruncateRunes(name, MaxNameLength)
}

// TruncateRunes cuts s to at most max runes without splitting a character.
func TruncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return strings.TrimSpace(string(runes[:max]))
}

// DisplayName is the name shown in the server: nickname, then global name, then
// username.
func DisplayName(ms *dstate.MemberState) string {
	if ms == nil {
		return ""
	}
	if ms.Member != nil && ms.Member.Nick != "" {
		return ms.Member.Nick
	}
	if ms.User.Globalname != "" {
		return ms.User.Globalname
	}
	return ms.User.Username
}

// ChannelOverwrites returns the permission overwrites of a new temporary channel:
// those of its category (so server-wide rules still apply), then the owner and the
// bot, which replace any category overwrite for the same member.
func ChannelOverwrites(category []discordgo.PermissionOverwrite, ownerID, botID int64) []*discordgo.PermissionOverwrite {
	overwrites := make([]*discordgo.PermissionOverwrite, 0, len(category)+2)
	for _, o := range category {
		if o.Type == discordgo.PermissionOverwriteTypeMember && (o.ID == ownerID || o.ID == botID) {
			continue
		}
		o := o
		overwrites = append(overwrites, &o)
	}

	overwrites = append(overwrites,
		&discordgo.PermissionOverwrite{ID: ownerID, Type: discordgo.PermissionOverwriteTypeMember, Allow: OwnerPerms},
		&discordgo.PermissionOverwrite{ID: botID, Type: discordgo.PermissionOverwriteTypeMember, Allow: BotPerms},
	)
	return overwrites
}

// FindOverwrite returns the allow and deny bits of the overwrite for target.
func FindOverwrite(overwrites []discordgo.PermissionOverwrite, target int64, typ discordgo.PermissionOverwriteType) (allow, deny int64) {
	for _, o := range overwrites {
		if o.ID == target && o.Type == typ {
			return o.Allow, o.Deny
		}
	}
	return 0, 0
}

// DenyBit sets (deny=true) or clears the deny bit perm, removing it from allow too.
func DenyBit(allow, deny, perm int64, denied bool) (int64, int64) {
	allow &^= perm
	if denied {
		deny |= perm
	} else {
		deny &^= perm
	}
	return allow, deny
}

// ParseIDs converts the string IDs of a select menu, skipping invalid ones.
func ParseIDs(values []string) []int64 {
	ids := make([]int64, 0, len(values))
	for _, v := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err == nil && id != 0 {
			ids = append(ids, id)
		}
	}
	return ids
}
