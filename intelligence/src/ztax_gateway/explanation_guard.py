"""Deterministic guard that checks an AI explanation against cited data rows.

This module is the governed, production-path version of the guard that was
originally embedded in ``tools/report_ai_http.py`` (demo-only).  It has no
dependency on the demo fixture, on Flask, or on any model provider.

Decision reference: D-07.  The guard is intentionally append-only: it never
mutates the rows or the explanation, and it never emits an event itself.
Callers that want to record the outcome should use ``AnswerCheckedEvent`` /
``CheckEventSink`` from ``ztax_gateway.invocation_check_event``; the
``invocation_id`` keyword on :func:`apply_guard` is plumbing for that
follow-up (WP5) and is not used yet.

Reason codes (``GUARD_*``) match the ``CheckReason`` values in
``invocation_check_event.py`` so that a future PR can wire them together
without renaming anything.

Strict limits (stated plainly)
-------------------------------
The guard checks that cited ids exist and that numbers and status words in
the explanation match the cited rows.  It **cannot** catch:

* swapped numbers (a real value written against the wrong row), or
* invented causes or narratives (``"because the filer was late"``),

because every word of those can be a real row value in the wrong place.  A
human must still read the output.

Status vocabulary scope
-----------------------
The guard recognises obligation lifecycle states (ZTAX-OBL-001 chapter 15)
and payment remittance states only.  Shadow Assurance case states (from the
Go ``shadow.go`` enum) are **not** in the vocabulary.  An explanation that
uses an unrecognised shadow status word will not trigger
``GUARD_STATUS_MISMATCH``; the word is treated as ordinary prose.  If shadow
explanations are ever generated, extend ``_LIFECYCLE_STATES`` and add tests.
"""

from __future__ import annotations

import re
from collections.abc import Iterator
from dataclasses import dataclass
from decimal import Decimal
from typing import Any

__all__: list[str] = ["GuardResult", "apply_guard", "check_guard"]

# ---------------------------------------------------------------------------
# Public reason codes -- match CheckReason in invocation_check_event.py.
# ---------------------------------------------------------------------------

GUARD_NO_CITATION = "GUARD_NO_CITATION"
GUARD_FOREIGN_CITATION = "GUARD_FOREIGN_CITATION"
GUARD_STATUS_MISMATCH = "GUARD_STATUS_MISMATCH"
GUARD_NUMBER_NOT_IN_ROWS = "GUARD_NUMBER_NOT_IN_ROWS"

# ---------------------------------------------------------------------------
# Number patterns.
# ---------------------------------------------------------------------------

# A number as a person writes it: either thousands-grouped (``"1,443,155.15"``:
# 1-3 leading digits, then complete ``,ddd`` groups, not glued to other digits)
# or plain (``"1443155.15"``).  Malformed grouping (``"1,44,3155"``) never
# matches the grouped form, so its pieces are checked one by one -- stricter,
# not looser.  Currency symbols and codes (``"$"``, ``"€"``, ``"USD"``) are
# not digits and fall away.
_NUMBER_RE = re.compile(
    r"(?<![\d.,])\d{1,3}(?:,\d{3})+(?!\d)(?:\.\d+)?%?|\d+(?:\.\d+)?%?"
)
_PLAIN_NUMBER_RE = re.compile(r"^\d+(?:\.\d+)?$")
_PERCENT_CLOSE_ENOUGH = 0.1

# ---------------------------------------------------------------------------
# Obligation lifecycle and payment status words.
#
# Source: ZTAX-OBL-001 chapter 15 "Obligation Lifecycle State Machine" for
# the lifecycle states; remittance-domain words for payment states.
# These are inlined here so the guard has no dependency on the demo fixture.
# ---------------------------------------------------------------------------

_LIFECYCLE_STATES: tuple[str, ...] = (
    "POTENTIAL", "ACTIVE", "DATA_REQUIRED", "READY", "IN_REVIEW", "APPROVED",
    "FILED", "ACCEPTED", "PAYMENT_DUE", "PAID", "CLOSED", "AMENDMENT_REQUIRED",
    "OVERDUE", "DISPUTED", "SUSPENDED", "CANCELLED",
)
_PAYMENT_STATES: tuple[str, ...] = ("SETTLED", "PROCESSING", "SUBMITTED")
_STATUS_WORDS: tuple[str, ...] = (*_LIFECYCLE_STATES, *_PAYMENT_STATES)
_ANY_CASE_STATUS_WORDS: tuple[str, ...] = (
    "OVERDUE", "DISPUTED", "SUSPENDED", "CANCELLED", "SETTLED",
    "PAYMENT_DUE", "AMENDMENT_REQUIRED", "DATA_REQUIRED",
)

