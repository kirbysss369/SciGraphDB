"""Resumable, bounded paper embedding generation."""

from __future__ import annotations

import argparse
import hashlib
import json
import logging
import math
import os
import re
from dataclasses import dataclass
from itertools import pairwise
from typing import TYPE_CHECKING, Protocol

from dotenv import dotenv_values
from psycopg import connect
from psycopg.conninfo import make_conninfo

if TYPE_CHECKING:
    from psycopg import Connection

MODEL_ID = "sentence-transformers/all-MiniLM-L6-v2"
MODEL_REVISION = "1110a243fdf4706b3f48f1d95db1a4f5529b4d41"
TEXT_VERSION = "title-abstract-v1"
DIMENSION = 384
LOG = logging.getLogger(__name__)


@dataclass(frozen=True)
class Paper:
    id: int
    title: str
    abstract: str | None
    embedding_model: str | None
    embedding_revision: str | None
    embedding_text_version: str | None
    embedding_text_sha256: str | None
    has_embedding: bool


@dataclass
class Counts:
    scanned: int = 0
    embedded: int = 0
    unchanged: int = 0
    insufficient: int = 0
    conflicted: int = 0


class Encoder(Protocol):
    def encode(self, texts: list[str]) -> list[list[float]]: ...


def normalize(value: str | None) -> str:
    return re.sub(r"\s+", " ", value or "").strip()


def build_text(title: str, abstract: str | None) -> str | None:
    clean_title = normalize(title)
    clean_abstract = normalize(abstract)
    if len((clean_title + " " + clean_abstract).split()) < 3:
        return None
    if clean_abstract:
        return f"Title: {clean_title}\nAbstract: {clean_abstract}"
    return f"Title: {clean_title}"


