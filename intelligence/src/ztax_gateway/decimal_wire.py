"""Fiscal quantities crossing the Go/Python boundary.

ADR-0006 §2.3: every fiscal quantity in a ZoikoTax proto is a ``string``
carrying the ADR-0011 canonical decimal form. ``double`` and ``float`` are
forbidden in every ZoikoTax proto file, without exception. The Python side
parses with ``decimal.Decimal``; the Go side with ``apd``. Both implement the
General Decimal Arithmetic Specification, which is the property ADR-0001 §3.1
selected ``apd`` for in the first place.

This module is where that holds or fails on the Python side. Two things it does
that a thinner wrapper would not:

* ``parse`` refuses a ``float`` **argument**, not just a malformed string. The
  failure this guards is not somebody sending "1.2.3"; it is somebody writing
  ``parse(amount)`` where ``amount`` came from ``json.loads``, which yields a
  float for any JSON number and yields it silently.
* ``render`` preserves trailing zeros. ``0.10`` and ``0.1`` are different
  assertions about precision and must digest differently (ADR-0011 §2.2), and
  ``Decimal.__str__`` gets this right only because the exponent is carried —
  which is exactly why the context below never normalises.
"""

from __future__ import annotations

from decimal import (
    ROUND_HALF_EVEN,
    Context,
    Decimal,
    DivisionByZero,
    InvalidOperation,
    Overflow,
    Underflow,
)
from typing import Any

# 34 digits: the decimal128 coefficient width, and the precision ADR-0002 §2.1
# fixes for the estate. Python's default context is 28, so leaving it alone
# would mean the two sides disagreed on a 30-digit value — silently, and only
# for inputs nobody tests with.
PRECISION = 34

# The context for parsing and for any arithmetic this process does.
#
# Rounding is set because a Context requires one; it should never be reached.
# The Gateway does no fiscal arithmetic — ADR-0006 §2.6 gives it no path to a
# fiscal type at all — so a rounding event here means something is computing
# that should not be.
WIRE_CONTEXT = Context(
    prec=PRECISION,
    rounding=ROUND_HALF_EVEN,
    traps=[InvalidOperation, DivisionByZero, Overflow, Underflow],
)


class DecimalWireError(ValueError):
    """A value that cannot cross the boundary as a fiscal quantity."""


def parse(value: Any) -> Decimal:
    """Read a canonical decimal string into a ``Decimal``.

    Accepts ``str`` and ``Decimal``. Refuses ``float`` and ``int``:

    * ``float`` because it has already lost the precision this whole mechanism
      exists to carry — by the time a value is a Python float, ``0.1`` is
      ``0.1000000000000000055511151231257827`` and no amount of care downstream
      recovers the original.
    * ``int`` because it carries no scale. ``1`` and ``1.00`` are different
      assertions and an int cannot express which was meant, so the caller is
      made to say.
    """
    if isinstance(value, bool):
        # bool is a subclass of int; catching it first stops True becoming 1.
        raise DecimalWireError("a boolean is not a fiscal quantity")
    if isinstance(value, float):
        raise DecimalWireError(
            "a float cannot carry a fiscal quantity (ADR-0006 §2.3); "
            "the value has already lost precision before this call"
        )
    if isinstance(value, int):
        raise DecimalWireError(
            "an int carries no scale; send the canonical string form, "
            "which states whether 1 or 1.00 was meant"
        )
    if isinstance(value, Decimal):
        parsed = value
    elif isinstance(value, str):
        if value.strip() != value:
            raise DecimalWireError(f"{value!r} carries surrounding whitespace")
        try:
            # Decimal(), not WIRE_CONTEXT.create_decimal(). The context version
            # applies its precision by *rounding*, and rounding to 34 digits is
            # not trapped — so a 35-digit value would arrive silently truncated
            # rather than refused, which is the exact failure the precision
            # check below exists to catch. Parsing at arbitrary precision and
            # checking the width explicitly is what the Go side does for the
            # same reason (see withinPrecision in internal/domain/fiscal).
            parsed = Decimal(value)
        except (InvalidOperation, ValueError) as exc:
            raise DecimalWireError(f"{value!r} is not a decimal") from exc
    else:
        raise DecimalWireError(f"{type(value).__name__} is not a fiscal quantity")

    if not parsed.is_finite():
        # NaN and the infinities are legal Decimals and are not amounts. A NaN
        # compares false against everything, which in a tax figure is worse
        # than a refused call.
        raise DecimalWireError(f"{value!r} is not finite")

    digits = len(parsed.as_tuple().digits)
    if digits > PRECISION:
        raise DecimalWireError(
            f"{value!r} has {digits} significant digits, above the estate precision of {PRECISION}"
        )
    return parsed


def render(value: Decimal) -> str:
    """Render the ADR-0011 §2.2 normal form.

    Plain notation, no exponent, a single leading zero below one, a minus sign
    only for negatives and never a ``-0``, trailing zeros preserved to the
    value's significant scale.
    """
    if not isinstance(value, Decimal):
        raise DecimalWireError(f"{type(value).__name__} is not a Decimal")
    if not value.is_finite():
        raise DecimalWireError("a non-finite value has no canonical form")

    # Plain notation. Decimal.__str__ switches to scientific for small
    # exponents, which the profile forbids, so it cannot be used directly.
    sign, digits, exponent = value.as_tuple()
    assert isinstance(exponent, int)  # guaranteed by is_finite above

    digit_str = "".join(str(d) for d in digits)
    if exponent >= 0:
        body = digit_str + "0" * exponent
    else:
        scale = -exponent
        if len(digit_str) <= scale:
            digit_str = digit_str.rjust(scale + 1, "0")
        body = digit_str[:-scale] + "." + digit_str[-scale:]

    # Never -0: an amount that rounds to nothing is the same amount whichever
    # direction it came from, and the two must digest identically.
    if sign and any(d != 0 for d in digits):
        return "-" + body
    return body


def round_trips(value: str) -> bool:
    """Whether a string survives parse then render unchanged.

    Used by the cross-check harness. A value that does not round-trip is a
    value the two sides would disagree about, and the disagreement would show
    up as a digest mismatch somewhere far from here.
    """
    try:
        return render(parse(value)) == value
    except DecimalWireError:
        return False
