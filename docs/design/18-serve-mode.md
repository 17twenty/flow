# 18 — Serve Mode (future; constraints only)

v1 code must keep this additive: engine returns values (no printing/exit), state
behind small interfaces (`LockStore`, `Cache`, `Clock`), JSON types reusable as
REST bodies, multi-repo config possible. Future `flow serve`: scheduled doctor +
catch-up for many repos, read-only HTML health page, REST, MCP tools mirroring
`doctor`/`check pr --dry-run`. Not built in v1.
