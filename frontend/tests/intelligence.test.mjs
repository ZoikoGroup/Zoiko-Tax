import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, test } from 'node:test'

import {
  DESCRIPTION_MAX,
  SUBJECT_REF_MAX,
  SUBJECT_REF_SENTENCE,
  SLOW_AFTER_MS,
  emptyNoteVisible,
  subjectRefNote,
  subjectRefToSend,
  subjectRefTooLong,
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

  test('every state tells the person what to do next, in its own words', () => {
    const cases = [
      [viewOfProposal(proposed(), 1), /A person must review this suggestion/],
      [viewOfProposal(reviewRequired('AI_PROPOSAL_NOT_AN_ONTOLOGY_NODE'), 1), /Classify it by hand/],
      [viewOfProposal(failure(apiError(403)), 1), /ask a reviewer/],
      [viewOfProposal(failure(transportError()), 1), /Try again in a moment/],
      [viewOfProposal(failure(apiError(400)), 1), /Check the fields and try again/],
      [viewOfProposal(failure(apiError(401)), 1), /Sign in again/],
      [viewOfWaiting(SLOW_AFTER_MS), /keep waiting/],
    ]
    for (const [v, phrase] of cases) assert.match(v.sentence, phrase, v.headline)
  })

  test('each state has its own sentence, so one is never used for another', () => {
    const sentences = [
      viewOfProposal(proposed(), 1).sentence,
      viewOfProposal(reviewRequired('AI_PROPOSAL_NOT_AN_ONTOLOGY_NODE'), 1).sentence,
      viewOfProposal(failure(apiError(403)), 1).sentence,
      viewOfProposal(failure(transportError()), 1).sentence,
      viewOfProposal(failure(apiError(400)), 1).sentence,
      viewOfProposal(failure(apiError(401)), 1).sentence,
      viewOfWaiting(SLOW_AFTER_MS).sentence,
    ]
    assert.equal(new Set(sentences).size, sentences.length)
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
    assert.equal(SLOW_AFTER_MS, 3000)
    assert.equal(tookText(SLOW_AFTER_MS - 1), 'took 2 seconds')
    assert.equal(tookText(SLOW_AFTER_MS), 'took 3 seconds')
    assert.equal(viewOfWaiting(SLOW_AFTER_MS - 1), null)
    assert.equal(viewOfWaiting(SLOW_AFTER_MS + 1).elapsed, '3 seconds so far')
  })

  test('the layers keep their order: slow line < Go deadline < screen limit', () => {
    const client = readFileSync(join(process.cwd(), 'src', 'platform', 'client.ts'), 'utf8')
    const limit = /REQUEST_TIMEOUT_MS\s*=\s*([0-9_]+)/.exec(client)
    assert.ok(limit !== null, 'REQUEST_TIMEOUT_MS not found in platform/client.ts')
    assert.match(client, /timeoutMs:\s*REQUEST_TIMEOUT_MS/)
    const limitMs = Number(limit[1].replaceAll('_', ''))
    const goFile = join(process.cwd(), '..', 'backend', 'internal', 'platform', 'config', 'config.go')
    if (existsSync(goFile)) {
      const m = /"ZTAX_AI_GATEWAY_DEADLINE":\s*\{def:\s*"(\d+)s"\}/.exec(readFileSync(goFile, 'utf8'))
      assert.ok(m !== null, 'the Go deadline default was not found in config.go')
      const goMs = Number(m[1]) * 1000
      assert.ok(SLOW_AFTER_MS > 0 && SLOW_AFTER_MS < goMs, `slow line ${SLOW_AFTER_MS} is not below the Go deadline ${goMs}`)
      assert.ok(goMs < limitMs, `Go deadline ${goMs} is not below the screen limit ${limitMs}`)
    }
    assert.ok(SLOW_AFTER_MS < limitMs)
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

describe('a request the service did not accept, and an ended session', () => {
  test('400 and 422 say to check the fields, not that the AI was unreachable', () => {
    for (const status of [400, 422]) {
      const v = viewOfProposal(failure(apiError(status)), 400)
      assert.equal(v.state, 'review', String(status))
      assert.match(v.sentence, /Check the fields and try again/)
      assert.ok(!/could not be reached/i.test(`${v.headline} ${v.sentence}`), String(status))
      assert.equal(v.code, null)
    }
  })

  test('401 says the session ended and to sign in again, not that the AI was unreachable', () => {
    const v = viewOfProposal(failure(apiError(401)), 400)
    assert.equal(v.state, 'unavailable')
    assert.equal(v.headline, 'Your session has ended')
    assert.match(v.sentence, /Your session has ended\. Sign in again\./)
    assert.equal(v.code, null)
  })

  test('other statuses and no status still read as unreachable', () => {
    for (const status of [500, 502, 503, 404, undefined]) {
      assert.equal(viewOfProposal(failure(apiError(status)), 400).headline, 'The AI could not be reached', String(status))
    }
  })

  test('403 is still "not allowed", and not the new sentences', () => {
    const v = viewOfProposal(failure(apiError(403)), 400)
    assert.match(v.sentence, /not allowed/)
  })

  test('the description limit is the service limit (classification.go) and is named in the 400 sentence', () => {
    assert.equal(DESCRIPTION_MAX, 2000)
    assert.match(viewOfProposal(failure(apiError(400)), 1).sentence, new RegExp(String(DESCRIPTION_MAX)))
    const goFile = join(process.cwd(), '..', 'backend', 'internal', 'app', 'classification.go')
    if (existsSync(goFile)) {
      const m = /len\(in\.Description\) > (\d+)/.exec(readFileSync(goFile, 'utf8'))
      assert.ok(m !== null, 'the description limit was not found in classification.go')
      assert.equal(DESCRIPTION_MAX, Number(m[1]))
    }
  })
})

describe('a provenance of null is the same as no provenance', () => {
  test('PROPOSED with provenance null is "review", with no code', () => {
    for (const provenance of [null, undefined]) {
      const v = viewOfProposal(proposed({ provenance }), 100)
      assert.equal(v.state, 'review')
      assert.equal(v.code, null)
    }
  })
})

describe('the "No proposal yet" note', () => {
  test('shows only when nothing is running and there is no result', () => {
    assert.equal(emptyNoteVisible(false, false), true)
  })

  test('never shows while a call is running, so a new call hides an old result and the note', () => {
    assert.equal(emptyNoteVisible(true, false), false)
    assert.equal(emptyNoteVisible(true, true), false)
  })

  test('never shows while a result exists, so it cannot flash between the call ending and the result', () => {
    assert.equal(emptyNoteVisible(false, true), false)
  })
})

describe('the evidence id', () => {
  const withId = { ...PROVENANCE, invocationId: 'call-id-1' }
  test('it never appears in the failure and review-required views', () => {
    const endings = [
      failure(apiError(403)), failure(apiError(400)), failure(apiError(401)), failure(transportError()),
      { kind: 'proposal', proposal: { disposition: 'REVIEW_REQUIRED', reviewReason: 'AI_GATEWAY_UNAVAILABLE', provenance: withId } },
      { kind: 'proposal', proposal: { disposition: 'REVIEW_REQUIRED', reviewReason: 'AI_OUTPUT_REJECTED', provenance: withId } },
    ]
    for (const e of endings) {
      assert.ok(!JSON.stringify(viewOfProposal(e, 3000)).includes('call-id-1'), JSON.stringify(e))
    }
  })

  test('the card shows the Evidence id row only behind the helper check', () => {
    const src = readFileSync(join(process.cwd(), 'src', 'components', 'ai', 'ProposalCard.tsx'), 'utf8')
    const row = src.indexOf('Evidence id')
    assert.ok(row > 0)
    assert.match(src.slice(Math.max(0, row - 200), row), /evidenceId === null \? null/)
  })
})

describe('the subject reference limit', () => {
  const page = () => readFileSync(join(process.cwd(), 'src', 'pages', 'IntelligencePage.tsx'), 'utf8')

  test('the limit is the service limit (classification.go) and the sentence names it', () => {
    assert.equal(SUBJECT_REF_MAX, 512)
    assert.match(SUBJECT_REF_SENTENCE, /512/)
    const goFile = join(process.cwd(), '..', 'backend', 'internal', 'app', 'classification.go')
    if (existsSync(goFile)) {
      const m = /len\(in\.SubjectRef\) > (\d+)/.exec(readFileSync(goFile, 'utf8'))
      assert.ok(m !== null, 'the subject reference limit was not found in classification.go')
      assert.equal(SUBJECT_REF_MAX, Number(m[1]))
    }
  })

  test('exactly at the limit passes, one over fails', () => {
    assert.equal(subjectRefTooLong('a'.repeat(512)), false)
    assert.equal(subjectRefTooLong('a'.repeat(513)), true)
    assert.equal(subjectRefToSend('a'.repeat(512)), 'a'.repeat(512))
    assert.equal(subjectRefToSend('a'.repeat(513)), null)
  })

  test('spaces around the value are not counted, as in the service', () => {
    assert.equal(subjectRefTooLong('  ' + 'a'.repeat(512) + '  '), false)
    assert.equal(subjectRefToSend('  sku:A  '), 'sku:A')
  })

  test('the count is in bytes, as in Go: 257 two-byte characters are over', () => {
    assert.equal(subjectRefTooLong('é'.repeat(256)), false)
    assert.equal(subjectRefTooLong('é'.repeat(257)), true)
  })

  test('300 two-byte characters (600 bytes) are refused, 256 (512 bytes) are accepted', () => {
    const v = 'é'.repeat(300)
    assert.ok(v.length < SUBJECT_REF_MAX)
    assert.equal(subjectRefTooLong(v), true)
    assert.equal(subjectRefNote(v), SUBJECT_REF_SENTENCE)
    assert.equal(subjectRefToSend(v), null)
    // The other side of the boundary: 256 two-byte characters are exactly 512 bytes.
    const edge = 'é'.repeat(256)
    assert.equal(subjectRefTooLong(edge), false)
    assert.equal(subjectRefNote(edge), null)
    assert.equal(subjectRefToSend(edge), edge)
  })

  test('empty and blank are not "too long" and not given a sentence', () => {
    for (const v of ['', '   ']) {
      assert.equal(subjectRefTooLong(v), false)
      assert.equal(subjectRefNote(v), null)
    }
    assert.equal(subjectRefToSend(''), '')
  })

  test('the sentence shows when the limit is reached or passed, not before', () => {
    assert.equal(subjectRefNote('a'.repeat(511)), null)
    assert.equal(subjectRefNote('a'.repeat(512)), SUBJECT_REF_SENTENCE)
    assert.equal(subjectRefNote('a'.repeat(600)), SUBJECT_REF_SENTENCE)
  })

  test('the box has the limit as maxLength, so a paste of 600 is cut as the description is', () => {
    assert.match(page(), /maxLength=\{SUBJECT_REF_MAX\}/)
    assert.match(page(), /maxLength=\{DESCRIPTION_MAX\}/)
  })

  test('submit refuses an over-limit value before any request is made', () => {
    const src = page()
    const guard = src.indexOf('subjectRefToSend(subjectRef)')
    const call = src.indexOf('proposeClassification(')
    assert.ok(guard > 0 && call > guard, 'the limit check must come before the call')
    assert.match(src.slice(guard, call), /if \(ref === null\) return/)
    assert.match(src.slice(call, call + 80), /subjectRef: ref,/)
  })
})

describe('a code that is not approved', () => {
  const notApproved = (extra = {}) => ({
    kind: 'proposal',
    proposal: { disposition: 'REVIEW_REQUIRED', reviewReason: 'AI_PROPOSAL_CODE_NOT_APPROVED', ...extra },
  })

  test('it has its own headline and sentence, as a person\'s review', () => {
    const v = viewOfProposal(notApproved(), 2000)
    const generic = viewOfProposal(reviewRequired('AI_OUTPUT_REJECTED'), 2000)
    assert.equal(v.state, 'review')
    assert.equal(v.headline, 'A person needs to review this item')
    assert.notEqual(v.headline, generic.headline)
    assert.notEqual(v.sentence, generic.sentence)
    assert.match(v.sentence, /A person classifies this item/)
    assert.match(v.sentence, /Nothing was changed/)
  })

  test('it shows no code and offers no retry, and never the reason code or list jargon', () => {
    const v = viewOfProposal(notApproved({ proposedCode: 'ontology:telecom/made-up/node' }), 2000)
    assert.equal(v.code, null)
    const text = `${v.headline} ${v.sentence}`
    assert.ok(!/try again|retry|AI_PROPOSAL|approved list|ontology/i.test(text), text)
  })

  test('no provenance, id or code of the answer is in the view', () => {
    const v = viewOfProposal(notApproved({ provenance: { ...PROVENANCE, invocationId: 'call-id-1' }, proposedCode: 'ontology:x/y' }), 1000)
    assert.ok(!JSON.stringify(v).includes('call-id-1'))
    assert.ok(!JSON.stringify(v).includes('ontology:'))
  })

  test('the other reasons are unchanged', () => {
    assert.equal(viewOfProposal(reviewRequired('AI_GATEWAY_UNAVAILABLE'), 1).state, 'unavailable')
    assert.equal(viewOfProposal(reviewRequired('AI_GATEWAY_NOT_CONFIGURED'), 1).state, 'unavailable')
    const other = viewOfProposal(reviewRequired('AI_GATEWAY_REPLY_UNEVIDENCED'), 1)
    assert.equal(other.headline, 'Set aside for a person to review')
    assert.match(other.sentence, /did not give a usable suggestion/)
  })
})
