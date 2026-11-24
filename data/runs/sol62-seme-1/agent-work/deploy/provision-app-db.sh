#!/usr/bin/env bash
set -euo pipefail
read -r password
sudo mysql <<SQL
CREATE USER IF NOT EXISTS 'isupipe_worker'@'10.99.0.12' IDENTIFIED BY '$password';
ALTER USER 'isupipe_worker'@'10.99.0.12' IDENTIFIED BY '$password';
GRANT SELECT, INSERT, UPDATE, DELETE ON isupipe.* TO 'isupipe_worker'@'10.99.0.12';

CREATE USER IF NOT EXISTS 'isupipe_initializer'@'10.99.0.11' IDENTIFIED BY '$password';
ALTER USER 'isupipe_initializer'@'10.99.0.11' IDENTIFIED BY '$password';
GRANT SELECT, INSERT, UPDATE, DELETE, ALTER, DROP, CREATE ON isupipe.* TO 'isupipe_initializer'@'10.99.0.11';
SQL
