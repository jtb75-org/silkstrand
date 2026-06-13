# Collectors

Engine-specific **configuration-fact collectors** for CIS benchmark evaluation
(ADR 011). Each collector is a small, standalone Go module that the edge agent
invokes for an authenticated compliance scan: it reads credentials + target as
JSON on **stdin**, connects to the target engine, runs the CIS-relevant queries,
and writes a **facts JSON** document to **stdout**. Collectors make *no* pass/fail
decisions — they only report observed configuration; the rules/bundles evaluate.

## Modules

| Module | Engine | Status |
|--------|--------|--------|
| [`mssql/`](mssql/) | Microsoft SQL Server (2019, 2022) | live |

The agent maps a target engine to a collector binary in
`agent/internal/runner/collector.go` (`collectorMap`); `postgresql` and `mongodb`
slots are reserved there but have no Go collector module yet.

## Building

Built via the repo `Makefile`, not the container-image pipeline:

```bash
make collector-mssql           # current host GOOS/GOARCH → dist/
make collector-mssql-all       # linux/darwin × amd64/arm64 → dist/
```

## CI

Each collector module is linted + tested by the **Collectors Go Lint & Test**
job in `.github/workflows/ci.yml` (golangci-lint + `go test`), gated on the
`collectors/**` paths-filter. **Adding a new collector:** create
`collectors/<engine>/` as its own Go module and add `<engine>` to that job's
`strategy.matrix.collector` list so it gets coverage too.
