"""The Gateway behind each long-running main() reads its kill state live.

Static check: each file is read as text and parsed with ``ast``; nothing is
imported, no server is started, no socket is opened and no environment
variable is read.

* ``src/ztax_gateway/server.py``: ``main()`` builds ``Gateway(...)`` itself and
  must pass a ``kill_state_path`` that is not the literal ``None``.
* ``tools/serve_live_demo.py`` and ``tools/report_ai_http.py``: ``main()`` must
  call ``build_gateway`` and build no ``Gateway(...)`` of its own, and
  ``src/ztax_gateway/gateway_factory.py``'s ``build_gateway`` must build
  ``Gateway(...)`` with a ``kill_state_path`` that is not the literal ``None``.

``tools/live_demo.py`` is excluded on purpose: it is a one-shot script that
kills and revives in memory.
"""

from __future__ import annotations

import ast
from pathlib import Path

import pytest

_INTELLIGENCE = Path(__file__).resolve().parent.parent
_SERVER = _INTELLIGENCE / "src" / "ztax_gateway" / "server.py"
_FACTORY = _INTELLIGENCE / "src" / "ztax_gateway" / "gateway_factory.py"
_FACTORY_MAINS = (
    _INTELLIGENCE / "tools" / "serve_live_demo.py",
    _INTELLIGENCE / "tools" / "report_ai_http.py",
)


def _is_gateway_call(node: ast.Call) -> bool:
    func = node.func
    return (isinstance(func, ast.Name) and func.id == "Gateway") or (
        isinstance(func, ast.Attribute) and func.attr == "Gateway"
    )


def _is_call_to(node: ast.Call, name: str) -> bool:
    func = node.func
    return (isinstance(func, ast.Name) and func.id == name) or (
        isinstance(func, ast.Attribute) and func.attr == name
    )


def _reachable(tree: ast.Module, entry: str) -> dict[str, ast.FunctionDef] | None:
    """``entry`` plus every same-file function it calls, directly or through others."""
    functions = {n.name: n for n in tree.body if isinstance(n, ast.FunctionDef)}
    if entry not in functions:
        return None
    reachable: dict[str, ast.FunctionDef] = {}
    pending = [entry]
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
    return reachable


def kill_state_problems(source: str, entry: str = "main") -> list[str]:
    """Problems with the ``Gateway(...)`` calls reachable from ``entry``.

    An empty list means every such call passes a ``kill_state_path`` that is
    not the literal ``None``. A source with no ``entry`` function or no
    reachable ``Gateway(...)`` call is itself a problem, so the check cannot
    pass vacuously.
    """
    reachable = _reachable(ast.parse(source), entry)
    if reachable is None:
        return [f"no module-level {entry}()"]

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
        problems.append(f"{entry}() builds no Gateway(...)")
    return problems


def factory_use_problems(
    source: str, entry: str = "main", factory: str = "build_gateway"
) -> list[str]:
    """Problems with how ``entry`` obtains its Gateway: it must call *factory* and
    must not construct ``Gateway(...)`` itself."""
    reachable = _reachable(ast.parse(source), entry)
    if reachable is None:
        return [f"no module-level {entry}()"]
    calls = [
        node for fn in reachable.values() for node in ast.walk(fn) if isinstance(node, ast.Call)
    ]
    problems: list[str] = []
    if not any(_is_call_to(c, factory) for c in calls):
        problems.append(f"{entry}() does not call {factory}")
    for c in calls:
        if _is_gateway_call(c):
            problems.append(f"{entry}() builds Gateway(...) itself at line {c.lineno}")
    return problems


def test_server_main_passes_a_live_kill_state_path() -> None:
    assert kill_state_problems(_SERVER.read_text(encoding="utf-8")) == []


@pytest.mark.parametrize("path", _FACTORY_MAINS, ids=lambda p: f"{p.parent.name}/{p.name}")
def test_demo_main_gets_its_gateway_from_build_gateway(path: Path) -> None:
    assert factory_use_problems(path.read_text(encoding="utf-8")) == []


def test_build_gateway_passes_a_live_kill_state_path() -> None:
    source = _FACTORY.read_text(encoding="utf-8")
    assert kill_state_problems(source, entry="build_gateway") == []


# Controls: the helpers must be able to fail.
def test_helper_reports_a_missing_kill_state_path() -> None:
    source = (
        "def main():\n"
        "    gateway = Gateway(registry, routes, runtime, region, train)\n"
        "    return 0\n"
    )
    problems = kill_state_problems(source)
    assert len(problems) == 1
    assert "no kill_state_path" in problems[0]


def test_factory_check_fails_when_kill_state_path_is_dropped_from_the_factory() -> None:
    real = _FACTORY.read_text(encoding="utf-8")
    assert "kill_state_path=config_path" in real
    mutated = real.replace("kill_state_path=config_path", "kill_state_path=None")
    assert kill_state_problems(mutated, entry="build_gateway") == [
        f"build_gateway() line {_gateway_line(mutated)}: kill_state_path is the literal None"
    ]
    dropped = real.replace("kill_state_path=config_path,", "")
    problems = kill_state_problems(dropped, entry="build_gateway")
    assert len(problems) == 1
    assert "no kill_state_path" in problems[0]


def _gateway_line(source: str) -> int:
    for node in ast.walk(ast.parse(source)):
        if isinstance(node, ast.Call) and _is_gateway_call(node):
            return node.lineno
    raise AssertionError("no Gateway(...) call")


def test_main_check_fails_when_a_main_stops_using_build_gateway() -> None:
    real = _FACTORY_MAINS[0].read_text(encoding="utf-8")
    assert "build_gateway(" in real
    stopped = real.replace("build_gateway(", "something_else(")
    assert factory_use_problems(stopped) == ["main() does not call build_gateway"]
    bypass = "def main():\n    return Gateway(r, t, rt, 'x', 'y', kill_state_path=p)\n"
    problems = factory_use_problems(bypass)
    assert "main() does not call build_gateway" in problems
    assert any("builds Gateway(...) itself" in p for p in problems)


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
    assert factory_use_problems("def _go():\n    build_gateway(a)\ndef main():\n    _go()\n") == []


def test_helper_does_not_pass_vacuously() -> None:
    assert kill_state_problems("def main():\n    return 0\n") == ["main() builds no Gateway(...)"]
    assert kill_state_problems("x = 1\n") == ["no module-level main()"]
    assert factory_use_problems("x = 1\n") == ["no module-level main()"]
    # A Gateway built outside main() and its callees does not count.
    outside = (
        "def other():\n    Gateway(a, b, c, 'x', 'y', kill_state_path=p)\ndef main():\n    pass\n"
    )
    assert kill_state_problems(outside) == ["main() builds no Gateway(...)"]
