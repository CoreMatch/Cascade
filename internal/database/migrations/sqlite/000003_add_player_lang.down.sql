-- SQLite does not support DROP COLUMN directly; recreate table without lang.
CREATE TABLE IF NOT EXISTS player_bindings_backup (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    game_uuid    TEXT    NOT NULL,
    ha_user_uuid TEXT    NOT NULL DEFAULT '',
    player_name  TEXT    NOT NULL DEFAULT '',
    cbh          INTEGER NOT NULL DEFAULT 0,
    quick_auth   INTEGER NOT NULL DEFAULT 1,
    last_login   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    claimed_at   DATETIME,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(game_uuid)
);
INSERT INTO player_bindings_backup SELECT id, game_uuid, ha_user_uuid, player_name, cbh, quick_auth, last_login, claimed_at, created_at FROM player_bindings;
DROP TABLE player_bindings;
ALTER TABLE player_bindings_backup RENAME TO player_bindings;
CREATE INDEX IF NOT EXISTS idx_bindings_game_uuid ON player_bindings(game_uuid);
CREATE INDEX IF NOT EXISTS idx_bindings_ha_user ON player_bindings(ha_user_uuid);
CREATE INDEX IF NOT EXISTS idx_bindings_cbh ON player_bindings(cbh);