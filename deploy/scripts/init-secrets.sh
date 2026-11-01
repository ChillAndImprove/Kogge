#!/bin/sh
# SPDX-FileCopyrightText: 2026 The Kogge authors
# SPDX-License-Identifier: EUPL-1.2
#
# Creates the secrets of the Compose stack in deploy/secrets/ (once; running it again keeps them). Keep a copy of
# master_key outside the server: encrypted secrets cannot be read without it.
#
# Docker Compose mounts the files into the containers as they are on the host, and the containers run as users of
# their own (Kogge 10001, PostgreSQL 70, Keycloak 1000): the files are readable for every user, and the directory,
# accessible to its owner only, keeps the other users of the host out.
set -eu
cd "$(dirname "$0")/.."
umask 077
mkdir -p secrets
chmod 700 secrets
rand() { head -c 32 /dev/urandom | base64 | tr -d '/+=' | cut -c1-32; }
create() { [ -s "secrets/$1" ] || printf '%s' "$2" > "secrets/$1"; }
create db_owner_password "$(rand)"
create db_app_password "$(rand)"
create keycloak_db_password "$(rand)"
create keycloak_admin_password "$(rand)"
create oidc_client_secret "$(rand)"
create sync_client_secret "$(rand)"
create master_key "$(head -c 32 /dev/urandom | base64)"
create db_owner_url "postgres://kogge_owner:$(cat secrets/db_owner_password)@db:5432/kogge?sslmode=disable"
create db_app_url "postgres://kogge_app:$(cat secrets/db_app_password)@db:5432/kogge?sslmode=disable"
chmod 644 secrets/*
# The backup directory belongs to the container's user.
if [ ! -d backups ]; then
  mkdir -p backups && chmod 700 backups
  chown 10001:10001 backups 2>/dev/null || echo "Run: sudo chown 10001:10001 $(pwd)/backups"
fi
echo "Secrets are in $(pwd)/secrets"
