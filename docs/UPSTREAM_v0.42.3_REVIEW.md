# Upstream review: PgBase fork vs PocketBase v0.41.0 – v0.42.3

Date: 2026-11-17 · Scope: read-only code research. No source code was modified.
Downloads (upstream source archives + per-commit diffs) were kept outside the repo in `/tmp/pb-dl/` and `/tmp/pb-upstream-probe/` and were never executed.

## Sources: what could / couldn't be accessed

| Source | Result |
|---|---|
| BugStage site HTML (`https://ganigeorgiev.com/pocketbase/pocketbase/releases?...`) | ⚠️ Partial — plain `curl` returns only the SPA shell (637 bytes). The data was obtained via the site's own JSON API (see below), discovered by inspecting `/@assets/index.js`. |
| BugStage PocketBase API (`/api/collections/releases/records?expand=channel,channel.repository`) | ✅ Worked — full release notes for v0.41.0, v0.41.1, v0.42.0, v0.42.1, v0.42.2, v0.42.3 (PocketBase repo only; the collection mixes all of the author's projects). |
| BugStage git API (`/api/git/pocketbase/pocketbase/refs?type=tag`, `.../commits`, `.../commits/{hash}/diff`) | ✅ Worked — tag hashes and per-commit diffs for all ~49 commits between v0.40.6 (`9d281cca`) and v0.42.3 (`c5003ce6`). |
| Source archives (`/api/git/pocketbase/pocketbase/archive?rev={full-hash}`) | ✅ Worked — downloaded and diffed v0.40.6, v0.41.0, v0.42.0, v0.42.3 zips in `/tmp/pb-dl/`. Note: `rev` must be a full commit hash; `rev=v0.42.3` returns `400 Invalid or missing revision`. |
| `git ls-remote https://ganigeorgiev.com/pocketbase/pocketbase(.git)` | ❌ Fails (as expected; BugStage is not a plain git remote). Not used. |
| Local `upstream` remote (`github.com/pocketbase/pocketbase`) | ⚠️ Stale/frozen at the move point — `upstream/master` is `7f2773ec` (2026-10-26, just after v0.40.6). Used only to confirm the fork's base; all post-move changes (v0.41.0+) come from BugStage above. GitHub issue/discussion links in older notes still resolve conceptually but new discussions live on BugStage. |

### What "the fork has" actually means here (verified, not tags)

- Local tags `v0.41.0`–`v0.41.3` are **fork-local version tags, not upstream merges**: e.g. `v0.41.3` = fork commit `750e5574` ("Update asset references…", `ui/.env` says `PB_VERSION=v0.41.3`), whereas upstream v0.41.0 is `f6baa0c4`. `git merge-base --is-ancestor v0.40.6 HEAD` is also false. Do not read the tag list as upstream coverage.
- The fork's real base is v0.39.6-era PocketBase (`de3c3f71`) + upstream `master` merged at `325a43b2` + the selective v0.40.6 backport described in [CHANGELOG.md](/Volumes/mini512/Go%20Projects/pb-postgress/CHANGELOG.md:1) + `ecb62fec` "Port latestpb backend fixes while preserving Postgres support" (in HEAD). Because of that last port, **several v0.41.0 items are already in the code even though the changelog only claims v0.40.6** (verified per-item below).
- Upstream window reviewed: **v0.41.0 (2026-10-26) → v0.42.3 (2026-11-16)**, i.e. everything after upstream v0.40.6. Release notes: [v0.41.0](https://ganigeorgiev.com/pocketbase/pocketbase/releases?expr=tag%3D%22v0.41.0%22), [v0.41.1](https://ganigeorgiev.com/pocketbase/pocketbase/releases?expr=tag%3D%22v0.41.1%22), [v0.42.0](https://ganigeorgiev.com/pocketbase/pocketbase/releases?expr=tag%3D%22v0.42.0%22), [v0.41.1](https://ganigeorgiev.com/pocketbase/pocketbase/releases?expr=tag%3D%22v0.41.1%22) — see per-version links in each item; canonical pattern is `https://ganigeorgiev.com/pocketbase/pocketbase/releases?expr=tag%3D%22vX.Y.Z%22`.

## Summary (sorted by priority)

| # | Pri | Item (upstream) | In fork? | Postgres-applicable? | Effort |
|---|---|---|---|---|---|
| 1 | **High** | S3 `CompleteMultipartUpload` 200-with-`<Error>` body check (v0.42.3, `6760e8ea`) | ❌ No | Yes — S3 file storage | S |
| 2 | **High** | Global `rejectUnknownToken` 401 middleware (v0.42.0 `6ff009c2`, fixed v0.42.1) | ❌ No | Yes — core auth | M |
| 3 | Medium | Relax realtime subscription IP check + OAuth2 fallback IP (v0.42.3, `7b0cac5b`+`0a2caef0`) | ❌ No (strict check kept) | Yes — realtime, multi-instance sensitive | M |
| 4 | Medium | `cron.Stop()` race guard (v0.42.0, `3595f22c`) | ❌ No | Yes | S |
| 5 | Medium | Benign mid-response errors logged as INFO, not ERROR (v0.42.0 `fc871aec`+fixes; +`write error: client disconnected` v0.42.3) | ❌ No | Yes | S–M |
| 6 | Medium | Bump `golang.org/x/*` (+ Go toolchain) to v0.42.3 levels (v0.41.1, v0.42.0, v0.42.3) | ❌ Behind | Yes | S–M |
| 7 | Medium | `app.CountLogs()` helper; logs-cleanup vacuum — **adapt, don't copy** (v0.42.0 `7e0f8919`, threshold 50k in v0.42.3 `7b9ab7cc`) | ❌ No (`CountLogs` missing) | Partial — `PRAGMA wal_checkpoint` is SQLite-only; PG `VACUUM` semantics differ | M |
| 8 | Low | goja bump + remove nil-map workaround (v0.41.0, `76060cfe`) | ❌ No (workaround kept) | Yes — JSVM | S |
| 9 | Low | `ServeEvent.StartBannerFunc` (v0.41.0, `98a173c8`) | ❌ No | Yes | S |
| 10 | Low | OAuth2 redirect failures DEBUG→WARN (v0.42.3, part of `7b0cac5b`) | ❌ No | Yes — auth observability | S (trivial) |
| 11 | Low | `update` plugin: optional `Config.Channel` (v0.42.0, `3c0e7186`) | ❌ No | Yes (BugStage update plugin already present) | S |
| 12 | Low | `hex.EncodeToString` instead of `fmt.Sprintf("%x")` (v0.42.0, `1ec78f26`) | ❌ No | Yes | S (trivial) |
| 13 | Low | Thumb-too-large error message hints at env var (v0.42.x, `5ca0099f`) | ❌ No (limit itself ✅) | Yes | XS |
| 14 | Low | UI: `.jxl` image + `.webm` video preview extensions (v0.42.0 `7ee001ec`, v0.42.3 `d5d85853`) | ❌ No (mime types only) | N/A (UI) | S |
| 15 | Low | UI: reorder OAuth2 providers; logs-chart y-axis fixes (v0.42.0) | ❌ No | N/A (UI) | S–M |
| — | ✅ Done | `Event.Extra` map on all hook events (v0.41.0, `65356a4d`) | ✅ Yes | — | — |
| — | ✅ Done | Thumb default `250<<20` → `100<<20` (v0.41.0, `71da7ef3`) | ✅ Yes | — | — |
| — | ✅ Done | Mailer fixes: SMTP skip empty HTML; sendmail plain-text + Message-ID + custom headers (v0.41.0, `1cf5d6b8`) | ✅ Yes | — | — |

Effort key: XS <1h, S ~half day, M 1–3 days including tests.

## Per-item details

### 1. [High] S3 `CompleteMultipartUpload` can fail silently with HTTP 200 — v0.42.3
- **Upstream:** [v0.42.3 notes](https://ganigeorgiev.com/pocketbase/pocketbase/releases?expr=tag%3D%22v0.42.3%22); commit `6760e8ea2c` "check for error in 200 CompleteMultipartUpload response". S3 can return `200 OK` with an XML `<Error>` body when chunk post-processing fails; upstream now reads the body in `completeMultipartUpload()` and returns a parsed `ResponseError`, with a regression test `TestUploaderMultipartUploadCompleteFailureWith200`.
- **In fork? No.** [uploader.go](/Volumes/mini512/Go%20Projects/pb-postgress/tools/filesystem/internal/s3blob/s3/uploader.go:290) ends with `defer resp.Body.Close(); return nil` — a failed multipart completion is reported to the user as a successful upload (silent data loss / missing file on later download).
- **Postgres applicability:** Fully applicable. The fork uses this same vendored S3 client for S3-backed file storage (see the fork's S3/bucket setup); nothing SQLite-specific.
- **Port:** Copy the ~20-line body-check from upstream `uploader.go` plus its test. Files: `tools/filesystem/internal/s3blob/s3/uploader.go`, `uploader_test.go`. Effort **S**. This is the single best value/effort item in the window.

### 2. [High] Reject unknown/expired `Authorization` tokens globally with 401 — v0.42.0 (+ v0.42.1 fix)
- **Upstream:** [v0.42.0 notes](https://ganigeorgiev.com/pocketbase/pocketbase/releases?expr=tag%3D%22v0.42.0%22); commit `6ff009c297` adds `rejectUnknownToken()` (`pbRejectUnknownToken`, priority `9999`, runs last, only when `e.Auth == nil` and a token header is present) bound in `apis/base.go`, plus `TestRejectUnknownToken`. Previously invalid tokens were silently ignored, so clients couldn't detect "password changed on another device" except via `authRefresh`. Follow-ups: v0.42.1 fixed a superuser-UI regression it caused ([#8368](https://ganigeorgiev.com/pocketbase/discussions/8368), commit `457ae171` — JS-SDK/UI side in `ui/src/pb.js`), and `7a41c7b5` added JS-SDK auth-state fallbacks.
- **In fork? No.** No `rejectUnknownToken`/`RejectUnknownToken` symbol anywhere under [apis/](/Volumes/mini512/Go%20Projects/pb-postgress/apis/middlewares.go:174); `loadAuthToken()` silently ignores bad tokens as before.
- **Postgres applicability:** Fully applicable (pure HTTP/auth middleware). **Caution for this fork specifically:** audit every non-standard token consumer before enabling — the fork adds OAuth2 popup sign-in, an installer flow, file tokens (`/api/files/token`), batch requests and multi-instance realtime. Anything that forwards a stale/custom `Authorization` header to a public endpoint would newly 401. Upstream's own v0.42.1 regression is the warning: ship the backend middleware together with the `ui/src/pb.js` fallback behaviour, behind tests for (a) no token → pass, (b) valid token → pass, (c) garbage/expired token → 401, (d) file-token and installer endpoints unaffected.
- **Port:** `apis/base.go` (one `Bind`), `apis/middlewares.go` (handler + constants; note upstream also moved `getAuthTokenFromRequest`), `apis/middlewares_test.go`. Effort **M** (mostly verification across the fork's extra auth surfaces).

### 3. [Medium] Realtime IP check is too strict for mobile/NAT clients — v0.42.3
- **Upstream:** [v0.42.3 notes](https://ganigeorgiev.com/pocketbase/pocketbase/releases?expr=tag%3D%22v0.42.3%22) / [discussion #7758](https://ganigeorgiev.com/pocketbase/discussions/7758); commits `7b0cac5b95` (removes the "subscription request IP must equal connect IP" rejection in `apis/realtime.go`; OAuth2 redirect logs → WARN) and `0a2caef014` (keeps a narrower guard for the all-in-one OAuth2 flow: stores the first `@oauth2` subscription IP under new key `RealtimeOAuth2SubscriptionClientIPKey` and accepts either IP at redirect). Authenticated subscribers remain protected by the same-auth requirement + 30-min max connection lifetime.
- **In fork? No.** The strict check is still present at [realtime.go:221](/Volumes/mini512/Go%20Projects/pb-postgress/apis/realtime.go:221) (`the subscription request IP doesn't match with the realtime client IP` → 400), and [record_auth_with_oauth2_redirect.go:67](/Volumes/mini512/Go%20Projects/pb-postgress/apis/record_auth_with_oauth2_redirect.go:67) has the original single-IP comparison.
- **Postgres applicability:** Applicable and arguably **more relevant here**: this fork's headline feature is multi-instance realtime (Postgres `LISTEN/NOTIFY` + polling in [core/cache_sync.go](/Volumes/mini512/Go%20Projects/pb-postgress/core/cache_sync.go:1028), `core/realtime_bus.go`). Clients roaming between Wi-Fi/mobile IPs (or hitting different instances behind a load balancer) hit exactly this false-positive. Trade-off to state explicitly when porting: this is a deliberate *relaxation* of a brute-force mitigation — keep the OAuth2 fallback-IP half (`0a2caef0`), don't just delete the check.
- **Port:** `apis/realtime.go`, `apis/record_auth_with_oauth2_redirect.go`, both `_test.go` files. Effort **M** (tests need the fork's trusted-proxy `x-test-ip` setup, which already exists).

### 4. [Medium] `cron.Stop()` race can deadlock/panic — v0.42.0
- **Upstream:** commit `3595f22c81`: `tickerDone` becomes a buffered `chan struct{}` (size 1) and the `AfterFunc` ticker-init callback bails if `startTimer == nil` (Stop called in between). Upstream itself calls it "unlikely to trigger in real usage".
- **In fork? No.** [tools/cron/cron.go:26](/Volumes/mini512/Go%20Projects/pb-postgress/tools/cron/cron.go:26) still has unbuffered `chan bool` and the unguarded init.
- **Applicability:** Dialect-agnostic. Cron drives backups/log cleanup in every deployment; a shutdown-time hang is an operational bug worth the tiny fix. Effort **S** (one file, copy upstream hunks; no test upstream, add a Stop-immediately-after-Start smoke test if cheap).

### 5. [Medium] Harmless network errors are logged as ERROR — v0.42.0 (+ v0.42.3)
- **Upstream:** commit `fc871aec82` (plus `f67b8feb` ordering fix and `a9577e7f` range fix `200 ≤ status < 400`) reworks `logRequest` around a `benignErrors` list (`broken pipe`, `connection reset by peer`, http2 stream closures, …) + `canBeIgnored(err)`: OK-status requests with benign errors log at INFO with an "(often benign, see #8361)" suffix. [v0.42.3](https://ganigeorgiev.com/pocketbase/pocketbase/releases?expr=tag%3D%22v0.42.3%22) adds `write error: client disconnected` (`b6f792e2`).
- **In fork? No.** [apis/middlewares.go](/Volumes/mini512/Go%20Projects/pb-postgress/apis/middlewares.go:354) `logRequest` has no benign concept: any `err != nil` → `Logger().Error(...)`. On a busy Postgres-backed instance this floods the `_logs` table / external alerting with client-disconnect noise.
- **Applicability:** Fully applicable. Port the final upstream state (all three commits squashed), not the intermediate one. Effort **S–M** (one function + list; verify the fork's `LogIP`/`LogAuthId` attrs survive the reorder).

### 6. [Medium] Dependency bumps: `golang.org/x/*` to v0.42.3 levels — v0.41.1 / v0.42.0 / v0.42.3
- **Upstream:** v0.41.1 bumped `x/net` (minor security fixes); v0.42.0 bumped all `golang.org/x/*` and builds with Go 1.27.4; v0.42.3 bumped them again (`c5003ce6`).
- **In fork? Behind.** Fork [go.mod](/Volumes/mini512/Go%20Projects/pb-postgress/go.mod:1): `go 1.27.0`, `x/crypto v0.58.0`, `x/image v0.41.0`, `x/net v0.62.0` (already current), `x/oauth2 v0.36.0`, `x/sync v0.24.0`. Upstream v0.42.3: `x/crypto v0.59.0`, `x/image v0.48.0`, `x/oauth2 v0.38.0`, `x/sync v0.25.0`, `x/sys v0.50.0`, `x/text v0.44.0`.
- **Applicability:** Applicable; none of these are critical-for-PocketBase CVEs per upstream's own notes, so this is hygiene/scanner-noise rather than an emergency. `x/image` is the one to test (thumbnail decoding path). Effort **S–M** (`go get` + full test suite + thumbs tests). Do **not** take upstream's `modernc.org/sqlite v1.62.0` as part of this (see Skipped).

### 7. [Medium] `app.CountLogs()` + auto-vacuum after log cleanup — v0.42.0, threshold raised v0.42.3
- **Upstream:** commit `7e0f89199c` adds `CountLogs(exprs ...dbx.Expression)` ([core/log_query.go](https://ganigeorgiev.com/pocketbase/pocketbase/releases?expr=tag%3D%22v0.42.0%22)) and, in the `__pbLogsCleanup__` cron, counts before/after `DeleteOldLogs`, running `AuxVacuum()` once ≥20k rows were deleted (raised to **50k** in v0.42.3, `7b9ab7ccce`), then `PRAGMA wal_checkpoint(TRUNCATE)`. Rationale: drivers don't reliably report affected rows, hence manual counting.
- **In fork? No.** No `CountLogs` in [core/log_query.go](/Volumes/mini512/Go%20Projects/pb-postgress/core/log_query.go:60); the cleanup cron is still delete-only ([core/base.go](/Volumes/mini512/Go%20Projects/pb-postgress/core/base.go:1765)).
- **Postgres applicability — adapt, don't copy:**
  - `CountLogs()` itself: ✅ portable (`LogQuery().Select("count(*)")` over the aux/log query). Port as-is.
  - `PRAGMA wal_checkpoint(TRUNCATE)`: ❌ SQLite-only — will error on Postgres/MySQL. Drop it.
  - `AuxVacuum()`: ⚠️ In this fork `initAuxDB()` aliases the aux DB to the **main server database** ([core/base.go:1367](/Volumes/mini512/Go%20Projects/pb-postgress/core/base.go:1367)), and `vacuum()` issues a bare `VACUUM` ([core/db_table.go:79](/Volumes/mini512/Go%20Projects/pb-postgress/core/db_table.go:79)). On Postgres that is a full-database vacuum of production data (cannot run inside a transaction; heavy on large instances) — very different from vacuuming a small SQLite `auxiliary.db` file. MySQL already errors ("use OPTIMIZE TABLE"). Recommendation: port `CountLogs` + the count/threshold bookkeeping, but gate the vacuum call to SQLite-backed aux DBs (or make it opt-in), and skip the 20k→50k threshold change otherwise. If log-table bloat on Postgres is a real concern, `VACUUM (ANALYZE) _logs` targeted at the logs table is the PG-idiomatic equivalent — upstream has no such thing; treat as fork-specific work, not a backport.
- **Port:** `core/app.go` (interface), `core/log_query.go` (+ upstream `log_query_test.go` test), `core/base.go` (cron). Effort **M**.

### 8. [Low] goja bump removes the nil-map workaround — v0.41.0
- **Upstream:** commit `76060cfe`: goja `20261021…64adc8` → `20261023…6ace4d`, and `plugins/jsvm/binds.go` `nullObject()` reverts to `var v types.JSONMap[any]` (goja issue #822 fixed).
- **In fork? No.** Fork is still on goja `v0.0.0-20261021140635-64adc8bb4d51` with the `types.JSONMap[any]{}` workaround at [plugins/jsvm/binds.go:475](/Volumes/mini512/Go%20Projects/pb-postgress/plugins/jsvm/binds.go:475) (the v0.40.6 backport deliberately kept it).
- **Applicability:** JSVM is dialect-agnostic. Only worth doing together with a goja bump + regenerated JSVM types; on its own it's cosmetic. Effort **S**.

### 9. [Low] `ServeEvent.StartBannerFunc` — v0.41.0
- **Upstream:** commit `98a173c8`: new `core.ServeEvent.StartBannerFunc func(baseURL string)`; `apis/serve.go` extracts `defaultStartBannerFunc` (also switches banner bullets `├─/└─` → `>`). Setting an empty func disables the banner.
- **In fork? No.** [apis/serve.go:292](/Volumes/mini512/Go%20Projects/pb-postgress/apis/serve.go:292) still prints the banner inline gated only by `ShowStartBanner`.
- **Applicability:** Portable, tiny, zero DB interaction. Nice for embedders (this fork is used as a library with Postgres config). Effort **S**. Note the fork's banner order (REST API first when no UI) differs slightly from upstream's new order — preserve or accept upstream's, consciously.

### 10. [Low] OAuth2 redirect failures: DEBUG → WARN — v0.42.3
- **Upstream:** part of `7b0cac5b95`: all seven `Logger().Debug(...)` failure paths in `record_auth_with_oauth2_redirect.go` become `Warn`, and the IP-mismatch log gains `connectionClientIP`/`subscriptionClientIP` fields.
- **In fork? No** — all still `Debug` ([record_auth_with_oauth2_redirect.go:36](/Volumes/mini512/Go%20Projects/pb-postgress/apis/record_auth_with_oauth2_redirect.go:36)).
- **Applicability:** Trivial and useful (OAuth2 failures are invisible at default log levels today). Port with item 3 (same file/commits). Effort **S** standalone, **free** with #3.

### 11. [Low] `update` plugin: optional release channel — v0.42.0
- **Upstream:** commit `3c0e7186a5`: empty `Config.Channel` no longer forces `"default"`; the release query drops the channel filter so the latest semver across channels wins.
- **In fork? No.** [plugins/update/update.go:121](/Volumes/mini512/Go%20Projects/pb-postgress/plugins/update/update.go:121) still defaults empty → `"default"`.
- **Applicability:** The fork already ships the BugStage `update` plugin (per its v0.40.6 changelog), so this is a 15-line port. Only matters if the fork ever publishes to a non-`default` BugStage channel. Effort **S**.

### 12. [Low] `hex.EncodeToString` for hash hex encoding — v0.42.0
- Upstream `1ec78f26` replaces `fmt.Sprintf("%x", …)` in MD5/SHA256/SHA512/HS256/HS512 ([crypto.go](https://ganigeorgiev.com/pocketbase/pocketbase/releases?expr=tag%3D%22v0.42.0%22)). Fork still uses `Sprintf` (5 sites in [tools/security/crypto.go](/Volumes/mini512/Go%20Projects/pb-postgress/tools/security/crypto.go:28)). Pure perf micro-fix, behaviour-identical. Effort **S (trivial)** — bundle with any other change touching that file; not worth a standalone PR.

### 13. [Low] Thumb limit error should name the env var — v0.42.x
- Upstream `5ca0099f` appends `(you can set the env 'PB_THUMBS_MAX_SOURCE_PIXELS=max' to change the limit)` to the too-large error. Fork has the limit (`100<<20`, [filesystem.go:657](/Volumes/mini512/Go%20Projects/pb-postgress/tools/filesystem/filesystem.go:657)) but not the hint. One-line port. Effort **XS**.

### 14–15. [Low] UI-only items (v0.42.0 / v0.42.2 / v0.42.3)
- `.jxl` added to UI image-preview extensions (`7ee001ec`, touches `tools/filesystem/filesystem.go` list + `ui/src/utils.js`); `.webm` added to video-preview extensions (`d5d85853`, `ui/src/utils.js`). Fork: both extensions exist in [ui/src/mimeTypes.js](/Volumes/mini512/Go%20Projects/pb-postgress/ui/src/mimeTypes.js:53) but **not** in the `imageExtensions`/`videoExtensions` lists at [ui/src/utils.js:1296](/Volumes/mini512/Go%20Projects/pb-postgress/ui/src/utils.js:1296), so previews don't trigger. No thumbs support for JPEG XL upstream either. Effort **S** (UI rebuild required — this fork maintains UI separately, per its changelog).
- OAuth2 provider drag-reorder in the collection UI (`eb867157`, `ui/src/collections/oauth2Accordion.js` + `ui/src/base/sortable.js`) and logs-chart y-axis widening (`86c11d64`, `7b3ca4e7`, `ui/src/logs/logsChart.js`): fork's `oauth2Accordion.js` has no sortable wiring. UI-only, effort **S–M** including rebuild.
- v0.42.2 (`9873ed8f`) is a one-line JS-SDK/UI OAuth2-success redirect fix and v0.42.1's fix (`457ae171`) is likewise `ui/src/pb.js`-only: **only relevant if item 2 is ported** (they fix regressions that item 2 introduces in the stock UI/SDK). This fork's UI is heavily customised (OAuth2 popup flow), so verify behaviour rather than copying dist assets.

### Already in the fork (verified in code — no action)
- **`Event.Extra map[string]any`** (v0.41.0, `65356a4d`): present at [tools/hook/event.go:29](/Volumes/mini512/Go%20Projects/pb-postgress/tools/hook/event.go:29). (No core code consumes `.Extra` yet — same as upstream; it's an extension point for hook authors.)
- **Thumb default `100<<20`** (v0.41.0, `71da7ef3`): present at [tools/filesystem/filesystem.go:657](/Volumes/mini512/Go%20Projects/pb-postgress/tools/filesystem/filesystem.go:657). The fork's changelog still describes the old `250<<20` default — changelog is stale here, code is current.
- **Mailer fixes** (v0.41.0, `1cf5d6b8`, discussion #7871): SMTP skips empty `Message.HTML` ([tools/mailer/smtp.go:99](/Volumes/mini512/Go%20Projects/pb-postgress/tools/mailer/smtp.go:99)); sendmail sets `text/plain` when no HTML, copies custom headers, and generates a `Message-ID` ([tools/mailer/sendmail.go:54](/Volumes/mini512/Go%20Projects/pb-postgress/tools/mailer/sendmail.go:54)). Fully ported.

## Not applicable / skipped (with reason)

| Item | Reason |
|---|---|
| Go module migration `github.com/pocketbase/pocketbase` → `ganigeorgiev.com/pocketbase/pocketbase` (v0.41.0 headline, commits `61711550`, `cc8999cf`, `dbe96ffa`) | **Deliberately not applicable.** The fork keeps module `github.com/pocketbase/pocketbase` ([go.mod:1](/Volumes/mini512/Go%20Projects/pb-postgress/go.mod:1)); following upstream would rewrite every import in the fork and break downstream consumers for zero functional gain. Revisit only if the fork ever needs to consume upstream as a library dependency. (Upstream's own v0.41.0 migration note also contains a copy-paste error — both find/replace strings are the new host — don't follow it literally.) |
| `modernc.org/sqlite` bumps 1.60.1 → 1.61.0 (reverted upstream) → 1.62.0 / SQLite 3.54.1, `_defensive=1`, `modernc_versions_check.go` (v0.41.0–v0.42.0) | SQLite-only. The fork's server DB is Postgres/MySQL via `ConnectDatabase` ([core/database_config.go:44](/Volumes/mini512/Go%20Projects/pb-postgress/core/database_config.go:44)); `modernc.org/sqlite v1.52.0` remains only for reading legacy SQLite backup files ([core/backup_pocketbase.go:19](/Volumes/mini512/Go%20Projects/pb-postgress/core/backup_pocketbase.go:19)) and tests. A bump there is optional hygiene, unrelated to upstream's server-DB changes. |
| `PRAGMA wal_checkpoint(TRUNCATE)` after log cleanup (part of `7e0f8919`) | SQLite-only pragma; errors on Postgres/MySQL. See item 7 for the adapted design. |
| Upstream backup refactor / backup-DB copy pool changes (pre-v0.41, already excluded by the fork's v0.40.6 backport) | Out of window and already a conscious fork exclusion (fork has its own backup/restore rework). Not re-litigated here. |
| `go fmt` / import-reordering / comment-URL rewrites (`cc8999cf`, `dbe96ffa`, `567a27a0` dprint removal, `4e4a8d5e` readme image) | No behaviour change; the URL rewrites point at the new host, which the fork intentionally doesn't follow (previous row). |
| Version-bump / changelog / `ui/dist` rebuild commits (`13d5508f`, `b7b76318`, `be8dc4f5`, `bec171fd`, `f1feb0d6`, `9873ed8f` assets, …) | Release mechanics, not code changes. The fork versions independently (its `v0.41.3` ≠ upstream `v0.41.3`). |
| v0.41.1 fonts-license READMEs, npm dev-dep bumps (`8d0e9c64`, `b98e5615`) | Documentation/packaging only. Take the fonts license files only if the fork redistributes the vendored fonts as-is. |
| `.goreleaser.yaml` / Makefile release task (`0660259f`) | Upstream release plumbing for BugStage binaries; the fork has its own release config. |
| BugStage hosting/discussion URL changes in UI strings | Cosmetic; fork maintains its own UI/docs links. |

## Suggested order

1. **Item 1** (S3 multipart) — small, prevents silent upload data loss. Do first, standalone.
2. **Items 4 + 5 + 12 + 13** — one "operational hardening" PR (cron race, benign log levels, hex, thumb hint), all small and independent.
3. **Item 2** (reject unknown tokens) — highest-impact behaviour change; do it deliberately with the auth-surface audit and UI/SDK fallbacks, not bundled with anything else.
4. **Items 3 + 10** (realtime IP relaxation + WARN logs) — together, with an explicit note in the fork changelog that a brute-force mitigation was relaxed and why.
5. **Item 7 adapted** (`CountLogs` only; vacuum gated/opt-in) and **item 6** (x/* bumps) — routine maintenance window.
6. Items 8, 9, 11, 14, 15 opportunistically (JSVM/goja refresh, banner hook, update channel, UI previews).
