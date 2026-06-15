"""Shared DB connection helper for the SilkStrand CIS PostgreSQL controls.

A CIS scanner must connect regardless of the server's TLS posture — TLS being
off is itself a finding, not a reason to abort the whole scan. connect_postgres
maps libpq-style sslmode to pg8000 with the behaviour the per-control checks were
missing: sslmode=prefer (the default) TRIES TLS, then falls back to a plaintext
connection when the server refuses it.

Lives in content/vendor so it's importable via the agent-set PYTHONPATH (the same
path the controls already add for pg8000).
"""

import pg8000.dbapi


def connect_postgres(host, port, database, user, password, sslmode="prefer", timeout=10):
    """Connect to PostgreSQL honouring a libpq-style sslmode.

    disable / allow / "" -> no TLS
    prefer (default)     -> try TLS; on refusal/handshake failure, retry plaintext
    require / verify-*   -> TLS required (no fallback; the error surfaces)
    """
    mode = (sslmode or "prefer").lower()

    def _connect(ssl_context):
        return pg8000.dbapi.connect(
            host=host, port=int(port), database=database,
            user=user, password=password,
            ssl_context=ssl_context, timeout=timeout,
        )

    if mode in ("disable", "allow", ""):
        return _connect(None)
    if mode == "prefer":
        try:
            return _connect(True)
        except Exception:
            # Server refused TLS (or the handshake failed). A CIS scan must
            # still connect to evaluate controls; pg-tls-enabled reports TLS=off.
            return _connect(None)
    # require / verify-ca / verify-full: TLS is mandatory — let the error surface.
    return _connect(True)
