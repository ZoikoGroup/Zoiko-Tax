import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  changeBody, changeOffers, EMPTY_CHANGE, recommendationOffers, refusalText, targetsFor, transitionBody,
} from '../src/pages/regchange/model.ts'

const ME = 'u-me'
const OTHER = 'u-other'

const change = (o = {}) => ({ id: 'c1', status: 'NEW', version: 1, acceptedRecommendationCount: 0, recommendationCount: 0, ...o })
const offer = (offers, act) => offers.find((o) => o.act === act)

describe('change offers', () => {
  test('only an ANALYST acts, and assessment is never a status a person names', () => {
    assert.match(offer(changeOffers(change(), ['OPERATOR']), 'TRIAGED').blocked, /ANALYST/)
    assert.equal(offer(changeOffers(change(), ['ANALYST']), 'TRIAGED').blocked, null)
    for (const s of ['NEW', 'TRIAGED', 'IN_IMPACT_ASSESSMENT', 'ON_HOLD']) {
      assert.equal(offer(changeOffers(change({ status: s }), ['ANALYST']), 'IN_IMPACT_ASSESSMENT'), undefined)
    }
    assert.deepEqual(changeOffers(change({ status: 'CLOSED' }), ['ANALYST']), [])
  })

  test('change needed waits for an accepted recommendation; a hold resumes where it was', () => {
    const assessing = change({ status: 'IN_IMPACT_ASSESSMENT', version: 3 })
    assert.match(offer(changeOffers(assessing, ['ANALYST']), 'CHANGE_NEEDED').blocked, /independently accepted/)
    assert.equal(offer(changeOffers({ ...assessing, acceptedRecommendationCount: 1 }, ['ANALYST']), 'CHANGE_NEEDED').blocked, null)
    assert.ok(offer(changeOffers(change({ status: 'ON_HOLD', resumesTo: 'TRIAGED' }), ['ANALYST']), 'TRIAGED'))
  })

  test('every transition carries its rationale and the version read', () => {
    const form = { priority: 'URGENT', hold: 'RIGHTS_HOLD', reason: '' }
    assert.ok('invalid' in transitionBody(change(), 'TRIAGED', form))
    assert.deepEqual(transitionBody(change(), 'TRIAGED', { ...form, reason: ' urgent ' }),
      { reason: 'urgent', expectedVersion: 1, to: 'TRIAGED', priority: 'URGENT' })
    assert.deepEqual(transitionBody(change({ version: 4 }), 'CHANGE_NEEDED', { ...form, reason: 'x' }),
      { reason: 'x', expectedVersion: 4, to: 'CLOSED', disposition: 'CHANGE_NEEDED' })
  })
})

describe('change intake', () => {
  const filled = {
    ...EMPTY_CHANGE, jurisdiction: 'us-ny', title: 'NY excise', authority: 'NY DTF', citation: '186-e', edition: 'TB-1',
    sha256: 'ab'.repeat(32), publishedOn: '2026-11-01', effectiveFrom: '2027-01-01', taxTypes: 'telecom_excise, telecom_excise',
  }

  test('the three clocks and the source digest are required, never assumed', () => {
    assert.ok('invalid' in changeBody({ ...filled, sha256: '' }))
    assert.ok('invalid' in changeBody({ ...filled, effectiveFrom: '' }))
    assert.ok('invalid' in changeBody({ ...filled, kind: 'BACKDATED_RULE' }))
    assert.ok('invalid' in changeBody({ ...filled, detectedBy: 'MONITOR' }))
    const body = changeBody(filled)
    assert.equal(body.jurisdiction, 'US-NY')
    assert.deepEqual(body.taxTypes, ['TELECOM_EXCISE'])
    assert.equal(body.source.documentSha256, 'ab'.repeat(32))
  })
})

describe('recommendation offers', () => {
  const rec = (o = {}) => ({
    id: 'r1', status: 'RECOMMENDED', stale: false, recommendedBy: OTHER,
    history: [{ seq: 1, kind: 'DRAFTED', recordedBy: 'u-author' }], ...o,
  })

  test('neither the recommender nor an author reviews; a stale one is not reviewed', () => {
    assert.match(offer(recommendationOffers(rec({ recommendedBy: ME }), ['ANALYST'], ME), 'ACCEPT').blocked, /independent reviewer/)
    assert.match(offer(recommendationOffers(rec({ history: [{ seq: 1, kind: 'DRAFTED', recordedBy: ME }] }), ['ANALYST'], ME), 'ACCEPT').blocked, /wrote a version/)
    assert.match(offer(recommendationOffers(rec({ stale: true }), ['ANALYST'], ME), 'ACCEPT').blocked, /reassessed/)
    assert.equal(offer(recommendationOffers(rec(), ['ANALYST'], ME), 'ACCEPT').blocked, null)
    assert.deepEqual(recommendationOffers(rec({ status: 'ACCEPTED' }), ['ANALYST'], ME), [])
  })

  test('an action offers only the assessed items it can name', () => {
    const items = [
      { kind: 'CONTENT_COMPONENT', ref: 'a', retrospective: false },
      { kind: 'COVERAGE_GAP', ref: 'g', retrospective: false },
      { kind: 'TENANT_OBLIGATION', ref: 'o', retrospective: true },
    ]
    assert.deepEqual(targetsFor('UPDATE_RULE', items).map((i) => i.ref), ['a'])
    assert.deepEqual(targetsFor('ADD_RULE', items).map((i) => i.ref), ['a', 'g'])
    assert.deepEqual(targetsFor('RETROSPECTIVE_REASSESSMENT', items).map((i) => i.ref), ['o'])
    assert.match(refusalText('ASSESSMENT_SUPERSEDED'), /reassessed/)
  })
})
