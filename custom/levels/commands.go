package levels

import (
	"fmt"
	"strings"

	"github.com/botlabs-gg/yagpdb/v2/bot/paginatedmessages"
	"github.com/botlabs-gg/yagpdb/v2/commands"
	"github.com/botlabs-gg/yagpdb/v2/lib/dcmd"
	"github.com/botlabs-gg/yagpdb/v2/lib/discordgo"
	"github.com/botlabs-gg/yagpdb/v2/lib/dstate"
)

const (
	leaderboardPageSize = 10
	embedColor          = 0xF1C40F
)

var _ commands.CommandProvider = (*Plugin)(nil)

func (p *Plugin) AddCommands() {
	commands.AddRootCommands(p, cmdLevel, cmdLeaderboard)

	container, _ := commands.CommandSystem.Root.Sub("xp")
	container.Description = "Gestisci l'esperienza dei membri"
	for _, cmd := range adminCmds {
		commands.AddContainerCommand(container, cmd)
	}
	commands.RegisterSlashCommandsContainer(container, false, func(gs *dstate.GuildSet) ([]int64, error) {
		return nil, nil
	})
}

func disabledReply() string {
	return "Il sistema di livelli non è attivo in questo server."
}

var cmdLevel = &commands.YAGCommand{
	CmdCategory:         commands.CategoryFun,
	Name:                "Livello",
	Aliases:             []string{"rank", "level"},
	Description:         "Mostra livello ed esperienza tua o di un altro membro",
	SlashCommandEnabled: true,
	DefaultEnabled:      true,
	Arguments: []*dcmd.ArgDef{
		{Name: "Utente", Type: dcmd.User},
	},
	RunFunc: func(data *dcmd.Data) (interface{}, error) {
		guildID := data.GuildData.GS.ID
		conf, err := CachedConfig(guildID)
		if err != nil {
			return nil, err
		}
		if !conf.Enabled {
			return disabledReply(), nil
		}

		target := data.Author
		if u := data.Args[0].User(); u != nil {
			target = u
		}
		if target.Bot {
			return "I bot non guadagnano esperienza.", nil
		}

		stats, rank, err := GetUserStats(data.Context(), guildID, target.ID)
		if err != nil {
			return nil, err
		}
		return levelEmbed(conf, target, stats, rank), nil
	},
}

func levelEmbed(conf *Config, user *discordgo.User, stats *UserStats, rank int) *discordgo.MessageEmbed {
	level, inLevel, size := conf.Progress(stats.XP)

	progress := fmt.Sprintf("%s\n%d / %d XP", ProgressBar(inLevel, size, 12), inLevel, size)
	if size == 0 {
		progress = "Livello massimo raggiunto"
	}
	rankStr := "—"
	if rank > 0 {
		rankStr = fmt.Sprintf("#%d", rank)
	}

	name := user.Globalname
	if name == "" {
		name = user.Username
	}

	return &discordgo.MessageEmbed{
		Author:    &discordgo.MessageEmbedAuthor{Name: name, IconURL: user.AvatarURL("128")},
		Color:     embedColor,
		Thumbnail: &discordgo.MessageEmbedThumbnail{URL: user.AvatarURL("256")},
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Livello", Value: fmt.Sprintf("**%d**", level), Inline: true},
			{Name: "Esperienza", Value: fmt.Sprintf("**%d** XP", stats.XP), Inline: true},
			{Name: "Classifica", Value: rankStr, Inline: true},
			{Name: "Prossimo livello", Value: progress},
			{Name: "Messaggi", Value: fmt.Sprintf("%d XP", stats.MessageXP), Inline: true},
			{Name: "Reazioni", Value: fmt.Sprintf("%d XP", stats.ReactionXP), Inline: true},
			{Name: "Voce", Value: fmt.Sprintf("%d XP · %s", stats.VoiceXP, formatVoiceTime(stats.VoiceSeconds)), Inline: true},
		},
	}
}

func formatVoiceTime(seconds int64) string {
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%d min", minutes)
	}
	return fmt.Sprintf("%d h %02d min", minutes/60, minutes%60)
}

var cmdLeaderboard = &commands.YAGCommand{
	CmdCategory:         commands.CategoryFun,
	Name:                "Classifica",
	Aliases:             []string{"leaderboard", "lb"},
	Description:         "Mostra la classifica dei livelli del server",
	SlashCommandEnabled: true,
	DefaultEnabled:      true,
	Arguments: []*dcmd.ArgDef{
		{Name: "Pagina", Type: dcmd.Int, Default: 1},
	},
	RunFunc: func(data *dcmd.Data) (interface{}, error) {
		guildID := data.GuildData.GS.ID
		conf, err := CachedConfig(guildID)
		if err != nil {
			return nil, err
		}
		if !conf.Enabled {
			return disabledReply(), nil
		}

		page := data.Args[0].Int()
		if page < 1 {
			page = 1
		}
		if data.Context().Value(paginatedmessages.CtxKeyNoPagination) != nil {
			return leaderboardPage(conf, guildID, page)
		}
		return paginatedmessages.NewPaginatedResponse(guildID, data.ChannelID, page, 0,
			func(p *paginatedmessages.PaginatedMessage, page int) (*discordgo.MessageEmbed, error) {
				return leaderboardPage(conf, guildID, page)
			}), nil
	},
}

