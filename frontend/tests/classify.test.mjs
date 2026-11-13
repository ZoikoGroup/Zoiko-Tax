import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, mock, test } from 'node:test'

import { DEMO_AI_TIMEOUT_MS } from '../src/platform/demoAi.ts'
import { requestClassify } from '../src/pages/reportsDemo/api.ts'
import { createClassifyRunner } from '../src/pages/reportsDemo/classifyRun.ts'
import * as outcomes from '../src/pages/reportsDemo/outcomes.ts'

const { viewOf, viewOfFailure } = outcomes

const KEYS = (o = {}) => ({
  outcome: 'PROPOSED', verdict: 'PASS', reason_code: 'CHECKS_PASSED', evidence_id: 'evid-1',
  list_version: 'synthetic-demo-v0', confidence_calibrated: false, synthetic: true,
  proposed_code: 'ontology:telecom/voice/mobile', ...o,
})
const PARAMS = { subjectRef: 'sku:DEMO-1', description: 'synthetic demo text' }
const FAILURES = ['rejected', 'not_found', 'timeout', 'network', 'service', 'unreadable']
const json = (status, body) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
const tick = () => new Promise((r) => setImmediate(r))

const realFetch = globalThis.fetch
afterEach(() => {
  globalThis.fetch = realFetch
  mock.timers.reset()
})
const fakeFetch = (fn) => { globalThis.fetch = fn }

describe('texts (outcomes.ts)', () => {
  test('every failure has its own headline and its own sentence', () => {
    const views = FAILURES.map((f) => viewOfFailure(f))
    assert.equal(new Set(views.map((v) => v.headline)).size, FAILURES.length)
    assert.equal(new Set(views.map((v) => v.sentence)).size, FAILURES.length)
    for (const v of views) assert.ok(!v.sentence.includes('undefined') && v.code === null)
  })

  test('every non-success state tells the person what to do next', () => {
    const next = /(try again|press Classify|Ask whoever|A person should check|ask a reviewer)/i
    const all = [
      ...FAILURES.map((f) => viewOfFailure(f)),
      viewOf(KEYS({ outcome: 'ABSTAIN', verdict: 'ABSTAIN', reason_code: 'GUARD_ONTOLOGY_UNKNOWN' })),
      viewOf(KEYS({ outcome: 'REFUSED', verdict: 'BLOCK', reason_code: 'AI_KILL_SWITCH_ENGAGED' })),
      viewOf(KEYS({ outcome: 'REFUSED', verdict: 'BLOCK', reason_code: 'AI_BRAND_NEW' })),
      viewOf(KEYS({ outcome: 'NOT_CONFIGURED', verdict: 'ABSTAIN', reason_code: 'GUARD_ONTOLOGY_NOT_CONFIGURED' })),
      viewOf(KEYS({ outcome: 'SOMETHING_NEW' })),
    ]
    for (const v of all) assert.match(v.sentence, next, v.headline)
  })

  test('the rejected-input text says to shorten; the slow and the down service say to try again', () => {
    assert.match(viewOfFailure('rejected').sentence, /shorten/)
    for (const f of ['timeout', 'network', 'service', 'unreadable']) assert.match(viewOfFailure(f).sentence, /try again/i)
  })

  test('only a service that may answer next time offers a retry', () => {
    for (const f of FAILURES) assert.equal(viewOfFailure(f).canRetry, f !== 'rejected' && f !== 'not_found', f)
    assert.equal(viewOf(KEYS({ outcome: 'SOMETHING_NEW' })).canRetry, false)
    assert.equal(viewOf(KEYS({ outcome: 'REFUSED', verdict: 'BLOCK', reason_code: 'AI_KILL_SWITCH_ENGAGED' })).canRetry, false)
    assert.equal(viewOf(KEYS()).canRetry, false)
  })

  test('a gateway that could not answer is a service problem, never "blocked"', () => {
    const v = viewOf(KEYS({ outcome: 'REFUSED', verdict: 'BLOCK', reason_code: 'AI_GATEWAY_UNAVAILABLE' }))
    assert.equal(v.kind, 'service')
    assert.equal(v.headline, viewOfFailure('service').headline)
    assert.equal(v.canRetry, true)
    const blocked = viewOf(KEYS({ outcome: 'REFUSED', verdict: 'BLOCK', reason_code: 'AI_KILL_SWITCH_ENGAGED' }))
    assert.equal(blocked.kind, 'blocked')
    assert.notEqual(v.sentence, blocked.sentence)
  })

  test('a model that is not set up is "not configured", not "blocked"', () => {
    const v = viewOf(KEYS({ outcome: 'REFUSED', verdict: 'BLOCK', reason_code: 'AI_GATEWAY_NOT_CONFIGURED' }))
    assert.equal(v.kind, 'not_configured')
    assert.match(v.sentence, /AI model is not set up/)
  })

  test('a missing route says so, offers no retry and sends the person to whoever runs the demo', () => {
    const v = viewOfFailure('not_found')
    assert.equal(v.headline, 'Route not found on the AI service')
    assert.equal(v.sentence, 'The AI service does not have this route. Ask whoever runs the demo to check that the service is up to date.')
    assert.equal(v.canRetry, false)
  })

  test('the timeout text names the real limit', () => {
    assert.ok(viewOfFailure('timeout').sentence.includes(`${String(DEMO_AI_TIMEOUT_MS / 1000)} seconds`))
  })

  test('an unknown outcome is "could not be interpreted" and shows no code', () => {
    const v = viewOf(KEYS({ outcome: 'SOMETHING_NEW' }))
    assert.equal(v.kind, 'needs_review')
    assert.equal(v.code, null)
  })
})

