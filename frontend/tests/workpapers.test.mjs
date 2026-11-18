import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { actionBody, EMPTY_FORM } from '../src/pages/workpapers/model.ts'

const evidence = { sha256: 'ab'.repeat(32), filename: 'audit.pdf', mediaType: 'application/pdf' }
const exception = { key: 'SUBLEDGER_SUSPENSE_OPEN/USD', fingerprint: 'zt1:' + '0'.repeat(64) }
const w = { eventCount: 4, frozenBuildVersion: 2, exceptions: [exception] }

describe('workpaper action bodies', () => {
  test('every act carries the event count it was decided against', () => {
    assert.deepEqual(actionBody(w, 'FREEZE', EMPTY_FORM, null), { body: { action: 'FREEZE', expectedSeq: 4 } })
    assert.deepEqual(actionBody(w, 'APPROVE', { ...EMPTY_FORM, note: ' tied out ' }, null),
      { body: { action: 'APPROVE', expectedSeq: 4, note: 'tied out', buildVersion: 2 } })
  })

  test('an adjustment amount stays the string typed, and needs its evidence', () => {
    const form = { ...EMPTY_FORM, adjCategory: 'Billing correction', adjAmount: '-1200.00', adjReason: 'Credit notes' }
    assert.ok('invalid' in actionBody(w, 'PROPOSE_ADJUSTMENT', form, null))
    assert.ok('invalid' in actionBody(w, 'PROPOSE_ADJUSTMENT', { ...form, adjAmount: '-1.2e3' }, evidence))
    const built = actionBody(w, 'PROPOSE_ADJUSTMENT', form, evidence)
    assert.equal(built.body.adjustment.amount, '-1200.00')
    assert.equal(built.body.evidence.sha256, evidence.sha256)
  })

  test('a resolution names the exception and its fingerprint', () => {
    const built = actionBody(w, 'PROPOSE_RESOLUTION', { ...EMPTY_FORM, exceptionKey: exception.key, note: 'Mapping pending' }, evidence)
    assert.equal(built.body.fingerprint, exception.fingerprint)
    assert.ok('invalid' in actionBody(w, 'DECIDE_RESOLUTION', { ...EMPTY_FORM, exceptionKey: exception.key, decision: 'REJECTED' }, null))
  })

  test('a documented rate is complete and positive', () => {
    const form = { ...EMPTY_FORM, fxCurrency: 'cad', fxRate: '0.73', fxSource: 'Bank of Canada', fxDate: '2026-09-30', fxBasis: 'Period-end' }
    assert.equal(actionBody(w, 'DOCUMENT_FX_RATE', form, null).body.fx.currency, 'CAD')
    assert.ok('invalid' in actionBody(w, 'DOCUMENT_FX_RATE', { ...form, fxRate: '-0.73' }, null))
    assert.ok('invalid' in actionBody(w, 'DOCUMENT_FX_RATE', { ...form, fxBasis: '' }, null))
  })
})
