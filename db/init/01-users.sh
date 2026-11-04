#!/bin/bash
set -euo pipefail

# The MySQL entrypoint already created MYSQL_USER (biz_app) with ALL
# privileges on MYSQL_DATABASE. Keep DML, remove DDL, and add a dedicated
# migration account. This script only runs on first initialization.
mysql --protocol=socket -uroot -p"${MYSQL_ROOT_PASSWORD}" <<SQL
CREATE USER IF NOT EXISTS 'biz_migrator'@'%' IDENTIFIED BY '${MYSQL_MIGRATOR_PASSWORD}';
GRANT ALL PRIVILEGES ON \`${MYSQL_DATABASE}\`.* TO 'biz_migrator'@'%';
REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'biz_app'@'%';
GRANT SELECT, INSERT, UPDATE, DELETE ON \`${MYSQL_DATABASE}\`.* TO 'biz_app'@'%';
FLUSH PRIVILEGES;
SQL
