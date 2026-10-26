package tempvoice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/common/testutils"
	"github.com/botlabs-gg/yagpdb/v2/lib/discordgo"
	"github.com/botlabs-gg/yagpdb/v2/lib/dstate"
)

func TestMain(m *testing.M) {
	conn, err := testutils.InitPQ([]string{"custom_tempvoice_channels"}, DBSchemas)
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

func TestRenderChannelName(t *testing.T) {
	cases := []struct{ template, user, want string }{
		{"Canale di {user}", "Mario", "Canale di Mario"},
		{"", "Mario", "Canale di Mario"},
		{"   ", "Mario", "Canale di Mario"},
		{"🎮 {user} | gioca", "Luigi", "🎮 Luigi | gioca"},
		{"{user}", "", "Canale di"},
		{"Stanza fissa", "Mario", "Stanza fissa"},
	}
	for _, c := range cases {
		if got := RenderChannelName(c.template, c.user); got != c.want {
			t.Errorf("RenderChannelName(%q, %q) = %q, want %q", c.template, c.user, got, c.want)
		}
	}

	long := RenderChannelName("{user}", strings.Repeat("é", 150))
	if n := utf8.RuneCountInString(long); n != MaxNameLength {
		t.Errorf("long name has %d runes, want %d", n, MaxNameLength)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := TruncateRunes("🎧🎧🎧", 2); got != "🎧🎧" {
		t.Errorf("TruncateRunes split a character: %q", got)
	}
	if got := TruncateRunes("ciao", 10); got != "ciao" {
		t.Errorf("TruncateRunes changed a short string: %q", got)
	}
}

func TestDisplayName(t *testing.T) {
	ms := &dstate.MemberState{User: discordgo.User{Username: "user", Globalname: "Global"}}
	if got := DisplayName(ms); got != "Global" {
		t.Errorf("global name: got %q", got)
	}
	ms.Member = &dstate.MemberFields{Nick: "Nick"}
	if got := DisplayName(ms); got != "Nick" {
		t.Errorf("nickname: got %q", got)
	}
	ms.Member.Nick, ms.User.Globalname = "", ""
	if got := DisplayName(ms); got != "user" {
		t.Errorf("username: got %q", got)
	}
	if got := DisplayName(nil); got != "" {
		t.Errorf("nil member: got %q", got)
	}
}

func TestChannelOverwrites(t *testing.T) {
	const owner, botID, everyone, role = 10, 20, 1, 2
	category := []discordgo.PermissionOverwrite{
		{ID: everyone, Type: discordgo.PermissionOverwriteTypeRole, Deny: discordgo.PermissionViewChannel},
		{ID: role, Type: discordgo.PermissionOverwriteTypeRole, Allow: discordgo.PermissionViewChannel},
		{ID: owner, Type: discordgo.PermissionOverwriteTypeMember, Deny: discordgo.PermissionVoiceConnect},
		{ID: botID, Type: discordgo.PermissionOverwriteTypeMember, Deny: discordgo.PermissionViewChannel},
	}

	got := ChannelOverwrites(category, owner, botID)
	if len(got) != 4 {
		t.Fatalf("expected 4 overwrites, got %d: %+v", len(got), got)
	}

	byID := map[int64]*discordgo.PermissionOverwrite{}
	for _, o := range got {
		byID[o.ID] = o
	}
	if o := byID[everyone]; o == nil || o.Deny != discordgo.PermissionViewChannel {
		t.Errorf("category overwrite for @everyone not kept: %+v", o)
	}
	if o := byID[role]; o == nil || o.Allow != discordgo.PermissionViewChannel {
		t.Errorf("category overwrite for the role not kept: %+v", o)
	}
	if o := byID[owner]; o == nil || o.Allow != OwnerPerms || o.Deny != 0 {
		t.Errorf("owner overwrite must replace the category one: %+v", o)
	}
	if o := byID[botID]; o == nil || o.Allow != BotPerms || o.Deny != 0 {
		t.Errorf("bot overwrite must replace the category one: %+v", o)
	}

	// The category slice must not be aliased by the result.
	byID[everyone].Deny = 0
	if category[0].Deny != discordgo.PermissionViewChannel {
		t.Error("ChannelOverwrites returned pointers into the category overwrites")
	}
}

func TestFindOverwriteAndDenyBit(t *testing.T) {
	overwrites := []discordgo.PermissionOverwrite{
		{ID: 5, Type: discordgo.PermissionOverwriteTypeMember, Allow: 1},
		{ID: 5, Type: discordgo.PermissionOverwriteTypeRole, Allow: 2, Deny: 4},
	}
	if allow, deny := FindOverwrite(overwrites, 5, discordgo.PermissionOverwriteTypeRole); allow != 2 || deny != 4 {
		t.Errorf("FindOverwrite matched the wrong type: %d %d", allow, deny)
	}
	if allow, deny := FindOverwrite(overwrites, 9, discordgo.PermissionOverwriteTypeRole); allow != 0 || deny != 0 {
		t.Errorf("missing overwrite must be zero: %d %d", allow, deny)
	}

	connect := discordgo.PermissionVoiceConnect
	allow, deny := DenyBit(connect|discordgo.PermissionVoiceSpeak, 0, connect, true)
	if allow != discordgo.PermissionVoiceSpeak || deny != connect {
		t.Errorf("deny: allow=%d deny=%d", allow, deny)
	}
	allow, deny = DenyBit(allow, deny, connect, false)
	if allow != discordgo.PermissionVoiceSpeak || deny != 0 {
		t.Errorf("undeny: allow=%d deny=%d", allow, deny)
	}
}

func TestParseIDsAndIsHub(t *testing.T) {
	ids := ParseIDs([]string{"123", " 456 ", "x", "0", ""})
	if fmt.Sprint(ids) != "[123 456]" {
		t.Errorf("ParseIDs = %v", ids)
	}

	conf := &Config{HubChannels: []int64{11, 22}}
	if !conf.IsHub(22) || conf.IsHub(33) || conf.IsHub(0) {
		t.Error("IsHub")
	}
}

func TestPanelMessage(t *testing.T) {
	embeds, components := PanelMessage(&Config{HubChannels: []int64{42}})
	if len(embeds) != 1 || !strings.Contains(embeds[0].Description, "<#42>") {
		t.Errorf("the panel must point to the hub channels: %+v", embeds)
	}
	if len(components) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(components))
	}

	seen := map[string]bool{}
	for _, c := range components {
		row, ok := c.(discordgo.ActionsRow)
		if !ok || len(row.Components) == 0 || len(row.Components) > 5 {
			t.Fatalf("invalid row: %#v", c)
		}
		for _, b := range row.Components {
			button := b.(discordgo.Button)
			if !strings.HasPrefix(button.CustomID, customIDPrefix) || len(button.CustomID) > 100 {
				t.Errorf("invalid custom id %q", button.CustomID)
			}
			if seen[button.CustomID] {
				t.Errorf("duplicate custom id %q", button.CustomID)
			}
			seen[button.CustomID] = true
		}
	}
	if len(seen) != 9 {
		t.Errorf("expected 9 buttons, got %d", len(seen))
	}
}

