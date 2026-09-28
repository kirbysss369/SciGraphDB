ALTER TABLE public.papers
    ADD COLUMN embedding public.vector(384),
    ADD COLUMN embedding_model TEXT,
    ADD COLUMN embedding_revision TEXT,
    ADD COLUMN embedding_text_version TEXT,
    ADD COLUMN embedding_text_sha256 TEXT,
    ADD COLUMN embedded_at TIMESTAMPTZ,
    ADD CONSTRAINT papers_embedding_metadata_check CHECK (
        (embedding IS NULL AND embedding_model IS NULL AND embedding_revision IS NULL
         AND embedding_text_version IS NULL AND embedding_text_sha256 IS NULL AND embedded_at IS NULL)
        OR
        (embedding IS NOT NULL AND embedding_model IS NOT NULL AND embedding_revision IS NOT NULL
         AND embedding_text_version IS NOT NULL
         AND embedding_text_sha256 ~ '^[0-9a-f]{64}$' AND embedded_at IS NOT NULL)
    );

-- Imported title/abstract changes invalidate previously computed vectors.
-- Other OpenAlex metadata updates preserve them.
CREATE FUNCTION public.invalidate_paper_embedding() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.title IS DISTINCT FROM OLD.title OR NEW.abstract IS DISTINCT FROM OLD.abstract THEN
        NEW.embedding := NULL;
        NEW.embedding_model := NULL;
        NEW.embedding_revision := NULL;
        NEW.embedding_text_version := NULL;
        NEW.embedding_text_sha256 := NULL;
        NEW.embedded_at := NULL;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER papers_invalidate_embedding
BEFORE UPDATE OF title, abstract ON public.papers
FOR EACH ROW EXECUTE FUNCTION public.invalidate_paper_embedding();
