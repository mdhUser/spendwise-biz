#!/bin/bash
set -euo pipefail

# The MySQL entrypoint created MYSQL_USER with ALL privileges on
# MYSQL_DATABASE. Keep DML for the app account, remove DDL, and add a dedicated
# migration account. This script only runs on first initialization.
#
# In compose.integration.yaml MYSQL_USER is the auth account, so biz_app is
# created here explicitly. CREATE USER IF NOT EXISTS keeps standalone biz
# compose (where MYSQL_USER=biz_app) idempotent.
mysql --protocol=socket -uroot -p"${MYSQL_ROOT_PASSWORD}" <<SQL
CREATE USER IF NOT EXISTS 'biz_app'@'%' IDENTIFIED BY '${MYSQL_APP_PASSWORD}';
CREATE USER IF NOT EXISTS 'biz_migrator'@'%' IDENTIFIED BY '${MYSQL_MIGRATOR_PASSWORD}';
GRANT ALL PRIVILEGES ON \`${MYSQL_DATABASE}\`.* TO 'biz_migrator'@'%';
REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'biz_app'@'%';
GRANT SELECT, INSERT, UPDATE, DELETE ON \`${MYSQL_DATABASE}\`.* TO 'biz_app'@'%';
FLUSH PRIVILEGES;
SQL