describe('what the service sent (api.ts, demoAi.ts)', () => {
  const run = () => requestClassify(PARAMS, new AbortController().signal)

  test('a good reply is a reply', async () => {
    fakeFetch(() => Promise.resolve(json(200, KEYS())))
    assert.equal((await run()).status, 'reply')
  })

  test('HTTP 400 is the only "rejected"', async () => {
    fakeFetch(() => Promise.resolve(json(400, { error: 'subject_ref is too long' })))
    assert.equal((await run()).status, 'rejected')
  })

  test('HTTP 404 is its own ending: the service has no such route', async () => {
    fakeFetch(() => Promise.resolve(json(404, { error: 'not found' })))
    assert.equal((await run()).status, 'not_found')
  })

  for (const status of [401, 403, 413, 429, 500, 502, 503]) {
    test(`HTTP ${String(status)} is a service problem, not a rejected input`, async () => {
      fakeFetch(() => Promise.resolve(json(status, { error: 'x' })))
      assert.equal((await run()).status, 'service')
    })
  }

  test('no answer at all is a network failure', async () => {
    fakeFetch(() => Promise.reject(new TypeError('Failed to fetch')))
    assert.equal((await run()).status, 'network')
  })

  test('a 200 whose body is not JSON is unreadable, not "unavailable"', async () => {
    fakeFetch(() => Promise.resolve(new Response('<html>oops</html>', { status: 200 })))
    assert.equal((await run()).status, 'unreadable')
  })

  test('a 200 whose JSON is not a classify reply is unreadable', async () => {
    for (const body of [{}, [], 'x', null, { outcome: 'PROPOSED' }]) {
      fakeFetch(() => Promise.resolve(json(200, body)))
      assert.equal((await run()).status, 'unreadable', JSON.stringify(body))
    }
  })

  test('a request that outlives the limit is a timeout', async () => {
    mock.timers.enable({ apis: ['setTimeout'] })
    fakeFetch((_url, init) => new Promise((_resolve, reject) => {
      init.signal.addEventListener('abort', () => { reject(new DOMException('aborted', 'AbortError')) })
    }))
    const pending = run()
    mock.timers.tick(DEMO_AI_TIMEOUT_MS)
    assert.equal((await pending).status, 'timeout')
  })

  test('an answer that arrives after the timeout is dropped, even if the transport delivers it', async () => {
    mock.timers.enable({ apis: ['setTimeout'] })
    let deliver = () => {}
    fakeFetch(() => new Promise((resolve) => { deliver = () => { resolve(json(200, KEYS())) } }))
    const pending = run()
    mock.timers.tick(DEMO_AI_TIMEOUT_MS)
    deliver() // the transport ignores the abort and answers late
    assert.equal((await pending).status, 'timeout')
  })

  test('an error status that arrives after the timeout is still a timeout', async () => {
    mock.timers.enable({ apis: ['setTimeout'] })
    let deliver = () => {}
    fakeFetch(() => new Promise((resolve) => { deliver = () => { resolve(json(503, { error: 'x' })) } }))
    const pending = run()
    mock.timers.tick(DEMO_AI_TIMEOUT_MS)
    deliver()
    assert.equal((await pending).status, 'timeout')
  })

  test('a body that is delivered after the timeout is dropped, even if the transport ignores the abort', async () => {
    mock.timers.enable({ apis: ['setTimeout'] })
    let finish = () => {}
    fakeFetch(() => {
      const body = new ReadableStream({
        start(controller) {
          finish = () => { controller.enqueue(new TextEncoder().encode(JSON.stringify(KEYS()))); controller.close() }
        },
      })
      return Promise.resolve(new Response(body, { status: 200 }))
    })
    const pending = run()
    await tick()
    mock.timers.tick(DEMO_AI_TIMEOUT_MS)
    finish() // not an error: the stream simply completes late
    assert.equal((await pending).status, 'timeout')
  })

  test('a body that stalls past the limit is a timeout, not an unreadable reply', async () => {
    mock.timers.enable({ apis: ['setTimeout'] })
    fakeFetch((_url, init) => {
      const body = new ReadableStream({
        start(controller) { init.signal.addEventListener('abort', () => { controller.error(new DOMException('aborted', 'AbortError')) }) },
      })
      return Promise.resolve(new Response(body, { status: 200 }))
    })
    const pending = run()
    await tick()
    mock.timers.tick(DEMO_AI_TIMEOUT_MS)
    assert.equal((await pending).status, 'timeout')
  })
})

