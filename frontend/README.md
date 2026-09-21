# ZoikoTax — Frontend

React + TypeScript + Vite, served as static files by nginx. `ztax-web` in the local cell stack.

## A deviation from ADR-0019, recorded

[ADR-0019](../../adr/ADR-0019-frontend-stack-deferred.md) §2.1 defers the stack decision to **W3 lane N**, taken alongside `UX-001` and the first five screens, and §2.4 says this directory holds "nothing that presupposes a framework" until then. This tree presupposes one.

The reason is narrow: the estate needed a frontend that starts in the container stack, and a container that starts needs something to serve. The stack chosen is the first candidate ADR-0019 §2.3 names — React + TypeScript + Vite as an SPA against the generated SDK — so the deviation is a **sequencing** decision rather than a different answer to the question the ADR poses.

What the deviation does **not** do is settle what §2.3 says the decision actually turns on: whether `UX-001`'s persistent legal-context shell implies long-lived client state. Nothing here holds client state beyond a health poll, so a W3 evaluation that lands on a server-rendered framework with a BFF discards this shell and keeps the constraints. That is the intended cost, and it is small: the shell is one screen.

**Expires at W3 lane N**, like ADR-0007's topology deviation, and should be closed by an ADR that either records the choice or supersedes it.

## Constraints that are already binding

Not preferences. They apply to whatever is chosen, and they are held in the code rather than in this list.

| | Constraint | Where it lives |
|---|---|---|
| **C1** | No fiscal arithmetic in the browser. Ever. | [`src/fiscal/money.ts`](src/fiscal/money.ts) — `FiscalAmount` is a branded string with no arithmetic on it, so `Number(amount)` is a type error. [`eslint.config.js`](eslint.config.js) bans `parseFloat`, `parseInt`, `Number()`, `toFixed`, unary `+` and `Intl.NumberFormat` as the second line |
| **C2** | Authoritative and advisory are different components, not a prop | [`AuthoritativeAmount`](src/fiscal/AuthoritativeAmount.tsx) requires a `decisionId`; [`AdvisoryAmount`](src/fiscal/AdvisoryAmount.tsx) requires a stated `basis` and cannot take a decision. Neither can be rendered as the other |
| **C3** | Degraded, stale, queued and uncertain render as themselves | [`CellHealth`](src/platform/CellHealth.tsx) — four states, four treatments, no spinner anywhere in the component |
| **C4** | The API client is the generated TypeScript SDK | The SDK opens in W2 lane K. Until it exists, `fetch` is banned by lint everywhere except [`src/platform/health.ts`](src/platform/health.ts), which calls only `/healthz` and `/readyz` — operational endpoints, not the fiscal contract |
| **C5** | Consequential actions name their legal effect | No consequential action exists yet. The pattern is W3 lane N |
| **C6** | Accessibility is a gate from the first screen | `eslint-plugin-jsx-a11y` recommended rules, on from commit one |
| **C7** | Any SSR or BFF on our infrastructure is a backend service | Nothing renders on the server. nginx serves static files and proxies `/api`; it holds no session and calls nothing on a user's behalf |
| **C8** | No fiscal or personal data in browser storage beyond a session | `localStorage` and `sessionStorage` are banned by lint. Nothing is cached |

## Running it

With the rest of the stack, from the repository root:

```bash
docker compose up --build        # postgres + ztax-core + ztax-web
# http://localhost:3000
```

On its own, against a cell you are already running:

```bash
npm ci
npm run dev                      # http://localhost:5173, proxies /api to :8080
ZTAX_API_ORIGIN=http://localhost:8080 npm run dev
```

```bash
npm run lint         # the constraints above, as rules
npm run typecheck    # tsc --build, no emit shortcuts
npm run build        # type-check then bundle; a type error fails the build
```

## What is here, and what is deliberately not

The shell is one screen: the regional cell's health, and two amounts that exist to make C2 visible from the first day. The amounts are constants, not API responses — the determination surface opens in W2 lane K, and until the generated SDK exists there is nothing legitimate to call.

Not here, and not by accident: a router, a state library, a component library, a design system, an auth flow. Each is a W3 lane N decision, and each is cheaper to make once `UX-001` exists than to unpick afterwards.

The image ships no JavaScript toolchain. Node exists at build time; the runtime is `nginx-unprivileged` serving compiled output as uid 101, so nothing in the running container can execute application code that was not compiled into the bundle.
