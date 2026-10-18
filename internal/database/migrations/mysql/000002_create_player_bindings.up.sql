CREATE TABLE IF NOT EXISTS player_bindings (
    id           BIGINT AUTO_INCREMENT PRIMARY KEY,
    game_uuid    VARCHAR(255) NOT NULL,
    ha_user_uuid VARCHAR(255) NOT NULL DEFAULT '',
    player_name  VARCHAR(255) NOT NULL DEFAULT '',
    cbh          INT NOT NULL DEFAULT 0,
    quick_auth   TINYINT(1) NOT NULL DEFAULT 1,
    last_login   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    claimed_at   DATETIME(3) NULL,
    created_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    UNIQUE INDEX idx_bindings_game_uuid (game_uuid),
    INDEX idx_bindings_ha_user (ha_user_uuid),
    INDEX idx_bindings_cbh (cbh)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;