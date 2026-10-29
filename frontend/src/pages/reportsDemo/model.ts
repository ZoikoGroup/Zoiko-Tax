/**
 * Types and strict decoders for the demo AI service's two JSON bodies --
 * GET /demo/fixture (intelligence/tools/report_fixture.py) and
 * POST /demo/explain (intelligence/tools/report_ai_http.py).
 *
 * Decoders refuse a body that does not have the expected shape rather than
 * defaulting a missing field: the page shows only what the service sent, so
 * a value that is not there is an error, never a made-up number. Every
 * number stays the string the service sent (ADR-0019 C1).
 */

export class ShapeError extends Error {}

type Rec = Record<string, unknown>

function rec(v: unknown, where: string): Rec {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) {
    throw new ShapeError(`${where}: not an object`)
  }
  return v as Rec
}

function str(r: Rec, key: string, where: string): string {
  const v = r[key]
  if (typeof v !== 'string') {
    throw new ShapeError(`${where}.${key}: not a string`)
  }
  return v
}

function optStr(r: Rec, key: string): string | undefined {
  const v = r[key]
  return typeof v === 'string' ? v : undefined
}

function strs(v: unknown, where: string): readonly string[] {
  if (!Array.isArray(v)) {
    throw new ShapeError(`${where}: not a list`)
  }
  const items = v as unknown[]
  if (!items.every((x): x is string => typeof x === 'string')) {
    throw new ShapeError(`${where}: not a list of strings`)
  }
  return items
}

function list<T>(v: unknown, where: string, decode: (r: Rec, where: string) => T): readonly T[] {
  if (!Array.isArray(v)) {
    throw new ShapeError(`${where}: not a list`)
  }
  return (v as unknown[]).map((item, i) => decode(rec(item, `${where}[${i}]`), `${where}[${i}]`))
}

// ---------------------------------------------------------------------------
// the fixture
// ---------------------------------------------------------------------------

export interface Figure {
  readonly id: string
  readonly label: string
  readonly value: string
  readonly unit: string
  readonly group: string
  readonly kind: string
  readonly basis: string
  readonly sourceIds: readonly string[]
  readonly authority: string | undefined
  readonly asOf: string | undefined
  readonly windowDays: string | undefined
  readonly direction: string | undefined
  readonly changeAmount: string | undefined
  readonly filedCount: string | undefined
  readonly totalFilings: string | undefined
  readonly authorities: readonly string[] | undefined
}

export interface TrendPoint {
  readonly id: string
  readonly month: string
  readonly label: string
  readonly value: string
  readonly unit: string
}

export interface Bucket {
  readonly id: string
  readonly label: string
  readonly count: string
  readonly totalFilings: string
  readonly percent: string
  readonly sourceIds: readonly string[]
}

export interface Obligation {
  readonly id: string
  readonly type: string
  readonly jurisdiction: string
  readonly authority: string
  readonly period: string
  readonly status: string
  readonly dueDate: string
  readonly amount: string
  readonly currency: string
  readonly amountUsd: string
  readonly fxRate: string | undefined
}

export interface Payment {
  readonly id: string
  readonly obligationId: string
  readonly amount: string
  readonly currency: string
  readonly date: string
  readonly status: string
}

export interface ShadowRow {
  readonly id: string
  readonly obligationId: string
  readonly comparatorValue: string
  readonly zoikoValue: string
  readonly absoluteVariance: string
  readonly currency: string
  readonly result: string
  readonly caseState: string
}

export interface Fixture {
  readonly disclaimer: string
  readonly currency: string
  readonly asOf: string
  readonly fxDemoRate: { readonly from: string; readonly to: string; readonly rate: string; readonly label: string }
  readonly figures: readonly Figure[]
  readonly vsLastMonth: Figure
  readonly trend: readonly TrendPoint[]
  readonly pipelineAsOf: string
  readonly buckets: readonly Bucket[]
  readonly obligations: readonly Obligation[]
  readonly payments: readonly Payment[]
  readonly shadow: readonly ShadowRow[]
}

function decodeFigure(r: Rec, where: string): Figure {
  const authorities = r.authorities
  return {
    id: str(r, 'id', where),
    label: str(r, 'label', where),
    value: str(r, 'value', where),
    unit: str(r, 'unit', where),
    group: str(r, 'group', where),
    kind: str(r, 'kind', where),
    basis: str(r, 'basis', where),
    sourceIds: r.source_ids === undefined ? [] : strs(r.source_ids, `${where}.source_ids`),
    authority: optStr(r, 'authority'),
    asOf: optStr(r, 'as_of'),
    windowDays: optStr(r, 'window_days'),
    direction: optStr(r, 'direction'),
    changeAmount: optStr(r, 'change_amount'),
    filedCount: optStr(r, 'filed_count'),
    totalFilings: optStr(r, 'total_filings'),
    authorities: authorities === undefined ? undefined : strs(authorities, `${where}.authorities`),
  }
}

function decodeTrendPoint(r: Rec, where: string): TrendPoint {
  return {
    id: str(r, 'id', where),
    month: str(r, 'month', where),
    label: str(r, 'label', where),
    value: str(r, 'value', where),
    unit: str(r, 'unit', where),
  }
}

function decodeBucket(r: Rec, where: string): Bucket {
  return {
    id: str(r, 'id', where),
    label: str(r, 'label', where),
    count: str(r, 'count', where),
    totalFilings: str(r, 'total_filings', where),
    percent: str(r, 'percent', where),
    sourceIds: strs(r.source_ids, `${where}.source_ids`),
  }
}

