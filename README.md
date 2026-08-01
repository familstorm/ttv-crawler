# TTV Personal Archiver

*[Tiếng Việt](README.vi.md)*

A Go crawler that archives a Vietnamese web-novel catalogue into PostgreSQL for
offline personal reading. The source site renders its pages server-side but
gates them behind client-side checks, so documents are fetched through headless
Chromium rather than a bare HTTP client. The interesting part is not the
scraping — it is that a run over ~284 catalogue pages and tens of thousands of
chapters has to survive being killed halfway through, so all crawl state lives
in Postgres and every write is idempotent. Stop it with `Ctrl+C`, start it
again, and it resumes without refetching what it already has.

## Architecture

```
                 ┌──────────────┐
  seed 284 URLs  │  crawl_jobs  │  phase gate: catalog → story → chapter
  ───────────────▶   (queue)    │  claim = UPDATE … RETURNING under a lease
                 └──────┬───────┘
                        │ claim(lease 10m)
                 ┌──────▼───────┐  robots.txt gate (per-host cache, fail-closed)
   N workers ────▶   fetcher    │  shared rate limiter: 1 req / 3s + jitter
                 │  (Chromium)  │  retry + exponential backoff, honours Retry-After
                 └──────┬───────┘
                        │ HTML
                 ┌──────▼───────┐
                 │    parser    │  catalogue rows / story metadata / chapter text
                 └──────┬───────┘
                        │
                 ┌──────▼───────┐
                 │    store     │  SHA-256 content hash, upsert on source_url
                 └──────────────┘
```

**Phase-gated queue.** Jobs are typed `catalog`, `story`, `chapter`, and the
queue enforces the order as a gate rather than as a priority hint. Every
catalogue page must leave `pending`/`processing` before a single story job can
be claimed, and all story jobs must finish before chapter jobs unlock. Priority
alone would let one slow catalogue page trail behind thousands of chapter
downloads; a gate guarantees the full list of stories is discovered while the
source is still reachable, before the crawler commits to the expensive part.

**DB-backed lease.** Claiming a job is a single `UPDATE … RETURNING` that sets
`status='processing'` and a `lease_until` ten minutes out, so concurrent workers
can never claim the same row and no in-memory queue can lose one. A clean
shutdown releases the job to `pending` immediately without spending a retry; a
hard kill leaves the lease to expire, and the row returns to the queue on its
own.

**Retry and backoff.** Failures increment `attempts` and set `next_attempt_at`
to `now() + 2^attempts` minutes, capped by `max_attempts`. Permanent client
errors (`404`, `410`) and robots.txt denials skip the ladder and fail straight
away — retrying them cannot change the answer. `429`, `5xx` and transport errors
back off, and a `Retry-After` header takes precedence over the computed delay.

**Idempotent upsert.** Every fetched document is hashed with SHA-256 and stored
against its URL in `source_documents`; chapters carry a `content_hash` and
upsert on `(story_id, chapter_number)`. Re-running a completed job rewrites the
same row rather than duplicating it, which is what makes a resumed run safe.

**robots.txt.** Each host's robots.txt is fetched once, parsed per RFC 9309 and
cached for 12 hours. `Disallow`/`Allow` patterns and `Crawl-delay` are honoured;
an advertised delay raises the shared rate limiter but never lowers the
configured interval. If robots.txt cannot be read at all — transport error or
`5xx` — the crawler fails closed and treats the host as fully disallowed. A
`404` is not a failure: per the RFC it means no restrictions were published.

## Schema

| Table | Purpose |
|---|---|
| `stories` | Story metadata: title, summary, status, rating, view/follower counts, expected chapter count. Unique on `source_url` and `source_slug`. |
| `chapters` | Chapter number, title, plain-text content and `content_hash`. Unique on `(story_id, chapter_number)`. |
| `authors` | Deduplicated by `normalized_name`. |
| `genres` | Deduplicated by `slug`. |
| `story_genres` | Many-to-many join between stories and genres. |
| `crawl_jobs` | The queue: `kind`, `status`, `attempts`, `next_attempt_at`, `lease_until`, `last_error`, `http_status`. |
| `source_documents` | Per-URL fetch trace: HTTP status, ETag, Last-Modified, content hash. Raw HTML is not retained. |
| `story_progress` | View joining stories to chapter counts, exposing `progress_percent`. |

