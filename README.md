# Feedrank

A Go service that pulls content from two providers (one JSON, one XML), turns
it into a single model, scores it with the formula from the brief, and serves
ranked search over it. A small React dashboard ships inside the same binary.

**Stack:** Go 1.27, PostgreSQL 18, Redis 8, React 19

## Running it

You need Docker, and the two provider URLs that came with the brief.

```bash
cp .env.example .env   # then set PROVIDER1_URL and PROVIDER2_URL
make up
```

The provider URLs are deliberately not in the repository, so the service has
no default for them and refuses to start until both are set.

`make up` builds the image and starts Postgres, Redis and the app. The app
applies its migrations and ingests both providers on start-up, so data is
there within a second or two.

- Dashboard: <http://localhost:8080>
- API docs (Swagger UI): <http://localhost:8080/docs>
- Health: <http://localhost:8080/healthz>, <http://localhost:8080/readyz>

```bash
make ingest            # ingest again now (ingestion also runs every 5 minutes)
make postman           # walk through every feature (see below)
make logs              # follow the app's logs
make down              # stop; `make clean` also deletes the data
make test              # unit tests
make test-integration  # adds the Postgres tests (starts a container)
make help              # everything else
```

To run the server outside Docker (Go 1.27+, Node 24+ for the dashboard), use
`make dashboard` once and then `make run`. Configuration is environment
variables only. Apart from the two provider URLs every one has a default,
and [.env.example](.env.example) lists them. The Makefile reads `.env` too.

## API

| Method | Path | |
| --- | --- | --- |
| GET | `/api/v1/search` | Search. Takes `q`, `type` (`video`/`text`), `sort` (`popularity`/`relevance`), `page`, `page_size` |
| POST | `/api/v1/ingest` | Run ingestion now and report each provider's result. Needs an `X-API-Key` header |
| GET | `/healthz`, `/readyz` | Liveness and readiness |

The full contract is in [api/openapi.yaml](api/openapi.yaml) and is served at
`/docs`.

```bash
curl 'localhost:8080/api/v1/search?page_size=3'               # everything, by score
curl 'localhost:8080/api/v1/search?q=docker'                   # keyword, by relevance
curl 'localhost:8080/api/v1/search?q=devops&sort=popularity'   # keyword, by score
curl 'localhost:8080/api/v1/search?type=text'                  # one content type
curl 'localhost:8080/api/v1/search?page=abc&type=podcast'      # 400 listing both problems
```

Each result includes its score broken down term by term (`score_breakdown`),
so any ranking can be checked by hand. Errors are RFC 7807 problem documents
carrying a `request_id` that also appears in the logs.

**Postman.** [postman/feedrank.postman_collection.json](postman/feedrank.postman_collection.json)
walks through every feature in order: ingestion and its lock, each search
option, caching, validation, request ids and the rate limit. Every request has
a short description and tests, and search results show as a table in the
Visualize tab. Import it into Postman, or run it with `make postman` (needs
Node).

## Scoring

```
final = base × type_coefficient + engagement + recency
```

| | Video | Text |
| --- | --- | --- |
| base | `views/1000 + likes/100` | `reading_time + reactions/50` |
| type coefficient | 1.5 | 1.0 |
| engagement | `(likes/views) × 10` | `(reactions/reading_time) × 5` |

Recency adds 5 points within a week, 3 within a month (30 days), 1 within three
months (90 days), and nothing after that. When a denominator is zero, the
engagement term is zero rather than infinite.

**Recency is computed per query, not stored.** It is the only term that depends
on the current time, so a stored final score would be wrong a week later.
Ingestion stores the other terms. The search query adds recency, using SQL
generated from the same tier table as the Go function, and an integration test
checks that the two agree.

**The live data.** Every item is older than three months, so none gets recency
points. The one article comes out on top by a wide margin, because text
engagement (reactions per minute of reading) has no upper bound while video
engagement can never exceed 10. That follows from the formula as specified,
so I kept it.

## How it works

```
provider1 (JSON) ─┐                                          ┌─> Postgres, one transaction per page
                  ├─> page by page: normalise ─> score ─> upsert
provider2 (XML) ──┘                                          └─> Redis: bump cache generation (end of run)

HTTP ─> rate limit ─> search service ─> Redis cache ─> Postgres full-text query
```

```
cmd/server         wiring, HTTP server, graceful shutdown
internal/
  domain           content, score, search query, typed errors
  scoring          the formula
  provider         provider contract, paged feed walk, JSON/XML decoders,
                   HTTP client, circuit breaker
  ingest           streams each provider into storage; the schedule
  search           validation and caching around the query
  postgres         schema, upsert, search SQL
  redisstore       search cache, ingestion lock, API rate limiter
  httpapi          routes, handlers, middleware
web/dashboard      React + Vite, embedded into the binary
```

