#!/usr/bin/env python3
"""Run checks with an isolated, bounded PostgreSQL dependency on Fedora or CI.

Credentials exist only in a private temporary directory and the child process
environment. The runtime role cannot migrate schemas or bypass RLS. This is a
module-test dependency and never connects to a shared business database.
"""

import os
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile
import time


IMAGE = "postgres@sha256:639ab7ceb90e13123085b741fb31ef493fba25463002f6da665352e7b534b652"


def main():
    if len(sys.argv) < 2:
        raise SystemExit("usage: with-test-postgres.py <command> [arguments...]")
    identifier = "cpu-p01-test-" + secrets.token_hex(12)
    owner_password = secrets.token_hex(24)
    runtime_password = secrets.token_hex(24)
    container_id = None
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix="cpu-p01-postgres-") as temporary:
        credentials = Path(temporary) / "postgres.env"
        credentials.write_text(
            "POSTGRES_USER=cpu_p01_owner\nPOSTGRES_DB=cpu_p01_test\n"
            f"POSTGRES_PASSWORD={owner_password}\n", encoding="utf-8",
        )
        try:
            container_id = subprocess.check_output([
                "docker", "run", "--detach", "--name", identifier,
                "--label", f"ani.cpu-p01.test={identifier}",
                "--cpus=1", "--memory=512m", "--pids-limit=128",
                "--tmpfs", "/var/lib/postgresql/data:rw,size=384m",
                "--publish", "127.0.0.1::5432", "--env-file", str(credentials), IMAGE,
                "-c", "shared_buffers=32MB", "-c", "max_wal_size=64MB",
                "-c", "min_wal_size=32MB", "-c", "checkpoint_timeout=30s",
            ], text=True).strip()
            for _ in range(30):
                # The image starts a socket-only temporary server during init.
                # Wait for the final TCP listener before issuing role DDL; a
                # successful socket probe can race database creation/restart.
                ready = subprocess.run([
                    "docker", "exec", container_id, "pg_isready", "-h", "127.0.0.1",
                    "-U", "cpu_p01_owner", "-d", "cpu_p01_test",
                ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
                if ready.returncode == 0:
                    break
                time.sleep(1)
            else:
                raise RuntimeError("isolated test PostgreSQL did not become ready")
            sql = (
                "CREATE ROLE cpu_p01_runtime LOGIN PASSWORD '" + runtime_password + "' "
                "NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS;\n"
                "REVOKE CREATE ON SCHEMA public FROM PUBLIC;\n"
            )
            # Install the fixed public helper once for this exclusive test DB;
            # individual behavioral tests still create isolated table schemas.
            migration = Path(__file__).resolve().parents[1] / "migrations/0003_execution_environment_projection.up.sql"
            sql += "CREATE FUNCTION" + migration.read_text(encoding="utf-8").split("CREATE FUNCTION", 1)[1]
            configured = subprocess.run([
                "docker", "exec", "-i", container_id, "psql", "-X", "-v", "ON_ERROR_STOP=1",
                "-U", "cpu_p01_owner", "-d", "cpu_p01_test",
            ], input=sql, text=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
            if configured.returncode != 0:
                raise RuntimeError("isolated restricted test-role setup failed")
            binding = subprocess.check_output(["docker", "port", container_id, "5432/tcp"], text=True).strip()
            host, port = binding.rsplit(":", 1)
            if host != "127.0.0.1" or not port.isdigit():
                raise RuntimeError("test PostgreSQL must bind only loopback")
            environment = os.environ.copy()
            environment["CPU_P01_TEST_DATABASE_URL"] = (
                f"postgres://cpu_p01_runtime:{runtime_password}@127.0.0.1:{port}/cpu_p01_test?sslmode=disable"
            )
            environment["CPU_P01_TEST_DATABASE_ADMIN_URL"] = (
                f"postgres://cpu_p01_owner:{owner_password}@127.0.0.1:{port}/cpu_p01_test?sslmode=disable"
            )
            print("Real PostgreSQL test dependency ready; restricted runtime role; loopback only.", flush=True)
            result = subprocess.run(sys.argv[1:], env=environment, check=False)
            if result.returncode:
                # Preserve a bounded infrastructure diagnosis before deleting
                # this invocation's exclusive fixture. Never emit DB URLs or
                # SQL statements, which may contain private test references.
                state = subprocess.check_output([
                    "docker", "inspect", "--format",
                    'running={{.State.Running}} oom_killed={{.State.OOMKilled}} exit={{.State.ExitCode}}',
                    container_id,
                ], text=True).strip()
                print("Test PostgreSQL diagnostic: " + state, flush=True)
                logs = subprocess.run(["docker", "logs", "--tail", "80", container_id], capture_output=True, text=True, check=False)
                for line in (logs.stdout + logs.stderr).splitlines():
                    for cause in ("No space left on device", "out of memory", "could not resize shared memory", "PANIC:"):
                        if cause in line:
                            print("Test PostgreSQL diagnostic: " + cause, flush=True)
                            break
            return result.returncode
        finally:
            if container_id:
                label = subprocess.check_output([
                    "docker", "inspect", "--format", '{{index .Config.Labels "ani.cpu-p01.test"}}', container_id,
                ], text=True).strip()
                if label != identifier:
                    raise RuntimeError("refusing test-container cleanup: ownership label mismatch")
                # This exact container was created by this invocation, uses only
                # bounded tmpfs, and contains no retained business data.
                subprocess.run(["docker", "rm", "--force", container_id], check=True, stdout=subprocess.DEVNULL)


if __name__ == "__main__":
    raise SystemExit(main())
