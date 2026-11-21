import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { describe, test } from 'node:test'

import { evidenceIdText } from '../src/components/ai/evidenceId.ts'
import { provenanceKind } from '../src/components/ai/provenanceKind.ts'
import { TEST_RUNTIME_TEXT, UNKNOWN_SOURCE_TEXT, provenanceNote } from '../src/components/ai/provenanceNote.ts'
import { acceptedCode, canRecordDecision, reviewerActions } from '../src/components/ai/reviewDecision.ts'

describe('provenanceKind', () => {
  test('the fake runtime ids (registry.dev.json) are "test"', () => {
    assert.equal(provenanceKind({ modelProfile: 'model:fake-v1', providerProfile: 'provider:fake' }), 'test')
    assert.equal(provenanceKind({ modelProfile: 'model:fake-v1' }), 'test')
    assert.equal(provenanceKind({ providerProfile: 'provider:fake' }), 'test')
  })

  test('the live demo ids are "model", with or without the model name after the prefix', () => {
    assert.equal(provenanceKind({ modelProfile: 'model:groq-live-demo', providerProfile: 'provider:groq-demo' }), 'model')
    assert.equal(provenanceKind({ modelProfile: 'model:groq-live-demo:some-model-1', providerProfile: 'provider:groq-demo' }), 'model')
  })

  test('other ids are "unknown", and so is no id at all', () => {
    assert.equal(provenanceKind({ modelProfile: 'model:classifier-v2', providerProfile: 'provider:eu-hosted' }), 'unknown')
    assert.equal(provenanceKind({ modelProfile: 'model:self-hosted-draft', providerProfile: 'provider:self-hosted-draft' }), 'unknown')
    assert.equal(provenanceKind({}), 'unknown')
  })

  test('empty strings are "unknown"', () => {
    assert.equal(provenanceKind({ modelProfile: '', providerProfile: '' }), 'unknown')
    assert.equal(provenanceKind({ modelProfile: undefined, providerProfile: undefined }), 'unknown')
  })

  test('an id that only contains the word by accident is not "test"', () => {
    for (const modelProfile of ['model:unfaked-v1', 'model:groq-fake-news', 'model:fakery', 'model:notfake-v1']) {
      assert.equal(provenanceKind({ modelProfile, providerProfile: 'provider:eu-hosted' }), 'unknown', modelProfile)
    }
    assert.equal(provenanceKind({ modelProfile: 'model:classifier', providerProfile: 'provider:fakery' }), 'unknown')
    assert.equal(provenanceKind({ modelProfile: 'model:classifier', providerProfile: 'provider:fake-it' }), 'unknown')
  })

  test('a "model" id is never claimed from the word groq alone', () => {
    assert.equal(provenanceKind({ modelProfile: 'model:groq-live-demo-x', providerProfile: 'provider:groq-demo' }), 'unknown')
    assert.equal(provenanceKind({ modelProfile: 'model:groq-live-demo', providerProfile: 'provider:other' }), 'unknown')
    assert.equal(provenanceKind({ modelProfile: 'model:groq', providerProfile: 'provider:groq-demo' }), 'unknown')
  })

  test('when a fake id and a live id disagree, the fake one wins', () => {
    assert.equal(provenanceKind({ modelProfile: 'model:groq-live-demo', providerProfile: 'provider:fake' }), 'test')
    assert.equal(provenanceKind({ modelProfile: 'model:fake-v1', providerProfile: 'provider:groq-demo' }), 'test')
  })
})

describe('the reviewer decision rules', () => {
  test('only Accept hands a code to onAccept', () => {
    const code = 'ontology:telecom/voice/mobile'
    assert.equal(acceptedCode('ACCEPT', code), code)
    for (const action of ['REJECT', 'MODIFY', 'ESCALATE']) assert.equal(acceptedCode(action, code), null, action)
  })

  test('Accept with no code hands nothing over', () => {
    assert.equal(acceptedCode('ACCEPT', undefined), null)
    assert.equal(acceptedCode('ACCEPT', ''), null)
  })

  test('the four actions are the ones the screen offers', () => {
    assert.deepEqual(reviewerActions.map((a) => a.kind), ['ACCEPT', 'REJECT', 'MODIFY', 'ESCALATE'])
  })

  test('a decision needs a reason; Modify also needs a code; nothing without an action', () => {
    assert.equal(canRecordDecision(null, 'why', ''), false)
    assert.equal(canRecordDecision('ACCEPT', '', ''), false)
    assert.equal(canRecordDecision('ACCEPT', '   ', ''), false)
    assert.equal(canRecordDecision('ACCEPT', 'why', ''), true)
    assert.equal(canRecordDecision('REJECT', 'why', ''), true)
    assert.equal(canRecordDecision('MODIFY', 'why', ''), false)
    assert.equal(canRecordDecision('MODIFY', 'why', '  '), false)
    assert.equal(canRecordDecision('MODIFY', 'why', 'ontology:a/b'), true)
  })
})

