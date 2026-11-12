# Upgrade Progress

This document records the implementation and verification completed for the Semi
Design and tweet archive upgrade. External credentials and deployments remain
explicit limitations rather than assumed acceptance.

Status on 2026-11-11: implementation, builds and local acceptance are verified.
Live authenticated user/list/following collection and its scheduled/backfill
paths still require X credentials, so the overall goal is not marked complete.

## Implemented

- Replaced production imports of the former `components/ui` and Radix controls
  with the Semi Design adapter, including navigation, authentication, task
  center, schedules, gallery, settings, drawers, dialogs, feedback, loading,
  focus trapping and responsive controls. React 19 and React Query remain in
  use; i18next language state is preserved across settings edits. Select and
  searchable-select Escape handling closes the options before the enclosing
  dialog, and tables expose one working expansion control per row.
- Added tweet detail parsing for long text, pure-text tweets, media tweets,
  quote, reply and retweet relationships. Tweet bodies, relations, media
  metadata, per-job body/media results and independent `tweet_sync` checkpoints
  are persisted. Text is retained when media fails; media can be retried
  independently and duplicate media keeps all tweet associations. Retries
  refresh expired media URLs without replacing archived bodies/relations or
  dropping items absent from partial responses. Active retries are rejected
  atomically, including alternate hosts and query strings for the same tweet.
- Added archive modes (`tweets`, `media`, `both`) to single, batch, user, list,
  following and scheduled jobs, plus a paginated/filterable Tweet Archive page
  with URL state, offline body/media display, task and gallery links, and
  incremental progress/backfill controls.
- Added versioned category storage for image originals, video/GIF originals,
  thumbnails and video covers. Local, NAS-style local roots and S3-compatible
  targets are supported. Rules, templates, previews, target versions, safe
  path rendering, collision handling, derivatives and explicit historical
  migration jobs are persisted independently.
- Added stable authenticated asset IDs with historical policy lookup and HTTP
  Range support. Recovery of the same interrupted execution retains its frozen
  strategy; a newly created retry captures the current strategy when it starts.
  Old asset records and retained source files remain readable after policy
  changes, with download/preview fallback when an asset is missing or corrupt.

## Verification

- Frontend unit suite: `31` files, `310` tests passed.
- Go tests: `go test ./...` passed with the S3 integration endpoint enabled;
  focused migration/retry/parser suites also passed. `go test -race -shuffle=on
  ./...` passed. `go vet ./...`, `gofmt -l cmd internal`, `go mod tidy -diff`
  and `git diff --check` passed.
- Explicit S3 acceptance used `OPEN_XDOWNLOAD_TEST_S3_ENDPOINT=http://127.0.0.1:18333`.
  Conditional writes/seeking, authenticated HTTP Range, four mixed-category
  destinations, retained-source recovery and migration checkpoint resume all
  passed without skips. The mixed configuration uses local images/covers, a
  NAS-compatible video directory and S3 thumbnails.
- The running preview also uses four distinct target IDs, directories/prefixes
  and naming rules. All nine original/derivative URLs returned `206` and the
  requested 32 bytes, covering `image`, `video`, `thumbnail` and `video_cover`.
- Production web build passed (`npm run build`), generated compressed assets
  were copied with `scripts/sync-webui.sh`, and the CGO-disabled embedded Go
  server build passed. The final preview explicitly runs with `-web-dir ''`,
  forcing embedded resources; desktop-Chinese and mobile-English workflow
  smoke checks also passed against this deployment (`2/2`).
- Playwright passed all `24` checks across desktop, tablet and mobile in
  Chinese and English. Checks include long text, image modal focus and restore,
  video playback, URL filters, task creation, batch/schedule creation with
  archive mode, schedule pause/delete, storage save/refresh, path preview,
  authenticated asset Range reads, offline retry, layout overflow and SSE
  fallback polling. Expanded checks cover Shift+Tab, dialog focus restoration,
  closed-select Escape, stable toast placement, horizontally scrolled task
  actions, the end of the storage form, decoded video frames and saved video
  covers. Screenshots are retained under `apps/web/test-results/`; the HTML
  report is `apps/web/playwright-report/index.html`.
