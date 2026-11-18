import assert from 'node:assert/strict'
import { test } from 'node:test'

import { attemptsOf, dispatchable, parseBalances, toneOf } from '../src/pages/erp/model.ts'

test('pasted GL balances are read line by line, amounts kept as typed', () => {
  const got = parseBalances('2310-00, 0.00, 17.50\n\n1990-00\t17.50\t0.00\r\n')
  assert.deepEqual(got, {
    ok: true,
    lines: [
      { glAccount: '2310-00', debits: '0.00', credits: '17.50' },
      { glAccount: '1990-00', debits: '17.50', credits: '0.00' },
    ],
  })
})

test('a bad pasted line is refused by its number', () => {
  assert.match(parseBalances('2310-00, 0.00').error, /Line 1/)
  assert.match(parseBalances('2310-00, -1.00, 0.00').error, /non-negative/)
  assert.match(parseBalances('2310-00, 1e3, 0').error, /non-negative/)
  assert.match(parseBalances('a,1,0\na,0,1').error, /Line 2: a is listed twice/)
  assert.match(parseBalances('  \n').error, /at least one/)
})

const batch = (status, kinds) => ({
  status,
  history: kinds.map((kind, i) => ({ seq: i + 1, kind, status, ...(kind === 'DISPATCHED' ? { attemptId: `a${i}` } : {}) })),
})

test('a batch is sent again only from PREPARED or after a status query found nothing', () => {
  assert.equal(dispatchable(batch('PREPARED', ['PREPARED'])), true)
  assert.equal(dispatchable(batch('TRANSMITTED', ['PREPARED', 'DISPATCHED'])), false)
  assert.equal(dispatchable(batch('OUTCOME_UNKNOWN', ['PREPARED', 'DISPATCHED', 'OUTCOME_UNKNOWN'])), false)
  assert.equal(dispatchable(batch('OUTCOME_UNKNOWN', ['PREPARED', 'DISPATCHED', 'OUTCOME_UNKNOWN', 'STATUS_NOT_FOUND'])), true)
  assert.equal(dispatchable(batch('REJECTED', ['PREPARED', 'DISPATCHED', 'REJECTED'])), false)
  assert.deepEqual(attemptsOf(batch('TRANSMITTED', ['PREPARED', 'DISPATCHED', 'OUTCOME_UNKNOWN', 'STATUS_NOT_FOUND', 'DISPATCHED'])), ['a1', 'a4'])
})

test('a transport receipt is not shown as posted', () => {
  assert.equal(toneOf('ERP_POSTED'), 'ok')
  assert.notEqual(toneOf('ACKNOWLEDGED'), 'ok')
  assert.equal(toneOf('HELD'), 'warn')
  assert.equal(toneOf('PARTIAL'), 'bad')
})
