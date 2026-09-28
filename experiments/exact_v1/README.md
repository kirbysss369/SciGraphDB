# Exact search baseline v1

`baseline.json` defines a small, synthetic PostgreSQL fixture. `query-*.json` are fixed 384-dimensional offline query vector files accepted by `cmd/vector-search`. The expected Top-K paper IDs and cosine distances are versioned in `baseline.json`. Identical embeddings deliberately tie; `papers.id ASC` resolves each tie. A stale model revision, a zero vector, and a null vector are present to check exclusion.

These one-hot vectors are **synthetic**, not outputs of the MiniLM model and not a benchmark for the imported OpenAlex corpus. They give a reproducible correctness baseline for later ANN work on the same fixture. Keep the fixture and expected answers fixed when comparing an approximate implementation. To build a baseline for a real corpus later, store the corpus snapshot and exact query outputs together; rankings depend on the corpus contents and embedding versions.

Run the disposable-database check and inspect its measured plan:

```bash
DATABASE_URL_TEST='postgres://.../disposable?sslmode=disable' go test -tags integration ./internal/search -run TestExactTopKBaseline -v
```

The test applies the migrations to an empty disposable database, inserts the fixture, logs `EXPLAIN (ANALYZE, BUFFERS)` for `positive-x`, and rolls everything back. The exact plan has a sequential scan and explicit sort even with migration 004's HNSW index present. The test then grows the disposable corpus to 3,005 eligible synthetic vectors to inspect HNSW plans and smoke-test `cmd/bench`. The fixture's original Top-K answers are checked before those extra rows are inserted. Times and buffer counts depend on the test machine.