- Additional desktop/mobile video checks decoded the stored 3840x2160 stream
  at 21.27 seconds and sampled visible frame pixels (50.5% above the black
  threshold). The separately stored cover returned `200 image/jpeg`.
- Live public jobs `126`, `127` and `128` completed for pure text (tweet `20`),
  three photos (`2119818965266612643`) and a 372-character video tweet
  (`2120206875581186293`). Bodies were persisted, media associations remained
  deduplicated and each stored media Range request returned `206` with 32 bytes.
- Preview server listens on `0.0.0.0:8787`; `/api/health` was verified through
  `http://10.10.11.3:8787/api/health` and returned `200`.

Evidence logs on the development machine:

- `/tmp/open-xdownload-web-upgrade-latest.log`
- `/tmp/open-xdownload-web-build-latest.log`
- `/tmp/open-xdownload-go-upgrade-latest.log`
- `/tmp/open-xdownload-race-upgrade-latest.log`
- `/tmp/open-xdownload-storage-acceptance-latest.log`
- `/tmp/open-xdownload-storage-runtime-acceptance-latest.log`
- `/tmp/open-xdownload-e2e-upgrade-latest.log`
- `/tmp/open-xdownload-embedded-browser-smoke-latest.log`
- `/tmp/open-xdownload-live-acceptance-latest.log`
- `/tmp/open-xdownload-guest-timeline-probe.log`
- `/tmp/open-xdownload-video-acceptance-latest.log`

## Upgrade and migration

1. Preserve the existing data directory and back up its SQLite database before
   deploying. Starting the new server upgrades existing records to schema 7.
   Category storage defaults to disabled, preserving the previous download
   behavior until a storage configuration is explicitly saved and enabled.
2. Configure targets and category rules in Settings. Existing tasks that resume
   keep their frozen strategy; subsequent executions capture the current one.
   Credentials returned by the configuration API are redacted; unchanged blank
   credential fields preserve existing secrets.
3. Use the explicit historical resource migration action to copy old records to
   the configured targets. It retains source files and historical asset IDs and
   checkpoints a fixed record snapshot after each processed download. Failures
   remain visible and retryable. Configuration changes alone do not move old
   files.

## Compatibility and limitations

- Existing SQLite records are upgraded to schema version 7. Legacy download
  paths remain readable; asset migration is an explicit job and does not delete
  legacy sources. Pre-v4 recorded regular files can recover their immediate
  trusted parent roots; covered paths do not weaken an existing sandbox, and
  ancestor symlinks are rejected. Missing NAS files defer completion of this
  recovery until a later restart. Unknown roots reached through symlinks need
  explicit configuration of their real trusted directory.
- Full-text progress is independent of the old media cursor. A failed
  incremental checkpoint with an old stop boundary is reset for explicit
  backfill, while an unfinished unbounded scan resumes its saved cursor.
- Public single-tweet collection was verified against live X responses,
  including text, photos and video. An authenticated full user timeline still
  requires a valid X `auth_token` and `ct0`; no such credentials were available
  in this environment. Live user backfill job `129` failed with the explicit
  missing-credentials message. Direct unauthenticated `UserTweets` probes with
  both viewer flags returned HTTP 200 but only a timeline ID, with no timeline
  instructions or tweets. This does not validate authenticated user/list/
  following collection or their scheduled/backfill behavior. To complete that
  acceptance, configure valid cookies in Settings and provide the permitted
  user/list/following scope; credentials need not be posted in conversation.
- The local NAS target in tests is a mounted-directory-compatible local root,
  not a production NAS deployment. S3 behavior was verified against an isolated
  SeaweedFS-compatible endpoint; provider-specific IAM, network and lifecycle
  settings still require deployment validation.
- Migration checkpoint recovery was verified by persisting a partial snapshot,
  reopening the store and resuming. A crash between failure-log insertion and
  checkpoint persistence can duplicate a failure-log row on replay; the
  processed/failed checkpoint totals remain consistent.
- Runtime video derivatives require `ffmpeg`. It is installed in this preview
  environment and was added to the Docker runtime stage. Docker CLI is not
  available here, so no container image build or container smoke test is claimed.
- The preview account is temporary: username `preview`, password
  `UpgradePreview2026!`.