function decodeObligation(r: Rec, where: string): Obligation {
  return {
    id: str(r, 'id', where),
    type: str(r, 'type', where),
    jurisdiction: str(r, 'jurisdiction', where),
    authority: str(r, 'authority', where),
    period: str(r, 'period', where),
    status: str(r, 'status', where),
    dueDate: str(r, 'due_date', where),
    amount: str(r, 'amount', where),
    currency: str(r, 'currency', where),
    amountUsd: str(r, 'amount_usd', where),
    fxRate: optStr(r, 'fx_rate'),
  }
}

function decodePayment(r: Rec, where: string): Payment {
  return {
    id: str(r, 'id', where),
    obligationId: str(r, 'obligation_id', where),
    amount: str(r, 'amount', where),
    currency: str(r, 'currency', where),
    date: str(r, 'date', where),
    status: str(r, 'status', where),
  }
}

function decodeShadow(r: Rec, where: string): ShadowRow {
  return {
    id: str(r, 'id', where),
    obligationId: str(r, 'obligation_id', where),
    comparatorValue: str(r, 'comparator_value', where),
    zoikoValue: str(r, 'zoiko_value', where),
    absoluteVariance: str(r, 'absolute_variance', where),
    currency: str(r, 'currency', where),
    result: str(r, 'result', where),
    caseState: str(r, 'case_state', where),
  }
}

function decodeFixture(json: unknown): Fixture {
  const top = rec(json, 'fixture')
  const fx = rec(top.fx_demo_rate, 'fixture.fx_demo_rate')
  const trend = rec(top.trend, 'fixture.trend')
  const pipeline = rec(top.filing_pipeline, 'fixture.filing_pipeline')
  const figures = list(top.report_figures, 'fixture.report_figures', decodeFigure)
  const vsId = str(trend, 'vs_last_month_id', 'fixture.trend')
  const vsLastMonth = figures.find((f) => f.id === vsId)
  if (vsLastMonth === undefined) {
    throw new ShapeError(`fixture.trend.vs_last_month_id: ${vsId} is not a report figure`)
  }
  return {
    disclaimer: str(top, 'disclaimer', 'fixture'),
    currency: str(top, 'currency', 'fixture'),
    asOf: str(top, 'as_of', 'fixture'),
    fxDemoRate: {
      from: str(fx, 'from', 'fx_demo_rate'),
      to: str(fx, 'to', 'fx_demo_rate'),
      rate: str(fx, 'rate', 'fx_demo_rate'),
      label: str(fx, 'label', 'fx_demo_rate'),
    },
    figures,
    vsLastMonth,
    trend: list(trend.points, 'fixture.trend.points', decodeTrendPoint),
    pipelineAsOf: str(pipeline, 'as_of', 'fixture.filing_pipeline'),
    buckets: list(pipeline.buckets, 'fixture.filing_pipeline.buckets', decodeBucket),
    obligations: list(top.obligations, 'fixture.obligations', decodeObligation),
    payments: list(top.payments, 'fixture.payments', decodePayment),
    shadow: list(top.shadow_assurance, 'fixture.shadow_assurance', decodeShadow),
  }
}

/** The fixture, or null if the body is not the shape the service documents. */
export function parseFixture(json: unknown): Fixture | null {
  try {
    return decodeFixture(json)
  } catch (e) {
    if (e instanceof ShapeError) {
      return null
    }
    throw e
  }
}

// ---------------------------------------------------------------------------
// the explanation
// ---------------------------------------------------------------------------

/** The kinds POST /demo/explain accepts. */
export type ExplainKind =
  | 'report-figure'
  | 'trend-point'
  | 'filing-bucket'
  | 'obligation-row'
  | 'payment-row'
  | 'shadow-variance'

export interface ExplainTarget {
  readonly title: string
  readonly kind: ExplainKind
  readonly ids: readonly string[]
}

export interface Provenance {
  readonly useCase: string
  readonly model: string
  readonly provider: string
  readonly prompt: string
  readonly aiTrain: string
}

export type ExplainOutcome = 'EXPLAINED' | 'ABSTAINED' | 'REVIEW_REQUIRED'

export interface ExplainReply {
  readonly outcome: ExplainOutcome
  readonly explanation: string
  readonly citedIds: readonly string[]
  readonly reason: string
  readonly provenance: Provenance | null
}

function outcomeOf(v: string): ExplainOutcome {
  if (v === 'EXPLAINED' || v === 'ABSTAINED' || v === 'REVIEW_REQUIRED') {
    return v
  }
  throw new ShapeError(`reply.outcome: unknown outcome ${JSON.stringify(v)}`)
}

function decodeReply(json: unknown): ExplainReply {
  const r = rec(json, 'reply')
  const p = r.provenance
  let provenance: Provenance | null = null
  if (p !== null && p !== undefined) {
    const pr = rec(p, 'reply.provenance')
    provenance = {
      useCase: str(pr, 'useCase', 'provenance'),
      model: str(pr, 'model', 'provenance'),
      provider: str(pr, 'provider', 'provenance'),
      prompt: str(pr, 'prompt', 'provenance'),
      aiTrain: str(pr, 'aiTrain', 'provenance'),
    }
  }
  return {
    outcome: outcomeOf(str(r, 'outcome', 'reply')),
    explanation: str(r, 'explanation', 'reply'),
    citedIds: strs(r.cited_ids, 'reply.cited_ids'),
    reason: str(r, 'reason', 'reply'),
    provenance,
  }
}

/** The reply, or null if the body is not the shape the service documents. */
export function parseExplainReply(json: unknown): ExplainReply | null {
  try {
    return decodeReply(json)
  } catch (e) {
    if (e instanceof ShapeError) {
      return null
    }
    throw e
  }
}
