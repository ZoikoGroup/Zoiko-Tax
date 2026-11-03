import { useEffect, useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../../platform/client'
import { Card, Empty, Outcome, Submit, useAction } from '../../ui/kit'
import { Invalid } from '../determine/shared'
import { type Coverage, type Service } from './coverage'
import {
  CURRENCY, DeterminationCard, emptyLocation, LocationFields, locationDocument, locationEventTime, locationProblem,
  type LocationDraft,
} from './document'

export interface Preset {
  readonly country: string
  readonly subdivision: string
  readonly service: Service
  /** Changes on every preset, so the same state picked twice still applies. */
  readonly nonce: number
}

/**
 * Quote one line at a place of supply the person picks, and show what the
 * engine decided: where, under which regime, and each component with its
 * citation. A quote records nothing and is never authoritative.
 */
export const TryLocationTab = ({ coverage, preset }: { coverage: Coverage; preset: Preset | null }) => {
  const [draft, setDraft] = useState<LocationDraft>(emptyLocation)
  const [problem, setProblem] = useState<string | null>(null)
  const quote = useAction<S<'Quote'>>()

  useEffect(() => {
    if (preset === null) return
    setDraft((d) => ({
      ...d,
      country: preset.country,
      subdivision: preset.subdivision,
      service: preset.service,
      currency: CURRENCY[preset.country] ?? d.currency,
      evidence: preset.service === 'EQUIPMENT_SALE' ? 'SERVICE_ADDRESS' : d.evidence,
    }))
    quote.reset()
  }, [preset, quote.reset])

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const p = locationProblem(draft)
    setProblem(p)
    if (p !== null) return
    void quote.run(() => ztax.createQuote({ eventTime: locationEventTime(draft), document: locationDocument(draft) }))
  }

  return (
    <>
      <Card title="Try a location">
        <p className="muted zp-intro">
          Quotes one line against the active content. A quote is an estimate: it records nothing and is never
          authoritative. To record a decision at a location, commit it from Determine.
        </p>
        <form className="ztax-form loc-form" onSubmit={onSubmit} aria-label="Quote a line at a location">
          <LocationFields coverage={coverage} draft={draft} onChange={setDraft} />
          <Invalid message={problem} />
          <div className="ztax-form-actions">
            <Submit busy={quote.busy}>Quote</Submit>
          </div>
          <Outcome error={quote.error} done={null} />
        </form>
      </Card>

      {quote.done === null ? null : quote.done.document === undefined ? (
        <Card title="Result"><Empty>The quote carried no location-aware determination.</Empty></Card>
      ) : (
        <DeterminationCard determination={quote.done.document} />
      )}
    </>
  )
}
