"""The decimal boundary of ADR-0006 §2.3.

These assert the same properties the Go side asserts in
``internal/adapter/postgres/numeric_integration_test.go`` and in the golden
cross-check, against the same boundary values. Where the two sides disagree,
the cross-check catches it; these catch it earlier and say why.
"""

from __future__ import annotations

from decimal import Decimal

import pytest

from ztax_gateway import DecimalWireError, parse, render
from ztax_gateway.decimal_wire import PRECISION, round_trips

# Scale is semantic (ADR-0011 §2.2). These are the values that break a lazy
# implementation, and each is here for a stated reason.
ROUND_TRIP_CASES = [
    "1.50",  # trailing zero preserved
    "1.500000",  # several preserved
    "42",  # integer keeps zero scale
    "0.1",  # not representable in binary floating point
    "0.2",
    "0.06375",  # a rate below the currency minor unit
    "0.0416667",  # an apportionment factor
    "-19.99",
    "-0.01",
    "0",
    "0.00",  # zero with scale — the case pgx's binary codec lost
    "99999999999999999999.99",
    "1234567890123456789012345678901234",  # 34 digits, the estate precision
    "12345678901234567890123456789.01234",
]


@pytest.mark.parametrize("value", ROUND_TRIP_CASES)
def test_canonical_strings_round_trip(value: str) -> None:
    assert round_trips(value), f"{value!r} did not survive parse then render"


def test_trailing_zeros_are_significant() -> None:
    """0.10 and 0.1 are different assertions about precision and must not
    collapse — they digest differently on the Go side."""
    assert render(parse("0.10")) != render(parse("0.1"))


def test_negative_zero_is_normalised() -> None:
    """A credit that rounds to nothing and a debit that rounds to nothing are
    the same amount."""
    assert render(parse("-0.00")) == "0.00"
    assert render(parse("-0")) == "0"


def test_a_float_is_refused() -> None:
    """The failure this guards is not a malformed string. It is json.loads,
    which yields a float for any JSON number, silently."""
    with pytest.raises(DecimalWireError, match="float"):
        parse(0.1)


def test_a_bool_is_refused() -> None:
    """bool subclasses int; catching it first stops True becoming 1."""
    with pytest.raises(DecimalWireError, match="boolean"):
        parse(True)


def test_an_int_is_refused() -> None:
    """An int carries no scale and cannot express whether 1 or 1.00 was meant."""
    with pytest.raises(DecimalWireError, match="scale"):
        parse(1)


def test_non_finite_values_are_refused() -> None:
    for value in ("NaN", "Infinity", "-Infinity"):
        with pytest.raises(DecimalWireError, match="finite"):
            parse(value)


def test_values_above_the_estate_precision_are_refused() -> None:
    too_many = "1" * (PRECISION + 1)
    with pytest.raises(DecimalWireError, match="significant digits"):
        parse(too_many)


def test_values_at_the_estate_precision_are_accepted() -> None:
    assert parse("1" * PRECISION) is not None


def test_whitespace_is_refused() -> None:
    """Trimming would make " 1.00" and "1.00" digest identically here and
    differently on a side that did not trim."""
    for value in (" 1.00", "1.00 ", "\t1.00"):
        with pytest.raises(DecimalWireError, match="whitespace"):
            parse(value)


def test_malformed_strings_are_refused() -> None:
    for value in ("", "1.2.3", "abc", "1,00", "--1"):
        with pytest.raises(DecimalWireError):
            parse(value)


def test_render_never_uses_scientific_notation() -> None:
    """Decimal.__str__ switches to scientific for small exponents, which the
    profile forbids — which is why render does not use it."""
    small = Decimal("0.0000001")
    assert "E" not in render(small) and "e" not in render(small)
    assert render(small) == "0.0000001"


def test_render_refuses_a_non_decimal() -> None:
    with pytest.raises(DecimalWireError):
        render("1.00")  # type: ignore[arg-type]


def test_parse_accepts_a_decimal_unchanged() -> None:
    assert render(parse(Decimal("1.50"))) == "1.50"