Each package that needs something from another declares a small interface for
it (`ingest.Store`, `search.Cache`, `httpapi.Searcher`, …). `main` connects the
pieces, and every service is tested against fakes.

### Providers

The two feeds differ in more than their syntax:

| | Provider 1 (JSON) | Provider 2 (XML) |
| --- | --- | --- |
| title | `title` | `headline` |
| tags | `tags[]` | `categories/category` |
| counters | `metrics{}` | `stats{}` |
| text type | n/a (videos only) | `article` |
| date | RFC 3339 | date only, read as midnight UTC |

Each provider is described by a `provider.Spec`: URL, paging parameters, a
decode function, a type mapping and its date layouts. Everything else is
shared: the paging walk, validation, the HTTP client and the error handling.

A few things the real endpoints do:

- **Ids collide.** Both use `v1`, `v2`, `v3` for different items, so content is
  keyed on `(provider, external_id)`.
- **Paging is ignored.** Both claim far more items than they serve (150 and 75,
  against 4 each) and return the same page whatever `page` says. The walk stops
  at a short page, or at a page with nothing new since the one before, so it
  costs one request.
- **Bad items are dropped, not guessed.** An unknown type, an unparseable date
  or a negative counter leaves that item out and logs why. It never fails the
  page. Guessing a type would score the item with the wrong equation.

**Adding a provider:** write a decode function that maps its fields onto
`provider.RawItem`, wrap it in a `Spec` (see [json.go](internal/provider/json.go)),
and add it to `newProviders` in [main.go](cmd/server/main.go). Nothing else
changes.

### Ingestion

- **Streamed a page at a time.** Each page is normalised, scored and written in
  its own transaction before the next is fetched, so memory stays flat however
  large a feed is.
- **A failure keeps what was stored.** A run that stops part-way (a provider
  error, a timeout, a lost lock) keeps the pages it wrote and reports how far
  it got: `pages`, `fetched`, `changed` and the `error`. That is safe because
  upserts are idempotent and no item depends on another; the next run goes
  over the same ground again.
- **Unchanged rows are not rewritten.** Re-ingesting identical data writes
  nothing and leaves the cache alone, and a formula change rewrites every row
  on the next run.
- **Providers run in parallel**, so a slow one does not hold up the other.
- **A manual run outlives its request.** `POST /api/v1/ingest` answers when the
  run is over; a client that gives up waiting does not cancel it.

### Resilience

Each provider gets its own HTTP client, so one provider's trouble never holds
up the other:

- **Rate limit:** at most 5 requests per second per provider by default
  (`x/time/rate`, `PROVIDER_REQUESTS_PER_SECOND`).
- **Retries:** network errors, 429 and 5xx are retried twice, with exponential
  backoff and full jitter. `Retry-After` is honoured. Other 4xx fail at once.
- **Circuit breaker:** after three failed calls in a row the provider is left
  alone for ten minutes (two poll cycles). Then a single trial call decides
  whether to close the breaker again.
- **Limits:** a 5 second timeout per attempt and a 4 MB cap on the body.

### Search

- **Full-text search** runs on a generated `tsvector` column (title weighted
  above tags) with a GIN index. Matching ignores case and accents and folds the
  Turkish İ/ı, so `egitim` finds "eğitim" and `istanbul` finds "İstanbul".
  Keywords accept quoted phrases, `or` and `-word`.
- **Partial words match.** Words of three or more letters also match as
  prefixes, so results show up while a word is still being typed (`begin`
  finds "Beginners"). Shorter words must match whole, or `go` would match
  "good" and "google".
- **Popularity order ranks a window, not every match.** Recency adds at most 5
  points, so a row whose stored score is more than 5 below the k-th best
  stored score can never reach the first k places. The query reads that cutoff
  from an index on the stored score, then ranks only the rows above it. This
  is exact, not an approximation; a test checks every page against a full
  sort.
- **Relevance order** ranks every match by text relevance, then by score.

### Large feeds

The live feeds hold four items each, but nothing assumes that. Paging and pace
are settings: `PROVIDER_PAGE_SIZE` (100), `PROVIDER_MAX_PAGES` (10,000) and
`PROVIDER_REQUESTS_PER_SECOND` (5). The defaults allow a million items per
provider in about half an hour, and `INGEST_TIMEOUT` (1h) bounds a run.
Reaching the page limit is reported as an error, never skipped in silence.

Measured locally against a fake provider serving a million items, with the pace
limit lifted. Every page below was also checked against a brute-force sort of
all rows, scored separately from the raw counters; all of them matched.

