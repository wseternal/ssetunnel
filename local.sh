#!/bin/bash

LOCALDB=""

if [[ "$@" =~ (^|[[:space:]])(-l|--local)($|[[:space:]]) ]]; then
  echo "Found -l or --local"
  LOCALDB="env DB_USER=${DB_USER:-$USER} DB_NAME=ssetunnel DB_PASSWORD= DB_URL_TEMPLATE=postgres://[username]:[password]@[host]:[port]/[database_name]?sslmode=disable"
fi


$LOCALDB go run ./cmd/ssetunnel "$@"
