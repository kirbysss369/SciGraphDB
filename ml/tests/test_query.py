import json
import stat

import pytest

from ml.embed import DIMENSION, MODEL_ID, MODEL_REVISION
from ml.query import write_vector


def test_offline_query_file(tmp_path) -> None:
    target = tmp_path / "query.json"
    vector = [0.0] * DIMENSION
    vector[0] = 1.0
    write_vector(target, vector)
    payload = json.loads(target.read_text())
    assert payload["model_id"] == MODEL_ID
    assert payload["model_revision"] == MODEL_REVISION
    assert payload["embedding"] == vector
    assert stat.S_IMODE(target.stat().st_mode) == 0o600
    with pytest.raises(ValueError):
        write_vector(target, [1.0])
