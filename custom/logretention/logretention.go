// Package logretention deletes message logs older than a configurable number of
// days. It replaces the fixed 30 day purge of the upstream logs plugin, which the
// production Compose file turns off (YAGPDB_ENABLE_MESSAGE_LOG_PURGE=false).
//
// The retention is the option yagpdb.custom.message_log_retention_days: editable by
// the bot owner in /admin/config, or YAGPDB_CUSTOM_MESSAGE_LOG_RETENTION_DAYS in
// yagpdb.env. Unlike other options it is read again before every purge, so a change
// from the panel applies within the hour without a restart.
package logretention

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/common/backgroundworkers"
	"github.com/botlabs-gg/yagpdb/v2/common/config"
	"github.com/botlabs-gg/yagpdb/v2/logs"
)

const (
	DefaultRetentionDays = 30
	purgeInterval        = time.Hour
)

var (
	confRetentionDays = config.RegisterOption("yagpdb.custom.message_log_retention_days",
		"Message logs older than this many days are deleted, checked every hour; 0 keeps them forever. Applies without restart.",
		DefaultRetentionDays)

	logger = common.GetPluginLogger(&Plugin{})

	// Tables purged by the upstream logs plugin (logs/backgroundworkers.go).
	purgedTables = []string{"messages2", "message_logs2"}
)

type Plugin struct {
	stop chan *sync.WaitGroup
}

func RegisterPlugin() {
	common.RegisterPlugin(&Plugin{stop: make(chan *sync.WaitGroup)})
}

func (p *Plugin) PluginInfo() *common.PluginInfo {
	return &common.PluginInfo{
		Name:     "Message log retention",
		SysName:  "custom_log_retention",
		Category: common.PluginCategoryMisc,
	}
}

var _ backgroundworkers.BackgroundWorkerPlugin = (*Plugin)(nil)

func (p *Plugin) RunBackgroundWorker() {
	if logs.ConfEnableMessageLogPurge.GetBool() {
		logger.Warn("yagpdb.enable_message_log_purge is on: the upstream purge keeps deleting logs older than 30 days whatever the configured retention")
	}

	purge()

	ticker := time.NewTicker(purgeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			purge()
		case wg := <-p.stop:
			wg.Done()
			return
		}
	}
}

func (p *Plugin) StopBackgroundWorker(wg *sync.WaitGroup) {
	p.stop <- wg
}

// RetentionDays returns the configured retention. As in the config package, a value
// saved from /admin/config (Redis) wins over the environment; invalid or negative
// values are skipped with a warning.
func RetentionDays() int {
	sources := []config.ConfigSource{
		&config.RedisConfigStore{Pool: common.RedisPool},
		&config.EnvSource{},
	}

	for _, source := range sources {
		v := source.GetValue(confRetentionDays.Name)
		if v == nil {
			continue
		}

		days, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(v)))
		if err != nil || days < 0 {
			logger.Warnf("ignoring invalid %s=%q from %s", confRetentionDays.Name, v, source.Name())
			continue
		}
		return days
	}

	return DefaultRetentionDays
}

func purge() {
	days := RetentionDays()
	if days == 0 {
		logger.Debug("message log retention is 0: nothing deleted")
		return
	}

	for _, table := range purgedTables {
		if _, err := PurgeTable(table, days); err != nil {
			logger.WithError(err).Errorf("failed deleting old rows from %s", table)
		}
	}
}

// PurgeTable deletes the rows of table created more than days days ago.
func PurgeTable(table string, days int) (int64, error) {
	started := time.Now()
	res, err := common.PQ.Exec("DELETE FROM "+table+" WHERE created_at < now() - make_interval(days => $1)", days)
	if err != nil {
		return 0, err
	}

	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	logger.Infof("deleted %d rows older than %d days from %s in %s", deleted, days, table, time.Since(started))
	return deleted, nil
}
