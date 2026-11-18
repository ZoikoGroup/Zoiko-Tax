import assert from 'node:assert/strict'
import { File } from 'node:buffer'
import { describe, test } from 'node:test'

import { certificateOffers, documentOf, endOf, refusalText, registrationOffers } from '../src/pages/registrations/model.ts'

const MAKER = 'u-maker'
const CHECKER = 'u-checker'

const reg = (o = {}) => ({
  id: 'r1', status: 'VERIFIED', version: 4, flags: ['AWAITING_ACTIVATION'], effectiveFrom: '2026-01-01',
  submittedBy: MAKER, history: [], attachments: [], ...o,
})

const cert = (o = {}) => ({
  id: 'c1', status: 'REVIEW_REQUIRED', evidenceVersion: 1, flags: [],
  history: [{ seq: 1, kind: 'RECEIVED', status: 'SUBMITTED', evidenceVersion: 1, document: { sha256: 'a' }, recordedBy: MAKER }],
  ...o,
})

const offer = (offers, act) => offers.find((o) => o.act === act)

describe('registration offers', () => {
  test('the submitter is told a different person activates, rather than shown a dead button', () => {
    const mine = offer(registrationOffers(reg(), ['ANALYST'], MAKER), 'ACTIVE')
    assert.match(mine.blocked, /different person/)
    assert.equal(offer(registrationOffers(reg(), ['ANALYST'], CHECKER), 'ACTIVE').blocked, null)
    assert.match(offer(registrationOffers(reg(), ['OPERATOR'], CHECKER), 'ACTIVE').blocked, /ANALYST/)
  })

  test('VERIFIED is never offered as a move, and a closed registration offers nothing', () => {
    for (const s of ['DRAFT', 'SUBMITTED', 'VERIFIED', 'ACTIVE', 'SUSPENDED']) {
      assert.equal(offer(registrationOffers(reg({ status: s }), ['ANALYST'], CHECKER), 'VERIFIED'), undefined)
    }
    for (const s of ['EXPIRED', 'REVOKED', 'WITHDRAWN']) {
      assert.deepEqual(registrationOffers(reg({ status: s }), ['ANALYST', 'OPERATOR'], CHECKER), [])
    }
  })

  test('expiry is offered only once the end date has passed', () => {
    assert.notEqual(offer(registrationOffers(reg({ status: 'ACTIVE', flags: [] }), ['ANALYST'], CHECKER), 'EXPIRED').blocked, null)
    assert.equal(offer(registrationOffers(reg({ status: 'ACTIVE', flags: ['PAST_EXPIRY'] }), ['ANALYST'], CHECKER), 'EXPIRED').blocked, null)
  })
})

describe('certificate offers', () => {
  const pending = { recommendation: 'APPROVE', evidenceVersion: 1, rationale: 'ok', recordedBy: CHECKER, recordedAt: 'x' }

  test('neither the recommender nor the uploader may confirm', () => {
    const c = cert({ pendingRecommendation: pending })
    assert.match(offer(certificateOffers(c, ['ANALYST'], CHECKER), 'CONFIRM').blocked, /recommendation/)
    assert.match(offer(certificateOffers(c, ['ANALYST'], MAKER), 'CONFIRM').blocked, /document/)
    assert.equal(offer(certificateOffers(c, ['ANALYST'], 'u-third'), 'CONFIRM').blocked, null)
  })

  test('without a pending recommendation, the next act is to recommend', () => {
    assert.equal(offer(certificateOffers(cert(), ['ANALYST'], CHECKER), 'RECOMMEND').blocked, null)
    assert.equal(offer(certificateOffers(cert(), ['ANALYST'], CHECKER), 'CONFIRM'), undefined)
  })

  test('an approved certificate takes no new document; a renewal is a new certificate', () => {
    assert.equal(offer(certificateOffers(cert({ status: 'APPROVED' }), ['ANALYST', 'OPERATOR'], CHECKER), 'VERSION'), undefined)
  })
})

test('refusals read as plain words', () => {
  assert.match(refusalText('VERSION_CONFLICT'), /changed/)
  assert.match(refusalText('REGISTRATION_SCHEME_MISMATCH'), /never converted/)
  assert.equal(refusalText('SOMETHING_ELSE'), null)
})

test('the renewal queue sorts by the approved end, else the stated one', () => {
  assert.equal(endOf({ approval: { effectiveTo: '2027-01-01' }, expiresOn: '2028-01-01' }), '2027-01-01')
  assert.equal(endOf({ effectiveTo: '2026-12-31' }), '2026-12-31')
  assert.equal(endOf({}), '9999-12-31')
})

test('a document is hashed locally, and a prohibited type is refused before anything is sent', async () => {
  const pdf = new File([new Uint8Array([1, 2, 3])], 'permit.pdf', { type: 'application/pdf' })
  const got = await documentOf(pdf)
  assert.equal(got.sha256, '039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81')
  assert.equal(got.bytes, 3)
  const exe = new File([new Uint8Array([1])], 'x.exe', { type: 'application/x-msdownload' })
  assert.equal(typeof (await documentOf(exe)), 'string')
})
