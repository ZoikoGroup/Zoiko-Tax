import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { describe, test } from 'node:test'

import { actionBody, grantRequest, offersFor, operationsFor } from '../src/pages/entitlements/model.ts'
import { basisDigest, planOffers, planRequest, usedPercent } from '../src/pages/subscriptions/model.ts'

const MAKER = 'u-maker'
const CHECKER = 'u-checker'

const grant = (o = {}) => ({
  id: 'g1', version: 2, createdBy: MAKER, submittedBy: MAKER, actions: ['APPROVE', 'REJECT', 'WITHDRAW'], history: [], ...o,
})

const form = (o = {}) => ({
  legalEntityId: '', jurisdiction: 'gb', service: 'MOBILE_VOICE', mode: 'NATIVE', operation: 'FILE',
  effectiveFrom: '2026-11-20', effectiveTo: '', contractRef: ' MSA-1 ', contractRevision: '', submit: true, ...o,
})

describe('entitlement offers and bodies', () => {
  test('the recorder is told a second administrator approves, rather than shown a live button', () => {
    const mine = offersFor(grant(), ['ADMIN'], MAKER).find((o) => o.action === 'APPROVE')
    assert.match(mine.blocked, /different administrator/)
    assert.equal(offersFor(grant(), ['ADMIN'], CHECKER).find((o) => o.action === 'APPROVE').blocked, null)
    assert.match(offersFor(grant(), ['ANALYST'], CHECKER)[0].blocked, /ADMIN/)
  })

  test('a resume is not approved by its requester or the suspender', () => {
    const g = grant({
      actions: ['RESUME', 'REVOKE'],
      history: [{ kind: 'SUSPENDED', recordedBy: CHECKER }, { kind: 'RESUME_REQUESTED', recordedBy: MAKER }],
    })
    assert.match(offersFor(g, ['ADMIN'], MAKER)[0].blocked, /different administrator/)
    assert.match(offersFor(g, ['ADMIN'], CHECKER)[0].blocked, /different administrator/)
    assert.equal(offersFor(g, ['ADMIN'], 'u-third')[0].blocked, null)
  })

  test('every act but submission gives its reason, against the version read', () => {
    assert.ok('invalid' in actionBody({ version: 2 }, 'APPROVE', '  '))
    assert.deepEqual(actionBody({ version: 2 }, 'SUBMIT', ''), { body: { action: 'SUBMIT', expectedVersion: 2 } })
    assert.deepEqual(actionBody({ version: 3 }, 'SUSPEND', ' dispute '), { body: { action: 'SUSPEND', expectedVersion: 3, reason: 'dispute' } })
  })

  test('a mode carries only its own operations, and a grant is never backdated', () => {
    assert.deepEqual(operationsFor('SHADOW_ASSURANCE'), ['SHADOW_COMPARE'])
    assert.ok('invalid' in grantRequest(form({ mode: 'SHADOW_ASSURANCE' }), '2026-11-20'))
    assert.ok('invalid' in grantRequest(form({ effectiveFrom: '2026-11-19' }), '2026-11-20'))
    assert.ok('invalid' in grantRequest(form({ contractRef: ' ' }), '2026-11-20'))
    assert.deepEqual(grantRequest(form(), '2026-11-20').body, {
      jurisdiction: 'GB', service: 'MOBILE_VOICE', mode: 'NATIVE', operation: 'FILE', effectiveFrom: '2026-11-20',
      contractRef: 'MSA-1', submit: true,
    })
  })
})

describe('subscriptions and usage', () => {
  test('only a proposed plan moves, and its proposer does not approve it', () => {
    assert.deepEqual(planOffers({ status: 'APPROVED', createdBy: MAKER }, ['ADMIN'], CHECKER), [])
    const offers = planOffers({ status: 'PROPOSED', createdBy: MAKER }, ['ADMIN'], MAKER)
    assert.match(offers.find((o) => o.action === 'APPROVE').blocked, /different administrator/)
    assert.equal(offers.find((o) => o.action === 'WITHDRAW').blocked, null)
  })

  test('included quantities are whole numbers, sent as integers', () => {
    const quotas = [
      { meter: 'COMMITTED_DECISIONS', included: '100000', overage: 'ALLOW_OVERAGE', on: true },
      { meter: 'SHADOW_RUNS', included: 'x', overage: 'HARD_LIMIT', on: false },
    ]
    const f = { planCode: 'pilot', planName: 'Pilot', contractRef: 'MSA-1', termStart: '2026-11-20', termEnd: '', quotas }
    const ok = planRequest(f, '2026-11-20')
    assert.deepEqual(ok.body.quotas, [{ meter: 'COMMITTED_DECISIONS', included: 100000, overage: 'ALLOW_OVERAGE' }])
    assert.equal(ok.body.planCode, 'PILOT')
    assert.ok('invalid' in planRequest({ ...f, quotas: [{ ...quotas[0], included: '1.5' }] }, '2026-11-20'))
    assert.ok('invalid' in planRequest({ ...f, quotas: [{ ...quotas[0], on: false }] }, '2026-11-20'))
    assert.ok('invalid' in planRequest({ ...f, termStart: '2026-11-01' }, '2026-11-20'))
  })

  test('the bar is a whole percentage of the included quantity, and absent without a quota', () => {
    assert.equal(usedPercent({ count: 1, included: 3 }), 33)
    assert.equal(usedPercent({ count: 9, included: 3 }), 100)
    assert.equal(usedPercent({ count: 0, included: 0 }), 0)
    assert.equal(usedPercent({ count: 4 }), null)
  })

  test('the basis digest follows the cell rule: sorted ids joined by newlines, SHA-256 hex', async () => {
    assert.equal(await basisDigest(['c', 'a', 'b']), createHash('sha256').update('a\nb\nc').digest('hex'))
    assert.equal(await basisDigest([]), createHash('sha256').update('').digest('hex'))
  })
})
