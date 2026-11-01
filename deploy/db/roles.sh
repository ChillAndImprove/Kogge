#!/bin/sh
# SPDX-FileCopyrightText: 2026 The Kogge authors
# SPDX-License-Identifier: EUPL-1.2
#
# PostgreSQL init script: the container's superuser owns the schema (migrations); Kogge runs as kogge_app, which
# may read and write data but cannot change the audit log (see migration 00001).
set -eu
app_password=$(cat /run/secrets/db_app_password)
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<SQL
CREATE ROLE kogge_app LOGIN PASSWORD '${app_password}';
GRANT CONNECT ON DATABASE "$POSTGRES_DB" TO kogge_app;
CREATE ROLE kogge_maint NOLOGIN;
SQL
