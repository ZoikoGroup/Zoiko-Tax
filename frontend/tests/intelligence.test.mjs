import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  SLOW_AFTER_MS,
  tookText,
  viewOfProposal,
  viewOfWaiting,
} from '../src/pages/intelligence/viewOfProposal.ts'

const PROVENANCE = { useCase: 'classification-review', modelProfile: 'm', providerProfile: 'p', promptProfile: 'q', aiTrainVersion: '0' }
const proposed = (over = {}) => ({
  kind: 'proposal',
  proposal: { disposition: 'PROPOSED', proposedCode: 'ontology:telecom/voice/mobile', provenance: PROVENANCE, ...over },
})
const reviewRequired = (reviewReason) => ({
  kind: 'proposal',
  proposal: { disposition: 'REVIEW_REQUIRED', reviewReason },
})
const failure = (error) => ({ kind: 'failure', error })
const apiError = (status) => Object.assign(new Error('POST /v1/classifications:propose failed'), { status, reasonCode: 'AI_KILL_SWITCH_ENGAGED' })
const transportError = () => Object.assign(new Error('POST /v1/classifications:propose did not reach the service'), { status: undefined })

describe('the four states', () => {
  test('a proposal with a code and provenance is "proposed" and carries the code', () => {
    const v = viewOfProposal(proposed(), 2500)
    assert.equal(v.state, 'proposed')
    assert.equal(v.code, 'ontology:telecom/voice/mobile')
    assert.match(v.sentence, /person must review/)
    assert.equal(v.elapsed, 'took 2 seconds')
  })

  test('a reply the AI answered but that was set aside is "review", with no code', () => {
    for (const reason of ['AI_GATEWAY_REPLY_UNEVIDENCED', 'AI_PROPOSAL_NOT_AN_ONTOLOGY_NODE']) {
      const v = viewOfProposal(reviewRequired(reason), 800)
      assert.equal(v.state, 'review', reason)
      assert.equal(v.code, null)
    }
  })

  test('a governance refusal (403) is "review": blocked, not down', () => {
    const v = viewOfProposal(failure(apiError(403)), 300)
    assert.equal(v.state, 'review')
    assert.equal(v.code, null)
  })

  test('an AI that is not configured or down is "unavailable", whether it came as a reply or an error', () => {
    for (const reason of ['AI_GATEWAY_NOT_CONFIGURED', 'AI_GATEWAY_UNAVAILABLE']) {
      assert.equal(viewOfProposal(reviewRequired(reason), 1000).state, 'unavailable', reason)
    }
    assert.equal(viewOfProposal(failure(transportError()), 10000).state, 'unavailable')
    assert.equal(viewOfProposal(failure(apiError(503)), 400).state, 'unavailable')
    assert.equal(viewOfProposal(failure('not even an Error'), 400).state, 'unavailable')
    assert.equal(viewOfProposal(failure(null), 400).state, 'unavailable')
  })

  test('still waiting is "slow", and only once the threshold is reached', () => {
    assert.equal(viewOfWaiting(SLOW_AFTER_MS - 1), null)
    const v = viewOfWaiting(SLOW_AFTER_MS)
    assert.equal(v.state, 'slow')
    assert.equal(v.code, null)
    assert.match(v.headline, /Still waiting/)
  })

  test('every state tells the person what to do next', () => {
    const next = /(review|Try again|Classify|keep waiting|classify)/
    const all = [
      viewOfProposal(proposed(), 1),
      viewOfProposal(reviewRequired('AI_PROPOSAL_NOT_AN_ONTOLOGY_NODE'), 1),
      viewOfProposal(failure(transportError()), 1),
      viewOfWaiting(SLOW_AFTER_MS),
    ]
    for (const v of all) assert.match(v.sentence, next, v.state)
  })
})