describe('provenanceNote', () => {
  test('a test runtime gets the clear badge text', () => {
    assert.deepEqual(provenanceNote('test'), { tone: 'badge', text: 'Test runtime: not a real AI answer' })
    assert.equal(TEST_RUNTIME_TEXT, 'Test runtime: not a real AI answer')
  })

  test('an unknown source gets a quiet note, not the badge', () => {
    const n = provenanceNote('unknown')
    assert.equal(n.tone, 'quiet')
    assert.match(n.text, /could not be confirmed/)
    assert.equal(n.text, UNKNOWN_SOURCE_TEXT)
  })

  test('a model says nothing: the ids prove no more than the table shows', () => {
    assert.equal(provenanceNote('model'), null)
  })

  test('test and unknown are never swapped', () => {
    assert.notEqual(provenanceNote('test').tone, provenanceNote('unknown').tone)
    assert.notEqual(provenanceNote('test').text, provenanceNote('unknown').text)
    assert.ok(!/could not be confirmed/.test(provenanceNote('test').text))
    assert.ok(!/not a real AI answer/.test(provenanceNote('unknown').text))
  })

  test('the note follows the ids end to end', () => {
    const noteFor = (ids) => provenanceNote(provenanceKind(ids))
    assert.equal(noteFor({ modelProfile: 'model:fake-v1', providerProfile: 'provider:fake' }).tone, 'badge')
    assert.equal(noteFor({ modelProfile: 'model:classifier', providerProfile: 'provider:eu-hosted' }).tone, 'quiet')
    assert.equal(noteFor({ modelProfile: 'model:groq-live-demo', providerProfile: 'provider:groq-demo' }), null)
  })
})

describe('evidenceIdText', () => {
  test('a valid id is returned as it is', () => {
    assert.equal(evidenceIdText({ invocationId: 'call-id-1' }), 'call-id-1')
  })

  test('spaces around the id are trimmed', () => {
    assert.equal(evidenceIdText({ invocationId: '  call-id-1 	' }), 'call-id-1')
  })

  test('a 64-character id is kept', () => {
    assert.equal(evidenceIdText({ invocationId: 'a'.repeat(64) }), 'a'.repeat(64))
  })

  test('no provenance, no field, empty or blank gives null', () => {
    assert.equal(evidenceIdText(undefined), null)
    assert.equal(evidenceIdText(null), null)
    assert.equal(evidenceIdText({}), null)
    assert.equal(evidenceIdText({ invocationId: '' }), null)
    assert.equal(evidenceIdText({ invocationId: '   ' }), null)
  })

  test('a value that is not a string gives null', () => {
    assert.equal(evidenceIdText({ invocationId: 12345 }), null)
    assert.equal(evidenceIdText({ invocationId: null }), null)
    assert.equal(evidenceIdText({ invocationId: ['call-id-1'] }), null)
  })

  test('a wrong shape gives null: a space, a slash, other marks', () => {
    assert.equal(evidenceIdText({ invocationId: 'call id' }), null)
    assert.equal(evidenceIdText({ invocationId: 'call/id' }), null)
    assert.equal(evidenceIdText({ invocationId: 'call_id' }), null)
    assert.equal(evidenceIdText({ invocationId: '<b>x</b>' }), null)
  })

  test('a 65-character id gives null', () => {
    assert.equal(evidenceIdText({ invocationId: 'a'.repeat(65) }), null)
  })
})

// Text saved in one encoding and read in another turns an ellipsis, dash or
// quote into a run of odd characters. The sequences below are written as
// escapes so that this file stays plain ASCII.
describe('source text encoding', () => {
  const GARBLED = [
    ['a broken ellipsis, dash or quote (U+00E2 U+20AC)', /\u00e2\u20ac/],
    ['a broken accent (U+00C3 then a symbol or letter)', /\u00c3[\u0080-\u00ff]/],
    ['a stray U+00C2 before a space or symbol', /\u00c2[ \u00a0-\u00bf]/],
    ['the replacement character (U+FFFD)', /\ufffd/],
  ]
  const sources = (dir) => readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = join(dir, e.name)
    return e.isDirectory() ? sources(p) : /\.(ts|tsx|css)$/.test(e.name) ? [p] : []
  })

  test('no .ts, .tsx or .css file under src holds garbled characters', () => {
    const found = []
    for (const file of sources(join(process.cwd(), 'src'))) {
      readFileSync(file, 'utf8').split('\n').forEach((line, i) => {
        for (const [what, re] of GARBLED) {
          if (re.test(line)) found.push(`${file.slice(process.cwd().length + 1)}:${String(i + 1)} ${what}`)
        }
      })
    }
    assert.deepEqual(found, [], `garbled characters:\n${found.join('\n')}`)
  })
})