# ---------------------------------------------------------------------------
# Date / month / period component patterns.
#
# A person quotes ``"2026-10-31"`` as ``"31 October 2026"`` and ``"2026-Q3"``
# as ``"Q3 2026"``.  Those digits are components of a supplied row string,
# not new numbers -- but only in these narrow shapes, so a bare ``"10"`` or
# ``"3"`` (a count) is never excused just because some date contains a 10 or
# a 3:
#
#   * a 4-digit year that is the year part of a supplied date / month / period;
#   * a 1-2 digit day written directly beside a month NAME, only when that
#     exact month and day occur together in a supplied full date (YYYY-MM-DD);
#   * ``"Q<n>"`` where a supplied period has that quarter (``"YYYY-Q<n>"``).
#
# Nothing else is accepted: no decimals, no thousands groups, no percentages.
# ---------------------------------------------------------------------------

_FULL_DATE_RE = re.compile(r"(?<!\d)(\d{4})-(\d{2})-(\d{2})(?!\d)")
_YEAR_MONTH_RE = re.compile(r"(?<!\d)(\d{4})-(\d{2})(?!\d)")
_YEAR_QUARTER_RE = re.compile(r"(?<!\d)(\d{4})-Q([1-4])(?!\d)")
_QUARTER_TOKEN_RE = re.compile(r"(?<![A-Za-z0-9])Q([1-4])(?!\d)")

_MONTH_NUMBERS: dict[str, int] = {
    "january": 1, "february": 2, "march": 3, "april": 4, "may": 5, "june": 6,
    "july": 7, "august": 8, "september": 9, "october": 10, "november": 11, "december": 12,
    "jan": 1, "feb": 2, "mar": 3, "apr": 4, "jun": 6, "jul": 7, "aug": 8,
    "sep": 9, "sept": 9, "oct": 10, "nov": 11, "dec": 12,
}
_MONTH_NAME = "(" + "|".join(sorted(_MONTH_NUMBERS, key=len, reverse=True)) + r")\.?"
_MONTH_NAME_BEFORE_RE = re.compile(rf"\b{_MONTH_NAME}\s+$", re.IGNORECASE)
_MONTH_NAME_AFTER_RE = re.compile(rf"\s+(?:of\s+)?{_MONTH_NAME}\b", re.IGNORECASE)


# ---------------------------------------------------------------------------
# Internal helpers.
# ---------------------------------------------------------------------------


def _string_values(rows: list[dict[str, Any]]) -> list[str]:
    """Every string value in *rows*, including strings inside list values."""
    found: list[str] = []
    for row in rows:
        for value in row.values():
            items = value if isinstance(value, list) else [value]
            found.extend(item for item in items if isinstance(item, str) and item)
    return found


def _verbatim_strings(rows: list[dict[str, Any]]) -> list[str]:
    """Every non-numeric string value in *rows* (ids, linked ids, dates,
    months, periods, labels), longest first.

    These contain digits that are not number claims: a row's own id
    (``"OBL-001"``), the obligation a payment or shadow row links to, a due
    date (``"2026-10-31"``), a trend month (``"2026-09"``) or a period
    (``"2026-Q3"``).  Quoting one verbatim repeats the supplied rows; it never
    introduces a number the rows do not hold.
    """
    found = {s for s in _string_values(rows) if not _PLAIN_NUMBER_RE.match(s)}
    return sorted(found, key=len, reverse=True)


def _date_components(
    rows: list[dict[str, Any]],
) -> tuple[set[int], set[tuple[int, int]], set[int]]:
    """``(years, (month, day) pairs of full dates, quarters)`` found in *rows*' strings."""
    years: set[int] = set()
    month_days: set[tuple[int, int]] = set()
    quarters: set[int] = set()
    for s in _string_values(rows):
        for year, month, day in _FULL_DATE_RE.findall(s):
            if 1 <= int(month) <= 12 and 1 <= int(day) <= 31:
                years.add(int(year))
                month_days.add((int(month), int(day)))
        for year, month in _YEAR_MONTH_RE.findall(s):
            if 1 <= int(month) <= 12:
                years.add(int(year))
        for year, quarter in _YEAR_QUARTER_RE.findall(s):
            years.add(int(year))
            quarters.add(int(quarter))
    return years, month_days, quarters


def _number_matches(
    text: str, verbatim: list[str], quarters: set[int]
) -> list[re.Match[str]]:
    """Every number (optionally a ``"N%"``) in *text*, as regex matches.

    Strings quoted verbatim from the supplied rows, and ``"Q<n>"`` tokens for
    a quarter a supplied period really has, are blanked out first -- with
    spaces of the same length, so each match's position still indexes *text*.
    """
    blanked = text
    for value in verbatim:
        blanked = blanked.replace(value, " " * len(value))
    blanked = _QUARTER_TOKEN_RE.sub(
        lambda m: " " * len(m.group()) if int(m.group(1)) in quarters else m.group(), blanked
    )
    return list(_NUMBER_RE.finditer(blanked))


