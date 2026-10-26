// Package levels is an experience and level system fed by three sources: written
// messages, reactions and time spent in voice channels. Every source has its own
// anti-abuse controls (cooldowns, daily caps, minimum message length, AFK and mute
// rules in voice), and levels can award roles.
//
// Settings live in Redis (custom:levels:<guild>), experience in PostgreSQL
// (custom_levels_users).
package levels

import (
	"sort"
	"strconv"
	"strings"

	"github.com/botlabs-gg/yagpdb/v2/common"
)

// Sources of experience.
const (
	SourceMessage  = "message"
	SourceReaction = "reaction"
	SourceVoice    = "voice"
	SourceAdmin    = "admin"
)

const (
	MaxLevel   = 1000
	MaxRewards = 50

	DefaultAnnounceMessage = "🎉 {user} è salito al livello **{level}**!"
)

var logger = common.GetPluginLogger(&Plugin{})

type Plugin struct{}

func RegisterPlugin() {
	common.RegisterPlugin(&Plugin{})
	common.RegisterDBSchemas("custom_levels", DBSchemas...)
}

func (p *Plugin) PluginInfo() *common.PluginInfo {
	return &common.PluginInfo{
		Name:     "Levels",
		SysName:  "custom_levels",
		Category: common.PluginCategoryMisc,
	}
}

// Reward is a role given when a member reaches Level.
type Reward struct {
	Level int   `valid:"0,1000"` // 0: empty row, dropped by Normalize
	Role  int64 `json:",string" valid:"role,true"`
}

// Config is the per-server configuration, edited from the control panel.
type Config struct {
	Enabled bool

	// Written messages: random XP between min and max, at most once per cooldown,
	// only for messages with at least MessageMinLength characters.
	MessageXPMin     int `valid:"0,1000"`
	MessageXPMax     int `valid:"0,1000"`
	MessageCooldown  int `valid:"0,86400"` // seconds
	MessageMinLength int `valid:"0,2000"`
	MessageDailyCap  int `valid:"0,1000000"` // XP per day, 0 = no cap

	// Reactions: XP for the member reacting (once per message) and for the author
	// of the message receiving it.
	ReactionXP         int `valid:"0,1000"`
	ReactionReceivedXP int `valid:"0,1000"`
	ReactionCooldown   int `valid:"0,86400"` // seconds, per reacting member
	ReactionDailyCap   int `valid:"0,1000000"`

	// Voice: XP for every minute in a voice channel that is not the AFK channel.
	VoiceXPPerMinute int  `valid:"0,1000"`
	VoiceMinMembers  int  `valid:"0,99"` // other members (not bots) required in the channel
	VoiceIgnoreMuted bool // self or server muted members earn nothing
	VoiceIgnoreDeaf  bool // self or server deafened members earn nothing
	// Anti-AFK: earn voice XP only after writing or reacting in the last N minutes
	// (0 disables the check).
	VoiceActivityWindow int `valid:"0,1440"`
	VoiceDailyCap       int `valid:"0,1000000"`

	// No experience in these channels (and their threads) or for these roles.
	IgnoredChannels []int64 `valid:"channel,true"`
	IgnoredRoles    []int64 `valid:"role,true"`

	// Level curve: reaching level n+1 from n takes LevelBase + LevelStep*n XP.
	LevelBase int `valid:"1,1000000"`
	LevelStep int `valid:"0,1000000"`

	Rewards []Reward `valid:"traverse"`
	// Keep the roles of lower levels when a higher reward is reached.
	StackRewards bool

	AnnounceEnabled bool
	// 0 announces in the channel of the message that caused the level up; level
	// ups from reactions or voice are then only announced if a channel is set.
	AnnounceChannel int64  `json:",string" valid:"channel,true"`
	AnnounceMessage string `valid:",500"`

	PublicLeaderboard bool
}

// DefaultConfig is what a server starts from (disabled).
func DefaultConfig() *Config {
	return &Config{
		MessageXPMin:     15,
		MessageXPMax:     25,
		MessageCooldown:  60,
		MessageMinLength: 5,

		ReactionXP:         2,
		ReactionReceivedXP: 3,
		ReactionCooldown:   30,
		ReactionDailyCap:   100,

		VoiceXPPerMinute: 5,
		VoiceMinMembers:  1,
		VoiceIgnoreMuted: true,
		VoiceIgnoreDeaf:  true,
		VoiceDailyCap:    1200,

		LevelBase: 100,
		LevelStep: 50,

		StackRewards:    true,
		AnnounceEnabled: true,
		AnnounceMessage: DefaultAnnounceMessage,
	}
}

