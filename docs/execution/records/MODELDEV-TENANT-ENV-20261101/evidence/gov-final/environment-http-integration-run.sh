#!/usr/bin/env bash
set -Eeuo pipefail
readonly runroot=/home/chabking/workspace/ani-governance-runs/modeldev-tenant-env-20261101
cd "$runroot/source"
readonly pg_image=docker.io/library/postgres@sha256:4ef4dbc939d61acea57712655ddb4b4ab27419c913f94cca0cd57cb3ea3c2280
readonly redis_image=sha256:a9cc41d6d01da2aa26c219e4f99ecbeead955a7b656c1c499cce8922311b2514
pg_container=''
redis_container=''
fixture_file="$runroot/environment-http-fixture-dsn.private"
cleanup(){
 if [[ -n "$pg_container" ]];then docker rm -f -v "$pg_container" >/dev/null;printf 'removed owned PostgreSQL container=%s\n' "$pg_container";fi
 if [[ -n "$redis_container" ]];then docker rm -f -v "$redis_container" >/dev/null;printf 'removed owned Redis container=%s\n' "$redis_container";fi
 rm -f "$fixture_file"
}
trap cleanup EXIT
password=$(python3 -c 'import secrets;print(secrets.token_hex(24))')
pg_container=$(docker run --pull=never -d --label ani.task=modeldev-environment-http-20261101 --memory=768m --cpus=1 --mount type=tmpfs,destination=/var/lib/postgresql -e POSTGRES_PASSWORD="$password" -p 127.0.0.1::5432 "$pg_image")
redis_container=$(docker run --pull=never -d --label ani.task=modeldev-environment-http-20261101 --memory=128m --cpus=1 -p 127.0.0.1::6379 "$redis_image" redis-server --save '' --appendonly no)
for attempt in $(seq 1 45);do if docker exec "$pg_container" pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1;then break;fi;sleep 1;done
docker exec "$pg_container" pg_isready -h 127.0.0.1 -U postgres
pg_port=$(docker port "$pg_container" 5432/tcp | awk -F: '$1=="127.0.0.1"{print $2}')
redis_port=$(docker port "$redis_container" 6379/tcp | awk -F: '$1=="127.0.0.1"{print $2}')
docker exec -i "$pg_container" psql -v ON_ERROR_STOP=1 -U postgres <<SQL >/dev/null
CREATE ROLE env_owner LOGIN PASSWORD '$password';
CREATE ROLE env_runtime LOGIN PASSWORD '$password' NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
CREATE DATABASE env_http OWNER env_owner;
REVOKE ALL ON DATABASE env_http FROM PUBLIC;
GRANT CONNECT ON DATABASE env_http TO env_runtime;
SQL
docker exec -i "$pg_container" psql -v ON_ERROR_STOP=1 -U env_owner -d env_http < app/admin/service/schema.sql >/dev/null
docker exec -i "$pg_container" psql -v ON_ERROR_STOP=1 -U env_owner -d env_http <<SQL >/dev/null
GRANT USAGE ON SCHEMA public TO env_runtime;
GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO env_runtime;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO env_runtime;
SQL
umask 077
printf 'postgres://env_owner:%s@127.0.0.1:%s/env_http?sslmode=disable\n' "$password" "$pg_port" > "$fixture_file"
export ANI_TEST_DATABASE_DSN="postgres://env_runtime:$password@127.0.0.1:$pg_port/env_http?sslmode=disable" ANI_TEST_DATABASE_EXCLUSIVE=1 ANI_MODELDEV_FIXTURE_DSN_FILE="$fixture_file" ANI_MODELDEV_REDIS_ADDR="127.0.0.1:$redis_port" GOTOOLCHAIN=auto GOMAXPROCS=2 GOFLAGS=-p=2
printf 'PostgreSQL=%s Redis=%s; exclusive loopback resources and separate runtime/fixture roles\n' "$pg_image" "$redis_image"
gofmt -w app/admin/service/internal/server/modeldev_environment_integration_test.go
go test -race -count=1 -v -timeout=3m -tags=modeldev_pg,modeldev_registration_pg -run '^TestModelDevEnvironmentHTTPCurrentAuthorizationAndNoAcceptance$' ./app/admin/service/internal/server
