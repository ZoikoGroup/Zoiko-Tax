import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, test } from 'node:test'

import { AI_LABEL } from '../src/components/ai/aiLabel.ts'

import { SLOW_AFTER_MS } from '../src/pages/intelligence/viewOfProposal.ts'
import { viewOfExplanation, viewOfExplanationWaiting } from '../src/pages/obligations/viewOfExplanation.ts'

const ROW = '01920a4d-1b2c-7d3e-8f40-5a6b7c8d9e01'
const TEXT = 'Synthetic explanation text.'
const MARKER = 'raw-error-marker-text'

const reply = (over = {}) => ({
  kind: 'reply',
  reply: {
    obligationId: ROW, status: 'ANSWERED', explanation: TEXT, citedIds: [ROW], authoritative: false,
    provenance: { useCase: 'explain-obligation-row', invocationId: 'call-id-1' }, ...over,
  },
})
const review = (reviewReason) => ({ kind: 'reply', reply: { obligationId: ROW, status: 'REVIEW_REQUIRED', reviewReason, citedIds: [], authoritative: false } })
const apiError = (status, reasonCode = 'X_CODE') =>
  ({ kind: 'failure', error: Object.assign(new Error(`POST failed ${MARKER}`), { status, reasonCode }) })
const transport = (extra = {}) =>
  ({ kind: 'failure', error: Object.assign(new Error(`did not reach ${MARKER}`), { status: undefined, ...extra }) })
const view = (ending, ms = 2000) => viewOfExplanation(ending, ms, ROW)

const ROW_SENTENCE = 'A person should read the row.'

