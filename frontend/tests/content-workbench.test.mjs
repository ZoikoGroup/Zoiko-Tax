import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { crosswalkOffers, parseEntries, refusalText, ruleChangeOffers } from '../src/pages/content/model.ts'

const MAKER = 'u-maker'
const CHECKER = 'u-checker'
const THIRD = 'u-third'
const offer = (offers, act) => offers.find((o) => o.act === act)

const version = (o = {}) => ({
  version: 1, status: 'IN_REVIEW', revision: 2, createdBy: MAKER, entries: [],
  history: [{ seq: 1, kind: 'CREATED', recordedBy: MAKER }, { seq: 2, kind: 'SUBMITTED', recordedBy: MAKER }], ...o,
})

const change = (o = {}) => ({
  id: 'c1', status: 'PROPOSED', revision: 1, createdBy: MAKER, citation: { authorityClass: 'A0' }, history: [], ...o,
})

describe('crosswalk offers', () => {
  test('the author is told a different person approves, rather than shown a dead button', () => {
    assert.match(offer(crosswalkOffers(version(), ['ANALYST'], MAKER), 'APPROVED').blocked, /different person/)
    assert.equal(offer(crosswalkOffers(version(), ['ANALYST'], CHECKER), 'APPROVED').blocked, null)
    assert.match(offer(crosswalkOffers(version(), ['OPERATOR'], CHECKER), 'APPROVED').blocked, /ANALYST/)
  })
  test('a superseded version admits nothing', () => {
    assert.deepEqual(crosswalkOffers(version({ status: 'SUPERSEDED' }), ['ANALYST'], CHECKER), [])
  })
})

describe('rule change offers', () => {
  test('the proposer cannot review their own change, and evidence comes first', () => {
    assert.match(offer(ruleChangeOffers(change(), ['ANALYST'], MAKER), 'RECOMMENDED').blocked, /conflict of interest/)
    assert.match(offer(ruleChangeOffers(change(), ['ANALYST'], CHECKER), 'RECOMMENDED').blocked, /test evidence/)
    assert.equal(offer(ruleChangeOffers(change({ testEvidence: { ref: 'GS-1' } }), ['ANALYST'], CHECKER), 'RECOMMENDED').blocked, null)
  })
  test('the recommender does not approve, and only A0/A1 is approvable', () => {
    const rec = change({ status: 'RECOMMENDED', recommendedBy: CHECKER, testEvidence: { ref: 'GS-1' } })
    assert.match(offer(ruleChangeOffers(rec, ['ANALYST'], CHECKER), 'APPROVED_FOR_RELEASE').blocked, /independent/)
    assert.equal(offer(ruleChangeOffers(rec, ['ANALYST'], THIRD), 'APPROVED_FOR_RELEASE').blocked, null)
    const weak = { ...rec, citation: { authorityClass: 'B1' } }
    assert.match(offer(ruleChangeOffers(weak, ['ANALYST'], THIRD), 'APPROVED_FOR_RELEASE').blocked, /A0 or A1/)
  })
  test('an approved change admits nothing more here: release is elsewhere', () => {
    assert.deepEqual(ruleChangeOffers(change({ status: 'APPROVED_FOR_RELEASE' }), ['ANALYST'], THIRD), [])
  })
})

describe('entries editor', () => {
  test('parses codes, targets and predicates', () => {
    const { entries, error } = parseEntries('010101 | Local | EXACT | ontology:telecom/voice/fixed/local\n# comment\n99 | Adj | no_equivalent\n05 | Prepaid | PARTIAL | ontology:x | retail')
    assert.equal(error, null)
    assert.equal(entries.length, 3)
    assert.equal(entries[1].serviceTarget, undefined)
    assert.equal(entries[2].predicate, 'retail')
  })
  test('refuses a match without a canonical target', () => {
    assert.match(parseEntries('01 | Local | EXACT | SCC-1').error, /ontology/)
    assert.match(parseEntries('').error, /at least one/)
  })
  test('explains the content reason codes', () => {
    assert.match(refusalText('CROSSWALK_UNRESOLVED_AMBIGUITY'), /predicate/)
    assert.match(refusalText('SOURCE_AUTHORITY_INSUFFICIENT'), /A0 or A1/)
  })
})