| | |
| --- | --- |
| Ingest 1M items (1,000 pages of 1,000) | 73 s, server memory about 22 MB |
| Ingest the same feed again | 75 s, 0 rows written |
| Everything by popularity, pages 1 to 100 | 30–70 ms |
| Deepest page allowed (page 500 of 100) | 200 ms |
| One content type, any page | 40–140 ms |
| Keyword matching 186k rows, by popularity | 60–200 ms (600 ms at page 500) |
| Keyword matching 186k rows, by relevance | 1.3 s (see trade-offs) |

### Caching

Search pages are cached in Redis for a minute. Cache keys include a generation
number, and a run that changed rows bumps it when it ends. That retires every
cached page at once without deleting anything, and the old entries expire on
their own.

Redis is optional, and the app keeps working while it is down. Searches go
straight to Postgres, the rate limiter lets requests through, and ingestion
runs without the lock (upserts are idempotent, so the cost is duplicate work,
not bad data).

### Running more than one instance

Ingestion runs inside the server, so each replica runs the scheduler. A Redis
lock lets only one of them ingest at a time: `SET NX` with a 30 second TTL,
renewed every 10 seconds while the run works and released by a
compare-and-delete script. A long run keeps the lock, a crashed instance frees
it within 30 seconds, and a run that finds its lock taken by another instance
stops. A manual trigger that loses the race gets a 409.

Migrations take a Postgres advisory lock for the same reason. The API rate
limit is counted in Redis, so all replicas share one budget per client.

## Technology choices

- **Go:** small static binaries, straightforward concurrency for fetching
  providers in parallel, and the standard library covers HTTP, JSON, XML and
  routing.
- **PostgreSQL:** handles both the transactional upserts and ranked full-text
  search. A separate search engine would mean keeping two stores in sync,
  which only pays off once relevance ranking over very broad matches matters
  (see trade-offs).
- **Redis:** the cache, the ingestion lock and rate-limit counters. All of it
  can be lost without harm.
- **React + Vite, embedded with `go:embed`:** one artifact to deploy, no CORS,
  and the UI always matches the API version it ships with.

Libraries are kept to a few: `pgx`, `go-redis`, `goose` for migrations and
`x/time/rate`. Tests add `testcontainers` and `miniredis`.

## Testing

```bash
make test               # unit tests, -race
make test-integration   # plus Postgres via testcontainers
make test-live          # the real provider endpoints
make postman            # the API walkthrough, against the running stack
make lint
```

- **Scoring and query validation** are table-driven, covering every recency
  edge and zero denominators.
- **Providers** are tested against payloads captured from the live endpoints
  (`testdata/`, with titles and tags swapped for neutral ones), plus paging,
  the page limit, stopping early, rejected items, retries, `Retry-After`, the
  size cap and the breaker's state changes.
- **Services and handlers** run against fakes, covering one transaction per
  page, runs that fail part-way, timeouts, a lost lock, cache invalidation,
  auth, the rate limit (`Retry-After` rounds up), security headers and error
  mapping.
- **Redis code** runs against miniredis, including lock renewal, a lost lock
  and ownership after expiry.
- **SQL** runs against a real Postgres, covering upsert idempotency over many
  pages, ranking that matches the Go formula, the popularity window matching
  a full sort on every page, relevance, folding, prefix matching, the type
  filter and pagination.
- The **live** test checks the real endpoints still match the decoders, using
  the URLs from `.env`. It is not part of CI, so a network blip cannot fail a
  build.

CI runs lint, the unit and integration tests, the dashboard build and the
image build.

## Trade-offs and next steps

- **Relevance order ranks every match.** Text relevance is computed per row,
  so a keyword that matches a large share of the table is slow to sort by
  relevance (1.3 s for 186k matches). Popularity order does not have this
  problem. A RUM index, which returns rows in rank order, or a dedicated
  search engine would be the next step.
- **A run is not applied atomically.** Pages are committed as they arrive, so
  while a large run is in progress a search can see some of a provider's
  items updated and others not yet. For a feed of independent items that is
  fine; a snapshot swap (load into a staging table, then switch) would be the
  fix if it ever mattered.
- **Offset pagination**, capped at page 500. With the popularity window even
  the deepest page is quick, and page numbers are what the dashboard needs.
  Keyset (cursor) pagination would lift the cap, at the cost of jumping to a
  page.
- **Totals are exact counts.** That is about 100 ms over a million matches;
  beyond that an estimate ("10,000+") would do.
- **Deletions upstream are not mirrored.** An item that disappears from a
  provider stays in the index.
- **The rate limit is a fixed window per client IP.** It ignores
  `X-Forwarded-For`, since that header can be forged without a trusted proxy.
- **Text search uses English stemming.** That suits the current content.
- **Observability is structured logs only.** Metrics and tracing would come next.
- **No Content-Security-Policy.** Every response sets `nosniff`, `DENY` framing
  and `no-referrer`, but a CSP would also need to allow the CDN that `/docs`
  loads Swagger UI from.
