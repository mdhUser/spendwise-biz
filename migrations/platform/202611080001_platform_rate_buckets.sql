-- +goose Up
-- platform module: shared operational tables. M2 adds the MySQL rate limiter;
-- M3 adds async_jobs here.

CREATE TABLE rate_buckets (
  bucket_key VARCHAR(191) NOT NULL,
  count INT NOT NULL DEFAULT 0,
  expires_at DATETIME(3) NOT NULL,
  PRIMARY KEY (bucket_key),
  KEY rate_buckets_expiry_idx (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
