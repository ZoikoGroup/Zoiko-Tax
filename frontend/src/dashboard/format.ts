/**
 * Display helpers for the Tax Command Center.
 *
 * Amounts go through fiscal/money.ts and stay strings (ADR-0019 C1). The only
 * arithmetic here is on counts — decisions, items, authorities — which are
 * integers the server sends as integers, never on an amount.
 */
import type { MoneyValue } from '@zoikotax/sdk'

import { displayAmount, money } from '../fiscal/money'

/** "EUR 1,234.50" — the currency beside the amount, as the API pairs them. */
export function moneyText(value: MoneyValue): string {
  const m = money(value.amount, value.currency)
  return `${m.currency} ${displayAmount(m.amount)}`
}

/** One line per currency; amounts in different currencies are never summed. */
export function moneyLines(values: readonly MoneyValue[]): string[] {
  return values.length === 0 ? ['—'] : values.map(moneyText)
}

/** A whole-number percentage of two counts, or null when there is no base. */
export function percent(part: number, total: number): number | null {
  if (total <= 0) {
    return null
  }
  return Math.round((part * 100) / total)
}

/** "▲ 12% vs. 2026-08" for counts; null when there is nothing to compare. */
export function countChange(current: number, previous: number): { text: string; up: boolean } | null {
  if (previous <= 0) {
    return null
  }
  const change = Math.round(((current - previous) * 100) / previous)
  return { text: `${change >= 0 ? '▲ +' : '▼ '}${change}%`, up: change >= 0 }
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

/** "2026-09" → "Sep 2026". */
export function periodLabel(period: string): string {
  const [year, month] = period.split('-')
  const name = month === undefined ? undefined : MONTHS[parseMonth(month) - 1]
  return name === undefined || year === undefined ? period : `${name} ${year}`
}

function parseMonth(mm: string): number {
  // Two digits, 01 to 12, from a value the API validated; no parseInt (C1
  // bans it for amounts, and a month does not need it either).
  return (mm.charCodeAt(0) - 48) * 10 + (mm.charCodeAt(1) - 48)
}

/** The last n months ending with the current UTC month, newest first. */
export function recentPeriods(now: Date, n: number): string[] {
  const out: string[] = []
  let year = now.getUTCFullYear()
  let month = now.getUTCMonth() + 1
  for (let i = 0; i < n; i += 1) {
    out.push(`${year}-${month < 10 ? '0' : ''}${month}`)
    month -= 1
    if (month === 0) {
      month = 12
      year -= 1
    }
  }
  return out
}

/** "2026-10-20" → "20 Oct 2026"; the civil date as the authority's calendar has it. */
export function civilDate(date: string): string {
  const [year, month, day] = date.split('-')
  if (year === undefined || month === undefined || day === undefined) {
    return date
  }
  return `${day} ${MONTHS[parseMonth(month) - 1] ?? month} ${year}`
}

/** "2026-10-09T10:32:00.000000Z" → "09 Oct 2026, 10:32 (UTC)". */
export function instant(ts: string): string {
  const date = ts.slice(0, 10)
  const time = ts.slice(11, 16)
  return `${civilDate(date)}, ${time} (UTC)`
}