def text_sha256(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def needs_embedding(paper: Paper, digest: str) -> bool:
    return not (
        paper.has_embedding
        and paper.embedding_model == MODEL_ID
        and paper.embedding_revision == MODEL_REVISION
        and paper.embedding_text_version == TEXT_VERSION
        and paper.embedding_text_sha256 == digest
    )


def next_checkpoint(last_id: int, papers: list[Paper]) -> int:
    """Advance past a fully handled batch; a rerun starts at zero and skips fresh rows."""
    if not papers:
        return last_id
    if papers[0].id <= last_id or any(a.id >= b.id for a, b in pairwise(papers)):
        raise ValueError("paper IDs must be strictly increasing past the checkpoint")
    return papers[-1].id


def database_url() -> str:
    values = dotenv_values(".env", interpolate=False)
    if url := os.getenv("DATABASE_URL"):
        return url

    # A legacy DATABASE_URL inside .env must not override local Compose settings.
    def local(name: str) -> str | None:
        return os.environ[name] if name in os.environ else values.get(name)

    password = local("POSTGRES_PASSWORD")
    if not password:
        raise ValueError("set POSTGRES_PASSWORD in .env or provide DATABASE_URL")
    port = local("PG_PORT") or "5432"
    if not port.isdecimal() or not 1 <= int(port) <= 65535:
        raise ValueError("PG_PORT must be between 1 and 65535")
    return make_conninfo(
        host="127.0.0.1",
        port=port,
        dbname="scigraph",
        user=local("POSTGRES_USER") or "scigraph",
        password=password,
        sslmode="disable",
        connect_timeout=5,
    )


def _papers(conn: Connection, last_id: int, amount: int) -> list[Paper]:
    with conn.cursor() as cur:
        cur.execute(
            """SELECT id, title, abstract, embedding_model, embedding_revision,
                      embedding_text_version, embedding_text_sha256, embedding IS NOT NULL
               FROM public.papers WHERE id > %s ORDER BY id LIMIT %s""",
            (last_id, amount),
        )
        return [Paper(*row) for row in cur.fetchall()]


def _vector_literal(values: list[float]) -> str:
    if len(values) != DIMENSION:
        raise ValueError(f"model returned {len(values)} dimensions, expected {DIMENSION}")
    if not all(math.isfinite(float(value)) for value in values):
        raise ValueError("model returned a non-finite vector")
    return "[" + ",".join(str(float(value)) for value in values) + "]"


def generate(conn: Connection, encoder: Encoder, *, limit: int, batch_size: int) -> Counts:
    if limit < 0 or not 1 <= batch_size <= 100:
        raise ValueError("limit must be non-negative and batch_size must be between 1 and 100")
    if not conn.autocommit:
        raise ValueError("generate requires an autocommit connection for per-batch commits")
    counts = Counts()
    last_id = 0
    while limit == 0 or counts.scanned < limit:
        amount = batch_size if limit == 0 else min(batch_size, limit - counts.scanned)
        papers = _papers(conn, last_id, amount)
        if not papers:
            break
        pending: list[tuple[Paper, str, str]] = []
        for paper in papers:
            text = build_text(paper.title, paper.abstract)
            if text is None:
                counts.insufficient += 1
                continue
            digest = text_sha256(text)
            if needs_embedding(paper, digest):
                pending.append((paper, text, digest))
            else:
                counts.unchanged += 1

        if pending:
            vectors = encoder.encode([text for _, text, _ in pending])
            if len(vectors) != len(pending):
                raise ValueError("model returned a different number of vectors")
            # Validate every vector before writing any of the batch.
            literals = [_vector_literal(vector) for vector in vectors]
            with conn.transaction(), conn.cursor() as cur:
                for (paper, _, digest), vector in zip(pending, literals):
                    cur.execute(
                        """UPDATE public.papers SET embedding = %s::public.vector,
                                  embedding_model = %s, embedding_revision = %s,
                                  embedding_text_version = %s, embedding_text_sha256 = %s,
                                  embedded_at = now()
                               WHERE id = %s AND title = %s
                                 AND abstract IS NOT DISTINCT FROM %s""",
                        (
                            vector,
                            MODEL_ID,
                            MODEL_REVISION,
                            TEXT_VERSION,
                            digest,
                            paper.id,
                            paper.title,
                            paper.abstract,
                        ),
                    )
                    if cur.rowcount == 1:
                        counts.embedded += 1
                    else:
                        counts.conflicted += 1
        counts.scanned += len(papers)
        last_id = next_checkpoint(last_id, papers)
        LOG.info("batch complete: scanned=%d embedded=%d", counts.scanned, counts.embedded)
    return counts


class SentenceEncoder:
    def __init__(self, device: str) -> None:
        # Import lazily: preprocessing and database tests need no model weights.
        from sentence_transformers import SentenceTransformer

        self.model = SentenceTransformer(
            MODEL_ID, revision=MODEL_REVISION, device=device, trust_remote_code=False
        )
        if self.model.get_embedding_dimension() != DIMENSION:
            raise ValueError("model dimension differs from migration 003")

    def encode(self, texts: list[str]) -> list[list[float]]:
        result = self.model.encode(
            texts,
            batch_size=len(texts),
            normalize_embeddings=True,
            convert_to_numpy=True,
            show_progress_bar=False,
        )
        return result.tolist()


def main() -> None:
    parser = argparse.ArgumentParser(description="Generate versioned paper embeddings")
    parser.add_argument("--limit", type=int, default=100, help="papers to scan (0 for all)")
    parser.add_argument("--batch-size", type=int, default=16)
    parser.add_argument("--device", choices=("cpu", "cuda"), default="cpu")
    args = parser.parse_args()
    if args.limit < 0 or not 1 <= args.batch_size <= 100:
        parser.error("limit must be >= 0 and batch-size must be 1..100")
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
    with connect(database_url(), autocommit=True) as conn:
        # Fail early if the migration is missing, before downloading model weights.
        with conn.cursor() as cur:
            cur.execute("SELECT embedding FROM public.papers LIMIT 0")
        encoder = SentenceEncoder(args.device)
        counts = generate(conn, encoder, limit=args.limit, batch_size=args.batch_size)
    print(json.dumps(counts.__dict__, sort_keys=True))


if __name__ == "__main__":
    main()
