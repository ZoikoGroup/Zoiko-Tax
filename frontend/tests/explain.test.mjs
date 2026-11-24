import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, test } from 'node:test'

import { explainOutcomeWords } from '../src/pages/reportsDemo/outcomes.ts'

const FALLBACK = 'The AI could not explain this. A person should check it.'

describe('explain outcome in plain words', () => {
  test('each known outcome gives its exact words, and EXPLAINED needs no heading', () => {
    assert.equal(explainOutcomeWords('EXPLAINED'), null)
    assert.equal(explainOutcomeWords('ABSTAINED'), 'The AI set this aside. A person should check it.')
    assert.equal(explainOutcomeWords('REVIEW_REQUIRED'), 'A person should review this.')
  })

  test('an unknown word, an empty value and no value give the fallback', () => {
    for (const odd of ['SOMETHING_NEW', 'explained', 'constructor', '__proto__', '', '   ', undefined, null, 7, {}]) {
      assert.equal(explainOutcomeWords(odd), FALLBACK, String(odd))
    }
  })

  test('no returned sentence holds an underscore or an outcome word in capitals', () => {
    const inputs = ['ABSTAINED', 'REVIEW_REQUIRED', 'SOMETHING_NEW', '', undefined]
    for (const input of inputs) {
      const words = explainOutcomeWords(input)
      assert.ok(words !== null, String(input))
      assert.ok(!words.includes('_'), words)
      assert.ok(!/\b[A-Z]{4,}\b/.test(words), words)
      assert.ok(!words.includes(String(input)) || input === '' || input === undefined, words)
    }
  })
})

describe('the explain panel source', () => {
  const src = () => readFileSync(join(process.cwd(), 'src', 'pages', 'reportsDemo', 'ExplainPanel.tsx'), 'utf8')
  const code = () => src().replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '')

  test("it does not turn the outcome into words itself with replaceAll('_'", () => {
    assert.ok(!/replaceAll\(\s*['"]_/.test(code()))
  })

  test('the raw outcome is only ever handed to explainOutcomeWords, never printed', () => {
    const c = code()
    const uses = c.match(/reply\.outcome/g) ?? []
    const handed = c.match(/explainOutcomeWords\(reply\.outcome\)/g) ?? []
    assert.equal(uses.length, handed.length)
    assert.ok(handed.length >= 1)
    assert.ok(!/\{\s*reply\.outcome/.test(c))
  })

  test('it shows the plain words from the function', () => {
    assert.match(code(), /<strong>\{words\}<\/strong>/)
  })
})
