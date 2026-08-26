"""Bound image bytes and pixels before any OCR or vision inference."""

from __future__ import annotations

from io import BytesIO

from PIL import Image, UnidentifiedImageError


MAX_IMAGE_BYTES = 50 * 1024 * 1024
MAX_IMAGE_PIXELS = 50_000_000


def validate_image_input(
    value: bytes,
    *,
    max_bytes: int = MAX_IMAGE_BYTES,
    max_pixels: int = MAX_IMAGE_PIXELS,
) -> tuple[int, int]:
    if not isinstance(value, bytes) or not value:
        raise ValueError("image input must be non-empty bytes")
    if len(value) > max_bytes:
        raise ValueError("image byte limit exceeded")
    try:
        with Image.open(BytesIO(value)) as image:
            width, height = image.width, image.height
            valid = (
                type(width) is int
                and type(height) is int
                and width > 0
                and height > 0
            )
            if not valid:
                raise ValueError("image dimensions are invalid")
            if width * height > max_pixels:
                raise ValueError("image pixel limit exceeded")
            image.verify()
    except (UnidentifiedImageError, OSError) as error:
        raise ValueError("image decoding failed") from error
    return width, height


__all__ = [
    "MAX_IMAGE_BYTES",
    "MAX_IMAGE_PIXELS",
    "validate_image_input",
]