func leaderboardPage(conf *Config, guildID int64, page int) (*discordgo.MessageEmbed, error) {
	ctx, cancel := contextWithTimeout()
	defer cancel()

	offset := (page - 1) * leaderboardPageSize
	users, err := TopUsers(ctx, guildID, offset, leaderboardPageSize)
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		if page == 1 {
			return &discordgo.MessageEmbed{Title: "🏆 Classifica", Description: "Nessuno ha ancora guadagnato esperienza.", Color: embedColor}, nil
		}
		return nil, paginatedmessages.ErrNoResults
	}

	var b strings.Builder
	for i, u := range users {
		position := offset + i + 1
		medal := fmt.Sprintf("`#%d`", position)
		switch position {
		case 1:
			medal = "🥇"
		case 2:
			medal = "🥈"
		case 3:
			medal = "🥉"
		}
		fmt.Fprintf(&b, "%s <@%d> · livello **%d** · %d XP\n", medal, u.UserID, conf.LevelForXP(u.XP), u.XP)
	}

	return &discordgo.MessageEmbed{
		Title:       "🏆 Classifica",
		Description: b.String(),
		Color:       embedColor,
	}, nil
}

// Admin commands, for members with Manage Server.

func adminCommand(name, description string, args []*dcmd.ArgDef, run dcmd.RunFunc) *commands.YAGCommand {
	return &commands.YAGCommand{
		CmdCategory:         commands.CategoryTool,
		Name:                name,
		Description:         description,
		RequiredArgs:        len(args),
		Arguments:           args,
		RequireDiscordPerms: []int64{discordgo.PermissionManageGuild},
		SlashCommandEnabled: true,
		DefaultEnabled:      true,
		RunFunc:             run,
	}
}

var adminCmds = []*commands.YAGCommand{
	adminCommand("Aggiungi", "Aggiunge esperienza a un membro",
		[]*dcmd.ArgDef{{Name: "Utente", Type: dcmd.User}, {Name: "XP", Type: dcmd.Int}},
		func(data *dcmd.Data) (interface{}, error) {
			return adminAdjust(data, int64(data.Args[1].Int()))
		}),
	adminCommand("Togli", "Toglie esperienza a un membro",
		[]*dcmd.ArgDef{{Name: "Utente", Type: dcmd.User}, {Name: "XP", Type: dcmd.Int}},
		func(data *dcmd.Data) (interface{}, error) {
			return adminAdjust(data, -int64(data.Args[1].Int()))
		}),
	adminCommand("Imposta", "Imposta l'esperienza totale di un membro",
		[]*dcmd.ArgDef{{Name: "Utente", Type: dcmd.User}, {Name: "XP", Type: dcmd.Int}},
		func(data *dcmd.Data) (interface{}, error) {
			return adminSet(data, int64(data.Args[1].Int()))
		}),
	adminCommand("Azzera", "Azzera esperienza e livello di un membro",
		[]*dcmd.ArgDef{{Name: "Utente", Type: dcmd.User}},
		func(data *dcmd.Data) (interface{}, error) {
			return adminSet(data, 0)
		}),
}

func adminTarget(data *dcmd.Data) (*Config, *discordgo.User, string, error) {
	conf, err := CachedConfig(data.GuildData.GS.ID)
	if err != nil {
		return nil, nil, "", err
	}
	user := data.Args[0].User()
	if user == nil {
		return nil, nil, "Membro non trovato.", nil
	}
	if user.Bot {
		return nil, nil, "I bot non guadagnano esperienza.", nil
	}
	return conf, user, "", nil
}

func adminAdjust(data *dcmd.Data, delta int64) (interface{}, error) {
	conf, user, msg, err := adminTarget(data)
	if err != nil || msg != "" {
		return msg, err
	}
	if delta < -1_000_000_000 || delta > 1_000_000_000 {
		return "Valore troppo grande.", nil
	}
	xp, _, err := AdjustXP(data.Context(), data.GuildData.GS.ID, user.ID, delta)
	if err != nil {
		return nil, err
	}
	return adminResult(data, conf, user, xp)
}

func adminSet(data *dcmd.Data, value int64) (interface{}, error) {
	conf, user, msg, err := adminTarget(data)
	if err != nil || msg != "" {
		return msg, err
	}
	if value < 0 || value > 1_000_000_000 {
		return "Scrivi un valore tra 0 e 1.000.000.000.", nil
	}
	xp, _, err := SetXP(data.Context(), data.GuildData.GS.ID, user.ID, value)
	if err != nil {
		return nil, err
	}
	return adminResult(data, conf, user, xp)
}

// adminResult stores the level matching xp (it can go down) and fixes the reward
// roles, without announcing anything.
func adminResult(data *dcmd.Data, conf *Config, user *discordgo.User, xp int64) (interface{}, error) {
	guildID := data.GuildData.GS.ID
	level := conf.LevelForXP(xp)
	if err := StoreLevel(data.Context(), guildID, user.ID, level); err != nil {
		return nil, err
	}
	go SyncRewardRoles(conf, guildID, user.ID, level)
	return fmt.Sprintf("<@%d> ora ha **%d** XP (livello **%d**).", user.ID, xp, level), nil
}

