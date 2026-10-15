
"""Content Screening -- Prompt Injection, Corpus Poisoning, and Disclosure Detection.

A pure, deterministic, zero-dependency library that screens text for:

1. **Prompt-injection** in untrusted input -- override phrases, role-hijack
   lines, fake chat-template delimiters, and Unicode-obfuscation tricks.

2. **Poisoning / hidden-instruction** content in corpus chunks before indexing
   -- HTML/Markdown comments, CSS hidden-text markers, and imperative phrases
   addressed to the assistant inside statutory text.

3. **Secrets and PII** in model output -- with span-accurate redaction.

Prototype detection library; not wired into any pipeline; does not close any
requirement.

Known limits
------------
* English only: injection/override phrases are matched in English; other
  languages are not covered.
* Paraphrased injections not caught: detection relies on known phrase
  patterns; novel phrasing bypasses them.
* The ``###system`` delimiter pattern fires only when "system" is alone on
  its line (no further words); headings like "### System Requirements" are
  intentionally allowed.
* Base64/hex payloads: the screener does NOT decode and re-scan base64 or
  hex blobs.  Only the encoded form is inspected against injection patterns.
* Card detection requires 13-19 digits with single-space or hyphen
  separators only (e.g. ``4111 1111 1111 1111`` or ``4111-1111-1111-1111``).
  Digits run together without separators are detected as a single 16-digit
  token; digits separated by other characters are not detected.

Design rules (shared with all modules in this package)
------------------------------------------------------
* No imports from other ztax_gateway modules -- fully standalone.
* No fiscal imports (ADR-0006 SS2.6).
* Standard library only.
* All data-carrying types are ``frozen=True`` dataclasses -- immutable evidence.
* Functions are pure: identical inputs -> identical outputs, no side-effects.
* All regex patterns are linear-time (no nested quantifiers).
* Input larger than ``MAX_INPUT_CHARS`` is refused immediately; no regex scan.
* Obfuscation pass: injection rules re-run on NFKC-normalised copy with
  invisible chars stripped.  Findings carry original-text offsets via
  ``_build_norm_map``; whole-input span [0, len(text)) is the documented
  fallback when the mapping is ambiguous.
"""

from __future__ import annotations

import re
import unicodedata
from dataclasses import dataclass
from enum import StrEnum
from typing import Final