def _is_date_component(
    match: re.Match[str], text: str, years: set[int], month_days: set[tuple[int, int]]
) -> bool:
    """True if *match* is a year, or a day beside its month name, of a supplied date."""
    token = match.group()
    if not token.isdigit():
        return False
    if len(token) == 4:
        return int(token) in years
    if len(token) > 2:
        return False
    day = int(token)
    before = _MONTH_NAME_BEFORE_RE.search(text[: match.start()])
    if before is not None and (_MONTH_NUMBERS[before.group(1).lower()], day) in month_days:
        return True
    after = _MONTH_NAME_AFTER_RE.match(text[match.end() :])
    return after is not None and (_MONTH_NUMBERS[after.group(1).lower()], day) in month_days


def _numeric_values(rows: list[dict[str, Any]]) -> set[Decimal]:
    """Every numeric value appearing anywhere in *rows* (ids are not numbers)."""
    values: set[Decimal] = set()
    for row in rows:
        for key, value in row.items():
            if key == "id" or not isinstance(value, str):
                continue
            if _PLAIN_NUMBER_RE.match(value):
                values.add(Decimal(value))
    return values


def _value_allowed(num_str: str, allowed: set[Decimal]) -> bool:
    """True if *num_str*, once its thousands commas are removed, equals a row value.

    Exact, with no tolerance: ``"182,500"`` is the same number as the row
    value ``182500.00``, but ``182500.004`` is not.
    """
    try:
        value = Decimal(num_str.replace(",", ""))
    except ArithmeticError:
        return False
    return value in allowed


def _percentage_allowed(num_str: str, allowed: set[Decimal]) -> bool:
    """True if *num_str* (``"N%"``) is a simple percentage of two allowed values."""
    if not num_str.endswith("%"):
        return False
    try:
        target = float(num_str[:-1].replace(",", ""))
    except ValueError:
        return False
    for a in allowed:
        for b in allowed:
            if b == 0:
                continue
            if abs((float(a) / float(b) * 100) - target) < _PERCENT_CLOSE_ENOUGH:
                return True
    return False


def _status_pattern(words: tuple[str, ...], flags: int) -> re.Pattern[str]:
    alternatives = "|".join(w.replace("_", "[_ ]") for w in words)
    return re.compile(rf"(?<![A-Za-z0-9_])(?:{alternatives})(?![A-Za-z0-9_])", flags)


_STATUS_UPPER_RE = _status_pattern(_STATUS_WORDS, 0)
_STATUS_ANY_CASE_RE = _status_pattern(_ANY_CASE_STATUS_WORDS, re.IGNORECASE)


def _status_words_in(text: str) -> set[str]:
    """Canonical status words (upper case, underscored) that *text* uses."""
    found = _STATUS_UPPER_RE.findall(text) + _STATUS_ANY_CASE_RE.findall(text)
    return {w.upper().replace(" ", "_") for w in found}


def _check(
    rows: list[dict[str, Any]], cited_ids: list[str], explanation: str
) -> tuple[str, str]:
    """``("", "")`` if the reply passes, else ``(reason_code, reason_text)``.

    The code is a short fixed token safe to log; the text is for the browser
    and may quote a number or an id taken from the explanation.
    """
    if not cited_ids:
        return GUARD_NO_CITATION, "the explanation cites no row id"

    row_ids = {str(row["id"]) for row in rows}
    foreign = sorted({c for c in cited_ids if c not in row_ids})
    if foreign:
        return (
            GUARD_FOREIGN_CITATION,
            f"cited id(s) not in the supplied rows: {', '.join(foreign)}",
        )

    cited_statuses = {
        str(row["status"]) for row in rows if str(row["id"]) in cited_ids and "status" in row
    }
    if cited_statuses:
        # Only checked when a cited row carries a status at all: figures,
        # trend points and buckets have none, and their labels legitimately
        # contain words such as "Overdue" or "Filed".
        stray = sorted(_status_words_in(explanation) - cited_statuses)
        if stray:
            return (
                GUARD_STATUS_MISMATCH,
                f"explanation states a status that is not the cited row's: {', '.join(stray)}",
            )

    allowed = _numeric_values(rows)
    years, month_days, quarters = _date_components(rows)
    for match in _number_matches(explanation, _verbatim_strings(rows), quarters):
        num = match.group()
        if num.endswith("%"):
            # (2) the documented recomputable percentage of two supplied numbers.
            if _percentage_allowed(num, allowed):
                continue
        elif _value_allowed(num, allowed):
            # (1) an exact row value.
            continue
        elif _is_date_component(match, explanation, years, month_days):
            # (3) a year / day beside its month name, quoting a supplied date.
            continue
        return (
            GUARD_NUMBER_NOT_IN_ROWS,
            f"explanation states a number not present in the supplied rows: {num!r}",
        )
    return "", ""


