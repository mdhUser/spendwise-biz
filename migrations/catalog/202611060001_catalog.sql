-- +goose Up
-- catalog module. All tables are owned by spendwise-api and use the
-- catalog_ prefix. owner_id is an auth users.id value reference; there is no
-- cross-service foreign key. Money is BIGINT fen; NULL means unknown, never 0.

CREATE TABLE catalog_user_categories (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  name VARCHAR(120) NOT NULL,
  normalized_name VARCHAR(120) NOT NULL,
  sort_order INT NOT NULL DEFAULT 0,
  version INT NOT NULL DEFAULT 1,
  create_key VARCHAR(128) NULL,
  request_hash CHAR(64) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY catalog_user_categories_id_owner_uq (id, owner_id),
  UNIQUE KEY catalog_user_categories_owner_name_uq (owner_id, normalized_name),
  UNIQUE KEY catalog_user_categories_owner_key_uq (owner_id, create_key),
  KEY catalog_user_categories_owner_sort_idx (owner_id, sort_order, id),
  CONSTRAINT catalog_user_categories_name_chk CHECK (CHAR_LENGTH(TRIM(name)) BETWEEN 1 AND 120),
  CONSTRAINT catalog_user_categories_version_chk CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE catalog_user_products (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  category_id BINARY(16) NULL,
  title VARCHAR(120) NOT NULL,
  collection_state VARCHAR(16) NOT NULL DEFAULT 'saved',
  version INT NOT NULL DEFAULT 1,
  expires_at DATETIME(3) NOT NULL,
  create_key VARCHAR(128) NULL,
  request_hash CHAR(64) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY catalog_user_products_id_owner_uq (id, owner_id),
  UNIQUE KEY catalog_user_products_owner_key_uq (owner_id, create_key),
  KEY catalog_user_products_owner_state_idx (owner_id, collection_state, updated_at, id),
  KEY catalog_user_products_owner_category_idx (owner_id, category_id, id),
  KEY catalog_user_products_expiry_idx (expires_at, id),
  CONSTRAINT catalog_user_products_category_fk FOREIGN KEY (category_id, owner_id) REFERENCES catalog_user_categories(id, owner_id) ON DELETE RESTRICT,
  CONSTRAINT catalog_user_products_title_chk CHECK (CHAR_LENGTH(TRIM(title)) BETWEEN 1 AND 120),
  CONSTRAINT catalog_user_products_state_chk CHECK (collection_state IN ('saved', 'archived')),
  CONSTRAINT catalog_user_products_version_chk CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE catalog_user_skus (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  product_id BINARY(16) NOT NULL,
  display_name VARCHAR(120) NOT NULL,
  attributes JSON NOT NULL,
  attribute_schema_version INT NOT NULL DEFAULT 1,
  resolution_state VARCHAR(16) NOT NULL DEFAULT 'confirmed',
  version INT NOT NULL DEFAULT 1,
  create_key VARCHAR(128) NULL,
  request_hash CHAR(64) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY catalog_user_skus_id_owner_uq (id, owner_id),
  UNIQUE KEY catalog_user_skus_owner_key_uq (owner_id, create_key),
  KEY catalog_user_skus_owner_product_idx (owner_id, product_id, id),
  CONSTRAINT catalog_user_skus_product_fk FOREIGN KEY (product_id, owner_id) REFERENCES catalog_user_products(id, owner_id) ON DELETE CASCADE,
  CONSTRAINT catalog_user_skus_name_chk CHECK (CHAR_LENGTH(TRIM(display_name)) BETWEEN 1 AND 120),
  CONSTRAINT catalog_user_skus_version_chk CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE catalog_quote_snapshots (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  sku_id BINARY(16) NOT NULL,
  label_snapshot JSON NOT NULL,
  source_kind VARCHAR(32) NOT NULL,
  price_kind VARCHAR(16) NOT NULL,
  price_fen BIGINT NULL,
  shipping_fen BIGINT NULL,
  currency VARCHAR(3) NOT NULL DEFAULT 'CNY',
  stock VARCHAR(16) NOT NULL DEFAULT 'unknown',
  source_url VARCHAR(2000) NULL,
  specs_snapshot JSON NOT NULL,
  discounts_snapshot JSON NOT NULL,
  region_context JSON NOT NULL,
  eligibility_context JSON NOT NULL,
  observed_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  expires_at DATETIME(3) NOT NULL,
  schema_version INT NOT NULL DEFAULT 3,
  create_key VARCHAR(128) NULL,
  request_hash CHAR(64) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY catalog_quote_snapshots_owner_key_uq (owner_id, create_key),
  KEY catalog_quote_snapshots_owner_sku_idx (owner_id, sku_id, observed_at, id),
  KEY catalog_quote_snapshots_expiry_idx (expires_at, id),
  CONSTRAINT catalog_quote_snapshots_sku_fk FOREIGN KEY (sku_id, owner_id) REFERENCES catalog_user_skus(id, owner_id) ON DELETE CASCADE,
  CONSTRAINT catalog_quote_snapshots_price_chk CHECK (price_fen IS NULL OR price_fen >= 0),
  CONSTRAINT catalog_quote_snapshots_shipping_chk CHECK (shipping_fen IS NULL OR shipping_fen >= 0),
  CONSTRAINT catalog_quote_snapshots_price_kind_chk CHECK (price_kind IN ('exact', 'unknown'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE catalog_user_items (
  id BINARY(16) NOT NULL,
  owner_id VARCHAR(191) NOT NULL,
  title VARCHAR(120) NOT NULL,
  payload JSON NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  create_key VARCHAR(128) NULL,
  request_hash CHAR(64) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY catalog_user_items_owner_key_uq (owner_id, create_key),
  KEY catalog_user_items_owner_created_idx (owner_id, created_at, id),
  KEY catalog_user_items_expiry_idx (expires_at, id),
  CONSTRAINT catalog_user_items_title_chk CHECK (CHAR_LENGTH(TRIM(title)) BETWEEN 1 AND 120)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Reserved public catalog tables. M2 does not expose writes; seed/import in M4.
CREATE TABLE catalog_products (
  id BINARY(16) NOT NULL,
  name VARCHAR(200) NOT NULL,
  category VARCHAR(80) NOT NULL,
  specs JSON NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY catalog_products_category_idx (category, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE catalog_offers (
  id BINARY(16) NOT NULL,
  product_id BINARY(16) NOT NULL,
  payload JSON NOT NULL,
  verified_at DATETIME(3) NOT NULL,
  is_demo TINYINT(1) NOT NULL DEFAULT 0,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY catalog_offers_product_idx (product_id, verified_at),
  CONSTRAINT catalog_offers_product_fk FOREIGN KEY (product_id) REFERENCES catalog_products(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