describe('one request at a time (classifyRun.ts)', () => {
  const held = () => {
    const calls = []
    const send = (params, signal) => new Promise((resolve) => { calls.push({ params, signal, resolve }) })
    return { calls, send }
  }

  test('two quick submits send one request', () => {
    const { calls, send } = held()
    const runner = createClassifyRunner(send)
    assert.equal(runner.start(PARAMS, () => {}), true)
    assert.equal(runner.busy(), true)
    assert.equal(runner.start(PARAMS, () => {}), false)
    assert.equal(calls.length, 1)
  })

  test('after the answer a new request may start, and it sends the same form', async () => {
    const { calls, send } = held()
    const runner = createClassifyRunner(send)
    const endings = []
    runner.start(PARAMS, (e) => endings.push(e))
    calls[0].resolve({ status: 'timeout' })
    await tick()
    assert.deepEqual(endings, [{ kind: 'failure', failure: 'timeout' }])
    assert.equal(runner.busy(), false)
    assert.equal(runner.start(PARAMS, (e) => endings.push(e)), true)
    assert.deepEqual(calls[1].params, calls[0].params)
  })

  test('an answer is delivered once', async () => {
    const { calls, send } = held()
    const runner = createClassifyRunner(send)
    const endings = []
    runner.start(PARAMS, (e) => endings.push(e))
    calls[0].resolve({ status: 'service' })
    calls[0].resolve({ status: 'network' })
    await tick()
    assert.equal(endings.length, 1)
  })

  test('a late answer after the request was cancelled is dropped', async () => {
    const { calls, send } = held()
    const runner = createClassifyRunner(send)
    const endings = []
    runner.start(PARAMS, (e) => endings.push(e))
    runner.cancel()
    assert.equal(calls[0].signal.aborted, true)
    assert.equal(runner.busy(), false)
    calls[0].resolve({ status: 'reply', reply: KEYS() })
    await tick()
    assert.deepEqual(endings, [])
  })

  test("a cancelled request's late answer never replaces the new request's answer", async () => {
    const { calls, send } = held()
    const runner = createClassifyRunner(send)
    const endings = []
    runner.start(PARAMS, (e) => endings.push(e))
    runner.cancel()
    runner.start(PARAMS, (e) => endings.push(e))
    calls[1].resolve({ status: 'service' })
    calls[0].resolve({ status: 'reply', reply: KEYS() })
    await tick()
    assert.deepEqual(endings, [{ kind: 'failure', failure: 'service' }])
  })

  test('a late reply after a timeout does not turn the timeout into a result', async () => {
    mock.timers.enable({ apis: ['setTimeout'] })
    let deliver = () => {}
    fakeFetch(() => new Promise((resolve) => { deliver = () => { resolve(json(200, KEYS())) } }))
    const runner = createClassifyRunner()
    const endings = []
    runner.start(PARAMS, (e) => endings.push(e))
    mock.timers.tick(DEMO_AI_TIMEOUT_MS)
    deliver()
    await tick()
    assert.deepEqual(endings, [{ kind: 'failure', failure: 'timeout' }])
  })

  test('a reply passes through whole; every failure status maps to its own failure', async () => {
    for (const status of FAILURES) {
      const { calls, send } = held()
      const runner = createClassifyRunner(send)
      const endings = []
      runner.start(PARAMS, (e) => endings.push(e))
      calls[0].resolve({ status })
      await tick()
      assert.deepEqual(endings, [{ kind: 'failure', failure: status }])
    }
    const { calls, send } = held()
    const runner = createClassifyRunner(send)
    const endings = []
    runner.start(PARAMS, (e) => endings.push(e))
    calls[0].resolve({ status: 'reply', reply: KEYS() })
    await tick()
    assert.equal(endings[0].kind, 'reply')
  })

  test('an aborted result that we did not cancel ends as a network failure', async () => {
    const { calls, send } = held()
    const runner = createClassifyRunner(send)
    const endings = []
    runner.start(PARAMS, (e) => endings.push(e))
    calls[0].resolve({ status: 'aborted' })
    await tick()
    assert.deepEqual(endings, [{ kind: 'failure', failure: 'network' }])
    assert.equal(runner.busy(), false)
  })

  test('a send that throws does not leave the runner stuck busy', async () => {
    const runner = createClassifyRunner(() => Promise.reject(new Error('boom')))
    const endings = []
    runner.start(PARAMS, (e) => endings.push(e))
    await tick()
    assert.equal(runner.busy(), false)
    assert.deepEqual(endings, [{ kind: 'failure', failure: 'unreadable' }])
  })
})

