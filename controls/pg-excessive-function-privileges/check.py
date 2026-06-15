#!/usr/bin/env python3
"""Control: pg-excessive-function-privileges — Ensure excessive function privileges are revoked"""

import json
import os
import sys

CONTROL_ID = "pg-excessive-function-privileges"
TITLE = "Ensure excessive function privileges are revoked"
SEVERITY = "high"
REMEDIATION = """ALTER FUNCTION [functionname] SECURITY INVOKER;"""

QUERY = """SELECT n.nspname AS schema, p.proname AS function, r.rolname AS owner
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace
JOIN pg_authid r ON r.oid = p.proowner
WHERE p.prosecdef = true
  AND p.proname NOT LIKE 'pgaudit%'
  AND n.nspname NOT IN ('pg_catalog', 'information_schema');"""


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
