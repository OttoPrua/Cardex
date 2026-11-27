# Ranking evidence shown by the board

The board's **模型与额度 → 排名与证据** tab shows the published version (including an approved empty table) separately from proposals waiting for approval. Config seed rows are not a published ranking. Configured channel models absent from the published rows are listed as unranked. This view does not initialize or migrate the database, change proposals, alter placement, or affect routing. If it cannot inspect the database without initialization (for example, on a fresh home), it reports the published state as unknown. The published database snapshot and pending files have independent storage, so an approval in progress can temporarily leave a pending record next to the latest published version. Pending records are never used to construct that version. The pending list also explicitly marks partial or unavailable data, including when its file scan or per-proposal size limits are exceeded.

Evidence dates are opt-in, version-specific annotations created outside the board. The existing ranking proposals and legacy databases do not record the date their source evidence was observed. The board therefore shows **未知** until explicitly dated evidence is available; neither a proposal timestamp nor its approval date is treated as an evidence date.

For a published version with a confirmed evidence date, read the exact version and `evidence_key` from the existing board's `/api/channels?tab=ranking` response and place a file in `<home>/positioning/evidence/v<version>.json`:

```json
{"version": 4, "evidence_key": "<copy ranking.evidence_key from the board API>", "evidence_date": "2026-11-26"}
```

The key binds the date note to the actual published entries, including when a restored database reuses a version number with different content. Verify that the evidence actually covers the selected version before recording it. Do not include the original evidence, secrets, or private notes in this file. The board reads only bounded metadata and does not write, import, refresh, or validate the underlying research. UTC calendar dates more than 14 days old prompt a review; exactly 14 days does not. Missing, malformed, mismatched, and future dates show **未知**.
