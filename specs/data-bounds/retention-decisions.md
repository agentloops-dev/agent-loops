# Retention decisions for #6677

Status: product choices recorded on 2026-11-23. Gate 1 and gate 2 are **not approved**. This file adds the retention choices to [store kinds](store-kinds.md); it does not change their interfaces.

## Rule

Only time series roll up. Audit, conversations, logs, and event files expire without a summary. A `Forever` setting or a live conversation does not extend the 90-day maximum age. Long-term audit retention uses the customer's SIEM sink.

These choices follow Alex's decisions on [#6670](https://github.com/agentloops-dev/agent-loops/issues/6670) and answers on [#6677](https://github.com/agentloops-dev/agent-loops/issues/6677).

## Bounds

| Store | Maximum age | Size limit | Action |
|---|---:|---:|---|
| Local audit detail | 1–90 days, as configured | A per-store bound is still required by #6755 | Expire detail; send long-term records to the customer's SIEM; no local summary |
| One conversation transcript | 90 days, even when live or marked `Forever` | 10 MB per chat | Expire with no summary |
| One runtime account home | 90 days | 1,200 MB per home (`1 MB/session × 100 sessions/week × 12 weeks`) | Expire with no summary |
| `daemon.log` | 7 days | 10 MB | Expire lines with no summary |
| `mcp-logs/` | 7 days | 10 MB for the store | Expire lines with no summary |
| `control.log` | 7 days | 10 MB | Expire lines with no summary |
| Each Keep backup | 90 days | 10 MB per backup | Refuse an over-limit backup; do not roll up |
| Each operator backup | 90 days | 10 MB per backup | Refuse an over-limit backup; do not roll up |

The runtime-home budget is based on a 1 MB average per session over 12 weeks at 100 sessions per week.

## Implementation issues

The existing store issues cover conversations (#6706), runtime homes (#6707), machine backups (#6709), daemon logs (#6735), MCP logs (#6736), audit (#6755), and operator backups (#6787). Their requirements now name the decisions above. No duplicate issues are needed for these stores.

Issue [#6901](https://github.com/agentloops-dev/agent-loops/issues/6901) covers `control.log`, which had no store issue. Issue [#6681](https://github.com/agentloops-dev/agent-loops/issues/6681) already covers bounded log and time-series interfaces and time-series roll-ups.

## Approval status

This is a decision record for #6677. It does not claim gate 1 or gate 2 approval. Store-specific record, count, byte, and age limits still need to be declared in their implementation issues and checked against actual access patterns.