# ---------------------------------------------------------------------------
# Public API.
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class GuardResult:
    """Structured result of a guard check.

    Provides the same information as ``apply_guard``'s ``(bool, str)`` tuple,
    but also carries the reason *code* separately from the human-readable
    *message*.  WP5 uses ``result.code`` to construct an ``AnswerCheckedEvent``
    without having to parse free text.

    ``bool(result)`` is ``True`` iff the check passed (equivalent to
    ``result.ok``).

    Unpacking still works for callers that used the old two-element pattern::

        ok, message = check_guard(...)   # works; code is in result.code

    But prefer named-attribute access for new callers::

        result = check_guard(...)
        if not result:
            emit_event(result.code, result.message)

    Attributes
    ----------
    ok:
        ``True`` iff the guard accepted the explanation.
    code:
        One of the ``GUARD_*`` constants, or ``""`` when *ok* is ``True``.
        Safe to log and matches :class:`ztax_gateway.invocation_check_event.CheckReason`.
    message:
        Human-readable explanation of the rejection, or ``""`` on pass.
        May quote ids or numbers from the explanation; never log raw.
    """

    ok: bool
    code: str
    message: str

    def __bool__(self) -> bool:
        return self.ok

    def __iter__(self) -> Iterator[object]:
        """Yield ``(ok, message)`` so existing two-element unpacking still works.

        Yields two items: the ``bool`` *ok* flag, then the ``str`` *message*.
        New callers should prefer named-attribute access (``result.ok``,
        ``result.code``, ``result.message``) for clarity.
        """
        yield self.ok
        yield self.message


def check_guard(
    rows: list[dict[str, Any]],
    cited_ids: list[str],
    explanation: str,
    *,
    invocation_id: str | None = None,
) -> GuardResult:
    """Check an AI explanation and return a structured :class:`GuardResult`.

    Identical logic to :func:`apply_guard` but returns ``GuardResult`` with
    ``ok``, ``code`` (the ``GUARD_*`` constant), and ``message`` (human text).
    Use this when you need the reason code as a discrete value -- for example,
    when constructing an ``AnswerCheckedEvent`` in WP5.

    Parameters
    ----------
    rows:
        The data rows the explanation is about.
    cited_ids:
        The row ids the model claimed to have used.
    explanation:
        The free-text explanation produced by the model.
    invocation_id:
        Optional.  The ``ai_invocation_id`` from the evidence record.  Not used
        in this version; reserved for WP5, which will emit an
        ``AnswerCheckedEvent`` linked to this id for every guard outcome.
    """
    code, message = _check(rows, cited_ids, explanation)
    return GuardResult(ok=not message, code=code, message=message)


def apply_guard(
    rows: list[dict[str, Any]],
    cited_ids: list[str],
    explanation: str,
    *,
    invocation_id: str | None = None,
) -> tuple[bool, str]:
    """Check an AI explanation against the cited data rows.

    Returns ``(True, "")`` only if:

    0. At least one id is cited.
    1. Every cited id is one of *rows*' own ids.
    2. Every lifecycle / payment status word in *explanation* is the status
       of a cited row.
    3. Every number in *explanation* is exactly one of *rows*' own numeric
       values (once thousands commas are removed -- no tolerance), a simple
       percentage of two of them, or a year / day / quarter that quotes a
       supplied date, month or period.

    There is no sum, subtotal, average or any other aggregate rule: a total
    passes only if a supplied row carries it as a field.  Otherwise returns
    ``(False, reason)``; the caller turns that into an ABSTAINED outcome.

    .. note::
       For new callers that need the reason *code* as a discrete value (e.g.
       WP5's ``AnswerCheckedEvent``), prefer :func:`check_guard`, which
       returns a :class:`GuardResult` with ``ok``, ``code``, and ``message``
       attributes.

    Parameters
    ----------
    rows:
        The data rows the explanation is about.
    cited_ids:
        The row ids the model claimed to have used.
    explanation:
        The free-text explanation produced by the model.
    invocation_id:
        Optional.  The ``ai_invocation_id`` from the evidence record for this
        call.  Not used in this version; reserved for WP5, which will emit an
        ``AnswerCheckedEvent`` linked to this id for every guard outcome.

    LIMIT, stated plainly
    ---------------------
    This guard checks that ids exist and that the numbers and status words
    match the cited rows.  It **cannot** catch swapped numbers (one row's
    amount written against another row) or an invented cause or narrative
    (``"because the filer was late"``), because every word of those can be a
    real row value in the wrong place.  It does not prove a sentence is true;
    a person must still read it.
    """
    result = check_guard(rows, cited_ids, explanation, invocation_id=invocation_id)
    return result.ok, result.message

