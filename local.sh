#!/bin/bash

LOCALDB=""

if [[ "$@" =~ (^|[[:space:]])(-l|--local)($|[[:space:]]) ]]; then
  LOCALDB="env DB_USER=${DB_USER:-$USER} DB_NAME=ssetunnel DB_PASSWORD= DB_URL_TEMPLATE=postgres://[username]:[password]@[host]:[port]/[database_name]?sslmode=disable"
fi

GIT_SHA=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
GIT_RELEASE=$(git describe --tags --abbrev=0)
LDFLAGS="-X main.Version=${GIT_RELEASE} -X main.GitSHA=${GIT_SHA}"

$LOCALDB go run -ldflags "${LDFLAGS}" ./cmd/ssetunnel "$@"
