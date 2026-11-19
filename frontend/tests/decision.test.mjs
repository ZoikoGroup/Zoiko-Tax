import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, test } from 'node:test'

import { CODE_MAX, NOTE_MAX, REASON_MAX, buildDecisionRequest, isOntologyCode } from '../src/components/ai/decisionRequest.ts'
import { viewOfDecision } from '../src/components/ai/viewOfDecision.ts'

const PROPOSAL = {
  id: '01920a4d-5a6b-7c8d-9e0f-1a2b3c4d5e6f',
  subjectRef: 'sku:PLAN-UNL-5G',
  proposedCode: 'ontology:telecom/voice/mobile',
  provenance: { invocationId: 'call-id-1' },
}
const form = (over = {}) => ({ action: 'ACCEPT', reason: 'Matches the catalog', note: '', modifiedCode: '', ...over })
const build = (over, proposal = PROPOSAL) => buildDecisionRequest(proposal, form(over))

const MARKER_REASON = 'marker-reason-text'
const MARKER_NOTE = 'marker-note-text'
const apiError = (status) => Object.assign(new Error(`POST failed ${MARKER_REASON} ${MARKER_NOTE}`), { status, reasonCode: 'X' })
const transportError = () => Object.assign(new Error('did not reach the service'), { status: undefined })

describe('building the decision request', () => {
  test('each action builds a request with the proposal fields and a trimmed reason', () => {
    for (const action of ['ACCEPT', 'REJECT', 'ESCALATE']) {
      const r = build({ action, reason: '  because  ' })
      assert.equal(r.ok, true, action)
      assert.deepEqual(r.request, {
        proposalId: PROPOSAL.id, subjectRef: PROPOSAL.subjectRef, proposedCode: PROPOSAL.proposedCode,
        action, reason: 'because', evidenceId: 'call-id-1',
      })
    }
    const m = build({ action: 'MODIFY', modifiedCode: ' ontology:telecom/data/mobile ' })
    assert.equal(m.ok, true)
    assert.equal(m.request.action, 'MODIFY')
    assert.equal(m.request.modifiedCode, 'ontology:telecom/data/mobile')
  })

  test('a blank reason is refused', () => {
    for (const reason of ['', '   ', '\n\t']) {
      const r = build({ reason })
      assert.equal(r.ok, false)
      assert.equal(r.problem, 'REASON_BLANK')
    }
  })

  test('the reason limit is 500 characters: 500 passes, 501 is refused', () => {
    assert.equal(REASON_MAX, 500)
    assert.equal(build({ reason: 'a'.repeat(500) }).ok, true)
    assert.equal(build({ reason: 'a'.repeat(501) }).problem, 'REASON_LONG')
    // counted in characters, as the Go side does, not in bytes
    assert.equal(build({ reason: 'é'.repeat(500) }).ok, true)
  })

  test('the note is optional, trimmed, left out when blank, and limited to 2000', () => {
    assert.equal(NOTE_MAX, 2000)
    assert.equal('note' in build({ note: '   ' }).request, false)
    assert.equal(build({ note: ' why ' }).request.note, 'why')
    assert.equal(build({ note: 'a'.repeat(2000) }).ok, true)
    assert.equal(build({ note: 'a'.repeat(2001) }).problem, 'NOTE_LONG')
  })

  test('MODIFY without a code, or with a code that is not an ontology code, is refused', () => {
    assert.equal(build({ action: 'MODIFY', modifiedCode: '' }).problem, 'CODE_MISSING')
    assert.equal(build({ action: 'MODIFY', modifiedCode: '   ' }).problem, 'CODE_MISSING')
    assert.equal(build({ action: 'MODIFY', modifiedCode: 'telecom/voice' }).problem, 'CODE_BAD')
    assert.equal(build({ action: 'MODIFY', modifiedCode: 'ontology:' }).problem, 'CODE_BAD')
    assert.equal(build({ action: 'MODIFY', modifiedCode: 'ontology:' + 'a'.repeat(CODE_MAX) }).problem, 'CODE_LONG')
    assert.equal(CODE_MAX, 256)
    assert.equal(isOntologyCode('ontology:a'), true)
    assert.equal(isOntologyCode('ontology:'), false)
    assert.equal(isOntologyCode('Ontology:a'), false)
  })

  test('other actions send no modified code, even when one was typed', () => {
    for (const action of ['ACCEPT', 'REJECT', 'ESCALATE']) {
      const r = build({ action, modifiedCode: 'ontology:telecom/data/mobile' })
      assert.equal(r.ok, true)
      assert.equal('modifiedCode' in r.request, false, action)
    }
  })

  test('the evidence id is included only when the helper accepts it', () => {
    assert.equal(build({}).request.evidenceId, 'call-id-1')
    for (const bad of [undefined, '', '  ', 'call id', 'a'.repeat(65), 12345]) {
      const r = build({}, { ...PROPOSAL, provenance: { invocationId: bad } })
      assert.equal('evidenceId' in r.request, false, String(bad))
    }
    assert.equal('evidenceId' in build({}, { ...PROPOSAL, provenance: null }).request, false)
  })

  test('a proposal without an id, subject or code cannot be decided', () => {
    for (const drop of ['id', 'subjectRef', 'proposedCode']) {
      const p = { ...PROPOSAL, [drop]: undefined }
      assert.equal(build({}, p).problem, 'NO_PROPOSAL', drop)
    }
  })

  test('the request never carries a reviewer or tenant', () => {
    const r = build({ action: 'MODIFY', modifiedCode: 'ontology:a/b', note: 'n' })
    const keys = Object.keys(r.request)
    assert.deepEqual(keys.filter((k) => /reviewer|tenant|user|session/i.test(k)), [])
    const src = readFileSync(join(process.cwd(), 'src', 'components', 'ai', 'decisionRequest.ts'), 'utf8')
    assert.doesNotMatch(src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, ''), /reviewer(User)?Id|tenantId|userId/i)
  })

  test('no refusal sentence holds the text the person typed', () => {
    const bad = [
      build({ reason: '   ', note: MARKER_NOTE }),
      build({ reason: 'x'.repeat(501) }),
      build({ reason: MARKER_REASON, note: MARKER_NOTE.repeat(200) }),
      build({ action: 'MODIFY', reason: MARKER_REASON, modifiedCode: MARKER_NOTE }),
      build({ action: 'MODIFY', reason: MARKER_REASON, modifiedCode: '' }),
    ]
    for (const r of bad) {
      assert.equal(r.ok, false)
      assert.ok(!r.sentence.includes('marker-'), r.sentence)
    }
  })
})

