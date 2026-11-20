import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  dispatchBlocked,
  dispatches,
  profilesFor,
  receiptDigest,
  recordOffers,
  refusalText,
  responseKinds,
  responseNeeds,
  ruleCounts,
} from '../src/pages/einvoicing/model.ts'

const ME = 'me'
const OTHER = 'other'
const GATES = ['G0_SCOPE_AUTHORITY', 'G1_LEGAL_CONTENT_RIGHTS', 'G2_TECHNICAL_QA', 'G3_SECURITY_OPERATIONS', 'G4_CONTROLLED_PRODUCTION']

const record = (status, extra = {}) => ({
  id: 'r', status, createdBy: OTHER, currentVersion: 1, terms: { duty: 'REQUIRED' },
  versions: [{ version: 1, recordedBy: OTHER }], missingGates: [], evidence: [{ sha256: 'a' }], history: [], ...extra,
})
const offer = (offers, act) => offers.find((o) => o.act === act)

test('a national profile is offered only in its own country, and a prohibited duty has none', () => {
  assert.deepEqual(profilesFor('IT', 'REQUIRED'), ['PEPPOL_BIS_3', 'IT_FATTURAPA'])
  assert.deepEqual(profilesFor('DE', 'OPTIONAL'), ['PEPPOL_BIS_3'])
  assert.deepEqual(profilesFor('IT', 'PROHIBITED'), ['NONE'])
})

test('gates and activation are another specialist’s, and a disabled act names the missing gate', () => {
  const inReview = record('IN_REVIEW', { missingGates: ['G2_TECHNICAL_QA'] })
  assert.match(offer(recordOffers(inReview, ['OPERATOR'], ME), 'ACTIVATE').blocked, /ANALYST/)
  assert.match(offer(recordOffers(inReview, ['ANALYST'], ME), 'ACTIVATE').blocked, /G2 Technical/)
  assert.equal(offer(recordOffers(inReview, ['ANALYST'], ME), 'SIGN').blocked, null)
  const mine = record('IN_REVIEW', { createdBy: ME })
  assert.match(offer(recordOffers(mine, ['ANALYST'], ME), 'SIGN').blocked, /different person/)
  const authored = record('IN_REVIEW', { versions: [{ version: 1, recordedBy: OTHER }, { version: 2, recordedBy: ME }] })
  assert.match(offer(recordOffers(authored, ['ANALYST'], ME), 'ACTIVATE').blocked, /different person/)
  const noEvidence = record('IN_REVIEW', { evidence: [] })
  assert.match(offer(recordOffers(noEvidence, ['ANALYST'], ME), 'ACTIVATE').blocked, /evidence/)
  assert.equal(offer(recordOffers(record('IN_REVIEW'), ['ANALYST'], ME), 'ACTIVATE').blocked, null)
  assert.equal(GATES.length, 5)
})

test('the suspender does not resume, and a retired record offers nothing', () => {
  const s = record('SUSPENDED', { history: [{ kind: 'SUSPENDED', recordedBy: ME }] })
  assert.match(offer(recordOffers(s, ['ANALYST'], ME), 'RESUME').blocked, /suspended it/)
  assert.equal(offer(recordOffers(s, ['ANALYST'], OTHER), 'RESUME').blocked, null)
  assert.deepEqual(recordOffers(record('RETIRED'), ['ANALYST'], ME), [])
})

test('only an active record that dispatches carries documents', () => {
  assert.equal(dispatches(record('ACTIVE')), true)
  assert.equal(dispatches(record('ACTIVE', { terms: { duty: 'PROHIBITED' } })), false)
  assert.equal(dispatches(record('IN_REVIEW')), false)
})

const sub = (status, kinds, preparedBy = OTHER) => ({
  status, preparedBy,
  history: kinds.map((kind, i) => ({ seq: i + 1, kind, ...(kind === 'DISPATCHED' ? { attemptId: `a${i}` } : {}) })),
})

test('a submission is dispatched once, by someone other than its preparer, and resent only after an enquiry found nothing', () => {
  assert.equal(dispatchBlocked(sub('PREPARED', ['PREPARED']), ['ANALYST'], ME), null)
  assert.match(dispatchBlocked(sub('PREPARED', ['PREPARED'], ME), ['ANALYST'], ME), /different person/)
  assert.match(dispatchBlocked(sub('SENT', ['PREPARED', 'DISPATCHED']), ['ANALYST'], ME), /not dispatched/)
  assert.match(dispatchBlocked(sub('UNCERTAIN_EXTERNAL_STATE', ['PREPARED', 'DISPATCHED', 'OUTCOME_UNKNOWN']), ['ANALYST'], ME), /status enquiry/)
  assert.equal(dispatchBlocked(sub('UNCERTAIN_EXTERNAL_STATE', ['PREPARED', 'DISPATCHED', 'OUTCOME_UNKNOWN', 'STATUS_NOT_FOUND']), ['ANALYST'], ME), null)
  assert.match(dispatchBlocked(sub('PREPARED', ['PREPARED']), ['AUDITOR'], ME), /OPERATOR or ANALYST/)
})

test('a final outcome takes no answer, and a transport receipt is not acceptance', () => {
  assert.deepEqual(responseKinds(sub('AUTHORITY_ACCEPTED', [])), [])
  assert.deepEqual(responseKinds(sub('AUTHORITY_REJECTED', [])), [])
  assert.ok(responseKinds(sub('TRANSPORT_RECEIVED', [])).includes('AUTHORITY_ACCEPTED'))
  assert.ok(!responseKinds(sub('SENT', [])).includes('STATUS_NOT_FOUND'))
  assert.deepEqual(responseNeeds('AUTHORITY_ACCEPTED'), { transportRef: false, authorityRef: true, authorityCode: false, evidence: true, detail: false })
  assert.equal(responseNeeds('AUTHORITY_REJECTED').authorityCode, true)
})

test('a receipt digest is a lowercase SHA-256', () => {
  assert.equal(receiptDigest(' ' + 'AB'.repeat(32) + ' '), 'ab'.repeat(32))
  assert.equal(receiptDigest('sha256:' + 'ab'.repeat(32)), null)
})

test('NOT_EVALUATED is counted apart from a pass', () => {
  const v = { results: [
    { outcome: 'PASS', severity: 'FATAL' }, { outcome: 'FAIL', severity: 'FATAL' },
    { outcome: 'FAIL', severity: 'WARNING' }, { outcome: 'NOT_EVALUATED', severity: 'FATAL' },
  ] }
  assert.deepEqual(ruleCounts(v), { pass: 1, fail: 2, fatal: 1, notEvaluated: 1 })
})

test('the refusals the screen expects read in words', () => {
  assert.match(refusalText('CTC_GATE_SIGNOFF_MISSING'), /NOT_ACTIVATED/)
  assert.match(refusalText('CTC_OUTCOME_UNCERTAIN'), /status enquiry/)
  assert.equal(refusalText('SOMETHING_ELSE'), null)
})
