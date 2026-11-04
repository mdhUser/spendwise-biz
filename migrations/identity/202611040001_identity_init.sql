-- +goose Up
-- identity shared kernel

CREATE TABLE users (
  id BINARY(16) NOT NULL,
  email VARCHAR(254) NOT NULL,
  email_normalized VARCHAR(254) NOT NULL,
  password_hash VARCHAR(255) NOT NULL,
  display_name VARCHAR(60) NOT NULL DEFAULT '',
  status VARCHAR(16) NOT NULL DEFAULT 'active',
  failed_login_count INT NOT NULL DEFAULT 0,
  locked_until DATETIME(3) NULL,
  password_changed_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  last_login_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY users_email_normalized_key (email_normalized),
  CONSTRAINT users_email_len_chk CHECK (CHAR_LENGTH(email) <= 254),
  CONSTRAINT users_email_norm_chk CHECK (email_normalized = LOWER(TRIM(email_normalized))),
  CONSTRAINT users_status_chk CHECK (status IN ('active', 'locked', 'disabled')),
  CONSTRAINT users_failed_login_chk CHECK (failed_login_count >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE sessions (
  id BINARY(16) NOT NULL,
  user_id BINARY(16) NOT NULL,
  token_hash VARBINARY(32) NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  absolute_expires_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  last_seen_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  revoked_at DATETIME(3) NULL,
  client_label VARCHAR(120) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY sessions_token_hash_key (token_hash),
  KEY sessions_user_active_idx (user_id, last_seen_at),
  KEY sessions_expires_idx (expires_at),
  CONSTRAINT sessions_user_fk FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
  CONSTRAINT sessions_token_len_chk CHECK (OCTET_LENGTH(token_hash) = 32),
  CONSTRAINT sessions_abs_chk CHECK (absolute_expires_at >= expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE invites (
  id BINARY(16) NOT NULL,
  code_hash VARBINARY(32) NOT NULL,
  max_uses INT NOT NULL,
  uses INT NOT NULL DEFAULT 0,
  expires_at DATETIME(3) NOT NULL,
  disabled TINYINT(1) NOT NULL DEFAULT 0,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY invites_code_hash_key (code_hash),
  KEY invites_active_idx (expires_at),
  CONSTRAINT invites_max_chk CHECK (max_uses > 0),
  CONSTRAINT invites_uses_chk CHECK (uses >= 0 AND uses <= max_uses),
  CONSTRAINT invites_disabled_chk CHECK (disabled IN (0, 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE IF EXISTS invites;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
