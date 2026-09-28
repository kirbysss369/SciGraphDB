-- Research index for the pinned MiniLM vectors only. The migration is
-- reversible; m and ef_construction are build-time settings.
CREATE INDEX papers_embedding_hnsw_cosine_idx ON public.papers
    USING hnsw (embedding public.vector_cosine_ops)
    WITH (m = 16, ef_construction = 64)
    WHERE embedding IS NOT NULL
      AND public.vector_norm(embedding) > 0
      AND embedding_model = 'sentence-transformers/all-MiniLM-L6-v2'
      AND embedding_revision = '1110a243fdf4706b3f48f1d95db1a4f5529b4d41'
      AND embedding_text_version = 'title-abstract-v1';
