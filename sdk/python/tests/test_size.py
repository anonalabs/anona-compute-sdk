import e2b
import pytest

import anona_compute
from anona_compute.sandbox import SIZE_METADATA_KEY, size_metadata


def test_size_metadata_normalises_and_keeps_metadata():
    assert size_metadata("L", {"name": "x"}) == {"name": "x", SIZE_METADATA_KEY: "large"}
    assert size_metadata("xlarge") == {SIZE_METADATA_KEY: "xl"}
    with pytest.raises(ValueError, match="small, medium, large, xl"):
        size_metadata("huge")


def test_create_passes_the_size_as_metadata(monkeypatch):
    seen = {}

    def fake_create(cls, template=None, *args, **kwargs):
        seen["template"], seen["args"], seen["kwargs"] = template, args, kwargs
        return "sbx"

    monkeypatch.setattr(e2b.Sandbox, "create", classmethod(fake_create))
    assert anona_compute.Sandbox.create("desktop", size="medium", metadata={"name": "d"}) == "sbx"
    assert seen["kwargs"]["metadata"] == {"name": "d", SIZE_METADATA_KEY: "medium"}
    anona_compute.Sandbox.create("base", 300, {"k": "v"}, size="small")
    assert seen["args"] == (300, {"k": "v", SIZE_METADATA_KEY: "small"})
    anona_compute.Sandbox.create("base")
    assert "metadata" not in seen["kwargs"]
