#!/usr/bin/env python3
"""Control: pg-set-user-extension — Ensure the set_user extension is installed"""

import json
import os
import re
import sys

CONTROL_ID = "pg-set-user-extension"
TITLE = "Ensure the set_user extension is installed"
SEVERITY = "medium"
REMEDIATION = """shared_preload_libraries = 'set_user'
# Restart PostgreSQL after changes."""

QUERY = """SHOW shared_preload_libraries;"""

ASSERTIONS = [
    ("shared_preload_libraries", "pattern_match", "set_user"),
]


def _coerce(x):
    if isinstance(x, bool):
        return x
    if isinstance(x, (int, float)):
        return x
    if isinstance(x, str):
        s = x.strip()
        try:
            return int(s)
        except ValueError:
            try:
                return float(s)
            except ValueError:
                return s
    return x


OPS = {
    "equals": lambda a, b: _coerce(a) == _coerce(b),
    "not_equals": lambda a, b: _coerce(a) != _coerce(b),
    "in": lambda a, b: _coerce(a) in [_coerce(x) for x in b],
    "not_in": lambda a, b: _coerce(a) not in [_coerce(x) for x in b],
    "pattern_match": lambda a, b: re.search(b, str(a)) is not None,
    "pattern_not_match": lambda a, b: re.search(b, str(a)) is None,
    "greater_than_or_equal": lambda a, b: _coerce(a) >= _coerce(b),
}


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

    if not rows:
        _emit("fail", "query returned no rows")
        return

    for row in rows:
        for field, op, expected in ASSERTIONS:
            if field not in row:
                _emit("fail", f"field {field!r} not in result")
                return
            fn = OPS.get(op)
            if fn is None:
                _emit("error", f"unknown operator {op!r}")
                return
            if not fn(row[field], expected):
                _emit("fail", f"{field}={row[field]!r} failed {op} {expected!r}")
                return

    _emit("pass", f"all {len(rows)} row(s) matched assertions")


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
