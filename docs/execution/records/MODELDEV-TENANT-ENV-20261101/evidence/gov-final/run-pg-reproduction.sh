#!/usr/bin/env bash
set -Eeuo pipefail
cd /home/chabking/workspace/ani-governance-runs/modeldev-tenant-env-20261101/source
readonly image='docker.io/library/postgres@sha256:4ef4dbc939d61acea57712655ddb4b4ab27419c913f94cca0cd57cb3ea3c2280'
container=''
cleanup(){ if [[ -n "$container" ]];then docker rm -f -v "$container" >/dev/null;printf 'removed task PostgreSQL container=%s\n' "$container";fi; }
trap cleanup EXIT
password=$(python3 -c 'import secrets;print(secrets.token_hex(24))')
container=$(docker run -d --label ani.task=modeldev-tenant-environment-gov-20261101 --memory=768m --cpus=1 --mount type=tmpfs,destination=/var/lib/postgresql -e POSTGRES_PASSWORD="$password" -p 127.0.0.1::5432 "$image")
for attempt in $(seq 1 45);do if docker exec "$container" pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1;then break;fi;sleep 1;done
docker exec "$container" pg_isready -h 127.0.0.1 -U postgres
port=$(docker port "$container" 5432/tcp | awk -F: '$1=="127.0.0.1"{print $2}')
docker exec -i "$container" psql -v ON_ERROR_STOP=1 -U postgres <<SQL >/dev/null
CREATE ROLE env_owner LOGIN PASSWORD '$password';
CREATE ROLE env_runtime LOGIN PASSWORD '$password' NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
CREATE DATABASE env_test OWNER env_owner;
REVOKE ALL ON DATABASE env_test FROM PUBLIC;
GRANT CONNECT ON DATABASE env_test TO env_runtime;
SQL
docker exec -i "$container" psql -v ON_ERROR_STOP=1 -U env_owner -d env_test < app/admin/service/schema.sql >/dev/null
docker exec -i "$container" psql -v ON_ERROR_STOP=1 -U env_owner -d env_test <<SQL >/dev/null
GRANT USAGE ON SCHEMA public TO env_runtime;
GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO env_runtime;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO env_runtime;
SQL
export ANI_TEST_DATABASE_DSN="postgres://env_runtime:$password@127.0.0.1:$port/env_test?sslmode=disable" ANI_TEST_DATABASE_EXCLUSIVE=1 GOTOOLCHAIN=auto GOMAXPROCS=2 GOFLAGS=-p=2
printf 'PostgreSQL image=%s container=%s; owner/runtime roles are separate\n' "$image" "$container"
go test -count=1 -tags=modeldev_pg -timeout=5m -run '^TestModelDev(Environment(CurrentIndependentGrantsAndRevocation|PermissionDataStepGrantsOnlyTenantManagers)|CreateMaintenanceLeaseExitUsesExactWorker|CPUAcceptanceCreatesNoGPUAccounting|GPUAcceptanceOwnsOneDeliveryAndOriginalLedger|GPUConcurrentOriginalKeyChargesOnce)$' ./app/admin/service/internal/data
