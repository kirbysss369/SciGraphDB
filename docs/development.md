# Linux development

This setup uses one `compose.yaml` on Ubuntu and Fedora. It requires Git, Go 1.25 or newer for the API, and a working Compose provider. Python 3.12 and uv are also required when running the embedding workflow; they remain optional for the Go API. No desktop services are used, so Fedora Workstation with Hyprland works the same as a terminal session on another Linux desktop.

Ubuntu 25.04 compatibility is a target, but that release is past its support period. Use a supported Ubuntu release for a machine exposed to untrusted networks. Install Docker Engine and the Docker Compose plugin through your chosen package source, or use Podman; this repository does not install system packages. Ensure your user can invoke the selected provider.

On Fedora 44, install Podman and a Compose provider (for example `podman-compose`) through Fedora packages. Rootless Podman is suitable. `scripts/compose.sh` selects `docker compose`, then `podman compose`, then `podman-compose`, in that order. If both Docker and Podman are installed, Docker Compose takes priority.

```bash
cp .env.example .env
# Edit POSTGRES_PASSWORD in .env before starting.
make doctor
make dev
```

`make dev` starts the container, checks the credentials, applies the migrations, and runs the API in the foreground. In a second terminal, use `make ps`, `make db-shell`, and `curl http://127.0.0.1:8080/readyz`. Ctrl+C stops the API; `make down` also stops the database. Use `make up` and `make run` if you want to start them separately.

The database is available only on `127.0.0.1:${PG_PORT:-5432}` on the host. Set `PG_PORT` in `.env` if 5432 is already occupied. The database name is `scigraph`. A named volume stores PostgreSQL 18 data under `/var/lib/postgresql`; PostgreSQL 18 container images use this parent mount to support their versioned data directory. The volume survives `make down` and container replacement.

Fedora enables SELinux by default. A named volume lets the container engine handle the database storage context, avoiding permissions and labeling problems caused by a host directory bind mount. Do not disable SELinux or make the data directory world writable.

`make up` starts the service, waits for PostgreSQL, checks password authentication and verifies the Go connection. Compose also monitors the healthcheck. `make logs` follows the database logs; `make down` stops the service while retaining data. `make db-reset` prompts before deleting the database volume. The PostgreSQL container includes `psql`, so a host installation of `psql` is unnecessary.

To verify the image and persistence manually:

```sql
CREATE EXTENSION IF NOT EXISTS vector;
SELECT current_setting('server_version');
SELECT extversion FROM pg_extension WHERE extname = 'vector';
CREATE TABLE IF NOT EXISTS bootstrap_check (id integer PRIMARY KEY);
INSERT INTO bootstrap_check (id) VALUES (1) ON CONFLICT DO NOTHING;
```

Run `make down`, `make up`, and `make db-shell` again, then:

```sql
SELECT extversion FROM pg_extension WHERE extname = 'vector';
SELECT * FROM bootstrap_check;
DROP TABLE bootstrap_check;
```

The extension creation above is a manual environment check; migration 001 also enables it.

## Go API configuration

Go commands read `.env` automatically; set `POSTGRES_PASSWORD` there once. They derive a loopback connection URL from `POSTGRES_USER`, `POSTGRES_PASSWORD`, and `PG_PORT` and escape password punctuation automatically. Shell variables override values in `.env`. Use single quotes for a value containing `$` or spaces, such as `POSTGRES_PASSWORD='a$b c'`; interpolation syntax is rejected to avoid disagreement with Compose. Keep `.env` private. An explicitly exported `DATABASE_URL` is supported for direct `go run` commands and external databases; the local Make targets clear that override so a stale exported URL cannot break the local workflow. The defaults below apply when a variable is absent:

| Variable | Default | Purpose |
| --- | --- | --- |
| `HTTP_ADDR` | `127.0.0.1:8080` | HTTP listener |
| `POSTGRES_PASSWORD` | required | Local container and Go database password |
| `POSTGRES_USER` | `scigraph` | Local database user |
| `PG_PORT` | `5432` | Local host port |
| `DATABASE_URL` | derived for local use | Optional explicit URL for direct Go commands and CI |
| `HTTP_READ_TIMEOUT` | `5s` | Request read timeout |
| `HTTP_WRITE_TIMEOUT` | `10s` | Response write timeout |
| `HTTP_IDLE_TIMEOUT` | `60s` | Idle connection timeout |
| `DB_CONNECT_TIMEOUT` | `3s` | Connection attempt timeout |
| `DB_PING_TIMEOUT` | `3s` | Readiness check timeout |

Run `make run` in one terminal and send `curl` requests from another while it remains running. Ctrl+C ends the process, so later requests will get connection refused; Make may report `Error 1` because the foreground command was interrupted. The API starts even when the database is offline; `/readyz` then returns 503. It closes the listener gracefully on SIGINT or SIGTERM and waits up to 10 seconds for active requests. Run `make fmt`, `go vet ./...`, and `make test` after Go changes.

If `/healthz` returns 200 but `/readyz` returns 503, run `make ps`, `make migrate-status` and `make logs`. If you edit `POSTGRES_PASSWORD` in `.env` after the database volume exists, `make up` updates the local database role through its container socket and verifies a TCP login. This preserves your data. Other programs using the old password must update their credentials. Container initialization itself only uses `POSTGRES_PASSWORD` on an empty volume.

If `make up` cannot change the role password, inspect the error and run `make db-shell`, then `\password scigraph` at the `psql` prompt. Do not use `make db-reset` for a password mismatch: it deletes the database volume.

## Migrations

Run against the local database after `make up`:

```bash
make migrate-status
make migrate
make migrate-status
```

`make migrate` applies pending SQL files in a transaction. `make migrate-down` rolls back one version: version 003 removes the embedding column and all stored vectors, version 002 drops the unresolved citation queue, and version 001 drops the core tables and their data and removes the `vector` extension. PostgreSQL refuses to drop the extension if another object depends on it. The version ledger (`schema_migrations`) remains after rollback. Applied migration checksums are verified before subsequent changes, so edit an applied SQL file only by creating a new migration instead.

For a migration cycle on a **disposable database**:

```bash
export DATABASE_URL_TEST='postgres://scigraph:<your-password>@127.0.0.1:5432/scigraph_test?sslmode=disable'
go test -tags integration ./db/migrations
```

Create `scigraph_test` separately before running this command and use credentials that can create the extension. The integration test applies the schema, checks constraints, rolls it back, applies it again, and cleans up. Never point `DATABASE_URL_TEST` at a database with useful data.

## OpenAlex client

`cmd/openalex-probe` queries works without using PostgreSQL. Set these variables in `.env` or the shell:

| Variable | Default | Purpose |
| --- | --- | --- |
| `OPENALEX_BASE_URL` | `https://api.openalex.org` | API origin, or a local test server |
| `OPENALEX_API_KEY` | unset | Optional Bearer token; required by the probe for more than 10 works |
| `OPENALEX_TIMEOUT` | `10s` | Per-attempt request and response timeout |

For a small manual network check, run:

```bash
go run ./cmd/openalex-probe --search 'graph databases' --limit 3
```

For a larger probe, set `OPENALEX_API_KEY` in your ignored `.env` or a private shell. Keep it out of command arguments, logs, and commits. Requests use `select` for the fields the client reads, at most 100 results per page, and `meta.next_cursor` for pagination. Transient HTTP 429 and 5xx responses get at most three attempts with short backoff. A zero remaining daily budget or a `Retry-After` beyond the five-second retry window fails promptly so a probe cannot wait all day. The maximum client search limit is 10,000 works; use an OpenAlex snapshot for bulk exports. API responses and abstracts are kept in memory only.

Tests use local `httptest.Server` fixtures and never need a public API call:

```bash
go fmt ./...
go vet ./...
go test ./...
```

