# Gov final receipt

Final source commit `fa80f886d022177173d7e9cf81ec51e54c2df4c8` on `codex/modeldev-dynamic-tenant-environment`.

Only the completed, returned formal module pin and real PostgreSQL six-test output are PASS. Read `result.json`, `formal-module-pin-captured.log`, `formal-pg-six-captured.log` and `run-pg-reproduction.sh`.

Final default/race tests, make check and audit are RESULT_UNKNOWN. Their session IDs, commands and last actually observed output are in `pending-fedora-sessions.json`. The final recovery probe has exact UTC timestamps and exit 255; no additional probes or test retries were made. Waiting has stopped. Remote processes were not deliberately terminated.

`local-source-sha256.json` is a LOCAL manifest only. The final Fedora tree was NOT compared after the outage. No deployment or live A/B acceptance was performed. Maintenance branch failures are separate evidence and are not substituted for final Gov results.
