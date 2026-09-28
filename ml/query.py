"""Encode a text query into a portable, versioned JSON vector file."""

import argparse
import json
import os
from pathlib import Path

from ml.embed import DIMENSION, MODEL_ID, MODEL_REVISION, TEXT_VERSION, SentenceEncoder, normalize


def write_vector(output: Path, embedding: list[float]) -> None:
    if len(embedding) != DIMENSION:
        raise ValueError("query model returned the wrong dimension")
    data = {
        "model_id": MODEL_ID,
        "model_revision": MODEL_REVISION,
        "text_version": TEXT_VERSION,
        "embedding": embedding,
    }
    fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as file:
            fd = -1
            json.dump(data, file, separators=(",", ":"), allow_nan=False)
            file.write("\n")
    finally:
        if fd >= 0:
            os.close(fd)


def main() -> None:
    parser = argparse.ArgumentParser(description="Create an offline query vector")
    parser.add_argument("--text", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    text = normalize(args.text)
    if not text:
        parser.error("query text must not be blank")
    embedding = SentenceEncoder("cpu").encode([text])[0]
    write_vector(args.output, embedding)


if __name__ == "__main__":
    main()
