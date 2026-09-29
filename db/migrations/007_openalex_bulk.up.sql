CREATE TABLE public.openalex_bulk_imports (
    job_id TEXT PRIMARY KEY CHECK (job_id ~ '^[a-zA-Z0-9_-]{1,80}$'),
    source_filter TEXT NOT NULL,
    target_count INTEGER NOT NULL CHECK (target_count BETWEEN 1 AND 500000),
    next_cursor TEXT NOT NULL DEFAULT '*',
    fetched_count INTEGER NOT NULL DEFAULT 0 CHECK (fetched_count >= 0),
    inserted_count INTEGER NOT NULL DEFAULT 0 CHECK (inserted_count >= 0),
    updated_count INTEGER NOT NULL DEFAULT 0 CHECK (updated_count >= 0),
    skipped_count INTEGER NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
    citations_linked INTEGER NOT NULL DEFAULT 0 CHECK (citations_linked >= 0),
    finished BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (fetched_count <= target_count),
    CHECK (NOT finished OR next_cursor = '')
);
