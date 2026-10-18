CREATE TABLE IF NOT EXISTS player_bindings (
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

CREATE INDEX IF NOT EXISTS idx_bindings_game_uuid ON player_bindings(game_uuid);
CREATE INDEX IF NOT EXISTS idx_bindings_ha_user ON player_bindings(ha_user_uuid);
CREATE INDEX IF NOT EXISTS idx_bindings_cbh ON player_bindings(cbh);