func TestChannelStore(t *testing.T) {
	ctx := context.Background()
	testutils.ClearTables(common.PQ, "custom_tempvoice_channels")

	created := time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)
	for _, c := range []*TempChannel{
		{ChannelID: 100, GuildID: 1, OwnerID: 10, CreatedAt: created},
		{ChannelID: 101, GuildID: 1, OwnerID: 11, CreatedAt: created},
		{ChannelID: 200, GuildID: 2, OwnerID: 10, CreatedAt: created},
	} {
		if err := InsertChannel(ctx, c); err != nil {
			t.Fatalf("InsertChannel: %v", err)
		}
	}

	if err := InsertChannel(ctx, &TempChannel{ChannelID: 102, GuildID: 1, OwnerID: 10, CreatedAt: created}); err == nil {
		t.Error("an owner must not get two channels in the same server")
	}

	c, err := ChannelByOwner(ctx, 1, 10)
	if err != nil || c.ChannelID != 100 || !c.CreatedAt.Equal(created) {
		t.Fatalf("ChannelByOwner = %+v, %v", c, err)
	}
	if _, err := ChannelByOwner(ctx, 1, 99); !errors.Is(err, ErrNoChannel) {
		t.Errorf("unknown owner: %v", err)
	}
	if c, err := ChannelByID(ctx, 200); err != nil || c.GuildID != 2 {
		t.Errorf("ChannelByID = %+v, %v", c, err)
	}

	list, err := GuildChannels(ctx, 1)
	if err != nil || len(list) != 2 {
		t.Fatalf("GuildChannels = %d, %v", len(list), err)
	}

	if err := SetOwner(ctx, 100, 1, 11); !errors.Is(err, ErrOwnsChannel) {
		t.Errorf("transfer to someone who owns a channel: %v", err)
	}
	if err := SetOwner(ctx, 100, 1, 12); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}
	if c, _ := ChannelByID(ctx, 100); c.OwnerID != 12 {
		t.Errorf("owner after transfer = %d", c.OwnerID)
	}
	// The same owner in another server is unaffected.
	if c, err := ChannelByOwner(ctx, 2, 10); err != nil || c.ChannelID != 200 {
		t.Errorf("other server: %+v, %v", c, err)
	}

	if err := DeleteChannelRow(ctx, 100); err != nil {
		t.Fatalf("DeleteChannelRow: %v", err)
	}
	if _, err := ChannelByID(ctx, 100); !errors.Is(err, ErrNoChannel) {
		t.Errorf("deleted channel still found: %v", err)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	const guildID = 987654321

	conf, err := LoadConfig(guildID + 1)
	if err != nil || conf.Enabled || conf.NameTemplate != DefaultNameTemplate {
		t.Fatalf("defaults: %+v, %v", conf, err)
	}

	in := &Config{Enabled: true, HubChannels: []int64{1, 2}, Category: 3, ControlChannel: 4,
		NameTemplate: "{user}", DefaultUserLimit: 5, PanelChannel: 4, PanelMessage: 6}
	if err := SaveConfig(guildID, in); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	out, err := LoadConfig(guildID)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if fmt.Sprintf("%+v", in) != fmt.Sprintf("%+v", out) {
		t.Errorf("round trip changed the config:\n in %+v\nout %+v", in, out)
	}

	cached, err := CachedConfig(guildID)
	if err != nil || !cached.Enabled {
		t.Errorf("CachedConfig = %+v, %v", cached, err)
	}
}
