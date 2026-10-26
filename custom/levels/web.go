package levels

import (
	_ "embed"
	"net/http"

	"github.com/botlabs-gg/yagpdb/v2/common/cplogs"
	"github.com/botlabs-gg/yagpdb/v2/web"
	"goji.io"
	"goji.io/pat"
)

var (
	//go:embed assets/levels.html
	settingsHTML string
	//go:embed assets/levels_leaderboard.html
	leaderboardHTML string
)

var panelLogKeyUpdatedSettings = cplogs.RegisterActionFormat(&cplogs.ActionFormat{
	Key: "custom_levels_settings_updated", FormatString: "Updated level system settings",
})

const publicLeaderboardSize = 100

var _ web.Plugin = (*Plugin)(nil)

// Form is the settings form: the whole configuration.
type Form struct {
	Config `valid:"traverse"`
}

var _ web.SimpleConfigSaver = (*Form)(nil)

func (f Form) Save(guildID int64) error {
	conf := f.Config
	return SaveConfig(guildID, &conf)
}

func (f Form) Name() string {
	return "Levels"
}

func (p *Plugin) InitWeb() {
	web.AddHTMLTemplate("custom/levels/assets/levels.html", settingsHTML)
	web.AddHTMLTemplate("custom/levels/assets/levels_leaderboard.html", leaderboardHTML)
	web.AddSidebarItem(web.SidebarCategoryFun, &web.SidebarItem{
		Name: "Livelli",
		URL:  "levels",
		Icon: "fas fa-trophy",
	})

	mux := goji.SubMux()
	web.CPMux.Handle(pat.New("/levels"), mux)
	web.CPMux.Handle(pat.New("/levels/*"), mux)
	mux.Use(web.RequireBotMemberMW) // for the role pickers

	getHandler := web.RenderHandler(handleGetSettings, "cp_custom_levels")
	mux.Handle(pat.Get(""), getHandler)
	mux.Handle(pat.Get("/"), getHandler)
	mux.Handle(pat.Post(""), web.SimpleConfigSaverHandler(Form{}, getHandler, panelLogKeyUpdatedSettings))
	mux.Handle(pat.Post("/"), web.SimpleConfigSaverHandler(Form{}, getHandler, panelLogKeyUpdatedSettings))

	web.ServerPublicMux.Handle(pat.Get("/levels"), web.RenderHandler(handlePublicLeaderboard, "cp_custom_levels_leaderboard"))
}

func handleGetSettings(w http.ResponseWriter, r *http.Request) interface{} {
	activeGuild, tmpl := web.GetBaseCPContextData(r.Context())

	conf, err := LoadConfig(activeGuild.ID)
	web.CheckErr(tmpl, err, "Failed loading the settings", web.CtxLogger(r.Context()).Error)
	tmpl["Levels"] = conf

	return tmpl
}

// LeaderboardRow is one line of the public leaderboard.
type LeaderboardRow struct {
	Position     int
	Level        int
	VoiceMinutes int64
	*UserStats
}

func handlePublicLeaderboard(w http.ResponseWriter, r *http.Request) interface{} {
	activeGuild, tmpl := web.GetBaseCPContextData(r.Context())

	// LoadConfig always returns a config (the defaults on error), which the
	// template needs.
	conf, err := LoadConfig(activeGuild.ID)
	tmpl["Levels"] = conf
	if web.CheckErr(tmpl, err, "Failed loading the settings", web.CtxLogger(r.Context()).Error) {
		return tmpl
	}
	if !conf.Enabled || !conf.PublicLeaderboard {
		return tmpl
	}

	users, err := TopUsers(r.Context(), activeGuild.ID, 0, publicLeaderboardSize)
	if web.CheckErr(tmpl, err, "Failed loading the leaderboard", web.CtxLogger(r.Context()).Error) {
		return tmpl
	}
	rows := make([]LeaderboardRow, 0, len(users))
	for i, u := range users {
		rows = append(rows, LeaderboardRow{Position: i + 1, Level: conf.LevelForXP(u.XP), VoiceMinutes: u.VoiceSeconds / 60, UserStats: u})
	}
	tmpl["LeaderboardRows"] = rows
	return tmpl
}