describe('the endings and their sentences', () => {
  const cases = [
    ['answered', reply(), 'answered', 'Read it against the row. The figures come from the register, not from the AI.'],
    ['abstained', { kind: 'reply', reply: { status: 'ABSTAINED', citedIds: [] } }, 'abstained', `The AI set this aside. ${ROW_SENTENCE}`],
    ['review: AI unreachable', review('AI_GATEWAY_UNAVAILABLE'), 'review', 'Try again in a moment. If it keeps happening, a person should read the row.'],
    ['review: AI not set up', review('AI_GATEWAY_NOT_CONFIGURED'), 'review', `The AI is not set up in this cell. ${ROW_SENTENCE}`],
    ['review: answer without its record', review('AI_GATEWAY_REPLY_UNEVIDENCED'), 'review', `The AI answered without its record, so the answer was not used. ${ROW_SENTENCE}`],
    ['review: answer not usable', review('AI_EXPLANATION_REPLY_UNUSABLE'), 'review', `The AI answered, but the answer could not be used. ${ROW_SENTENCE}`],
    ['review: any other reason', review('SOMETHING_ELSE'), 'review', `The AI did not give a usable explanation. ${ROW_SENTENCE}`],
    ['review: no reason', review(undefined), 'review', `The AI did not give a usable explanation. ${ROW_SENTENCE}`],
    ['refused by the AI rules (data class)', apiError(403, 'AI_DATA_CLASS_REFUSED'), 'refused', `An AI explanation is not available for this item yet. ${ROW_SENTENCE}`],
    ['refused by the AI rules (kill switch)', apiError(403, 'AI_KILL_SWITCH_ENGAGED'), 'refused', `An AI explanation is not available for this item yet. ${ROW_SENTENCE}`],
    ['403 for a wrong role', apiError(403, 'FORBIDDEN'), 'failed', 'Only an operator or an analyst can ask for an explanation. Ask one of them.'],
    ['404', apiError(404, 'NOT_FOUND'), 'failed', 'This obligation could not be found. Refresh the list and try again.'],
    ['400', apiError(400, 'INVALID_VALUE'), 'failed', 'The request was not accepted. Refresh the list and try again.'],
    ['401', apiError(401, 'UNAUTHENTICATED'), 'failed', 'Your session has ended. Sign in again.'],
    ['503', apiError(503, 'UNAVAILABLE'), 'failed', `Explanations are not available in this cell. ${ROW_SENTENCE}`],
    ['timeout', transport({ cause: new Error('timed out after 10000ms') }), 'failed', 'The AI did not answer in time. Try again, or a person should read the row.'],
    ['network down', transport({ cause: new TypeError('Failed to fetch') }), 'failed', 'This page could not reach the service. Check your connection and try again.'],
    ['a reply that cannot be read (not JSON)', transport({ status: 200, cause: new SyntaxError('x') }), 'failed', 'The service sent a reply this page could not read. Try again.'],
    ['a reply that cannot be read (a proxy page)', transport({ status: 502 }), 'failed', 'The service sent a reply this page could not read. Try again.'],
    ['a reply that is not an object', { kind: 'reply', reply: 'text' }, 'failed', 'The service sent a reply this page could not read. Try again.'],
    ['a service problem', apiError(500, 'INTERNAL_ERROR'), 'failed', 'The service had a problem. Try again in a moment.'],
  ]

  for (const [name, ending, state, sentence] of cases) {
    test(`${name}: its own sentence`, () => {
      const v = view(ending)
      assert.equal(v.state, state)
      assert.equal(v.sentence, sentence)
      assert.ok(v.headline.length > 0)
    })
  }

  test('the slow line: nothing before SLOW_AFTER_MS, a line at it, and the limit above it', () => {
    assert.equal(viewOfExplanationWaiting(SLOW_AFTER_MS - 1), null)
    const v = viewOfExplanationWaiting(SLOW_AFTER_MS)
    assert.equal(v.state, 'slow')
    assert.equal(v.sentence, 'The AI is taking longer than usual. You can keep waiting, or close this and read the row.')
    assert.equal(v.elapsed, '3 seconds so far')
    assert.equal(viewOfExplanationWaiting(Number.NaN), null)
    const client = readFileSync(join(process.cwd(), 'src', 'platform', 'client.ts'), 'utf8')
    const limit = Number(/REQUEST_TIMEOUT_MS\s*=\s*([\d_]+)/.exec(client)?.[1]?.replaceAll('_', ''))
    assert.equal(limit, 10000)
    assert.ok(SLOW_AFTER_MS > 0 && SLOW_AFTER_MS < limit)
  })

  test('every kind of ending has its own sentence', () => {
    // 21 cases, 17 sentences: the same sentence is shared only by the cases that
    // are the same ending (any other reason or none; the two AI refusals; the three unreadable replies).
    assert.equal(new Set(cases.map(([, e]) => view(e).sentence)).size, 17)
  })

  test('only an ending that can succeed next time offers a retry', () => {
    const retry = (e) => view(e).retry
    for (const e of [review('AI_GATEWAY_UNAVAILABLE'), review('AI_GATEWAY_REPLY_UNEVIDENCED'), review('AI_EXPLANATION_REPLY_UNUSABLE'),
      transport({ cause: new Error('timed out after 10000ms') }), transport({}), transport({ status: 502 }), apiError(500, 'INTERNAL_ERROR')]) {
      assert.equal(retry(e), true)
    }
    for (const e of [reply(), { kind: 'reply', reply: { status: 'ABSTAINED' } }, review('AI_GATEWAY_NOT_CONFIGURED'), review('OTHER'),
      apiError(403, 'AI_DATA_CLASS_REFUSED'), apiError(403, 'FORBIDDEN'), apiError(404), apiError(400), apiError(401), apiError(503)]) {
      assert.equal(retry(e), false)
    }
  })
})

