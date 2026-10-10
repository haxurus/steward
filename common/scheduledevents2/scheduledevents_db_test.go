package scheduledevents2

// Storage tests added by the Haxurus fork. The upstream tests in
// scheduledevents_test.go drive the check loop, which needs a running bot (shard
// readiness and guild state), so they stay skipped; these cover what scheduling
// writes to PostgreSQL and Redis without the bot.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/common/scheduledevents2/models"
	"github.com/botlabs-gg/yagpdb/v2/common/testutils"
	"github.com/mediocregopher/radix/v3"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
)

func TestMain(m *testing.M) {
	conn, err := testutils.InitPQ([]string{"scheduled_events"}, DBSchemas)
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
	boil.SetDB(conn)

	os.Exit(m.Run())
}

type storedTestData struct {
	Flag bool   `json:"flag"`
	Text string `json:"text"`
}

// farFuture is beyond the one hour window that ScheduleEvent flushes to Redis, and
// truncated to the microsecond precision PostgreSQL stores.
func farFuture() time.Time {
	return time.Now().Add(48 * time.Hour).UTC().Truncate(time.Microsecond)
}

func resetEvents(t *testing.T) {
	t.Helper()
	testutils.ClearTables(common.PQ, "scheduled_events")
	if err := common.RedisPool.Do(radix.Cmd(nil, "DEL", "scheduled_events_soon")); err != nil {
		t.Fatalf("clearing redis: %v", err)
	}
}

func countEvents(t *testing.T, mods ...qm.QueryMod) int64 {
	t.Helper()
	n, err := models.ScheduledEvents(mods...).CountG(context.Background())
	if err != nil {
		t.Fatalf("counting events: %v", err)
	}
	return n
}

