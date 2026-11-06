-- +goose Up
-- conversation module. Table prefix conv_ keeps ownership visible in the
-- single spendwise database. No cross-module foreign keys.

CREATE TABLE conv_conversations (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  title VARCHAR(120) NOT NULL,
  needs JSON NOT NULL,
  asks INT NOT NULL DEFAULT 0,
  version INT NOT NULL DEFAULT 1,
  expires_at DATETIME(3) NOT NULL,
  create_key VARCHAR(128) NULL,
  request_hash CHAR(64) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY conv_conversations_owner_key_uq (owner_id, create_key),
  KEY conv_conversations_owner_updated_idx (owner_id, updated_at, id),
  KEY conv_conversations_expiry_idx (expires_at, id),
  CONSTRAINT conv_conversations_title_chk CHECK (CHAR_LENGTH(TRIM(title)) BETWEEN 1 AND 120),
  CONSTRAINT conv_conversations_version_chk CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE conv_messages (
  id BINARY(16) NOT NULL,
  conversation_id BINARY(16) NOT NULL,
  role VARCHAR(16) NOT NULL,
  content TEXT NOT NULL,
  create_key VARCHAR(128) NULL,
  request_hash CHAR(64) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY conv_messages_conversation_key_uq (conversation_id, create_key),
  KEY conv_messages_conversation_created_idx (conversation_id, created_at, id),
  KEY conv_messages_created_idx (created_at),
  CONSTRAINT conv_messages_conversation_fk FOREIGN KEY (conversation_id) REFERENCES conv_conversations(id) ON DELETE CASCADE,
  CONSTRAINT conv_messages_role_chk CHECK (role IN ('user', 'assistant', 'system'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE conv_use_case_revisions (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  conversation_id BINARY(16) NOT NULL,
  version INT NOT NULL,
  purpose VARCHAR(200) NOT NULL,
  requirements JSON NOT NULL,
  comparison_basis JSON NOT NULL,
  confirmation_state VARCHAR(16) NOT NULL,
  template_key VARCHAR(80) NOT NULL,
  template_version INT NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY conv_use_case_revisions_owner_version_uq (owner_id, conversation_id, version),
  KEY conv_use_case_revisions_owner_conversation_idx (owner_id, conversation_id, created_at),
  CONSTRAINT conv_use_case_revisions_conversation_fk FOREIGN KEY (conversation_id) REFERENCES conv_conversations(id) ON DELETE CASCADE,
  CONSTRAINT conv_use_case_revisions_confirmation_chk CHECK (confirmation_state IN ('draft', 'confirmed')),
  CONSTRAINT conv_use_case_revisions_version_chk CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE conv_search_runs (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  conversation_id BINARY(16) NOT NULL,
  use_case_revision_id BINARY(16) NULL,
  source_sku_id BINARY(16) NULL,
  source_quote_id BINARY(16) NULL,
  input JSON NOT NULL,
  status VARCHAR(16) NOT NULL,
  platform_status JSON NOT NULL,
  reply TEXT NOT NULL,
  request_key VARCHAR(128) NOT NULL,
  attempts INT NOT NULL DEFAULT 0,
  error VARCHAR(512) NOT NULL DEFAULT '',
  started_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  finished_at DATETIME(3) NULL,
  expires_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY conv_search_runs_owner_request_uq (owner_id, request_key),
  KEY conv_search_runs_owner_conversation_idx (owner_id, conversation_id, started_at, id),
  KEY conv_search_runs_status_idx (status, updated_at, id),
  KEY conv_search_runs_expiry_idx (expires_at, id),
  CONSTRAINT conv_search_runs_conversation_fk FOREIGN KEY (conversation_id) REFERENCES conv_conversations(id) ON DELETE CASCADE,
  CONSTRAINT conv_search_runs_status_chk CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE conv_substitution_assessments (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  search_run_id BINARY(16) NOT NULL,
  source_quote_id BINARY(16) NULL,
  candidate_quote_id BINARY(16) NULL,
  candidate_sku_id BINARY(16) NULL,
  relation_kind VARCHAR(32) NOT NULL,
  verdict VARCHAR(16) NOT NULL,
  constraint_checks JSON NOT NULL,
  cost_breakdown JSON NOT NULL,
  reason VARCHAR(500) NOT NULL,
  engine_version INT NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY conv_substitution_assessments_comparison_uq (owner_id, search_run_id, source_quote_id, candidate_sku_id),
  KEY conv_substitution_assessments_owner_run_idx (owner_id, search_run_id, id),
  CONSTRAINT conv_substitution_assessments_run_fk FOREIGN KEY (search_run_id) REFERENCES conv_search_runs(id) ON DELETE CASCADE,
  CONSTRAINT conv_substitution_assessments_verdict_chk CHECK (verdict IN ('equivalent', 'alternative', 'not_substitute', 'unknown'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE conv_field_evidence (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  quote_id BINARY(16) NOT NULL,
  field_path VARCHAR(200) NOT NULL,
  value JSON NOT NULL,
  origin VARCHAR(32) NOT NULL,
  source_url VARCHAR(2000) NULL,
  observed_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  verification_level VARCHAR(32) NOT NULL,
  PRIMARY KEY (id),
  KEY conv_field_evidence_owner_quote_idx (owner_id, quote_id, field_path)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE conv_assessment_evidence (
  assessment_id BINARY(16) NOT NULL,
  evidence_id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  purpose VARCHAR(32) NOT NULL,
  PRIMARY KEY (assessment_id, evidence_id),
  KEY conv_assessment_evidence_owner_idx (owner_id, evidence_id, assessment_id),
  CONSTRAINT conv_assessment_evidence_assessment_fk FOREIGN KEY (assessment_id) REFERENCES conv_substitution_assessments(id) ON DELETE CASCADE,
  CONSTRAINT conv_assessment_evidence_evidence_fk FOREIGN KEY (evidence_id) REFERENCES conv_field_evidence(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
