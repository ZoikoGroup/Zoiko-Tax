"""Typing names the generated schema needs that Python 3.10's stdlib lacks.

`schema.py` marks optional keys with `NotRequired`, which is in `typing` from
3.11. The generator would import it from `typing_extensions`, and that would
make a third-party package a runtime dependency of this SDK for the sake of
one annotation. `scripts/generate.py` points the import here instead.

On 3.10 the annotation is never evaluated at import time, because the schema
uses `from __future__ import annotations`. A type checker reads the source and
sees the real `NotRequired`, so static checking is exact on every version. The
fallback exists only so that `typing.get_type_hints()` on 3.10 does not fail;
there it reports every key as required, which is the one thing 3.10's own
`TypedDict` cannot represent anyway.
"""

from __future__ import annotations

import sys
from typing import TYPE_CHECKING

if sys.version_info >= (3, 11):
    from typing import NotRequired
elif TYPE_CHECKING:
    from typing_extensions import NotRequired
else:  # pragma: no cover - exercised only on Python 3.10
    try:
        from typing_extensions import NotRequired
    except ImportError:

        class _NotRequired:
            """`NotRequired[X]` evaluates to `X`."""

            def __getitem__(self, item: object) -> object:
                return item

            def __repr__(self) -> str:
                return "NotRequired"

        NotRequired = _NotRequired()

__all__ = ["NotRequired"]
