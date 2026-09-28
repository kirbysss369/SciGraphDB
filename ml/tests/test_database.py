import os

import pytest
from psycopg import connect

from ml.embed import DIMENSION, MODEL_ID, MODEL_REVISION, TEXT_VERSION, SentenceEncoder, generate


class FakeEncoder:
    def __init__(self, fail_after: int | None = None) -> None:
        self.calls = 0
        self.fail_after = fail_after

    def encode(self, texts: list[str]) -> list[list[float]]:
        self.calls += 1
        if self.fail_after is not None and self.calls > self.fail_after:
            raise RuntimeError("interrupted inference")
        return [[0.01] * DIMENSION for _ in texts]


@pytest.mark.integration
def test_100_papers_resume_stale_and_idempotent() -> None:
    dsn = os.getenv("DATABASE_URL_TEST")
    if not dsn:
        pytest.skip("DATABASE_URL_TEST must point to a disposable migrated database")
    with connect(dsn, autocommit=True) as conn:
        with conn.cursor() as cur:
            cur.execute("SELECT count(*) FROM public.papers")
            assert cur.fetchone()[0] == 0, "integration test requires an empty database"
            cur.execute("""INSERT INTO public.papers (openalex_id, title, abstract)
                SELECT 'fixture-' || i, 'Research paper number ' || i,
                       CASE WHEN i % 5 = 0 THEN NULL ELSE 'A reproducible abstract' END
                FROM generate_series(1, 100) AS i""")
        try:
            with pytest.raises(RuntimeError, match="interrupted"):
                generate(conn, FakeEncoder(fail_after=1), limit=100, batch_size=16)
            with conn.cursor() as cur:
                cur.execute("SELECT count(*) FROM public.papers WHERE embedding IS NOT NULL")
                assert cur.fetchone()[0] == 16  # First transaction survived the interruption.

            model = FakeEncoder()
            result = generate(conn, model, limit=100, batch_size=16)
            assert (result.scanned, result.embedded, result.unchanged) == (100, 84, 16)
            with conn.cursor() as cur:
                cur.execute(
                    """SELECT count(*), count(*) FILTER (WHERE embedding IS NULL),
                        count(*) FILTER (WHERE vector_dims(embedding) <> %s),
                        count(*) FILTER (WHERE embedding_model <> %s
                            OR embedding_revision <> %s OR embedding_text_version <> %s)
                    FROM public.papers""",
                    (DIMENSION, MODEL_ID, MODEL_REVISION, TEXT_VERSION),
                )
                assert cur.fetchone() == (100, 0, 0, 0)

            second = generate(conn, FakeEncoder(), limit=100, batch_size=25)
            assert (second.embedded, second.unchanged) == (0, 100)
            with conn.cursor() as cur:
                cur.execute(
                    "UPDATE public.papers SET title = 'A new research title' WHERE openalex_id = 'fixture-1'"
                )
                cur.execute(
                    "SELECT embedding IS NULL FROM public.papers WHERE openalex_id = 'fixture-1'"
                )
                assert cur.fetchone()[0]
                cur.execute(
                    "UPDATE public.papers SET embedding_text_version = 'old' WHERE openalex_id = 'fixture-2'"
                )
            third = generate(conn, FakeEncoder(), limit=100, batch_size=20)
            assert (third.embedded, third.unchanged) == (2, 98)
        finally:
            with conn.cursor() as cur:
                cur.execute("TRUNCATE public.papers CASCADE")


@pytest.mark.integration
@pytest.mark.model
def test_real_cpu_model_on_100_fixture_papers() -> None:
    dsn = os.getenv("DATABASE_URL_TEST")
    if not dsn or os.getenv("RUN_MODEL_SMOKE") != "1":
        pytest.skip("set DATABASE_URL_TEST and RUN_MODEL_SMOKE=1 for a real CPU smoke check")
    with connect(dsn, autocommit=True) as conn:
        with conn.cursor() as cur:
            cur.execute("SELECT count(*) FROM public.papers")
            assert cur.fetchone()[0] == 0, "integration test requires an empty database"
            cur.execute("""INSERT INTO public.papers (openalex_id, title, abstract)
                SELECT 'model-fixture-' || i, 'Graph database research paper ' || i,
                       CASE WHEN i % 5 = 0 THEN NULL
                            ELSE 'Scientific data and citation analysis using graph databases' END
                FROM generate_series(1, 100) AS i""")
        try:
            encoder = SentenceEncoder("cpu")
            result = generate(conn, encoder, limit=100, batch_size=16)
            assert (result.scanned, result.embedded, result.insufficient) == (100, 100, 0)
            with conn.cursor() as cur:
                cur.execute(
                    """SELECT count(*), count(*) FILTER (WHERE embedding IS NULL),
                           count(*) FILTER (WHERE vector_dims(embedding) <> %s),
                           count(*) FILTER (WHERE embedding_model <> %s
                               OR embedding_revision <> %s OR embedding_text_version <> %s)
                    FROM public.papers""",
                    (DIMENSION, MODEL_ID, MODEL_REVISION, TEXT_VERSION),
                )
                assert cur.fetchone() == (100, 0, 0, 0)
            repeated = generate(conn, encoder, limit=100, batch_size=16)
            assert (repeated.embedded, repeated.unchanged) == (0, 100)
        finally:
            with conn.cursor() as cur:
                cur.execute("TRUNCATE public.papers CASCADE")
