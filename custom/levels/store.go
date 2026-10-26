package levels

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/common/pubsub"
	"github.com/botlabs-gg/yagpdb/v2/lib/discordgo"
)

var DBSchemas = []string{`
CREATE TABLE IF NOT EXISTS custom_levels_users (
	guild_id BIGINT NOT NULL,
	user_id BIGINT NOT NULL,

	xp BIGINT NOT NULL DEFAULT 0,
	level INT NOT NULL DEFAULT 0,

	message_xp BIGINT NOT NULL DEFAULT 0,
	reaction_xp BIGINT NOT NULL DEFAULT 0,
	voice_xp BIGINT NOT NULL DEFAULT 0,
	voice_seconds BIGINT NOT NULL DEFAULT 0,

	username TEXT NOT NULL DEFAULT '',
	avatar TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),

	PRIMARY KEY (guild_id, user_id)
);
`, `
CREATE INDEX IF NOT EXISTS custom_levels_users_rank_idx ON custom_levels_users(guild_id, xp DESC);
`}

// Configuration

func configKey(guildID int64) string {
	return "custom:levels:" + discordgo.StrID(guildID)
}

var configCache = common.CacheSet.RegisterSlot("custom_levels_config", func(key interface{}) (interface{}, error) {
	return LoadConfig(key.(int64))
}, int64(0))

