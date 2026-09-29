DROP TABLE public.bench_filtered_samples;
DROP TABLE public.bench_filtered_queries;
DROP TABLE public.bench_filtered_runs;
DROP TABLE public.bench_index_builds;
DROP INDEX IF EXISTS public.papers_embedding_ivfflat_cosine_idx;
DROP FUNCTION public.bench_ivf_vector(public.vector);
