import hashlib
from pathlib import Path

import pytest

from ml.embed import (
    MODEL_ID,
    MODEL_REVISION,
    TEXT_VERSION,
    Paper,
    _vector_literal,
    build_text,
    database_url,
    needs_embedding,
    next_checkpoint,
    normalize,
    text_sha256,
)


def paper(identifier: int, *, present: bool = True) -> Paper:
    return Paper(
        identifier,
        "Research paper title",
        None,
        MODEL_ID,
        MODEL_REVISION,
        TEXT_VERSION,
        text_sha256("Title: Research paper title"),
        present,
    )


def test_preprocessing_is_deterministic_and_skips_short_text() -> None:
    assert normalize("  Multi\n  space\t title ") == "Multi space title"
    assert build_text("  A   research\n title ", " Some\t abstract   text  ") == (
        "Title: A research title\nAbstract: Some abstract text"
    )
    assert build_text("A good title", None) == "Title: A good title"
    assert build_text("AI", "  ") is None
    assert text_sha256("hello") == hashlib.sha256(b"hello").hexdigest()


def test_checkpoint_and_staleness() -> None:
    first, second = paper(4), paper(9)
    assert next_checkpoint(0, [first, second]) == 9
    assert next_checkpoint(9, []) == 9
    with pytest.raises(ValueError):
        next_checkpoint(9, [first])
    with pytest.raises(ValueError):
        next_checkpoint(0, [second, first])
    digest = first.embedding_text_sha256
    assert digest is not None
    assert not needs_embedding(first, digest)
    assert needs_embedding(first, text_sha256("new text"))
    assert needs_embedding(paper(4, present=False), digest)
    assert needs_embedding(
        Paper(4, first.title, None, MODEL_ID, "old", TEXT_VERSION, digest, True), digest
    )
    assert needs_embedding(
        Paper(4, first.title, None, MODEL_ID, MODEL_REVISION, "old", digest, True), digest
    )


def test_vector_validation() -> None:
    with pytest.raises(ValueError, match="dimensions"):
        _vector_literal([1.0])
    with pytest.raises(ValueError, match="non-finite"):
        _vector_literal([float("nan")] * 384)


def test_local_password_and_legacy_url(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    (tmp_path / ".env").write_text(
        "POSTGRES_PASSWORD='a$b c'\nPG_PORT=55432\n"
        "DATABASE_URL=postgres://obsolete:wrong@127.0.0.1/old\n"
    )
    monkeypatch.chdir(tmp_path)
    for key in ("DATABASE_URL", "POSTGRES_PASSWORD", "POSTGRES_USER", "PG_PORT"):
        monkeypatch.delenv(key, raising=False)
    conninfo = database_url()
    assert "host=127.0.0.1" in conninfo
    assert "port=55432" in conninfo
    assert "password='a$b c'" in conninfo
    assert "obsolete" not in conninfo
    monkeypatch.setenv("POSTGRES_PASSWORD", "shell-override")
    assert "password=shell-override" in database_url()
    monkeypatch.setenv("DATABASE_URL", "postgres://external/test")
    assert database_url() == "postgres://external/test"
