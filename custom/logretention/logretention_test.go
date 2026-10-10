package logretention

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/common/testutils"
	"github.com/botlabs-gg/yagpdb/v2/logs"
	"github.com/mediocregopher/radix/v3"
)

const envName = "YAGPDB_CUSTOM_MESSAGE_LOG_RETENTION_DAYS"

func TestMain(m *testing.M) {
	conn, err := testutils.InitPQ([]string{"message_logs2", "messages2"}, logs.DBSchemas)
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

func setPanelValue(t *testing.T, value string) {
	t.Helper()
	cmd := radix.Cmd(nil, "HDEL", "yagpdb_config", "custom.message_log_retention_days")
	if value != "" {
		cmd = radix.Cmd(nil, "HSET", "yagpdb_config", "custom.message_log_retention_days", value)
	}
	if err := common.RedisPool.Do(cmd); err != nil {
		t.Fatalf("writing redis config: %v", err)
	}
}

func TestRetentionDays(t *testing.T) {
	cases := []struct {
		name  string
		panel string
		env   string
		want  int
	}{
		{name: "default", want: DefaultRetentionDays},
		{name: "environment", env: "45", want: 45},
		{name: "panel wins over environment", panel: "7", env: "45", want: 7},
		{name: "panel zero disables", panel: "0", env: "45", want: 0},
		{name: "invalid panel falls back to environment", panel: "abc", env: "45", want: 45},
		{name: "negative panel falls back to environment", panel: "-3", env: "45", want: 45},
		{name: "invalid everywhere falls back to default", panel: "x", env: "-1", want: DefaultRetentionDays},
		{name: "spaces are trimmed", panel: " 14 ", want: 14},
	}

	defer setPanelValue(t, "")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setPanelValue(t, c.panel)
			t.Setenv(envName, c.env)
			if got := RetentionDays(); got != c.want {
				t.Errorf("RetentionDays() = %d, want %d", got, c.want)
			}
		})
	}
}

func TestPurgeTableDeletesOnlyOlderRows(t *testing.T) {
	testutils.ClearTables(common.PQ, "messages2", "message_logs2")

	now := time.Now()
	ages := map[int64]time.Duration{
		1: 2 * time.Hour,
		2: 9 * 24 * time.Hour,
		3: 11 * 24 * time.Hour,
		4: 400 * 24 * time.Hour,
	}
	for id, age := range ages {
		created := now.Add(-age)
		if _, err := common.PQ.Exec(`INSERT INTO messages2 (id, guild_id, created_at, updated_at, deleted, author_username, author_id, content)
			VALUES ($1, 1, $2, $2, false, 'user', 2, 'hello')`, id, created); err != nil {
			t.Fatalf("inserting message: %v", err)
		}
		if _, err := common.PQ.Exec(`INSERT INTO message_logs2 (id, guild_id, legacy_id, created_at, updated_at, channel_name, channel_id, author_id, author_username)
			VALUES ($1, 1, 0, $2, $2, 'general', 3, 2, 'user')`, id, created); err != nil {
			t.Fatalf("inserting message log: %v", err)
		}
	}

	for _, table := range purgedTables {
		deleted, err := PurgeTable(table, 10)
		if err != nil {
			t.Fatalf("PurgeTable(%s): %v", table, err)
		}
		if deleted != 2 {
			t.Errorf("PurgeTable(%s, 10) deleted %d rows, want 2", table, deleted)
		}

		var remaining []int64
		rows, err := common.PQ.Query("SELECT id FROM " + table + " ORDER BY id")
		if err != nil {
			t.Fatalf("reading %s: %v", table, err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scanning %s: %v", table, err)
			}
			remaining = append(remaining, id)
		}
		rows.Close()
		if fmt.Sprint(remaining) != "[1 2]" {
			t.Errorf("%s keeps %v, want [1 2]", table, remaining)
		}
	}
}

func TestPurgeWithZeroRetentionKeepsEverything(t *testing.T) {
	testutils.ClearTables(common.PQ, "messages2")
	if _, err := common.PQ.Exec(`INSERT INTO messages2 (id, guild_id, created_at, updated_at, deleted, author_username, author_id, content)
		VALUES (1, 1, now() - interval '1000 days', now(), false, 'user', 2, 'old')`); err != nil {
		t.Fatalf("inserting message: %v", err)
	}

	setPanelValue(t, "0")
	defer setPanelValue(t, "")
	purge()

	var n int
	if err := common.PQ.QueryRow("SELECT count(*) FROM messages2").Scan(&n); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if n != 1 {
		t.Errorf("retention 0 must keep logs, %d rows left", n)
	}
}
