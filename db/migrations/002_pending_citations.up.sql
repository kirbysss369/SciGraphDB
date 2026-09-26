CREATE TABLE public.pending_citations (
    citing_paper_id BIGINT NOT NULL REFERENCES public.papers(id) ON DELETE CASCADE,
    cited_openalex_id TEXT NOT NULL CHECK (btrim(cited_openalex_id) <> ''),
    PRIMARY KEY (citing_paper_id, cited_openalex_id)
);

CREATE INDEX pending_citations_cited_openalex_id_idx ON public.pending_citations (cited_openalex_id);
