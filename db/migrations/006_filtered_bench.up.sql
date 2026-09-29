-- Keep the IVFFlat expression distinct from the HNSW column index so each
-- method has one eligible ANN access path. This identity function is deliberately
-- not inlined; its per-row cost is part of the IVFFlat query measurement.
CREATE FUNCTION public.bench_ivf_vector(v public.vector) RETURNS public.vector
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE AS $$
BEGIN
    RETURN v;
END;
$$;

-- The index itself is built only after loading vectors by cmd/bench-index.
CREATE TABLE public.bench_index_builds (
    build_id UUID PRIMARY KEY,
    index_name TEXT NOT NULL,
    lists INTEGER NOT NULL CHECK (lists BETWEEN 2 AND 1000),
    build_settings JSONB NOT NULL,
    eligible_count BIGINT NOT NULL,
    dataset_sha256 TEXT NOT NULL,
    build_ms DOUBLE PRECISION NOT NULL CHECK (build_ms >= 0),
    postgres_version TEXT NOT NULL,
    pgvector_version TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE public.bench_filtered_runs (
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
    hnsw_index_options JSONB NOT NULL,
    ivfflat_build_id UUID NOT NULL REFERENCES public.bench_index_builds(build_id),
    ivfflat_index_options JSONB NOT NULL,
    ivfflat_build_settings JSONB NOT NULL,
    ivfflat_build_ms DOUBLE PRECISION NOT NULL,
    plan_mode TEXT NOT NULL CHECK (plan_mode = 'planner'),
    skipped_filters JSONB NOT NULL,
    percentile_definition TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE public.bench_filtered_queries (
    run_id UUID NOT NULL REFERENCES public.bench_filtered_runs(run_id) ON DELETE CASCADE,
    filter_id TEXT NOT NULL,
    filter_cutoff INTEGER,
    target_selectivity_pct DOUBLE PRECISION NOT NULL,
    matching_count BIGINT NOT NULL,
    actual_selectivity DOUBLE PRECISION NOT NULL CHECK (actual_selectivity BETWEEN 0 AND 1),
    query_id TEXT NOT NULL,
    query_sha256 TEXT NOT NULL,
    k INTEGER NOT NULL,
    method TEXT NOT NULL CHECK (method IN ('exact', 'hnsw', 'ivfflat')),
    search_parameters JSONB NOT NULL,
    postgres_settings JSONB NOT NULL,
    recall_at_k DOUBLE PRECISION NOT NULL CHECK (recall_at_k BETWEEN 0 AND 1),
    p50_ms DOUBLE PRECISION NOT NULL,
    p95_ms DOUBLE PRECISION NOT NULL,
    p99_ms DOUBLE PRECISION NOT NULL,
    returned_count INTEGER NOT NULL,
    returned_count_min INTEGER NOT NULL,
    returned_count_max INTEGER NOT NULL,
    too_few_samples INTEGER NOT NULL,
    plan_index_name TEXT,
    plan_json JSONB NOT NULL,
    PRIMARY KEY (run_id, filter_id, query_id, k, method)
);

CREATE TABLE public.bench_filtered_samples (
    run_id UUID NOT NULL,
    filter_id TEXT NOT NULL,
    query_id TEXT NOT NULL,
    k INTEGER NOT NULL,
    method TEXT NOT NULL,
    iteration INTEGER NOT NULL,
    latency_ms DOUBLE PRECISION NOT NULL CHECK (latency_ms >= 0),
    recall_at_k DOUBLE PRECISION NOT NULL CHECK (recall_at_k BETWEEN 0 AND 1),
    returned_count INTEGER NOT NULL,
    result_ids BIGINT[] NOT NULL,
    PRIMARY KEY (run_id, filter_id, query_id, k, method, iteration),
    FOREIGN KEY (run_id, filter_id, query_id, k, method)
        REFERENCES public.bench_filtered_queries(run_id, filter_id, query_id, k, method) ON DELETE CASCADE
);
