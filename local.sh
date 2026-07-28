#!/bin/bash

LOCALDB=""

if [[ "$@" =~ (^|[[:space:]])(-l|--local)($|[[:space:]]) ]]; then
  echo "Found -l or --local"
  LOCALDB="env DB_USER=${DB_USER:-$USER} DB_NAME=ssetunnel DB_PASSWORD= DB_URL_TEMPLATE=postgres://[username]:[password]@[host]:[port]/[database_name]?sslmode=disable"
fi

GIT_SHA=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS="-X main.Version=0.1.0 -X main.GitSHA=${GIT_SHA}"

$LOCALDB go run -ldflags "${LDFLAGS}" ./cmd/ssetunnel "$@"