describe('the panel holds no text of its own (ClassifyPanel.tsx)', () => {
  // `npm test` runs from frontend/, where the sources live.
  const source = readFileSync(join(process.cwd(), 'src', 'pages', 'reportsDemo', 'ClassifyPanel.tsx'), 'utf8')
  const code = source.replace(/\/\*[\s\S]*?\*\//g, '')
  const NAMES = [
    'BUSY_LABEL', 'SUBMIT_LABEL', 'RETRY_LABEL', 'DATA_NOTE', 'SYNTHETIC_BADGE', 'SYNTHETIC_ONLY_NOTE',
    'LIST_NOTE_BEFORE', 'LIST_NOTE_AFTER', 'EVIDENCE_LABEL', 'EVIDENCE_MISSING', 'CONFIDENCE_NOTE',
  ]

  test('every label and note is a named constant from outcomes.ts with a fixed value', () => {
    for (const name of NAMES) assert.equal(typeof outcomes[name], 'string', name)
    assert.equal(outcomes.RETRY_LABEL, 'Try again')
    assert.equal(outcomes.BUSY_LABEL, 'Classifying…')
    assert.equal(outcomes.CONFIDENCE_NOTE, 'Confidence: not calibrated')
    assert.equal(outcomes.EVIDENCE_MISSING, 'Evidence id not available')
  })

  test('the panel uses each constant and writes none of their text itself', () => {
    const afterImports = code.slice(code.indexOf("from './outcomes'"))
    for (const name of NAMES) {
      assert.ok(afterImports.split(/[^A-Z_]/).includes(name), `${name} is not used`)
      const text = outcomes[name].replace(/^\. /, '').replace(/:$/, '')
      // A short label such as "Classify" is also a prefix of code names, so it is looked for as a literal.
      const written = [`'${text}'`, `"${text}"`, `>${text}`, `${text}<`, `\n${text}`].some((x) => code.includes(x)) ||
        (text.length > 12 && code.includes(text))
      assert.ok(!written, `${name} text is written in the panel`)
    }
  })

  test('no sentence is written between tags (only the heading and two field labels remain)', () => {
    const between = [...code.matchAll(/>([^<>{}]*[A-Za-z]{3,}[^<>{}]*)</g)].map((m) => m[1].trim()).filter(Boolean)
    const allowed = ['Classification with approved-list check (demo)', 'Subject reference', 'Description']
    // `&&` marks a code fragment the pattern caught (an arrow function's `>`), not text.
    assert.deepEqual(between.filter((t) => !t.includes('&&') && !allowed.includes(t)), [])
  })
})
