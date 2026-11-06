-- +goose Up
-- M3: MySQL-backed async queue. One table, claimed with FOR UPDATE SKIP
-- LOCKED; no Redis/Kafka. The worker owns this table (platform module).

CREATE TABLE async_jobs (
  id BINARY(16) NOT NULL,
  kind VARCHAR(32) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  run_id BINARY(16) NULL,
  payload JSON NOT NULL,
  status ENUM('queued','running','succeeded','failed','cancelled') NOT NULL DEFAULT 'queued',
  attempts TINYINT UNSIGNED NOT NULL DEFAULT 0,
  max_attempts TINYINT UNSIGNED NOT NULL DEFAULT 3,
  run_after DATETIME(3) NOT NULL,
  locked_by VARCHAR(64) NULL,
  locked_until DATETIME(3) NULL,
  last_error VARCHAR(512) NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  KEY async_jobs_claim_idx (status, run_after, id),
  KEY async_jobs_owner_idx (owner_id, created_at),
  KEY async_jobs_run_idx (run_id),
  KEY async_jobs_expiry_idx (status, updated_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