describe('what the screen may show', () => {
  const everyEnding = () => [
    reply(), { kind: 'reply', reply: { status: 'ABSTAINED', reason: MARKER } },
    review('AI_GATEWAY_UNAVAILABLE'), review('AI_GATEWAY_NOT_CONFIGURED'), review('AI_GATEWAY_REPLY_UNEVIDENCED'),
    review('AI_EXPLANATION_REPLY_UNUSABLE'), review('AI_PROPOSAL_X'), review(undefined),
    apiError(403, 'AI_DATA_CLASS_REFUSED'), apiError(403, 'FORBIDDEN'), apiError(404, 'NOT_FOUND'), apiError(400, 'INVALID_VALUE'),
    apiError(401, 'UNAUTHENTICATED'), apiError(503, 'UNAVAILABLE'), apiError(500, 'INTERNAL_ERROR'),
    transport({ cause: new Error('timed out after 10000ms') }), transport({}), transport({ status: 502 }),
    { kind: 'reply', reply: null }, { kind: 'failure', error: null }, { kind: 'failure', error: 'text' },
  ]

  test('no ending shows a raw code, a status word or error text', () => {
    for (const ending of everyEnding()) {
      const v = view(ending)
      const shown = `${v.headline} ${v.sentence} ${v.elapsed}`
      assert.ok(!shown.includes(MARKER), shown)
      assert.ok(!/[A-Z]{2,}_[A-Z_]+/.test(shown), shown)
      assert.ok(!/\b(ANSWERED|ABSTAINED|REVIEW_REQUIRED|REFUSED|FORBIDDEN|NOT_FOUND|UNAVAILABLE)\b/.test(shown), shown)
      assert.ok(!/\b(AI_|X_CODE)/.test(shown), shown)
    }
    const slow = viewOfExplanationWaiting(9000)
    assert.ok(!/[A-Z]{2,}_[A-Z_]+/.test(`${slow.headline} ${slow.sentence}`))
  })

  test('the explanation is in the output only when the status is exactly ANSWERED', () => {
    for (const status of ['ABSTAINED', 'REVIEW_REQUIRED', 'REFUSED', 'PASSED', 'answered', ' ANSWERED', 'ANSWERED ', '', undefined, null, 7]) {
      const v = view(reply({ status, reviewReason: 'AI_GATEWAY_UNAVAILABLE' }))
      assert.equal(v.explanation, null, String(status))
      assert.deepEqual(v.citedIds, [], String(status))
      assert.equal(v.evidenceId, null, String(status))
      assert.ok(!JSON.stringify(v).includes(TEXT), String(status))
    }
    const answered = view(reply())
    assert.equal(answered.explanation, TEXT)
    assert.deepEqual(answered.citedIds, [ROW])
  })

  test('an answer that is not whole gives no text', () => {
    const broken = [
      { explanation: undefined }, { explanation: '' }, { explanation: '   ' }, { explanation: 7 },
      { citedIds: undefined }, { citedIds: [] }, { citedIds: 'x' }, { citedIds: [7] }, { citedIds: ['bad id'] },
      { citedIds: [ROW, '01920a4d-1b2c-7d3e-8f40-5a6b7c8d9e02'] }, { citedIds: ['01920a4d-1b2c-7d3e-8f40-5a6b7c8d9e02'] },
    ]
    for (const over of broken) {
      const v = view(reply(over))
      assert.equal(v.explanation, null, JSON.stringify(over))
      assert.equal(v.state, 'review', JSON.stringify(over))
      assert.equal(v.sentence, `The AI answered, but the answer could not be used. ${ROW_SENTENCE}`)
    }
  })

  test('the evidence id is shown only when it is valid, and only with an answer', () => {
    assert.equal(view(reply()).evidenceId, 'call-id-1')
    for (const bad of [undefined, '', '  ', 'call id', 'a'.repeat(65), 12345, null]) {
      const v = view(reply({ provenance: { invocationId: bad } }))
      assert.equal(v.evidenceId, null, String(bad))
      assert.equal(v.explanation, TEXT)
    }
    assert.equal(view(reply({ provenance: null })).evidenceId, null)
    assert.equal(view(reply({ provenance: undefined })).evidenceId, null)
    assert.equal(view({ kind: 'reply', reply: { status: 'ABSTAINED', provenance: { invocationId: 'call-id-1' } } }).evidenceId, null)
  })

  test('a word such as "constructor" is not a known reason', () => {
    assert.equal(view(review('constructor')).sentence, `The AI did not give a usable explanation. ${ROW_SENTENCE}`)
    assert.equal(view(review('__proto__')).sentence, `The AI did not give a usable explanation. ${ROW_SENTENCE}`)
  })
})

