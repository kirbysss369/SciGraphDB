CREATE TABLE public.bench_runs (
    run_id UUID PRIMARY KEY,
    experiment_id TEXT NOT NULL,
    config_version INTEGER NOT NULL,
    config_sha256 TEXT NOT NULL,
    git_commit TEXT NOT NULL,
    git_dirty BOOLEAN NOT NULL,
    postgres_version TEXT NOT NULL,
    pgvector_version TEXT NOT NULL,
    dataset_snapshot TEXT NOT NULL,
    dataset_sha256 TEXT NOT NULL,
    paper_count BIGINT NOT NULL,
    eligible_count BIGINT NOT NULL,
    model_id TEXT NOT NULL,
    model_revision TEXT NOT NULL,
    text_version TEXT NOT NULL,
    query_set_version TEXT NOT NULL,
    query_set_sha256 TEXT NOT NULL,
    seed BIGINT NOT NULL,
    warmup_iterations INTEGER NOT NULL,
    measured_iterations INTEGER NOT NULL,
    index_name TEXT NOT NULL,
    index_build_options JSONB NOT NULL,
    index_build_ms DOUBLE PRECISION,
    index_build_note TEXT NOT NULL,
    percentile_definition TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE public.bench_queries (
    run_id UUID NOT NULL REFERENCES public.bench_runs(run_id) ON DELETE CASCADE,
    query_id TEXT NOT NULL,
    query_sha256 TEXT NOT NULL,
    k INTEGER NOT NULL,
    method TEXT NOT NULL CHECK (method IN ('exact', 'hnsw')),
    search_parameters JSONB NOT NULL,
    recall_at_k DOUBLE PRECISION NOT NULL CHECK (recall_at_k BETWEEN 0 AND 1),
    p50_ms DOUBLE PRECISION NOT NULL,
    p95_ms DOUBLE PRECISION NOT NULL,
    p99_ms DOUBLE PRECISION NOT NULL,
    index_used BOOLEAN NOT NULL,
    plan_json JSONB NOT NULL,
    PRIMARY KEY (run_id, query_id, k, method)
);

CREATE TABLE public.bench_samples (
    run_id UUID NOT NULL,
    query_id TEXT NOT NULL,
    k INTEGER NOT NULL,
    method TEXT NOT NULL,
    iteration INTEGER NOT NULL,
    latency_ms DOUBLE PRECISION NOT NULL CHECK (latency_ms >= 0),
    recall_at_k DOUBLE PRECISION NOT NULL CHECK (recall_at_k BETWEEN 0 AND 1),
    result_ids BIGINT[] NOT NULL,
    PRIMARY KEY (run_id, query_id, k, method, iteration),
    FOREIGN KEY (run_id, query_id, k, method)
        REFERENCES public.bench_queries(run_id, query_id, k, method) ON DELETE CASCADE
);