describe('the time text', () => {
  test('0 ms is "less than a second", and a negative or bad number is too', () => {
    assert.equal(tookText(0), 'took less than a second')
    assert.equal(tookText(-5), 'took less than a second')
    assert.equal(tookText(Number.NaN), 'took less than a second')
  })

  test('one second is singular and the rest are whole seconds, rounded down', () => {
    assert.equal(tookText(999), 'took less than a second')
    assert.equal(tookText(1000), 'took 1 second')
    assert.equal(tookText(1999), 'took 1 second')
    assert.equal(tookText(2000), 'took 2 seconds')
  })

  test('just under and just over the slow threshold', () => {
    assert.equal(tookText(SLOW_AFTER_MS - 1), 'took 4 seconds')
    assert.equal(tookText(SLOW_AFTER_MS), 'took 5 seconds')
    assert.equal(viewOfWaiting(SLOW_AFTER_MS - 1), null)
    assert.equal(viewOfWaiting(SLOW_AFTER_MS + 1).elapsed, '5 seconds so far')
  })

  test('the threshold is below the 10 s the client allows, so the line shows before the call is ended', () => {
    assert.ok(SLOW_AFTER_MS > 0 && SLOW_AFTER_MS < 10000)
  })

  test('every ending carries the time as text', () => {
    assert.equal(viewOfProposal(proposed(), 3200).elapsed, 'took 3 seconds')
    assert.equal(viewOfProposal(failure(transportError()), 10000).elapsed, 'took 10 seconds')
    assert.equal(viewOfProposal(reviewRequired('AI_GATEWAY_UNAVAILABLE'), 0).elapsed, 'took less than a second')
  })
})

describe('only a proposal carries a code, and no raw text leaks', () => {
  test('a proposal that has a code but is not PROPOSED shows no code', () => {
    const v = viewOfProposal({ kind: 'proposal', proposal: { disposition: 'REVIEW_REQUIRED', reviewReason: 'AI_GATEWAY_UNAVAILABLE', proposedCode: 'ontology:x/y' } }, 100)
    assert.equal(v.code, null)
  })

  test('PROPOSED without provenance, or with an empty code, is not shown as proposed', () => {
    assert.equal(viewOfProposal(proposed({ provenance: undefined }), 100).state, 'review')
    assert.equal(viewOfProposal(proposed({ proposedCode: '' }), 100).code, null)
    assert.equal(viewOfProposal(proposed({ proposedCode: undefined }), 100).state, 'review')
  })

  test('an unknown outcome is "review" with no code, never "proposed"', () => {
    for (const proposal of [{ disposition: 'SOMETHING_NEW', proposedCode: 'ontology:a/b', provenance: PROVENANCE }, { disposition: 'SOMETHING_NEW', proposedCode: 'ontology:a/b' }, {}, { disposition: 'REVIEW_REQUIRED', reviewReason: 'AI_BRAND_NEW' }]) {
      const v = viewOfProposal({ kind: 'proposal', proposal }, 100)
      assert.equal(v.state, 'review')
      assert.equal(v.code, null)
    }
  })

  test('no state other than proposed has a code', () => {
    const others = [
      viewOfProposal(reviewRequired('AI_GATEWAY_REPLY_UNEVIDENCED'), 1),
      viewOfProposal(failure(apiError(403)), 1),
      viewOfProposal(failure(transportError()), 1),
      viewOfWaiting(SLOW_AFTER_MS),
    ]
    for (const v of others) assert.equal(v.code, null, v.state)
  })

  test('no headline, sentence or time shows a reason code, an error message or a status', () => {
    const secret = 'POST /v1/classifications:propose'
    const all = [
      viewOfProposal(failure(apiError(403)), 1),
      viewOfProposal(failure(transportError()), 1),
      viewOfProposal(reviewRequired('AI_GATEWAY_UNAVAILABLE'), 1),
      viewOfProposal(reviewRequired('AI_PROPOSAL_NOT_AN_ONTOLOGY_NODE'), 1),
      viewOfWaiting(SLOW_AFTER_MS),
    ]
    for (const v of all) {
      const text = `${v.headline} ${v.sentence} ${v.elapsed}`
      assert.ok(!text.includes(secret) && !/AI_[A-Z_]+/.test(text) && !/\b(403|503)\b/.test(text), text)
    }
  })
})