describe('the panel and the page', () => {
  const read = (...parts) => readFileSync(join(process.cwd(), 'src', ...parts), 'utf8')
  const code = (src) => src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '')
  const panel = () => code(read('pages', 'obligations', 'ObligationExplainPanel.tsx'))
  const page = () => code(read('pages', 'ObligationsPage.tsx'))

  test('the panel prints no status, reason code or error text itself', () => {
    const src = panel()
    assert.ok(!/reasonCode|reviewReason|\.status\b|\.message\b|errorText|ErrorNote/.test(src))
    assert.ok(!/\{(result|ending|error|reply)[.}]/.test(src))
    assert.match(src, /<strong>|view\.headline/)
  })

  test('neither the panel nor the page imports the demo service', () => {
    for (const src of [panel(), page(), code(read('pages', 'obligations', 'viewOfExplanation.ts'))]) {
      assert.ok(!/demoAi/.test(src))
    }
  })

  test('nothing under pages/obligations imports from reportsDemo', () => {
    const dir = join(process.cwd(), 'src', 'pages', 'obligations')
    const files = readdirSync(dir).filter((f) => /\.(ts|tsx)$/.test(f))
    assert.ok(files.length >= 2, 'no source files found under pages/obligations')
    for (const f of files) {
      const src = readFileSync(join(dir, f), 'utf8')
      assert.ok(!/from\s+['"][^'"]*reportsDemo/.test(src), `${f} imports from reportsDemo`)
    }
  })

  test('the AI label is one shared text, unchanged, used by both panels', () => {
    assert.equal(
      AI_LABEL,
      'AI-generated explanation. General information, not tax advice or an authoritative determination. ' +
      'The figures come from the deterministic register. The wording can be wrong: check it against the cited rows.',
    )
    assert.match(panel(), /from '\.\.\/\.\.\/components\/ai\/aiLabel'/)
    assert.match(read('pages', 'reportsDemo', 'ExplainPanel.tsx'), /from '\.\.\/\.\.\/components\/ai\/aiLabel'/)
    assert.ok(!/export const AI_LABEL/.test(read('pages', 'reportsDemo', 'ExplainPanel.tsx')))
  })

  test('the panel calls the SDK method and cancels the request when it goes away', () => {
    const src = panel()
    assert.match(src, /ztax\.explainObligation\(obligationId, controller\.signal\)/)
    assert.match(src, /return \(\) => \{[\s\S]*?controller\.abort\(\)[\s\S]*?\}\n\s*\}, \[obligationId, attempt\]\)/)
    // A cancelled request is dropped before it can touch the screen.
    assert.ok(src.indexOf('controller.signal.aborted') !== -1 && src.indexOf('controller.signal.aborted') < src.indexOf('setPhase({ kind: \'done\''))
  })

  test('the panel takes focus, closes with Escape, and announces its status in one region', () => {
    const src = panel()
    assert.match(src, /panelRef\.current\?\.focus\(\)/)
    assert.match(src, /e\.key === 'Escape'/)
    assert.equal((src.match(/role="status"/g) ?? []).length, 1)
    assert.match(src, /aria-live="polite"/)
  })

  test('the page puts the control on each row, for operators and analysts only, and returns focus on close', () => {
    const src = page()
    assert.match(src, /roles\.some\(\(r\) => r === 'OPERATOR' \|\| r === 'ANALYST'\)/)
    assert.match(src, /canExplain \? \(/)
    assert.match(src, /openers\.current\.get\(returnFocus\.current\)\?\.focus\(\)/)
    assert.match(src, /explaining === o\.id \? \(/)
    assert.match(read('pages', 'ObligationsPage.tsx').replace(/\s+/g, ' '), /Asking the AI to explain an obligation needs the Operator or Analyst role\./)
  })

  test('the panel shows the AI label and the explanation only from the view', () => {
    const src = panel()
    assert.match(src, /\{AI_LABEL\}/)
    assert.match(src, /view\.explanation === null \? null/)
  })
})
