"""The Gateway built by each long-running main() reads its kill state live.

Static check: each file is read as text and parsed with ``ast``; nothing is
imported, no server is started, no socket is opened and no environment
variable is read. ``main()`` (or a same-file helper it calls) must build
``Gateway(...)`` with a ``kill_state_path`` keyword whose value is not the
literal ``None``. ``tools/live_demo.py`` is excluded on purpose: it is a
one-shot script that kills and revives in memory.
"""

from __future__ import annotations

import ast
from pathlib import Path

import pytest

_INTELLIGENCE = Path(__file__).resolve().parent.parent
_MAINS = (
    _INTELLIGENCE / "src" / "ztax_gateway" / "server.py",
    _INTELLIGENCE / "tools" / "serve_live_demo.py",
    _INTELLIGENCE / "tools" / "report_ai_http.py",
)


def _is_gateway_call(node: ast.Call) -> bool:
    func = node.func
    return (isinstance(func, ast.Name) and func.id == "Gateway") or (
        isinstance(func, ast.Attribute) and func.attr == "Gateway"
    )


def kill_state_problems(source: str) -> list[str]:
    """Problems with the ``Gateway(...)`` calls reachable from ``main()``.

    An empty list means every such call passes a ``kill_state_path`` that is
    not the literal ``None``. A source with no ``main()`` or no reachable
    ``Gateway(...)`` call is itself a problem, so the check cannot pass
    vacuously.
    """
    tree = ast.parse(source)
    functions = {n.name: n for n in tree.body if isinstance(n, ast.FunctionDef)}
    if "main" not in functions:
        return ["no module-level main()"]

    # main() plus every same-file function it calls, directly or through others.
    reachable: dict[str, ast.FunctionDef] = {}
    pending = ["main"]
    while pending:
        name = pending.pop()
        if name in reachable:
            continue
        reachable[name] = functions[name]
        for node in ast.walk(functions[name]):
            if (
                isinstance(node, ast.Call)
                and isinstance(node.func, ast.Name)
                and node.func.id in functions
            ):
                pending.append(node.func.id)

    problems: list[str] = []
    found = 0
    for fn in reachable.values():
        for node in ast.walk(fn):
            if not (isinstance(node, ast.Call) and _is_gateway_call(node)):
                continue
            found += 1
            where = f"{fn.name}() line {node.lineno}"
            keywords = {kw.arg: kw.value for kw in node.keywords if kw.arg is not None}
            if "kill_state_path" not in keywords:
                problems.append(f"{where}: Gateway(...) has no kill_state_path")
                continue
            value = keywords["kill_state_path"]
            if isinstance(value, ast.Constant) and value.value is None:
                problems.append(f"{where}: kill_state_path is the literal None")
    if found == 0:
        problems.append("main() builds no Gateway(...)")
    return problems


@pytest.mark.parametrize("path", _MAINS, ids=lambda p: f"{p.parent.name}/{p.name}")
def test_main_passes_a_live_kill_state_path(path: Path) -> None:
    source = path.read_text(encoding="utf-8")
    assert kill_state_problems(source) == []


# Control: the helper must be able to fail.
def test_helper_reports_a_missing_kill_state_path() -> None:
    source = (
        "def main():\n"
        "    gateway = Gateway(registry, routes, runtime, region, train)\n"
        "    return 0\n"
    )
    problems = kill_state_problems(source)
    assert len(problems) == 1
    assert "no kill_state_path" in problems[0]


def test_helper_reports_a_none_kill_state_path() -> None:
    source = "def main():\n    Gateway(r, t, rt, 'x', 'y', kill_state_path=None)\n"
    problems = kill_state_problems(source)
    assert len(problems) == 1
    assert "literal None" in problems[0]


def test_helper_follows_a_same_file_helper_called_by_main() -> None:
    good = (
        "def _build():\n    return Gateway(r, t, rt, 'x', 'y', kill_state_path=path)\n"
        "def main():\n    _build()\n"
    )
    bad = "def _build():\n    return Gateway(r, t, rt, 'x', 'y')\ndef main():\n    _build()\n"
    assert kill_state_problems(good) == []
    assert len(kill_state_problems(bad)) == 1


def test_helper_does_not_pass_vacuously() -> None:
    assert kill_state_problems("def main():\n    return 0\n") == ["main() builds no Gateway(...)"]
    assert kill_state_problems("x = 1\n") == ["no module-level main()"]
    # A Gateway built outside main() and its callees does not count.
    outside = (
        "def other():\n    Gateway(a, b, c, 'x', 'y', kill_state_path=p)\ndef main():\n    pass\n"
    )
    assert kill_state_problems(outside) == ["main() builds no Gateway(...)"]
