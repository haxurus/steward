package tempvoice

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/common/pubsub"
	"github.com/botlabs-gg/yagpdb/v2/lib/discordgo"
)

var DBSchemas = []string{`
CREATE TABLE IF NOT EXISTS custom_tempvoice_channels (
	channel_id BIGINT PRIMARY KEY,
	guild_id BIGINT NOT NULL,
	owner_id BIGINT NOT NULL,
	created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
);
`, `
CREATE INDEX IF NOT EXISTS custom_tempvoice_channels_guild_idx ON custom_tempvoice_channels(guild_id);
`, `
CREATE UNIQUE INDEX IF NOT EXISTS custom_tempvoice_channels_owner_idx ON custom_tempvoice_channels(guild_id, owner_id);
`}

func configKey(guildID int64) string {
	return "custom:tempvoice:" + discordgo.StrID(guildID)
}

var configCache = common.CacheSet.RegisterSlot("custom_tempvoice_config", func(key interface{}) (interface{}, error) {
	return LoadConfig(key.(int64))
}, int64(0))

// LoadConfig reads the configuration from Redis; a server without one gets the
// defaults (disabled).
func LoadConfig(guildID int64) (*Config, error) {
	conf := &Config{NameTemplate: DefaultNameTemplate}
	err := common.GetRedisJson(configKey(guildID), conf)
	return conf, err
}

// CachedConfig is LoadConfig through the shared cache, evicted on every save.
func CachedConfig(guildID int64) (*Config, error) {
	v, err := configCache.Get(guildID)
	if err != nil {
		return nil, err
	}
	return v.(*Config), nil
}

// SaveConfig stores the configuration and evicts it from every process cache.
func SaveConfig(guildID int64, conf *Config) error {
	if err := common.SetRedisJson(configKey(guildID), conf); err != nil {
		return err
	}
	pubsub.EvictCacheSet(configCache, guildID)
	return nil
}

// TempChannel is a temporary voice channel created by the plugin.
type TempChannel struct {
	ChannelID int64
	GuildID   int64
	OwnerID   int64
	CreatedAt time.Time
}

var ErrNoChannel = errors.New("no temporary channel")

func scanChannel(row interface{ Scan(...interface{}) error }) (*TempChannel, error) {
	var c TempChannel
	err := row.Scan(&c.ChannelID, &c.GuildID, &c.OwnerID, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoChannel
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

const channelColumns = "channel_id, guild_id, owner_id, created_at"

func InsertChannel(ctx context.Context, c *TempChannel) error {
	_, err := common.PQ.ExecContext(ctx,
		"INSERT INTO custom_tempvoice_channels ("+channelColumns+") VALUES ($1, $2, $3, $4)",
		c.ChannelID, c.GuildID, c.OwnerID, c.CreatedAt)
	return err
}

func ChannelByID(ctx context.Context, channelID int64) (*TempChannel, error) {
	return scanChannel(common.PQ.QueryRowContext(ctx,
		"SELECT "+channelColumns+" FROM custom_tempvoice_channels WHERE channel_id = $1", channelID))
}

func ChannelByOwner(ctx context.Context, guildID, ownerID int64) (*TempChannel, error) {
	return scanChannel(common.PQ.QueryRowContext(ctx,
		"SELECT "+channelColumns+" FROM custom_tempvoice_channels WHERE guild_id = $1 AND owner_id = $2", guildID, ownerID))
}

func GuildChannels(ctx context.Context, guildID int64) ([]*TempChannel, error) {
	rows, err := common.PQ.QueryContext(ctx,
		"SELECT "+channelColumns+" FROM custom_tempvoice_channels WHERE guild_id = $1", guildID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*TempChannel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func DeleteChannelRow(ctx context.Context, channelID int64) error {
	_, err := common.PQ.ExecContext(ctx, "DELETE FROM custom_tempvoice_channels WHERE channel_id = $1", channelID)
	return err
}

// SetOwner moves a channel to a new owner. It fails with ErrOwnsChannel when the
// new owner already owns another temporary channel in the server.
func SetOwner(ctx context.Context, channelID, guildID, newOwner int64) error {
	if other, err := ChannelByOwner(ctx, guildID, newOwner); err == nil && other.ChannelID != channelID {
		return ErrOwnsChannel
	} else if err != nil && !errors.Is(err, ErrNoChannel) {
		return err
	}

	_, err := common.PQ.ExecContext(ctx,
		"UPDATE custom_tempvoice_channels SET owner_id = $1 WHERE channel_id = $2", newOwner, channelID)
	return err
}

var ErrOwnsChannel = errors.New("the member already owns a temporary channel")