```sql
-- How far along is each story?
SELECT title, downloaded_chapter_count, expected_chapter_count, progress_percent
FROM story_progress
ORDER BY progress_percent DESC, title;
```

## Quick start (Docker)

Requires Docker with Compose. No Go toolchain needed.

```bash
git clone https://github.com/familstorm/ttv-crawler.git
cd ttv-crawler
cp .env.example .env

docker compose up -d postgres                        # database
docker compose --profile crawler up --build crawler  # migrate, seed and crawl
```

Watch progress from another terminal:

```bash
docker compose run --rm crawler status
# Queue: pending=812 processing=1 completed=2043 failed=0
# Data:  stories=284 chapters=2043
```

There is a read-only admin UI for browsing the queue, crawl phase and per-story
progress:

```bash
docker compose --profile admin up -d --build admin
open http://localhost:8080/admin
```

It has no authentication and no write actions, so keep it on a trusted network.

Stop the crawler without losing work:

```bash
docker compose --profile crawler stop crawler
```

Postgres data is bind-mounted at `./volumes/postgres_data` and shared static
files at `./volumes/public_data`. `docker compose down` leaves both in place.

<details>
<summary>Running Go directly (requires Go 1.26+ and PostgreSQL)</summary>

```bash
cp .env.example .env
docker compose up -d postgres
go run ./cmd/ttv-crawler migrate
go run ./cmd/ttv-crawler run
```

```text
ttv-crawler migrate       create or update the schema
ttv-crawler seed          enqueue START_URL only
ttv-crawler run           seed, then run workers (default)
ttv-crawler status        show queue, story and chapter counts
ttv-crawler retry-failed  requeue jobs that exhausted their retries
ttv-crawler admin         serve the admin UI on ADMIN_ADDR
```

`run` waits on an empty queue by default, which suits a container. Set
`IDLE_EXIT_AFTER=30s` to make the process exit once the queue has been idle
that long.

</details>

## Configuration

Full list in [`.env.example`](.env.example). The ones that matter:

| Variable | Default | Meaning |
|---|---:|---|
| `REQUEST_INTERVAL` | `3s` | Global minimum gap between requests. Values under `1s` are rejected. |
| `REQUEST_JITTER` | `1.5s` | Random delay added on top. |
| `WORKERS` | `1` | Parser/DB workers, max 8. Does not increase request rate — the limiter is shared. |
| `ROBOTS_CACHE_TTL` | `12h` | How long a parsed robots.txt is reused per host. |
| `MAX_JOB_ATTEMPTS` | `8` | Durable queue retry ceiling. |
| `IDLE_EXIT_AFTER` | `0s` | `0` waits indefinitely. |

Raising `WORKERS` parallelises parsing and database writes only; all workers
share one rate limiter, so HTTP pressure on the origin stays constant. If the
source starts returning `429`, raise `REQUEST_INTERVAL` to `5s`–`10s`.

## Testing

```bash
go test ./...
go vet ./...
docker compose config --quiet
```

Coverage focuses on the parts most likely to break silently: the robots.txt
parser and its fail-closed behaviour, rate-limiter interaction with
`Crawl-delay`, retry classification, and HTML parsing across catalogue pages,
story metadata (thousands separators, Vietnamese date formats) and chapter
bodies.

## Scope and limits

This crawler talks to exactly one host and refuses redirects off it. It does not
log in, solve CAPTCHAs, bypass paywalls or call private APIs, and it blocks CSS,
JavaScript, images, fonts and media in the browser because the HTML it needs is
already server-rendered.

## License and intended use

[MIT](LICENSE).

Built for personal offline reading of content the operator can already access.
The MIT licence covers this source code, not anything the crawler downloads —
retrieved content remains under its original copyright. Check the target site's
terms and your local law before running it, and do not redistribute what you
collect.