The public probe above is optional. OpenAlex documents [authentication and rate limits](https://help.openalex.org/api/authentication/), [cursor paging](https://help.openalex.org/api/paging/), [work attributes](https://help.openalex.org/data/works/attributes/), and [error handling](https://help.openalex.org/api/errors/). Check these before substantially changing the client because API policies can change.

## Importing works

Apply the migrations (`make migrate`), then run a small import:

```bash
go run ./cmd/importer --search 'graph databases' --from-year 2020 --to-year 2024 --limit 10
```

The year bounds are inclusive OpenAlex publication-date filters and are applied before `--limit`. Without a key, the CLI permits at most 10 works. For a 100-work sample, set `OPENALEX_API_KEY` privately in `.env` or the shell and repeat with `--limit 100`; inspect the rows and daily API budget before trying `--limit 1000`. The client fetches at most 10,000 works per invocation. No automated test calls the public API, and a keyless probe/import should stay small.

Each batch of up to 100 works commits atomically. `papers` uses `ON CONFLICT (openalex_id) DO UPDATE` to replace DOI, title, abstract, year, citation count, metadata and `updated_at` while preserving `id` and `created_at`. `topics` updates names on `openalex_id` conflict. The import replaces each work's outgoing `paper_topics` and citations, then inserts topic pairs with `ON CONFLICT (paper_id, topic_id) DO UPDATE` for scores and queues unresolved references with `ON CONFLICT (citing_paper_id, cited_openalex_id) DO NOTHING`. Once both papers exist, a citation is inserted in the citing → cited direction with `ON CONFLICT (citing_paper_id, cited_paper_id) DO NOTHING`, and its queue entry is removed. Re-importing does not duplicate rows; removing a reference from OpenAlex removes that outgoing edge on the next import of its citing paper. Incoming edges from other works are retained. `metadata` stores only source and reference count, not raw API responses.

The summary reports `fetched` API works, `inserted` new papers, `updated` existing papers processed (including unchanged values), `skipped` malformed/duplicate works, `failed` works in a failed database batch, and `citations_linked` edges inserted during this invocation. A failed batch rolls back; earlier committed batches remain. Progress is logged once per committed batch. SIGINT/SIGTERM cancels requests and database work. Do not roll back migration 002 to retry an import: that drops unresolved references.

Check a 10-work sample, then a 100-work sample in `make db-shell`:

```sql
SELECT count(*) FROM papers;
SELECT count(*) FROM topics;
SELECT count(*) FROM paper_topics;
SELECT count(*) FROM citations;
SELECT count(*) FROM pending_citations;
SELECT openalex_id, count(*) FROM papers GROUP BY openalex_id HAVING count(*) > 1;
SELECT id, title, publication_year FROM papers ORDER BY cited_by_count DESC LIMIT 10;
```

Repeat the same import and compare table counts. Citation count may be low in a small sample because only references to already imported papers become edges. To run the deterministic fixture integration test, use an **empty disposable database** with `DATABASE_URL_TEST`:

```bash
go test -tags integration ./internal/importer -v
```

## Bulk OpenAlex imports

Migration 007 adds `openalex_bulk_imports`. The bulk command reads only the selected fields, fetches 100 works at a time with OpenAlex cursor paging, and commits each page and its next cursor in one transaction. Use one stable job ID and the exact same filter when resuming. Raising `--limit` extends the target without re-fetching earlier pages; a completed source cannot be extended. The target counts *fetched* works, while `inserted_count` and the `papers` count reflect actual rows. Invalid works are skipped, and pre-existing IDs are updated. The filter and counters are stored in the database; the API key stays in `.env` and is sent in a header.

For an initial computer science corpus with abstracts, spanning publication years:

```bash
make up
make migrate
go run ./cmd/import-bulk --job cs-articles-v1 \
  --filter 'primary_topic.field.id:17,type:article,has_abstract:true' --limit 200000
# Repeat exactly after an interruption or daily API budget reset.
# Later, extend the same cursor to 500,000 fetched works:
go run ./cmd/import-bulk --job cs-articles-v1 \
  --filter 'primary_topic.field.id:17,type:article,has_abstract:true' --limit 500000
```

Check `SELECT job_id,target_count,fetched_count,inserted_count,updated_count,skipped_count,finished FROM openalex_bulk_imports;` and `SELECT count(*) FROM papers;` in `make db-shell`. Do not roll back migration 007 during an import: that deletes the checkpoint. The source is OpenAlex's default core corpus, filtered to articles with abstracts whose *primary* field is Computer Science. It is the first cursor slice of a changing catalog, not a random sample or a frozen OpenAlex snapshot. Record the completed database snapshot and year distribution before benchmarking. A second job with a different filter can add rows, but will not continue the first cursor.

The local `cmd/importer` still caps one search at 10,000 works. OpenAlex permits larger cursor queries with up to 100 results per page. As of September 2026, list/filter calls cost about $0.10 per 1,000 requests and a free API key has a $1/day budget; 200,000 and 500,000 fetched works need roughly 2,000 and 5,000 successful page calls, respectively, excluding retries. An exhausted budget stops the job without losing committed pages. See [paging](https://help.openalex.org/api/paging/) and [example costs](https://help.openalex.org/access/example-costs/). The full works snapshot is about 615 GB compressed as of September 2026 and does not fit the 464 GB free on the initial Fedora server. See [snapshot](https://help.openalex.org/access/snapshot/).

## Paper embeddings

The Python workflow uses uv and Python 3.12 (`.python-version`, `pyproject.toml`, `uv.lock`). The model is [`sentence-transformers/all-MiniLM-L6-v2`](https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2), revision `1110a243fdf4706b3f48f1d95db1a4f5529b4d41`, Apache-2.0, with 384 output dimensions. Its model card describes truncation after 256 word pieces. The workflow uses CPU, including on machines with an Nvidia GPU. The lockfile selects CPU-only PyTorch wheels for Linux. GPU execution is optional and requires a separately managed CUDA-enabled PyTorch environment; the locked uv commands use CPU. Model weights are downloaded to the user's standard Hugging Face cache on first run and are never stored in the repository. This pretrained model produces embeddings only; this workflow does not train a model or create an ANN index.

For the Fedora 44 research server with an RTX 4060 and a driver reporting CUDA 13.4, use a local CUDA override after installing the locked dependencies. PyTorch publishes 2.13.0 wheels for CUDA 13.2. The override is deliberately local; `make embeddings` and a later `uv sync --locked` restore the CPU wheel. Verify CUDA availability before scanning a large corpus:

```bash
uv python install 3.12
make ml-sync
uv pip install --python .venv/bin/python --reinstall 'torch==2.13.0' \
  --index-url https://download.pytorch.org/whl/cu132
uv run --no-sync python -c 'import torch; print(torch.__version__, torch.version.cuda, torch.cuda.is_available(), torch.cuda.get_device_name(0))'
uv run --no-sync python -m ml.embed --limit 0 --batch-size 32 --device cuda
```

Run the last command only after the bulk import is complete. It can be interrupted and rerun; completed embeddings are skipped. The batch size of 32 is a conservative starting point for the 8 GB card. Query encoding through `ml.query` remains CPU only and takes just one query at a time. For a reproducible benchmark on imported works, generate a fixed set of actual model query vectors and point a copied filtered v2 config at its manifest; the bundled `exact_v1` vectors are synthetic correctness fixtures.

Text version `title-abstract-v1` strips leading/trailing whitespace, collapses internal whitespace to single spaces, and constructs `Title: <title>` followed by `\nAbstract: <abstract>` when the abstract is nonempty. Papers with fewer than three combined words are skipped. Titles alone can be embedded, including papers with no abstract. Inference normalizes vectors to unit length; the model's tokenizer truncates longer text. The SHA-256 of the constructed UTF-8 text, model ID, exact revision, text version, and timestamp are stored beside each vector. Migration 003 also clears a vector whenever an imported title or abstract changes. Changes to citation count or other metadata leave it intact. Bump `TEXT_VERSION` in `ml/embed.py` whenever preprocessing semantics change; the next scan refreshes stale versions. Changing models requires a new migration if the dimension changes.

After importing works, run:

```bash
make ml-sync
make embeddings                     # starts DB, migrates, scans first 100 IDs
make embeddings ML_LIMIT=0          # scan all IDs in batches
make embeddings ML_LIMIT=100 ML_BATCH_SIZE=16
```

Each batch is at most 100 papers (`16` by default), and its writes commit together. The scanner walks paper IDs in order, skips matching hashes and versions, and starts from the beginning on the next invocation. This makes interruption safe: previously committed rows are skipped on restart. The write checks that title and abstract still match the text read; a concurrent import leaves the changed row for a later run. `ML_LIMIT` counts **papers scanned**, including unchanged and insufficient text, and `0` scans all papers. The summary prints scanned, embedded, unchanged, insufficient, and conflicted counts; no API calls are made.

In `make db-shell`, inspect the first 100 papers and their status:

```sql
WITH sample AS (SELECT * FROM papers ORDER BY id LIMIT 100)
SELECT count(*) AS papers,
       count(*) FILTER (WHERE embedding IS NULL) AS null_vectors,
       count(*) FILTER (WHERE embedding IS NOT NULL AND vector_dims(embedding) <> 384) AS wrong_dimensions,
       count(*) FILTER (WHERE embedding IS NOT NULL AND (
           embedding_model <> 'sentence-transformers/all-MiniLM-L6-v2'
           OR embedding_revision <> '1110a243fdf4706b3f48f1d95db1a4f5529b4d41'
           OR embedding_text_version <> 'title-abstract-v1'
       )) AS stale_version
FROM sample;
```

Run `make embeddings` twice. The second summary should show `embedded=0` when source text and versions have not changed. `null_vectors` may reflect titles too short to embed; these remain null. To run database integration tests, use a **disposable database** with current migrations applied and `DATABASE_URL_TEST` set, then run `uv run --locked python -m pytest -m integration -q`. The fixture test inserts 100 synthetic works, interrupts after one batch, resumes, checks 384 dimensions, verifies version and source invalidation, reruns unchanged, and deletes its fixture. `RUN_MODEL_SMOKE=1` additionally runs the pinned model on 100 synthetic works using CPU, checks dimensions, null/stale counts, and idempotence; CI runs this check. No automated test alters your imported OpenAlex dataset. `make migrate-down` first removes benchmark history (migration 005), then the HNSW index (migration 004); the following rollback removes migration 003 and permanently discards vectors.

## Exact vector search

`POST /api/v1/search/vector` accepts exactly one JSON object. `embedding` must be 384 finite numbers that remain nonzero when converted to pgvector's float32 format; `limit` is optional (default 10, range 1–100). The endpoint accepts raw query vectors, including vectors generated from text by `ml.query`. It searches rows with non-null, nonzero vectors and matching model `sentence-transformers/all-MiniLM-L6-v2`, revision `1110a243fdf4706b3f48f1d95db1a4f5529b4d41`, and preprocessing version `title-abstract-v1`. Update the Go constants in `internal/search/exact.go` when changing the embedding workflow. Vectors from other models or text versions are excluded.

```json
{"embedding": [0.1, 0.2], "limit": 10}
```

The shortened array above illustrates the JSON shape; send **384** values. A successful response is `{"results":[{"id":1,"openalex_id":"https://openalex.org/W1","title":"Example","publication_year":2024,"distance":0.25}]}`. `publication_year` can be `null`. `distance` is cosine distance (`1 − cosine similarity`), so **smaller is closer**. Ranking is `distance ASC, id ASC`, including deterministic ID order for ties. Invalid JSON, vector, or limit returns HTTP 400; a failed database query returns HTTP 503. Results are an empty array when no eligible embeddings exist. The handler has a 64 KiB request bound and a five-second query deadline. The exact SQL materializes the filtered vectors before ordering them; an HNSW index cannot provide the exact result order, even after migration 004 adds one.

For a text query, encode locally on CPU, then pass the versioned vector file to the Go CLI:

```bash
uv run --locked python -m ml.query --text 'graph database citation analysis' --output /tmp/scigraph-query.json
go run ./cmd/vector-search --vector-file /tmp/scigraph-query.json --limit 10
```

The query text is whitespace-normalized, encoded directly (without the paper `Title:`/`Abstract:` labels), and not written to the JSON file. The file includes the model ID, exact revision, preprocessing version, and 384 floats; the Go CLI rejects a mismatched version. It reads `.env` using the same local password rules as the API. Query files are local artifacts: keep private queries out of commits.

[`experiments/exact_v1`](../experiments/exact_v1/README.md) pins synthetic query vectors and Top-K results as correctness ground truth for later ANN experiments. The test `go test -tags integration ./internal/search -run TestExactTopKBaseline -v` uses a disposable `DATABASE_URL_TEST`, repeats rankings, and logs `EXPLAIN (ANALYZE, BUFFERS)` for one query. Its buffer counts and execution time describe only that eight-paper synthetic fixture; measure imported datasets separately before making performance claims.

## HNSW experiment

Migration 004 builds a partial `vector_cosine_ops` HNSW index for nonzero vectors with the pinned MiniLM model, revision, and `title-abstract-v1` text version. Build-time settings are `m=16` (connections per graph layer) and `ef_construction=64` (build candidate list), following the [pgvector 0.8.6 HNSW documentation](https://github.com/pgvector/pgvector/blob/v0.8.6/README.md#hnsw). Rebuilding with other values requires a new research migration or dropping and recreating the index. The migration builds within the migrator's transaction, so apply it during a development maintenance window on large databases. Migration 005 stores benchmark history; roll it back before rolling back migration 004. Neither rollback removes paper embeddings.

The CLI's `--method hnsw --ef-search 80` selects approximate retrieval; `ef_search` is a **query-time** setting, applied locally in a transaction (range 1–1000). The default CLI and HTTP endpoint remain exact. The HNSW query uses an indexable `ORDER BY embedding <=> query LIMIT k` and then sorts the returned candidates by distance and ID. Equal-distance candidates at the Top-K boundary may be omitted by ANN, so only the exact path guarantees deterministic global ties. The HNSW index may not be selected for a small corpus; a sequential plan means that invocation did not measure ANN retrieval.

```bash
make migrate
go run ./cmd/vector-search --vector-file /tmp/scigraph-query.json --method hnsw --ef-search 80 --limit 10
make bench-smoke
go run ./cmd/bench --config experiments/configs/hnsw-cosine-v1.json
```

`cmd/bench` reads a validated, versioned JSON config from `experiments/configs/`. Paths are relative to the repository root. Both configs run the three fixed `experiments/exact_v1` query vectors at K=10 and K=20 against the **current database**. The seed deterministically shuffles each pass. Warm-up passes are excluded; each measured pass records client-observed latency and Recall@K (unique intersection divided by the smaller of K and exact result count). p50, p95 and p99 use **nearest rank**: sort measured samples ascending, select one-based rank `ceil(p*n/100)`, without interpolation. The JSON export includes every sample and `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` plan; CSV has one row per measured sample with summary percentiles. Exports go to ignored `experiments/results/` (or `--out-dir`) and are also stored atomically in `bench_runs`, `bench_queries` and `bench_samples`.

Each run records experiment/run/query IDs, config and query hashes, git commit and tracked dirty state, PostgreSQL and pgvector versions, a named dataset snapshot, SHA-256 digest of ordered eligible row IDs/source hashes/vector hashes, total and eligible paper counts, model and preprocessing versions, index options, query method and parameters, timestamp and sanitized query plans. Raw vectors and credentials are never exported. The index was built by migration 004 before the run, so `index_build_ms` is null and its build note distinguishes this from query latency. Exact ground truth uses the materialized exact query, and an exact plan containing HNSW fails. The runner records whether HNSW was selected. Set `require_hnsw_index` in a config to fail on sequential HNSW plans; inspect query shape, `ANALYZE` statistics, and dataset size first. It never forces a plan.

The small `smoke-v1.json` config runs one warm-up and three measured passes. On an empty database import papers and embed first. Its results, and the CI disposable synthetic fixture, do not establish a performance improvement for real OpenAlex works or a 100-row sample. The older integration test grows a disposable fixture to 3,005 eligible synthetic vectors before requiring an index plan and saves a separate `hnsw-synthetic-bench` artifact. The fixed vectors' expected Top-K IDs in `baseline.json` apply only to the eight-row fixture. Use an empty disposable database with `DATABASE_URL_TEST` for the runner persistence smoke: `go test -tags integration ./cmd/bench -v`.

## Filtered ANN research experiment

Migration 006 adds a reversible result schema and the `bench_ivf_vector` identity expression. It does **not** build IVFFlat on an empty or 100-row table. The separate index command checks that at least `max(2000, 500 × lists)` eligible, nonzero vectors exist before training. pgvector 0.8.6 recommends building IVFFlat only after loading data, starting around `rows / 1000` lists for up to one million rows, and choosing probes around the square root of lists. See the [pinned IVFFlat, filtering and iterative-scan documentation](https://github.com/pgvector/pgvector/blob/v0.8.6/README.md#ivfflat). Query probes must be less than lists here: pgvector notes that at probes equal to lists the planner does not use the index. This floor is a research guard, not evidence that 2,000 vectors make a meaningful performance benchmark.

Build on a disposable or local research dataset after embedding enough works:

```bash
make up
make migrate
make embeddings ML_LIMIT=0
go run ./cmd/bench-index --lists 3 build
go run ./cmd/bench-index status
go run ./cmd/bench --config experiments/configs/filtered-v2.json
# To remove the research index without removing paper vectors:
go run ./cmd/bench-index drop
```

Set `ivfflat_lists` in the JSON config to the index's actual `lists` value; `ivfflat_probes` must be below lists. `ivfflat_max_probes` and `hnsw_ef_search` are local query settings. In pgvector 0.8.6, `hnsw_iterative_scan` accepts `off`, `strict_order` or `relaxed_order`; `ivfflat_iterative_scan` accepts only `off` or `relaxed_order`. The sample config uses strict HNSW and relaxed IVFFlat, with an outer sort by distance and ID. `bench-index build` records training corpus hash/size, PostgreSQL/pgvector versions, lists, maintenance settings and index build milliseconds outside query timings. A changed corpus requires rebuilding the index before a filtered run. `bench-index drop` retains build history; rolling back migration 006 drops the IVFFlat index and filtered result tables.

The runner uses the same pinned queries at K=10 and K=20 for exact, HNSW and IVFFlat. It looks for `publication_year >= cutoff` values near the target 100%, 50%, 25%, 10%, 5% and 1% of **eligible vectors**. A target is skipped with a reason when distinct years or at least 20 matching rows are unavailable. The recorded matching count and actual selectivity are measured, not taken from the target. Null years count toward the unfiltered corpus but do not match a year cutoff. Exact search materializes the filtered set before sorting, so it is ground truth even when both ANN indexes exist.

pgvector applies the year filter after an ANN index scan. `strict_order` iterative scanning asks the index for more candidates up to its configured limit; it can still return fewer than K. The report stores every measured result count and Recall@K, a count range and the number of short samples, plus p50/p95/p99, PostgreSQL settings and redacted `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` for each method, filter and query. Warm-up, plans and IVFFlat training are outside measured latency. The JSON/CSV files are written to ignored `experiments/results/` and the same results to the migration 006 tables.

Both ANN access paths use exactly the stored vector. The IVFFlat index uses a non-inlined identity expression to keep it distinct from the existing HNSW index; the function call adds overhead to the IVFFlat query and is part of its measured latency. PostgreSQL chooses between the eligible index and a sequential plan. `plan_mode=planner` and `plan_index_name` show what actually executed; a sequential plan is not described as an IVFFlat or HNSW index performance result. No index is forced in this experiment. A future forced-plan comparison must have its own clearly labeled config and report.

For the disposable smoke with 3,040 synthetic eligible vectors in six year strata, run `DATABASE_URL_TEST=... go test -tags integration ./cmd/bench -run TestRunnerPersistenceSmoke -v`. It verifies the minimum-data guard, builds/drops IVFFlat, executes the fixed filters, exports results and checks database persistence. The smoke config is `experiments/configs/filtered-smoke-v2.json`. Its three measured samples and synthetic vectors are for wiring and correctness checks; they cannot support latency or speed claims for a real corpus, especially a 100-row sample.