// LoadConfig reads the configuration from Redis on top of the defaults, so options
// added later get their default value on servers configured before.
func LoadConfig(guildID int64) (*Config, error) {
	conf := DefaultConfig()
	err := common.GetRedisJson(configKey(guildID), conf)
	conf.Normalize()
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

func SaveConfig(guildID int64, conf *Config) error {
	conf.Normalize()
	if err := common.SetRedisJson(configKey(guildID), conf); err != nil {
		return err
	}
	pubsub.EvictCacheSet(configCache, guildID)
	return nil
}

// Experience

// UserStats is a member's experience in a server.
type UserStats struct {
	UserID       int64
	XP           int64
	Level        int
	MessageXP    int64
	ReactionXP   int64
	VoiceXP      int64
	VoiceSeconds int64
	Username     string
	Avatar       string
}

var sourceColumns = map[string]string{
	SourceMessage:  "message_xp",
	SourceReaction: "reaction_xp",
	SourceVoice:    "voice_xp",
}

// Identity is the name and avatar shown on the leaderboard, refreshed whenever the
// member earns experience (the web process has no member cache).
type Identity struct {
	Username string
	Avatar   string
}

// AddXP adds amount earned from source and returns the new total and the level
// stored before the call (the caller decides whether that is a level up).
func AddXP(ctx context.Context, guildID, userID int64, source string, amount, voiceSeconds int64, who Identity) (xp int64, storedLevel int, err error) {
	column, ok := sourceColumns[source]
	if !ok {
		return 0, 0, fmt.Errorf("unknown experience source %q", source)
	}

	query := `
INSERT INTO custom_levels_users (guild_id, user_id, xp, ` + column + `, voice_seconds, username, avatar)
VALUES ($1, $2, $3, $3, $4, $5, $6)
ON CONFLICT (guild_id, user_id) DO UPDATE SET
	xp = custom_levels_users.xp + EXCLUDED.xp,
	` + column + ` = custom_levels_users.` + column + ` + EXCLUDED.` + column + `,
	voice_seconds = custom_levels_users.voice_seconds + EXCLUDED.voice_seconds,
	username = CASE WHEN EXCLUDED.username <> '' THEN EXCLUDED.username ELSE custom_levels_users.username END,
	avatar = CASE WHEN EXCLUDED.avatar <> '' THEN EXCLUDED.avatar ELSE custom_levels_users.avatar END,
	updated_at = now()
RETURNING xp, level`

	err = common.PQ.QueryRowContext(ctx, query, guildID, userID, amount, voiceSeconds, who.Username, who.Avatar).Scan(&xp, &storedLevel)
	return xp, storedLevel, err
}

// AdjustXP changes a member's total by delta (admin commands), never below zero.
func AdjustXP(ctx context.Context, guildID, userID, delta int64) (xp int64, storedLevel int, err error) {
	const query = `
INSERT INTO custom_levels_users (guild_id, user_id, xp) VALUES ($1, $2, GREATEST($3, 0))
ON CONFLICT (guild_id, user_id) DO UPDATE SET
	xp = GREATEST(custom_levels_users.xp + $3, 0), updated_at = now()
RETURNING xp, level`
	err = common.PQ.QueryRowContext(ctx, query, guildID, userID, delta).Scan(&xp, &storedLevel)
	return xp, storedLevel, err
}

// SetXP replaces a member's total (admin commands).
func SetXP(ctx context.Context, guildID, userID, value int64) (xp int64, storedLevel int, err error) {
	const query = `
INSERT INTO custom_levels_users (guild_id, user_id, xp) VALUES ($1, $2, GREATEST($3, 0))
ON CONFLICT (guild_id, user_id) DO UPDATE SET xp = GREATEST($3, 0), updated_at = now()
RETURNING xp, level`
	err = common.PQ.QueryRowContext(ctx, query, guildID, userID, value).Scan(&xp, &storedLevel)
	return xp, storedLevel, err
}

// RaiseLevel stores level if it is higher than the stored one. Only the call that
// actually raises it returns true, so concurrent gains announce a level once.
func RaiseLevel(ctx context.Context, guildID, userID int64, level int) (bool, error) {
	res, err := common.PQ.ExecContext(ctx,
		"UPDATE custom_levels_users SET level = $3 WHERE guild_id = $1 AND user_id = $2 AND level < $3",
		guildID, userID, level)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// StoreLevel stores level whatever it was (admin commands can lower a level).
func StoreLevel(ctx context.Context, guildID, userID int64, level int) error {
	_, err := common.PQ.ExecContext(ctx,
		"UPDATE custom_levels_users SET level = $3 WHERE guild_id = $1 AND user_id = $2", guildID, userID, level)
	return err
}

// ResetUser removes a member's experience.
func ResetUser(ctx context.Context, guildID, userID int64) error {
	_, err := common.PQ.ExecContext(ctx, "DELETE FROM custom_levels_users WHERE guild_id = $1 AND user_id = $2", guildID, userID)
	return err
}

const statsColumns = "user_id, xp, level, message_xp, reaction_xp, voice_xp, voice_seconds, username, avatar"

func scanStats(row interface{ Scan(...interface{}) error }) (*UserStats, error) {
	var s UserStats
	err := row.Scan(&s.UserID, &s.XP, &s.Level, &s.MessageXP, &s.ReactionXP, &s.VoiceXP, &s.VoiceSeconds, &s.Username, &s.Avatar)
	return &s, err
}

// GetUserStats returns a member's experience and leaderboard position (1-based);
// a member without experience gets empty stats and rank 0.
func GetUserStats(ctx context.Context, guildID, userID int64) (*UserStats, int, error) {
	stats, err := scanStats(common.PQ.QueryRowContext(ctx,
		"SELECT "+statsColumns+" FROM custom_levels_users WHERE guild_id = $1 AND user_id = $2", guildID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return &UserStats{UserID: userID}, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}

	var higher int
	err = common.PQ.QueryRowContext(ctx,
		"SELECT count(*) FROM custom_levels_users WHERE guild_id = $1 AND (xp > $2 OR (xp = $2 AND user_id < $3))",
		guildID, stats.XP, userID).Scan(&higher)
	return stats, higher + 1, err
}

// TopUsers returns a page of the leaderboard, highest experience first.
func TopUsers(ctx context.Context, guildID int64, offset, limit int) ([]*UserStats, error) {
	rows, err := common.PQ.QueryContext(ctx,
		"SELECT "+statsColumns+" FROM custom_levels_users WHERE guild_id = $1 AND xp > 0 ORDER BY xp DESC, user_id ASC LIMIT $2 OFFSET $3",
		guildID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*UserStats
	for rows.Next() {
		s, err := scanStats(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// CountRanked is the number of members on the leaderboard.
func CountRanked(ctx context.Context, guildID int64) (int, error) {
	var n int
	err := common.PQ.QueryRowContext(ctx, "SELECT count(*) FROM custom_levels_users WHERE guild_id = $1 AND xp > 0", guildID).Scan(&n)
	return n, err
}
