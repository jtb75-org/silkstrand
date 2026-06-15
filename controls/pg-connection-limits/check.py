#!/usr/bin/env python3
"""Control: pg-connection-limits — Ensure per-account connection limits are used"""

import json
import os
import sys

CONTROL_ID = "pg-connection-limits"
TITLE = "Ensure per-account connection limits are used"
SEVERITY = "low"
REMEDIATION = """ALTER ROLE <username> CONNECTION LIMIT <n>;"""

# CIS 5.5 doesn't exempt the postgres superuser — on a default install where
# postgres is the only login role (rolconnlimit = -1, unlimited), excluding it
# made the check pass vacuously. Only the reserved pg_* roles are excluded
# (rolcanlogin already filters the NOLOGIN ones; the LIKE is belt-and-braces).
QUERY = """SELECT rolname, rolconnlimit
FROM pg_authid
WHERE rolcanlogin = true
  AND rolconnlimit = -1
  AND rolname NOT LIKE 'pg_%';"""


def _read_json(path):
    with open(path) as f:
        return json.load(f)


def main():
    config_path = os.environ.get("SILKSTRAND_TARGET_CONFIG")
    creds_path = os.environ.get("SILKSTRAND_CREDENTIALS")
    if not config_path:
        _emit("error", "SILKSTRAND_TARGET_CONFIG not set")
        return

    config = _read_json(config_path)
    creds = _read_json(creds_path) if creds_path else {}

    host = config.get("host", "localhost")
    port = int(config.get("port", 5432))
    database = config.get("database", "postgres")
    username = creds.get("username") or config.get("username", "postgres")
    password = creds.get("password") or config.get("password", "")
    sslmode = config.get("sslmode", "prefer")
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "bundles", "cis-postgresql-16", "content", "vendor"))
    from silkstrand_db import connect_postgres  # noqa: E402

    try:
        conn = connect_postgres(
            host=host, port=port, database=database,
            user=username, password=password,
            sslmode=sslmode, timeout=10,
        )
        conn.autocommit = True
    except Exception as exc:
        _emit("error", f"connection failed: {exc}")
        return

    try:
        cursor = conn.cursor()
        cursor.execute(QUERY)
        cols = [d[0] for d in cursor.description] if cursor.description else []
        rows = [dict(zip(cols, r)) for r in cursor.fetchall()]
    except Exception as exc:
        _emit("error", f"query failed: {exc}")
        return
    finally:
        try:
            conn.close()
        except Exception:
            pass

    if rows:
        _emit("fail", f"{len(rows)} row(s) returned; expected none")
    else:
        _emit("pass", "no rows returned (as required)")


def _emit(status, detail):
    result = {
        "control_id": CONTROL_ID,
        "status": status,
        "severity": SEVERITY,
        "title": TITLE,
        "evidence": {"detail": detail},
        "remediation": REMEDIATION.strip(),
    }
    print(json.dumps(result))


if __name__ == "__main__":
    main()