describe('how saving ends', () => {
  test('a saved reply (201) is saved and locks the form', () => {
    const v = viewOfDecision({ ok: true })
    assert.equal(v.state, 'saved')
    assert.equal(v.onRecord, true)
    assert.equal(v.locked, true)
    assert.match(v.sentence, /saved with who decided and when/)
  })

  test('409 is a decision already on record, and it locks the form', () => {
    const v = viewOfDecision({ ok: false, error: apiError(409) })
    assert.equal(v.state, 'already')
    assert.equal(v.onRecord, true)
    assert.equal(v.locked, true)
    assert.match(v.sentence, /already on record/)
  })

  test('400, 401, 403, 503 and a transport error each fail with their own sentence and allow a retry', () => {
    const endings = {
      400: apiError(400), 401: apiError(401), 403: apiError(403), 503: apiError(503), transport: transportError(),
    }
    const sentences = new Set()
    for (const [name, error] of Object.entries(endings)) {
      const v = viewOfDecision({ ok: false, error })
      assert.equal(v.state, 'failed', name)
      assert.equal(v.onRecord, false, name)
      assert.equal(v.locked, false, name)
      assert.match(v.sentence, name === 'transport' ? /It may not have been saved/ : /Nothing was saved/, name)
      sentences.add(v.sentence)
    }
    assert.equal(sentences.size, 5)
    assert.match(viewOfDecision({ ok: false, error: endings[400] }).sentence, /Check the fields/)
    assert.match(viewOfDecision({ ok: false, error: endings[401] }).sentence, /Sign in again/)
    assert.match(viewOfDecision({ ok: false, error: endings[403] }).sentence, /operator or an analyst/)
    assert.match(viewOfDecision({ ok: false, error: endings[503] }).sentence, /not available in this cell/)
    assert.equal(
      viewOfDecision({ ok: false, error: endings.transport }).sentence,
      'The service could not be reached or failed. It may not have been saved. If you try again and it says a decision is already on record, it was.',
    )
  })

  test('a transport error, a 500 and any other status say it may not have been saved, and never "Nothing was saved"', () => {
    for (const error of [transportError(), apiError(500), apiError(502), apiError(404), null]) {
      const v = viewOfDecision({ ok: false, error })
      assert.equal(v.headline, 'The decision could not be saved')
      assert.match(v.sentence, /It may not have been saved/)
      assert.ok(!/Nothing was saved/.test(v.sentence), v.sentence)
      assert.equal(v.locked, false)
    }
    for (const status of [400, 401, 403, 503]) {
      assert.match(viewOfDecision({ ok: false, error: apiError(status) }).sentence, /Nothing was saved\.$/, String(status))
    }
  })

  test('only a 201 or a 409 counts as saved: a 500 and a 404 are failures', () => {
    for (const status of [500, 502, 404, 422, 429]) {
      const v = viewOfDecision({ ok: false, error: apiError(status) })
      assert.equal(v.onRecord, false, String(status))
      assert.equal(v.state, 'failed', String(status))
      assert.equal(v.locked, false, String(status))
    }
    assert.equal(viewOfDecision({ ok: false, error: null }).onRecord, false)
    assert.equal(viewOfDecision({ ok: false, error: 'text' }).onRecord, false)
  })

  test('no sentence holds the reason, the note or the error text', () => {
    const endings = [{ ok: true }, ...[400, 401, 403, 409, 500, 503].map((s) => ({ ok: false, error: apiError(s) })), { ok: false, error: transportError() }]
    for (const e of endings) {
      const text = JSON.stringify(viewOfDecision(e))
      assert.ok(!text.includes('marker-'), text)
      assert.ok(!text.includes('POST failed'), text)
    }
  })
})