__all__: list[str] = [
    "MAX_INPUT_CHARS",
    "DisclosureResult",
    "Finding",
    "FindingCategory",
    "ScreeningResult",
    "ScreeningVerdict",
    "Severity",
    "scan_output_for_disclosure",
    "screen_corpus_chunk",
    "screen_untrusted_input",
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

MAX_INPUT_CHARS: Final[int] = 1_048_576  # 1 MiB hard ceiling

# ---------------------------------------------------------------------------
# Public enums & dataclasses
# ---------------------------------------------------------------------------


class ScreeningVerdict(StrEnum):
    """Overall safety verdict returned by screening functions."""

    CLEAN = "CLEAN"
    SUSPECT = "SUSPECT"
    BLOCK = "BLOCK"


class Severity(StrEnum):
    """Severity of an individual finding."""

    MEDIUM = "MEDIUM"
    HIGH = "HIGH"


class FindingCategory(StrEnum):
    """Coarse category of the finding -- used for routing decisions."""

    INJECTION_OVERRIDE = "INJECTION_OVERRIDE"
    INJECTION_ROLE_HIJACK = "INJECTION_ROLE_HIJACK"
    INJECTION_DELIMITER = "INJECTION_DELIMITER"
    OBFUSCATION_INVISIBLE = "OBFUSCATION_INVISIBLE"
    POISONING_HIDDEN_TEXT = "POISONING_HIDDEN_TEXT"
    DISCLOSURE_SECRET = "DISCLOSURE_SECRET"
    DISCLOSURE_PII = "DISCLOSURE_PII"
    INPUT_TOO_LARGE = "INPUT_TOO_LARGE"


@dataclass(frozen=True, slots=True)
class Finding:
    """A single screening finding.

    ``start`` and ``end`` are character-offsets into the *original* input
    (``original_text[start:end]`` covers the flagged region).

    There is deliberately **no matched-text field**: storing matched text
    would embed suspicious content in evidence records and logs.
    """

    category: FindingCategory
    code: str           # stable short id, e.g. "INJ_IGNORE_PREVIOUS"
    severity: Severity
    start: int          # inclusive, >= 0
    end: int            # exclusive, > start

    def __post_init__(self) -> None:
        if self.start < 0:
            raise ValueError(f"Finding.start must be >= 0, got {self.start}")
        if self.end <= self.start:
            raise ValueError(
                f"Finding.end must be > start; start={self.start} end={self.end}"
            )


@dataclass(frozen=True, slots=True)
class ScreeningResult:
    """Result of screening untrusted input or a corpus chunk."""

    verdict: ScreeningVerdict
    findings: tuple[Finding, ...]   # sorted by (start, code)


@dataclass(frozen=True, slots=True)
class DisclosureResult:
    """Result of scanning model output for secrets/PII."""

    findings: tuple[Finding, ...]   # sorted by (start, code)
    redacted_text: str              # original with each finding span replaced


# ---------------------------------------------------------------------------
# Internal rule types
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class _InjRule:
    code: str
    category: FindingCategory
    severity: Severity
    pattern: re.Pattern[str]


@dataclass(frozen=True, slots=True)
class _DiscRule:
    code: str
    category: FindingCategory
    severity: Severity
    pattern: re.Pattern[str]


# ---------------------------------------------------------------------------
# Invisible / obfuscation character set
# ---------------------------------------------------------------------------

# Zero-width: U+200B..U+200D, U+2060, U+FEFF
# Bidi controls: U+202A..U+202E, U+2066..U+2069
# Tag block: U+E0000..U+E007F
_INVISIBLE_RE: Final[re.Pattern[str]] = re.compile(
    r"[\u200b-\u200d\u2060\ufeff\u202a-\u202e\u2066-\u2069\U000e0000-\U000e007f]"
)

# ---------------------------------------------------------------------------
# Injection rules
# ---------------------------------------------------------------------------

_INJ_RULES: Final[tuple[_InjRule, ...]] = (
    # Override phrases
    _InjRule(
        code="INJ_IGNORE_PREVIOUS",
        category=FindingCategory.INJECTION_OVERRIDE,
        severity=Severity.HIGH,
        pattern=re.compile(
            r"\bignore\s+(?:all\s+|any\s+|the\s+)?(?:previous|prior|above)\s+instructions\b",
            re.IGNORECASE,
        ),
    ),
    _InjRule(
        code="INJ_DISREGARD",
        category=FindingCategory.INJECTION_OVERRIDE,
        severity=Severity.HIGH,
        pattern=re.compile(
            r"\bdisregard\s+(?:all\s+|any\s+|the\s+)?(?:previous|prior|above|your)\s+instructions\b",
            re.IGNORECASE,
        ),
    ),
    _InjRule(
        code="INJ_FORGET_ABOVE",
        category=FindingCategory.INJECTION_OVERRIDE,
        severity=Severity.HIGH,
        pattern=re.compile(
            r"\bforget\s+everything\s+above\b",
            re.IGNORECASE,
        ),
    ),
    _InjRule(
        code="INJ_YOU_ARE_NOW",
        category=FindingCategory.INJECTION_OVERRIDE,
        severity=Severity.HIGH,
        pattern=re.compile(
            r"\byou\s+are\s+now\s+(?:a|an|in\s+|operating|unrestricted|jailbroken|free\s+from)",
            re.IGNORECASE,
        ),
    ),
    _InjRule(
        code="INJ_NEW_INSTRUCTIONS",
        category=FindingCategory.INJECTION_OVERRIDE,
        severity=Severity.HIGH,
        pattern=re.compile(
            r"\bnew\s+instructions\s*:",
            re.IGNORECASE,
        ),
    ),
    _InjRule(
        code="INJ_REVEAL_SYSTEM_PROMPT",
        category=FindingCategory.INJECTION_OVERRIDE,
        severity=Severity.HIGH,
        pattern=re.compile(
            r"\b(?:reveal|print|repeat|output|show)\s+(?:the\s+)?system\s+prompt\b",
            re.IGNORECASE,
        ),
    ),
    # Role hijack
    _InjRule(
        code="INJ_ROLE_LINE",
        category=FindingCategory.INJECTION_ROLE_HIJACK,
        severity=Severity.HIGH,
        pattern=re.compile(
            r"^(?:system|assistant|developer)\s*:",
            re.IGNORECASE | re.MULTILINE,
        ),
    ),
    _InjRule(
        code="INJ_ACT_AS_JAILBREAK",
        category=FindingCategory.INJECTION_ROLE_HIJACK,
        severity=Severity.HIGH,
        pattern=re.compile(
            r"\bact\s+as\s+(?:an?\s+)?(?:unrestricted|jailbroken|DAN\b|evil\b|unfiltered|unlimited)",
            re.IGNORECASE,
        ),
    ),
    # Delimiter tricks
    _InjRule(
        code="INJ_DELIM_IM_START",
        category=FindingCategory.INJECTION_DELIMITER,
        severity=Severity.HIGH,
        pattern=re.compile(r"<\|im_start\|>", re.IGNORECASE),
    ),
    _InjRule(
        code="INJ_DELIM_INST",
        category=FindingCategory.INJECTION_DELIMITER,
        severity=Severity.HIGH,
        pattern=re.compile(r"\[/?INST\]", re.IGNORECASE),
    ),
    _InjRule(
        code="INJ_DELIM_HASH_SYSTEM",
        category=FindingCategory.INJECTION_DELIMITER,
        severity=Severity.HIGH,
        # Match "###system" or "### system" only when it is the entire line
        # (no further words).  Normal markdown headings like
        # "### System Requirements" must NOT fire.
        pattern=re.compile(
            r"^###[ \t]*system[ \t]*:?[ \t]*$",
            re.IGNORECASE | re.MULTILINE,
        ),
    ),
    _InjRule(
        code="INJ_DELIM_BACKTICK_SYSTEM",
        category=FindingCategory.INJECTION_DELIMITER,
        severity=Severity.HIGH,
        pattern=re.compile(r"```\s*system\b", re.IGNORECASE),
    ),
)

# ---------------------------------------------------------------------------
# Corpus-only poisoning rules
# ---------------------------------------------------------------------------

_CORPUS_RULES: Final[tuple[_InjRule, ...]] = (
    _InjRule(
        code="POI_HTML_COMMENT",
        category=FindingCategory.POISONING_HIDDEN_TEXT,
        severity=Severity.HIGH,
        pattern=re.compile(
            r"<!--[\s\S]*?\b(?:ignore|override|instruction|prompt|assistant)\b[\s\S]*?-->",
            re.IGNORECASE,
        ),
    ),
    _InjRule(
        code="POI_MD_COMMENT",
        category=FindingCategory.POISONING_HIDDEN_TEXT,
        severity=Severity.HIGH,
        pattern=re.compile(
            r"\[//\]:\s*#\s*\([\s\S]*?\b(?:ignore|override|instruction|prompt|assistant)\b[\s\S]*?\)",
            re.IGNORECASE,
        ),
    ),
    _InjRule(
        code="POI_CSS_DISPLAY_NONE",
        category=FindingCategory.POISONING_HIDDEN_TEXT,
        severity=Severity.HIGH,
        pattern=re.compile(r"\bdisplay\s*:\s*none\b", re.IGNORECASE),
    ),
    _InjRule(
        code="POI_CSS_FONT_SIZE_ZERO",
        category=FindingCategory.POISONING_HIDDEN_TEXT,
        severity=Severity.HIGH,
        pattern=re.compile(r"\bfont-size\s*:\s*0(?:px|pt|em|rem)?\b", re.IGNORECASE),
    ),
    _InjRule(
        code="POI_CSS_OPACITY_ZERO",
        category=FindingCategory.POISONING_HIDDEN_TEXT,
        severity=Severity.HIGH,
        pattern=re.compile(r"\bopacity\s*:\s*0(?:\.0+)?\b", re.IGNORECASE),
    ),
    _InjRule(
        code="POI_CSS_VISIBILITY_HIDDEN",
        category=FindingCategory.POISONING_HIDDEN_TEXT,
        severity=Severity.HIGH,
        pattern=re.compile(r"\bvisibility\s*:\s*hidden\b", re.IGNORECASE),
    ),
    _InjRule(
        code="POI_CSS_WHITE_ON_WHITE",
        category=FindingCategory.POISONING_HIDDEN_TEXT,
        severity=Severity.HIGH,
        pattern=re.compile(r"\bcolor\s*:\s*(?:white|#fff(?:fff)?)\b", re.IGNORECASE),
    ),
    _InjRule(
        code="POI_NOTE_TO_AI",
        category=FindingCategory.POISONING_HIDDEN_TEXT,
        severity=Severity.HIGH,
        pattern=re.compile(
            r"\bnote\s+to\s+(?:the\s+)?(?:assistant|model|ai|llm)\s*:",
            re.IGNORECASE,
        ),
    ),
    _InjRule(
        code="POI_INSTRUCTION_TO_AI",
        category=FindingCategory.POISONING_HIDDEN_TEXT,
        severity=Severity.HIGH,
        pattern=re.compile(
            r"\b(?:assistant|model|ai|llm)\s*,\s*(?:please\s+)?"
            r"(?:ignore|do not|never|always|must|output)\b",
            re.IGNORECASE,
        ),
    ),
)

# ---------------------------------------------------------------------------
# Disclosure / PII rules
# ---------------------------------------------------------------------------

_DISC_RULES: Final[tuple[_DiscRule, ...]] = (
    # Secrets (HIGH)
    _DiscRule(
        code="SEC_AWS_ACCESS_KEY",
        category=FindingCategory.DISCLOSURE_SECRET,
        severity=Severity.HIGH,
        pattern=re.compile(r"\b(?:AKIA|ASIA)[A-Z0-9]{16}\b"),
    ),
    _DiscRule(
        code="SEC_PEM_PRIVATE_KEY",
        category=FindingCategory.DISCLOSURE_SECRET,
        severity=Severity.HIGH,
        pattern=re.compile(r"-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----"),
    ),
    _DiscRule(
        code="SEC_JWT",
        category=FindingCategory.DISCLOSURE_SECRET,
        severity=Severity.HIGH,
        pattern=re.compile(
            r"\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\b"
        ),
    ),
    _DiscRule(
        code="SEC_BEARER_TOKEN",
        category=FindingCategory.DISCLOSURE_SECRET,
        severity=Severity.HIGH,
        pattern=re.compile(r"\bBearer\s+[A-Za-z0-9+/._~-]{20,}(?:={0,2})\b"),
    ),
    _DiscRule(
        code="SEC_GITHUB_TOKEN",
        category=FindingCategory.DISCLOSURE_SECRET,
        severity=Severity.HIGH,
        pattern=re.compile(r"\bgh[pousr]_[A-Za-z0-9]{36}\b"),
    ),
    _DiscRule(
        code="SEC_SLACK_TOKEN",
        category=FindingCategory.DISCLOSURE_SECRET,
        severity=Severity.HIGH,
        pattern=re.compile(r"\bxox[baprs]-[0-9A-Za-z-]{10,}\b"),
    ),
    _DiscRule(
        code="SEC_GENERIC_SK",
        category=FindingCategory.DISCLOSURE_SECRET,
        severity=Severity.HIGH,
        pattern=re.compile(r"\bsk-[A-Za-z0-9_-]{20,}\b"),
    ),
    # PII (MEDIUM)
    _DiscRule(
        code="PII_EMAIL",
        category=FindingCategory.DISCLOSURE_PII,
        severity=Severity.MEDIUM,
        pattern=re.compile(r"\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b"),
    ),
    _DiscRule(
        code="PII_US_SSN",
        category=FindingCategory.DISCLOSURE_PII,
        severity=Severity.MEDIUM,
        # NNN-NN-NNNN excluding area 000/666/9xx, group 00, serial 0000
        pattern=re.compile(
            r"\b(?!000|666|9\d{2})\d{3}-(?!00)\d{2}-(?!0000)\d{4}\b"
        ),
    ),
)

# Card detection uses an unambiguous two-phase approach to stay linear-time
# and to correctly handle cards adjacent to other numbers:
#
# Phase 1 -- find maximal digit-separator chains.  The separator between
# groups is mandatory (single space or hyphen); this makes the pattern
# unambiguous (no optional repetition inside repetition) and O(n).
#   e.g. "pay 4111111111111111 100 USD"
#        chains: "4111111111111111", "100"
#
# Phase 2 -- slide a two-pointer window over the digit groups within each
# chain.  Accept the longest window whose total digit count is 13-19 and
# that passes Luhn.  Continue scanning after the accepted window so that
# two adjacent cards are both found.
_CARD_CHAIN_RE: Final[re.Pattern[str]] = re.compile(r"\d+(?:[ -]\d+)*")

_IBAN_CANDIDATE_RE: Final[re.Pattern[str]] = re.compile(
    r"\b[A-Z]{2}\d{2}[A-Z0-9]{11,30}\b"
)

# ---------------------------------------------------------------------------
# Algorithmic validators
# ---------------------------------------------------------------------------


def _luhn_valid(digits: str) -> bool:
    """Return True iff *digits* (digit chars only) passes the Luhn check."""
    n = len(digits)
    if n < 13 or n > 19:
        return False
    total = 0
    for i, ch in enumerate(reversed(digits)):
        d = int(ch)
        if i % 2 == 1:
            d *= 2
            if d > 9:
                d -= 9
        total += d
    return total % 10 == 0


def _iban_valid(raw: str) -> bool:
    """Return True iff *raw* passes ISO 7064 mod-97 IBAN validation."""
    s = raw.upper()
    if len(s) < 15 or len(s) > 34:
        return False
    rearranged = s[4:] + s[:4]
    numeric: list[str] = []
    for ch in rearranged:
        if ch.isdigit():
            numeric.append(ch)
        elif ch.isalpha():
            numeric.append(str(ord(ch) - 55))   # A=10..Z=35
        else:
            return False
    try:
        return int("".join(numeric)) % 97 == 1
    except ValueError:
        return False


# ---------------------------------------------------------------------------
# Normalisation helpers
# ---------------------------------------------------------------------------


def _build_norm_map(text: str) -> tuple[str, list[int]]:
    """Return (normalised_text, orig_index_map).

    ``orig_index_map[i]`` is the index in *text* that produced position *i*
    in ``normalised_text``.  Steps:
    1. Strip invisible / zero-width / bidi / tag characters.
    2. Apply NFKC normalisation character by character.
    """
    stripped_chars: list[str] = []
    orig_positions: list[int] = []
    for i, ch in enumerate(text):
        if not _INVISIBLE_RE.match(ch):
            stripped_chars.append(ch)
            orig_positions.append(i)

    norm_chars: list[str] = []
    norm_orig: list[int] = []
    for idx, ch in enumerate(stripped_chars):
        expanded = unicodedata.normalize("NFKC", ch)
        orig_pos = orig_positions[idx]
        for xch in expanded:
            norm_chars.append(xch)
            norm_orig.append(orig_pos)

    return "".join(norm_chars), norm_orig


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------


def _verdict(findings: list[Finding]) -> ScreeningVerdict:
    if any(f.severity is Severity.HIGH for f in findings):
        return ScreeningVerdict.BLOCK
    if any(f.severity is Severity.MEDIUM for f in findings):
        return ScreeningVerdict.SUSPECT
    return ScreeningVerdict.CLEAN


def _sort_key(f: Finding) -> tuple[int, str]:
    return (f.start, f.code)


def _obfuscation_findings(text: str) -> list[Finding]:
    matches = list(_INVISIBLE_RE.finditer(text))
    if not matches:
        return []
    sev = Severity.HIGH if len(matches) >= 3 else Severity.MEDIUM
    return [
        Finding(
            category=FindingCategory.OBFUSCATION_INVISIBLE,
            code="OBF_INVISIBLE_CHAR",
            severity=sev,
            start=m.start(),
            end=m.end(),
        )
        for m in matches
    ]


def _norm_pass_findings(text: str, seen_codes: set[str]) -> list[Finding]:
    """Re-run injection rules on normalised text; map hits back to original offsets.

    A hit whose code is already in *seen_codes* is skipped (already caught on
    the raw text).  Offset mapping uses ``_build_norm_map``; if the resulting
    span would be zero-length or inverted, [0, len(text)) is used as the
    documented fallback.
    """
    norm_text, norm_map = _build_norm_map(text)
    if norm_text == text:
        return []

    out: list[Finding] = []
    for rule in _INJ_RULES:
        if rule.code in seen_codes:
            continue
        for m in rule.pattern.finditer(norm_text):
            ns, ne = m.start(), m.end()
            if ns < len(norm_map) and (ne - 1) < len(norm_map):
                orig_start = norm_map[ns]
                orig_end = norm_map[ne - 1] + 1
                if orig_end <= orig_start:
                    orig_start, orig_end = 0, len(text)
            else:
                orig_start, orig_end = 0, len(text)
            out.append(
                Finding(
                    category=rule.category,
                    code=rule.code,
                    severity=rule.severity,
                    start=orig_start,
                    end=orig_end,
                )
            )
            seen_codes.add(rule.code)
    return out


def _find_card_findings(text: str) -> list[Finding]:
    """Find payment-card numbers using a two-pointer sliding-window algorithm.

    Phase 1: find maximal chains of digit-groups separated by a single space
    or hyphen (``_CARD_CHAIN_RE``).  The separator is mandatory, so the
    pattern is unambiguous and O(n).

    Phase 2: for each chain, split on the separator character, build a prefix
    sum of digit counts, then slide a two-pointer window over groups:
    * Skip chains whose total digit count is < 13 immediately (fast guard).
    * Expand the right pointer until total digits >= 13.
    * Try extending right further to prefer the longest Luhn-valid window
      within 13-19 digits.
    * If Luhn valid, emit a Finding that covers exactly the matched groups and
      advance left past the accepted window.
    * If no Luhn-valid window starting at left: advance left past any groups
      whose removal keeps at least 13 digits remaining, so we skip in bulk
      instead of one group at a time.  This keeps total iterations O(n).
    """
    out: list[Finding] = []
    sep_re = re.compile(r"[ -]")
    for chain_m in _CARD_CHAIN_RE.finditer(text):
        chain_str = chain_m.group()
        chain_start = chain_m.start()

        # Split into (group_digits, group_start_in_chain, group_end_in_chain)
        groups: list[tuple[str, int, int]] = []
        pos = 0
        for seg_m in sep_re.finditer(chain_str):
            groups.append((chain_str[pos : seg_m.start()], pos, seg_m.start()))
            pos = seg_m.end()  # skip the separator char
        groups.append((chain_str[pos:], pos, len(chain_str)))

        n_groups = len(groups)
        # Prefix sum of digit counts for O(1) window digit total
        prefix: list[int] = [0] * (n_groups + 1)
        for i, (g, _, _) in enumerate(groups):
            prefix[i + 1] = prefix[i] + len(g)

        total_digits = prefix[n_groups]
        if total_digits < 13:
            continue  # fast guard: impossible to form a valid card

        left = 0
        right = 0
        while left < n_groups:
            # Fast guard: skip left forward if fewer than 13 digits remain
            while left < n_groups and prefix[n_groups] - prefix[left] < 13:
                left += 1
            if left >= n_groups:
                break

            # Expand right until digits(left..right) >= 13
            if right < left:
                right = left
            while right < n_groups and prefix[right + 1] - prefix[left] < 13:
                right += 1
            if right >= n_groups:
                break  # not enough digits left in chain from this left

            # Guard: if the window [left..right] already exceeds 19 digits
            # (a single group is too large for any valid card), no window
            # starting at `left` can succeed.  Skip left past this group.
            if prefix[right + 1] - prefix[left] > 19:
                left = right + 1
                right = left
                continue

            # Slide right as far as possible within 19 digits,
            # keeping track of the best (longest) Luhn-valid window
            best_right = -1
            r = right
            while r < n_groups and prefix[r + 1] - prefix[left] <= 19:
                total = prefix[r + 1] - prefix[left]
                if 13 <= total <= 19:
                    digits = "".join(g for g, _, _ in groups[left : r + 1])
                    if _luhn_valid(digits):
                        best_right = r  # keep extending for a longer valid window
                r += 1

            if best_right >= 0:
                span_start = chain_start + groups[left][1]
                span_end = chain_start + groups[best_right][2]
                out.append(
                    Finding(
                        category=FindingCategory.DISCLOSURE_PII,
                        code="PII_PAYMENT_CARD",
                        severity=Severity.MEDIUM,
                        start=span_start,
                        end=span_end,
                    )
                )
                left = best_right + 1  # advance past the accepted window
                right = left
            else:
                # No Luhn-valid window starts at `left`.  Jump left forward to
                # `r` (one past the last group the inner scan inspected).
                # r > left is guaranteed by the >19 guard above.
                left = r
    return out


def _run_disclosure_rules(text: str) -> list[Finding]:
    out: list[Finding] = []
    for rule in _DISC_RULES:
        for m in rule.pattern.finditer(text):
            out.append(
                Finding(
                    category=rule.category,
                    code=rule.code,
                    severity=rule.severity,
                    start=m.start(),
                    end=m.end(),
                )
            )
    out.extend(_find_card_findings(text))
    for m in _IBAN_CANDIDATE_RE.finditer(text):
        if _iban_valid(m.group()):
            out.append(
                Finding(
                    category=FindingCategory.DISCLOSURE_PII,
                    code="PII_IBAN",
                    severity=Severity.MEDIUM,
                    start=m.start(),
                    end=m.end(),
                )
            )
    return out


def _redact(text: str, findings: tuple[Finding, ...]) -> str:
    """Replace each finding span with ``[REDACTED:<code>]``.

    Overlapping spans are merged; the earlier-starting finding's code wins.
    """
    if not findings:
        return text
    intervals: list[tuple[int, int, str]] = []
    for f in sorted(findings, key=lambda x: (x.start, x.code)):
        if not intervals:
            intervals.append((f.start, f.end, f.code))
        else:
            ps, pe, pc = intervals[-1]
            if f.start < pe:
                intervals[-1] = (ps, max(pe, f.end), pc)
            else:
                intervals.append((f.start, f.end, f.code))
    parts: list[str] = []
    cursor = 0
    for start, end, code in intervals:
        parts.append(text[cursor:start])
        parts.append(f"[REDACTED:{code}]")
        cursor = end
    parts.append(text[cursor:])
    return "".join(parts)


# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------


def screen_untrusted_input(text: str) -> ScreeningResult:
    """Screen *text* from an untrusted source (user input, tool result, webhook).

    Checks:
    * Size limit -- returns BLOCK/INPUT_TOO_LARGE; no regex scanning.
    * Invisible/zero-width/bidi/tag character detection.
    * Injection patterns on the original text.
    * Injection patterns on NFKC-normalised + invisible-stripped copy, to
      catch obfuscated payloads (e.g. ig\u200bnore previous instructions).
      Findings from this pass carry the corresponding span in the *original*
      text; [0, len(text)) is used when the mapping is ambiguous.

    Args:
        text: Raw string to screen.

    Returns:
        ScreeningResult with verdict and sorted findings.

    Raises:
        TypeError: If *text* is not a str.
    """
    raw: object = text
    if not isinstance(raw, str):
        raise TypeError(f"screen_untrusted_input: expected str, got {type(raw).__name__}")
    text = raw

    if len(text) > MAX_INPUT_CHARS:
        f = Finding(
            category=FindingCategory.INPUT_TOO_LARGE,
            code="INPUT_TOO_LARGE",
            severity=Severity.HIGH,
            start=0,
            end=len(text),
        )
        return ScreeningResult(verdict=ScreeningVerdict.BLOCK, findings=(f,))

    findings: list[Finding] = []
    findings.extend(_obfuscation_findings(text))

    seen_codes: set[str] = set()
    for rule in _INJ_RULES:
        for m in rule.pattern.finditer(text):
            findings.append(
                Finding(
                    category=rule.category,
                    code=rule.code,
                    severity=rule.severity,
                    start=m.start(),
                    end=m.end(),
                )
            )
            seen_codes.add(rule.code)

    findings.extend(_norm_pass_findings(text, seen_codes))
    findings.sort(key=_sort_key)
    return ScreeningResult(verdict=_verdict(findings), findings=tuple(findings))


def screen_corpus_chunk(text: str) -> ScreeningResult:
    """Screen a corpus chunk before indexing for RAG retrieval.

    Runs all untrusted-input checks plus corpus-specific hidden-text and
    poisoning checks (HTML/Markdown comments, CSS hiding, imperative phrases
    to the assistant inside statutory text).

    Args:
        text: Corpus chunk string to screen.

    Returns:
        ScreeningResult with verdict and sorted findings.

    Raises:
        TypeError: If *text* is not a str.
    """
    raw: object = text
    if not isinstance(raw, str):
        raise TypeError(f"screen_corpus_chunk: expected str, got {type(raw).__name__}")
    text = raw

    base = screen_untrusted_input(text)
    if base.findings and base.findings[0].category is FindingCategory.INPUT_TOO_LARGE:
        return base

    findings: list[Finding] = list(base.findings)
    for rule in _CORPUS_RULES:
        for m in rule.pattern.finditer(text):
            findings.append(
                Finding(
                    category=rule.category,
                    code=rule.code,
                    severity=rule.severity,
                    start=m.start(),
                    end=m.end(),
                )
            )
    findings.sort(key=_sort_key)
    return ScreeningResult(verdict=_verdict(findings), findings=tuple(findings))


def scan_output_for_disclosure(text: str) -> DisclosureResult:
    """Scan model output for secrets and PII, returning findings and redacted text.

    Redaction is idempotent: applying this function to already-redacted text
    returns zero findings and the same text unchanged.

    Args:
        text: Model output text to scan.

    Returns:
        DisclosureResult with sorted findings and redacted_text.

    Raises:
        TypeError: If *text* is not a str.
    """
    raw: object = text
    if not isinstance(raw, str):
        raise TypeError(
            f"scan_output_for_disclosure: expected str, got {type(raw).__name__}"
        )
    text = raw

    findings = _run_disclosure_rules(text)
    findings.sort(key=_sort_key)
    redacted = _redact(text, tuple(findings))
    return DisclosureResult(findings=tuple(findings), redacted_text=redacted)
