DROP TRIGGER papers_invalidate_embedding ON public.papers;
DROP FUNCTION public.invalidate_paper_embedding();
ALTER TABLE public.papers
    DROP CONSTRAINT papers_embedding_metadata_check,
    DROP COLUMN embedding,
    DROP COLUMN embedding_model,
    DROP COLUMN embedding_revision,
    DROP COLUMN embedding_text_version,
    DROP COLUMN embedding_text_sha256,
    DROP COLUMN embedded_at;