describe('the card', () => {
  const card = () => readFileSync(join(process.cwd(), 'src', 'components', 'ai', 'ProposalCard.tsx'), 'utf8')

  test('it calls the SDK to save, and builds the request with the helper', () => {
    const src = card()
    assert.match(src, /ztax\.recordClassificationDecision\(built\.request\)/)
    assert.match(src, /buildDecisionRequest\(/)
    assert.match(src, /viewOfDecision\(/)
  })

  test('the Review paragraph says a saved decision does not change the classification', () => {
    assert.match(card().replace(/\s+/g, ' '), /Saving a decision does not change the item&apos;s classification\./)
  })

  test('the ontology sentence says the approved list is checked only when one is configured', () => {
    const src = card().replace(/\s+/g, ' ')
    assert.match(src, /checked for ontology format; whether it is an approved ontology node is checked only when an approved list is configured\./)
    assert.ok(!src.includes('is not verified here. A person confirms'))
  })

  test('the demo wording is gone', () => {
    const src = card()
    assert.ok(!src.includes('Demo only'))
    assert.ok(!src.includes('decision is not saved'))
    assert.ok(!src.includes('nothing below is sent anywhere'))
  })

  test('the buttons are disabled while saving and a second submit is ignored', () => {
    const src = card()
    assert.match(src, /disabled=\{saving\}/)
    assert.match(src, /<Submit busy=\{saving\}>/)
    assert.match(src, /inFlight\.current/)
  })

  test('the form is shown only while no decision is on record', () => {
    assert.match(card(), /onRecord !== null \? null : pending === null/)
  })

  test('the test-runtime badge note is still in the provenance note helper', () => {
    const src = readFileSync(join(process.cwd(), 'src', 'components', 'ai', 'provenanceNote.ts'), 'utf8')
    assert.match(src, /Test runtime: not a real AI answer/)
  })
})
