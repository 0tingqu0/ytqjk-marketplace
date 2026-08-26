from __future__ import annotations

import pytest

from test_image_document_extractor import (
    IMAGE,
    FakeBackend,
    _block,
    _extract,
    _quad,
    _result,
)

from scripts.image_document_extractor import (  # noqa: E402
    ImageDocumentExtractor,
    ImageExtractionStatus,
)


def test_pixel_limit_blocks_backend_before_ocr(monkeypatch) -> None:
    class Header:
        width = 10_000
        height = 10_000

        def __enter__(self):
            return self

        def __exit__(self, *_args):
            return None

    class CountingBackend:
        calls = 0

        def recognize(self, _source: bytes) -> object:
            self.calls += 1
            return _result()

    from scripts import image_input_guard

    backend = CountingBackend()
    monkeypatch.setattr(
        image_input_guard.Image,
        "open",
        lambda _stream: Header(),
    )
    outcome = ImageDocumentExtractor(backend).extract(IMAGE, "large.png")
    assert outcome.status is ImageExtractionStatus.FAILED
    assert backend.calls == 0


@pytest.mark.parametrize(
    "backend_result",
    (
        object(),
        _result((_block(quad=_quad(190, 10, 210, 30)),)),
        _result((_block(quad=_quad(10, 10, 10, 30)),)),
    ),
)
def test_invalid_backend_output_fails_closed(backend_result: object) -> None:
    outcome = _extract(backend_result)
    assert outcome.status is ImageExtractionStatus.FAILED
    assert outcome.result is None
    assert outcome.reason.startswith("EXTRACTION_FAILED")


@pytest.mark.parametrize("threshold", (0.87, 1.01, float("nan"), True))
def test_review_threshold_cannot_weaken_contract(threshold: object) -> None:
    with pytest.raises(ValueError, match="between 0.88 and 1"):
        ImageDocumentExtractor(
            FakeBackend(_result()),
            review_threshold=threshold,
        )