func TestScheduleEventStoresRow(t *testing.T) {
	resetEvents(t)

	runAt := farFuture()
	input := storedTestData{Flag: true, Text: "hello"}
	if err := ScheduleEvent("haxurus_test", 1001, runAt, input); err != nil {
		t.Fatalf("ScheduleEvent: %v", err)
	}

	events, err := models.ScheduledEvents().AllG(context.Background())
	if err != nil {
		t.Fatalf("loading events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	evt := events[0]
	if evt.EventName != "haxurus_test" || evt.GuildID != 1001 {
		t.Errorf("wrong event: name %q guild %d", evt.EventName, evt.GuildID)
	}
	if !evt.TriggersAt.Equal(runAt) {
		t.Errorf("triggers_at = %v, want %v", evt.TriggersAt, runAt)
	}
	if evt.Processed || evt.Error.Valid {
		t.Errorf("new event must be unprocessed without error, got processed=%v error=%v", evt.Processed, evt.Error)
	}

	var stored storedTestData
	if err := json.Unmarshal(evt.Data, &stored); err != nil {
		t.Fatalf("decoding data %s: %v", evt.Data, err)
	}
	if stored != input {
		t.Errorf("data = %+v, want %+v", stored, input)
	}
}

func TestScheduleEventWithoutDataStoresEmptyObject(t *testing.T) {
	resetEvents(t)

	if err := ScheduleEvent("haxurus_test", 1001, farFuture(), nil); err != nil {
		t.Fatalf("ScheduleEvent: %v", err)
	}

	evt, err := models.ScheduledEvents().OneG(context.Background())
	if err != nil {
		t.Fatalf("loading event: %v", err)
	}

	var stored map[string]interface{}
	if err := json.Unmarshal(evt.Data, &stored); err != nil {
		t.Fatalf("decoding data %s: %v", evt.Data, err)
	}
	if len(stored) != 0 {
		t.Errorf("data = %s, want {}", evt.Data)
	}
}

func TestScheduleEventFlushesOnlySoonEventsToRedis(t *testing.T) {
	resetEvents(t)

	soon := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Microsecond)
	if err := ScheduleEvent("haxurus_soon", 1002, soon, nil); err != nil {
		t.Fatalf("ScheduleEvent (soon): %v", err)
	}
	if err := ScheduleEvent("haxurus_later", 1002, farFuture(), nil); err != nil {
		t.Fatalf("ScheduleEvent (later): %v", err)
	}

	evt, err := models.ScheduledEvents(qm.Where("event_name = ?", "haxurus_soon")).OneG(context.Background())
	if err != nil {
		t.Fatalf("loading event: %v", err)
	}

	var members []string
	if err := common.RedisPool.Do(radix.Cmd(&members, "ZRANGE", "scheduled_events_soon", "0", "-1")); err != nil {
		t.Fatalf("reading redis: %v", err)
	}
	want := fmt.Sprintf("%d:%d", evt.ID, evt.GuildID)
	if len(members) != 1 || members[0] != want {
		t.Fatalf("scheduled_events_soon = %v, want only %q", members, want)
	}

	var score string
	if err := common.RedisPool.Do(radix.Cmd(&score, "ZSCORE", "scheduled_events_soon", want)); err != nil {
		t.Fatalf("reading score: %v", err)
	}
	parsed, err := strconv.ParseFloat(score, 64)
	if err != nil {
		t.Fatalf("parsing score %q: %v", score, err)
	}
	if int64(parsed) != soon.UnixMicro() {
		t.Errorf("score = %s, want %d", score, soon.UnixMicro())
	}

	id, guildID, err := parseIDGuildIDPair(want)
	if err != nil || id != evt.ID || guildID != evt.GuildID {
		t.Errorf("parseIDGuildIDPair(%q) = %d, %d, %v", want, id, guildID, err)
	}
}

func TestScheduleRemoveRoleReplacesPendingEvent(t *testing.T) {
	resetEvents(t)
	ctx := context.Background()

	first := farFuture()
	second := first.Add(time.Hour)
	if err := ScheduleRemoveRole(ctx, 1003, 2001, 3001, first); err != nil {
		t.Fatalf("ScheduleRemoveRole: %v", err)
	}
	if err := ScheduleRemoveRole(ctx, 1003, 2001, 3001, second); err != nil {
		t.Fatalf("ScheduleRemoveRole (again): %v", err)
	}
	if err := ScheduleRemoveRole(ctx, 1003, 2002, 3001, first); err != nil {
		t.Fatalf("ScheduleRemoveRole (other user): %v", err)
	}

	sameMember := qm.Where("event_name = 'std_remove_member_role' AND (data->>'user_id')::bigint = ?", 2001)
	if n := countEvents(t, sameMember); n != 1 {
		t.Fatalf("expected the second schedule to replace the first, found %d events", n)
	}
	evt, err := models.ScheduledEvents(sameMember).OneG(ctx)
	if err != nil {
		t.Fatalf("loading event: %v", err)
	}
	if !evt.TriggersAt.Equal(second) {
		t.Errorf("triggers_at = %v, want the latest schedule %v", evt.TriggersAt, second)
	}

	var data RmoveRoleData
	if err := json.Unmarshal(evt.Data, &data); err != nil {
		t.Fatalf("decoding data: %v", err)
	}
	if data != (RmoveRoleData{GuildID: 1003, UserID: 2001, RoleID: 3001}) {
		t.Errorf("data = %+v", data)
	}

	if err := CancelRemoveRole(ctx, 1003, 2001, 3001); err != nil {
		t.Fatalf("CancelRemoveRole: %v", err)
	}
	if n := countEvents(t, sameMember); n != 0 {
		t.Errorf("cancel left %d events for the member", n)
	}
	if n := countEvents(t, qm.Where("event_name = 'std_remove_member_role'")); n != 1 {
		t.Errorf("cancel must not touch other members, %d events left", n)
	}
}

func TestCancelAddRoleKeepsProcessedEvents(t *testing.T) {
	resetEvents(t)
	ctx := context.Background()

	if err := ScheduleAddRole(ctx, 1004, 2001, 3001, farFuture()); err != nil {
		t.Fatalf("ScheduleAddRole: %v", err)
	}
	if _, err := models.ScheduledEvents().UpdateAllG(ctx, models.M{"processed": true}); err != nil {
		t.Fatalf("marking processed: %v", err)
	}
	if err := ScheduleAddRole(ctx, 1004, 2001, 3001, farFuture()); err != nil {
		t.Fatalf("ScheduleAddRole (again): %v", err)
	}

	addRole := qm.Where("event_name = 'std_add_member_role' AND guild_id = ?", 1004)
	if n := countEvents(t, addRole); n != 2 {
		t.Fatalf("expected the processed event to be kept next to the new one, found %d", n)
	}

	if err := CancelAddRole(ctx, 1004, 2001, 3001); err != nil {
		t.Fatalf("CancelAddRole: %v", err)
	}
	if n := countEvents(t, addRole, qm.Where("processed = false")); n != 0 {
		t.Errorf("cancel left %d pending events", n)
	}
	if n := countEvents(t, addRole, qm.Where("processed = true")); n != 1 {
		t.Errorf("cancel must keep processed events, %d left", n)
	}
}

func TestParseIDGuildIDPair(t *testing.T) {
	cases := []struct {
		pair    string
		id      int64
		guildID int64
		wantErr bool
	}{
		{pair: "12:34", id: 12, guildID: 34},
		{pair: "0:0"},
		{pair: "12", wantErr: true},
		{pair: "x:34", wantErr: true},
		{pair: "12:y", wantErr: true},
	}

	for _, c := range cases {
		id, guildID, err := parseIDGuildIDPair(c.pair)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseIDGuildIDPair(%q): expected an error", c.pair)
			}
			continue
		}
		if err != nil || id != c.id || guildID != c.guildID {
			t.Errorf("parseIDGuildIDPair(%q) = %d, %d, %v", c.pair, id, guildID, err)
		}
	}

	if _, _, err := parseIDGuildIDPair("12"); !errors.Is(err, ErrBadPairLength) {
		t.Errorf("missing guild id must return ErrBadPairLength, got %v", err)
	}
}