// Normalize fixes inconsistent values and sorts the rewards by level.
func (c *Config) Normalize() {
	if c.MessageXPMax < c.MessageXPMin {
		c.MessageXPMin, c.MessageXPMax = c.MessageXPMax, c.MessageXPMin
	}
	if c.LevelBase < 1 {
		c.LevelBase = 1
	}
	if strings.TrimSpace(c.AnnounceMessage) == "" {
		c.AnnounceMessage = DefaultAnnounceMessage
	}

	rewards := make([]Reward, 0, len(c.Rewards))
	seen := make(map[Reward]bool)
	for _, r := range c.Rewards {
		if r.Level < 1 || r.Level > MaxLevel || r.Role == 0 || seen[r] {
			continue
		}
		seen[r] = true
		rewards = append(rewards, r)
	}
	sort.SliceStable(rewards, func(i, j int) bool { return rewards[i].Level < rewards[j].Level })
	if len(rewards) > MaxRewards {
		rewards = rewards[:MaxRewards]
	}
	c.Rewards = rewards
}

// XPForLevel is the total experience needed to reach level n.
func (c *Config) XPForLevel(n int) int64 {
	if n <= 0 {
		return 0
	}
	if n > MaxLevel {
		n = MaxLevel
	}
	base, step, levels := int64(c.LevelBase), int64(c.LevelStep), int64(n)
	return levels*base + step*levels*(levels-1)/2
}

// LevelForXP is the level reached with xp total experience.
func (c *Config) LevelForXP(xp int64) int {
	if xp <= 0 {
		return 0
	}
	// Binary search: XPForLevel is increasing.
	lo, hi := 0, MaxLevel
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if c.XPForLevel(mid) <= xp {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// Progress returns the experience earned inside the current level and the
// experience that level requires.
func (c *Config) Progress(xp int64) (level int, inLevel, levelSize int64) {
	level = c.LevelForXP(xp)
	start := c.XPForLevel(level)
	if level >= MaxLevel {
		return level, xp - start, 0
	}
	return level, xp - start, c.XPForLevel(level+1) - start
}

// RewardRoles returns the reward roles a member at level should have and the ones
// they should not (lower rewards when rewards do not stack, and rewards of higher
// levels).
func (c *Config) RewardRoles(level int) (keep, remove []int64) {
	highest := 0
	for _, r := range c.Rewards {
		if r.Level <= level && r.Level > highest {
			highest = r.Level
		}
	}

	wanted := make(map[int64]bool)
	for _, r := range c.Rewards {
		if r.Level <= level && (c.StackRewards || r.Level == highest) {
			wanted[r.Role] = true
		}
	}
	for _, r := range c.Rewards {
		if wanted[r.Role] {
			keep = appendUnique(keep, r.Role)
		} else {
			remove = appendUnique(remove, r.Role)
		}
	}
	return keep, remove
}

func appendUnique(list []int64, id int64) []int64 {
	for _, v := range list {
		if v == id {
			return list
		}
	}
	return append(list, id)
}

// IsIgnoredChannel reports whether experience is disabled in the channel or in the
// parent of the thread.
func (c *Config) IsIgnoredChannel(channelID, parentID int64) bool {
	for _, id := range c.IgnoredChannels {
		if id == channelID || (parentID != 0 && id == parentID) {
			return true
		}
	}
	return false
}

// HasIgnoredRole reports whether one of roles excludes the member from experience.
func (c *Config) HasIgnoredRole(roles []int64) bool {
	for _, ignored := range c.IgnoredRoles {
		for _, r := range roles {
			if r == ignored {
				return true
			}
		}
	}
	return false
}

// CapGrant returns how much of amount can still be granted when usedAfter is the
// daily total including amount; cap 0 means no cap.
func CapGrant(amount, usedAfter, cap int64) int64 {
	if cap <= 0 || usedAfter <= cap {
		return amount
	}
	over := usedAfter - cap
	if over >= amount {
		return 0
	}
	return amount - over
}

// RenderAnnouncement fills the level up message.
func RenderAnnouncement(template, mention string, level int, xp int64) string {
	if strings.TrimSpace(template) == "" {
		template = DefaultAnnounceMessage
	}
	r := strings.NewReplacer("{user}", mention, "{level}", itoa(int64(level)), "{xp}", itoa(xp))
	return r.Replace(template)
}

// ProgressBar draws the progress inside the current level.
func ProgressBar(inLevel, levelSize int64, width int) string {
	if width <= 0 {
		return ""
	}
	filled := width
	if levelSize > 0 {
		filled = int(inLevel * int64(width) / levelSize)
	}
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("▰", filled) + strings.Repeat("▱", width-filled)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
