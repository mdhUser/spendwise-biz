-- +goose Up
-- ledger module. owner_id references spendwise-auth users.id (VARCHAR(191))
-- but deliberately has no cross-service foreign key: auth owns its schema and
-- the two services only communicate over HTTP.

-- One-time rebuild: the previous identity-coupled ledger schema is replaced.
DROP TABLE IF EXISTS saving_goal_term_categories;
DROP TABLE IF EXISTS saving_goal_terms;
DROP TABLE IF EXISTS saving_goals;
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS idempotency_records;
DROP TABLE IF EXISTS transactions;
DROP TABLE IF EXISTS categories;
DROP TABLE IF EXISTS accounts;

CREATE TABLE accounts (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  name VARCHAR(40) NOT NULL,
  kind VARCHAR(16) NOT NULL,
  archived_at DATETIME(3) NULL,
  active_name VARCHAR(40) GENERATED ALWAYS AS (
    CASE WHEN archived_at IS NULL THEN LOWER(TRIM(name)) ELSE NULL END
  ) STORED,
  version INT NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY accounts_id_owner_key (id, owner_id),
  UNIQUE KEY accounts_owner_name_key (owner_id, active_name),
  KEY accounts_owner_created_idx (owner_id, created_at, id),
  CONSTRAINT accounts_name_chk CHECK (CHAR_LENGTH(TRIM(name)) BETWEEN 1 AND 40),
  CONSTRAINT accounts_kind_chk CHECK (kind IN ('cash', 'debit_card', 'credit_card', 'alipay', 'wechat', 'other')),
  CONSTRAINT accounts_version_chk CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE categories (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  name VARCHAR(40) NOT NULL,
  kind VARCHAR(16) NOT NULL,
  icon VARCHAR(40) NOT NULL DEFAULT '',
  color VARCHAR(7) NOT NULL DEFAULT '',
  sort_order INT NOT NULL DEFAULT 0,
  archived_at DATETIME(3) NULL,
  active_name VARCHAR(40) GENERATED ALWAYS AS (
    CASE WHEN archived_at IS NULL THEN LOWER(TRIM(name)) ELSE NULL END
  ) STORED,
  version INT NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY categories_id_owner_key (id, owner_id),
  UNIQUE KEY categories_owner_kind_name_key (owner_id, kind, active_name),
  KEY categories_owner_kind_sort_idx (owner_id, kind, sort_order, id),
  CONSTRAINT categories_name_chk CHECK (CHAR_LENGTH(TRIM(name)) BETWEEN 1 AND 40),
  CONSTRAINT categories_kind_chk CHECK (kind IN ('expense', 'income')),
  CONSTRAINT categories_color_chk CHECK (color = '' OR color REGEXP '^#[0-9A-Fa-f]{6}$'),
  CONSTRAINT categories_version_chk CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE transactions (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  type VARCHAR(16) NOT NULL,
  amount_fen BIGINT NOT NULL,
  occurred_at DATETIME(3) NOT NULL,
  category_id BINARY(16) NULL,
  account_id BINARY(16) NULL,
  note VARCHAR(500) NOT NULL DEFAULT '',
  merchant VARCHAR(120) NOT NULL DEFAULT '',
  tags JSON NOT NULL,
  version INT NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  KEY transactions_owner_occurred_idx (owner_id, occurred_at, id),
  KEY transactions_owner_category_occurred_idx (owner_id, category_id, occurred_at),
  KEY transactions_owner_account_occurred_idx (owner_id, account_id, occurred_at),
  KEY transactions_owner_type_occurred_idx (owner_id, type, occurred_at),
  KEY transactions_category_fk_idx (category_id, owner_id),
  KEY transactions_account_fk_idx (account_id, owner_id),
  CONSTRAINT transactions_category_fk FOREIGN KEY (category_id, owner_id) REFERENCES categories (id, owner_id) ON DELETE RESTRICT,
  CONSTRAINT transactions_account_fk FOREIGN KEY (account_id, owner_id) REFERENCES accounts (id, owner_id) ON DELETE RESTRICT,
  CONSTRAINT transactions_amount_chk CHECK (amount_fen > 0 AND amount_fen <= 100000000000),
  CONSTRAINT transactions_type_chk CHECK (type IN ('expense', 'income', 'refund')),
  CONSTRAINT transactions_note_chk CHECK (CHAR_LENGTH(note) <= 500),
  CONSTRAINT transactions_merchant_chk CHECK (CHAR_LENGTH(merchant) <= 120),
  CONSTRAINT transactions_version_chk CHECK (version > 0),
  CONSTRAINT transactions_tags_chk CHECK (JSON_LENGTH(tags) <= 10)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE idempotency_records (
  owner_id VARCHAR(191) NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  request_hash VARBINARY(32) NOT NULL,
  state VARCHAR(16) NOT NULL DEFAULT 'pending',
  response_status SMALLINT NULL,
  response_body JSON NULL,
  resource_id BINARY(16) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  expires_at DATETIME(3) NOT NULL,
  PRIMARY KEY (owner_id, idempotency_key),
  KEY idempotency_records_expires_idx (expires_at),
  KEY idempotency_records_state_idx (state, updated_at),
  CONSTRAINT idempotency_state_chk CHECK (state IN ('pending', 'completed')),
  CONSTRAINT idempotency_key_len_chk CHECK (CHAR_LENGTH(idempotency_key) BETWEEN 8 AND 128),
  CONSTRAINT idempotency_response_chk CHECK (
    (state = 'pending' AND response_status IS NULL AND response_body IS NULL)
    OR
    (state = 'completed' AND response_status IS NOT NULL AND response_status BETWEEN 200 AND 299 AND response_body IS NOT NULL)
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE audit_events (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  entity_type VARCHAR(32) NOT NULL,
  entity_id BINARY(16) NOT NULL,
  action_type VARCHAR(16) NOT NULL,
  actor_user_id VARCHAR(191) NOT NULL,
  request_id VARCHAR(64) NOT NULL DEFAULT '',
  before_data JSON NULL,
  after_data JSON NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY audit_events_owner_entity_idx (owner_id, entity_type, entity_id, created_at),
  KEY audit_events_actor_idx (actor_user_id),
  CONSTRAINT audit_events_entity_chk CHECK (entity_type IN ('transaction', 'account', 'category', 'saving_goal', 'saving_goal_term')),
  CONSTRAINT audit_events_action_chk CHECK (action_type IN ('create', 'update', 'delete', 'archive', 'restore')),
  CONSTRAINT audit_events_request_chk CHECK (CHAR_LENGTH(request_id) <= 64)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE saving_goals (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  name VARCHAR(60) NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'active',
  start_period DATE NOT NULL,
  end_period DATE NULL,
  version INT NOT NULL DEFAULT 1,
  archived_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY saving_goals_id_owner_key (id, owner_id),
  KEY saving_goals_owner_status_idx (owner_id, status, start_period, id),
  CONSTRAINT saving_goals_name_chk CHECK (CHAR_LENGTH(TRIM(name)) BETWEEN 1 AND 60),
  CONSTRAINT saving_goals_status_chk CHECK (status IN ('active', 'archived')),
  CONSTRAINT saving_goals_start_chk CHECK (DAY(start_period) = 1),
  CONSTRAINT saving_goals_end_chk CHECK (end_period IS NULL OR (DAY(end_period) = 1 AND end_period >= start_period)),
  CONSTRAINT saving_goals_version_chk CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE saving_goal_terms (
  id BINARY(16) NOT NULL,
  goal_id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  version INT NOT NULL,
  effective_from_period DATE NOT NULL,
  scope_kind VARCHAR(16) NOT NULL,
  baseline_kind VARCHAR(16) NOT NULL,
  baseline_fen BIGINT NULL,
  initial_baseline_fen BIGINT NULL,
  target_saving_fen BIGINT NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY saving_goal_terms_goal_version_key (goal_id, version),
  UNIQUE KEY saving_goal_terms_goal_effective_key (goal_id, effective_from_period),
  UNIQUE KEY saving_goal_terms_id_owner_key (id, owner_id),
  KEY saving_goal_terms_goal_owner_idx (goal_id, owner_id),
  CONSTRAINT saving_goal_terms_goal_fk FOREIGN KEY (goal_id, owner_id) REFERENCES saving_goals (id, owner_id) ON DELETE CASCADE,
  CONSTRAINT saving_goal_terms_scope_chk CHECK (scope_kind IN ('all', 'categories')),
  CONSTRAINT saving_goal_terms_baseline_kind_chk CHECK (baseline_kind IN ('fixed', 'previous_period')),
  CONSTRAINT saving_goal_terms_target_chk CHECK (target_saving_fen >= 0 AND target_saving_fen <= 100000000000),
  CONSTRAINT saving_goal_terms_baseline_chk CHECK (baseline_fen IS NULL OR (baseline_fen >= 0 AND baseline_fen <= 100000000000)),
  CONSTRAINT saving_goal_terms_initial_chk CHECK (initial_baseline_fen IS NULL OR (initial_baseline_fen >= 0 AND initial_baseline_fen <= 100000000000)),
  CONSTRAINT saving_goal_terms_fixed_chk CHECK (
    (baseline_kind = 'fixed' AND baseline_fen IS NOT NULL AND initial_baseline_fen IS NULL)
    OR
    (baseline_kind = 'previous_period' AND baseline_fen IS NULL)
  ),
  CONSTRAINT saving_goal_terms_fixed_target_chk CHECK (baseline_kind <> 'fixed' OR target_saving_fen <= baseline_fen),
  CONSTRAINT saving_goal_terms_initial_target_chk CHECK (initial_baseline_fen IS NULL OR target_saving_fen <= initial_baseline_fen),
  CONSTRAINT saving_goal_terms_effective_chk CHECK (DAY(effective_from_period) = 1),
  CONSTRAINT saving_goal_terms_version_chk CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE saving_goal_term_categories (
  term_id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  category_id BINARY(16) NOT NULL,
  PRIMARY KEY (term_id, category_id),
  KEY saving_goal_term_categories_term_owner_idx (term_id, owner_id),
  KEY saving_goal_term_categories_category_owner_idx (category_id, owner_id),
  CONSTRAINT saving_goal_term_categories_term_fk FOREIGN KEY (term_id, owner_id) REFERENCES saving_goal_terms (id, owner_id) ON DELETE CASCADE,
  CONSTRAINT saving_goal_term_categories_category_fk FOREIGN KEY (category_id, owner_id) REFERENCES categories (id, owner_id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE IF EXISTS saving_goal_term_categories;
DROP TABLE IF EXISTS saving_goal_terms;
DROP TABLE IF EXISTS saving_goals;
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS idempotency_records;
DROP TABLE IF EXISTS transactions;
DROP TABLE IF EXISTS categories;
DROP TABLE IF EXISTS accounts;
