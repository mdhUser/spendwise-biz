-- +goose Up
-- M4: PostgreSQL -> MySQL import bookkeeping. Only rebuildable/demo catalog
-- tables are imported by cmd/import-pg; anonymous users/conversations/messages
-- are intentionally not migrated (target architecture §6.2.1 ADR).

CREATE TABLE import_id_map (
  source_table VARCHAR(64) NOT NULL,
  source_id VARCHAR(191) NOT NULL,
  target_table VARCHAR(64) NOT NULL,
  target_id BINARY(16) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (source_table, source_id),
  KEY import_id_map_target_idx (target_table, target_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
