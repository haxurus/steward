package tempvoice

import (
	_ "embed"
	"net/http"

	"github.com/botlabs-gg/yagpdb/v2/common/cplogs"
	"github.com/botlabs-gg/yagpdb/v2/common/pubsub"
	"github.com/botlabs-gg/yagpdb/v2/web"
	"goji.io"
	"goji.io/pat"
)

//go:embed assets/tempvoice.html
var pageHTML string

var panelLogKeyUpdatedSettings = cplogs.RegisterActionFormat(&cplogs.ActionFormat{
	Key: "custom_tempvoice_settings_updated", FormatString: "Updated temporary voice channel settings",
})

var _ web.Plugin = (*Plugin)(nil)

// Form is the settings form; the panel message fields are kept from the stored
// configuration.
type Form struct {
	Enabled          bool
	HubChannels      []int64 `valid:"channel,true"`
	Category         int64   `valid:"channel,true"`
	ControlChannel   int64   `valid:"channel,true"`
	NameTemplate     string  `valid:",100"`
	DefaultUserLimit int     `valid:"0,99"`
}

var _ web.SimpleConfigSaver = (*Form)(nil)

func (f Form) Save(guildID int64) error {
	old, err := LoadConfig(guildID)
	if err != nil {
		return err
	}

	conf := &Config{
		Enabled:          f.Enabled,
		HubChannels:      f.HubChannels,
		Category:         f.Category,
		ControlChannel:   f.ControlChannel,
		NameTemplate:     f.NameTemplate,
		DefaultUserLimit: f.DefaultUserLimit,
		PanelChannel:     old.PanelChannel,
		PanelMessage:     old.PanelMessage,
	}
	if conf.NameTemplate == "" {
		conf.NameTemplate = DefaultNameTemplate
	}
	if err := SaveConfig(guildID, conf); err != nil {
		return err
	}

	// The bot process posts, moves or removes the control panel.
	return pubsub.Publish(pubsubPanelEvent, guildID, nil)
}

func (f Form) Name() string {
	return "Temporary voice channels"
}

func (p *Plugin) InitWeb() {
	web.AddHTMLTemplate("custom/tempvoice/assets/tempvoice.html", pageHTML)
	web.AddSidebarItem(web.SidebarCategoryTools, &web.SidebarItem{
		Name: "Canali temporanei",
		URL:  "tempvoice",
		Icon: "fas fa-microphone-alt",
	})

	mux := goji.SubMux()
	web.CPMux.Handle(pat.New("/tempvoice"), mux)
	web.CPMux.Handle(pat.New("/tempvoice/*"), mux)

	getHandler := web.RenderHandler(handleGetSettings, "cp_custom_tempvoice")
	mux.Handle(pat.Get(""), getHandler)
	mux.Handle(pat.Get("/"), getHandler)
	mux.Handle(pat.Post(""), web.SimpleConfigSaverHandler(Form{}, getHandler, panelLogKeyUpdatedSettings))
	mux.Handle(pat.Post("/"), web.SimpleConfigSaverHandler(Form{}, getHandler, panelLogKeyUpdatedSettings))
}

func handleGetSettings(w http.ResponseWriter, r *http.Request) interface{} {
	activeGuild, tmpl := web.GetBaseCPContextData(r.Context())

	conf, err := LoadConfig(activeGuild.ID)
	web.CheckErr(tmpl, err, "Failed loading the settings", web.CtxLogger(r.Context()).Error)
	tmpl["TempVoice"] = conf

	return tmpl
}
