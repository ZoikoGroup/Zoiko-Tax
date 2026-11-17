import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { instant, periodLabel } from '../dashboard/format'
import { ztax } from '../platform/client'
import {
  Amount,
  Card,
  Columns,
  Empty,
  ErrorNote,
  errorText,
  Field,
  Id,
  Loaded,
  PageHeader,
  Raw,
  Status,
  Submit,
  useAction,
  useLoad,
} from '../ui/kit'
import { CURRENCY, isDecimal, TabBar } from './determine/shared'
import {
  type Contribution,
  type ContributionStatus,
  downloadSummary,
  isRegime,
  isSourceType,
  isStatus,
  NOT_RECOVERY,
  reasonOf,
  type Regime,
  REGIMES,
  SOURCE_TYPES,
  type SourceType,
  STATUSES,
} from './contributions/shared'
import { useRoutedTab } from './routedTab'
import type { PageProps } from './types'
import { hasRole, Tiles, words } from './workspace'

type Tab = 'events' | 'summary' | 'record'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'events', label: 'Source events' },
  { value: 'summary', label: 'Control totals' },
  { value: 'record', label: 'Record an event' },
]

export const ContributionsPage = ({ session, period, route, onTabChange }: PageProps) => {
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'events', route, onTabChange)
  const [selected, setSelected] = useState<string | null>(null)
  const roles = session.user.roles
  const open = (id: string) => {
    setSelected(id)
    setTab('events')
  }

  return (
    <div className="ztax-page">
      <PageHeader
        title="Provider Contributions"
        subtitle="USF, CRTC NCF and similar fund contributions, from the source events that carry them. Totals are computed exactly by the cell."
      />
      <p className="ztax-note" role="note">{NOT_RECOVERY}</p>
      <TabBar label="Provider contributions" tabs={TABS} current={tab} onSelect={setTab} />
      {tab === 'events' ? <Events period={period} roles={roles} selected={selected} setSelected={setSelected} />
        : tab === 'summary' ? <Summary period={period} />
        : <Record canRecord={hasRole(roles, 'OPERATOR')} onOpen={open} />}
    </div>
  )
}

// ---------------------------------------------------------------------------
// source event explorer
// ---------------------------------------------------------------------------

const Events = ({ period, roles, selected, setSelected }: {
  period: string
  roles: readonly string[]
  selected: string | null
  setSelected: (id: string | null) => void
}) => {
  const [month, setMonth] = useState(period)
  const [regime, setRegime] = useState<Regime | ''>('')
  const [status, setStatus] = useState<ContributionStatus | ''>('')
  const [sourceType, setSourceType] = useState<SourceType | ''>('')
  const { load, reload } = useLoad(
    (signal) => ztax.listContributions({
      ...(month === '' ? {} : { period: month }),
      ...(regime === '' ? {} : { regime }),
      ...(status === '' ? {} : { status }),
      ...(sourceType === '' ? {} : { sourceType }),
      limit: 500,
    }, signal),
    [month, regime, status, sourceType],
  )

  return (
    <>
      <Card
        title="Source events"
        aside={
          <div className="ztax-filter-bar">
            <Field label="Period">
              <input type="month" value={month} onChange={(e) => { setMonth(e.target.value) }} />
            </Field>
            <Field label="Regime">
              <select value={regime} onChange={(e) => { setRegime(isRegime(e.target.value) ? e.target.value : '') }}>
                <option value="">All</option>
                {REGIMES.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
              </select>
            </Field>
            <Field label="Status">
              <select value={status} onChange={(e) => { setStatus(isStatus(e.target.value) ? e.target.value : '') }}>
                <option value="">All</option>
                {STATUSES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
              </select>
            </Field>
            <Field label="Source">
              <select value={sourceType} onChange={(e) => { setSourceType(isSourceType(e.target.value) ? e.target.value : '') }}>
                <option value="">All</option>
                {SOURCE_TYPES.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
              </select>
            </Field>
            <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
          </div>
        }
      >
        <Loaded load={load}>
          {(data) => data.contributions.length === 0 ? (
            <Empty>No source event matches. Events are recorded by an OPERATOR from the Record tab or an import.</Empty>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead>
                  <tr>
                    <th>Source event</th><th>Source</th><th>Regime</th><th>Period</th><th>Eligible</th><th>Excluded</th>
                    <th>Status</th><th>Action</th>
                  </tr>
                </thead>
                <tbody>
                  {data.contributions.map((c) => (
                    <tr key={c.id} className={c.id === selected ? 'ztax-row-selected' : undefined}>
                      <td className="mono" style={{ paddingRight: 8 }}>
                        {c.sourceEventId} v{c.sourceVersion}
                        {c.reverses === undefined ? null : <span className="muted"> reverses {c.reverses.sourceEventId}</span>}
                        {c.amendment ? <span className="muted"> · amendment</span> : null}
                      </td>
                      <td style={{ paddingRight: 8 }}>{words(c.sourceType)}</td>
                      <td style={{ paddingRight: 8 }}>{words(c.regime)}</td>
                      <td style={{ paddingRight: 8 }}>{c.period}</td>
                      <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}><Amount amount={c.eligibleBase} currency={c.currency} /></td>
                      <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}><Amount amount={c.excludedBase} currency={c.currency} /></td>
                      <td style={{ paddingRight: 8 }}><Status value={c.status} /></td>
                      <td>
                        <button type="button" className="ztax-table-action-btn" aria-pressed={c.id === selected}
                          onClick={() => { setSelected(c.id) }}>
                          Trace
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Loaded>
      </Card>
      {selected === null ? null : (
        <Trace key={selected} id={selected} roles={roles} onSelect={setSelected} onMoved={reload}
          onClose={() => { setSelected(null) }} />
      )}
    </>
  )
}

const Trace = ({ id, roles, onSelect, onMoved, onClose }: {
  id: string
  roles: readonly string[]
  onSelect: (id: string) => void
  onMoved: () => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getContributionTrace(id, signal), [id])
  return (
    <Loaded load={load}>
      {(t) => {
        const c = t.contribution
        return (
          <Columns>
            <Card title={`${c.sourceEventId} v${c.sourceVersion}`}
              aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}>
              <dl className="ztax-kv">
                <dt>Contribution</dt><dd><Id value={c.id} /></dd>
                <dt>Status</dt><dd><Status value={c.status} /></dd>
                <dt>Source</dt><dd>{words(c.sourceType)} · <span className="mono">{c.sourceRef}</span></dd>
                <dt>Source checksum</dt><dd><Id value={c.sourceChecksum} /></dd>
                <dt>Regime</dt><dd>{words(c.regime)}</dd>
                <dt>Period</dt><dd>{periodLabel(c.period)}{c.amendment ? <span className="muted"> · recorded late, an amendment</span> : null}</dd>
                <dt>Member entity</dt><dd><Id value={c.legalEntityId} /></dd>
                <dt>Obligation</dt><dd><Id value={c.obligationId} /></dd>
                <dt>Classification</dt><dd className="mono">{c.classificationRef}</dd>
                <dt>Situs</dt><dd className="mono">{c.situsRef}</dd>
                <dt>Component</dt><dd className="mono">{c.componentRef ?? '—'}</dd>
                <dt>Rule version</dt><dd className="mono">{c.ruleVersion}</dd>
                <dt>Eligible base</dt><dd><Amount amount={c.eligibleBase} currency={c.currency} /></dd>
                <dt>Excluded base</dt><dd><Amount amount={c.excludedBase} currency={c.currency} /></dd>
                <dt>Exclusion reason</dt><dd>{c.exclusionReason ?? '—'}</dd>
                <dt>Quantity</dt><dd className="mono">{c.quantity === undefined ? '—' : `${c.quantity} ${c.quantityUnit ?? ''}`}</dd>
                <dt>Immutable hash</dt><dd><Id value={c.immutableHash} /></dd>
              </dl>
              <Raw value={t} />
            </Card>
            <Card title="Links and history">
              <dl className="ztax-kv">
                <dt>Reverses</dt>
                <dd>
                  {c.reverses === undefined ? <span className="muted">— not a reversal</span>
                    : t.original === undefined ? (
                      <span>{c.reverses.sourceEventId} v{c.reverses.sourceVersion} <Status value="MISSING" />
                        <span className="muted"> the original has not arrived; its period is data-incomplete</span></span>
                    ) : (
                      <button type="button" className="ztax-table-action-btn" onClick={() => { onSelect(t.original?.id ?? id) }}>
                        {c.reverses.sourceEventId} v{c.reverses.sourceVersion}
                      </button>
                    )}
                </dd>
                <dt>Reversed by</dt>
                <dd>
                  {t.reversal === undefined ? <span className="muted">— not reversed</span> : (
                    <button type="button" className="ztax-table-action-btn" onClick={() => { onSelect(t.reversal?.id ?? id) }}>
                      {t.reversal.sourceEventId} v{t.reversal.sourceVersion}
                    </button>
                  )}
                </dd>
              </dl>
              <table className="ztax-action-table" style={{ marginTop: 12 }}>
                <thead><tr><th>#</th><th>Event</th><th>Status</th><th>Reason</th><th>When</th><th>By</th></tr></thead>
                <tbody>
                  {c.history.map((e) => (
                    <tr key={e.seq}>
                      <td>{e.seq}</td><td>{words(e.kind)}</td><td><Status value={e.status} /></td>
                      <td>{e.reason ?? '—'}</td><td>{instant(e.recordedAt)}</td><td><Id value={e.recordedBy} /></td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <Move contribution={c} roles={roles} onMoved={() => {
                reload()
                onMoved()
              }} />
            </Card>
          </Columns>
        )
      }}
    </Loaded>
  )
}

const Move = ({ contribution, roles, onMoved }: { contribution: Contribution; roles: readonly string[]; onMoved: () => void }) => {
  const [reason, setReason] = useState('')
  const action = useAction<Contribution>()
  const quarantining = contribution.status === 'ACCEPTED'
  const allowed = quarantining ? hasRole(roles, 'OPERATOR', 'ANALYST') : hasRole(roles, 'ANALYST')
  if (!allowed) {
    return (
      <p className="ztax-note" style={{ marginTop: 12 }}>
        {quarantining ? 'Quarantine needs the OPERATOR or ANALYST role.' : 'Release needs the ANALYST role, and someone other than the person who quarantined it.'}
      </p>
    )
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    const body = { reason: reason.trim() }
    void action.run(() => (quarantining
      ? ztax.quarantineContribution(contribution.id, body)
      : ztax.releaseContribution(contribution.id, body))).then((done) => {
      if (done !== null) {
        setReason('')
        onMoved()
      }
    })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <Field label={quarantining ? 'Quarantine reason' : 'Release reason'}
        help={quarantining
          ? 'Holds the event out of the control totals. Nothing is deleted, and the period shows as data-incomplete.'
          : 'Returns the event to the control totals. The person who quarantined it cannot release it.'}>
        <textarea value={reason} required maxLength={2000} onChange={(e) => { setReason(e.target.value) }} />
      </Field>
      <div className="ztax-form-actions">
        <Submit busy={action.busy}>{quarantining ? 'Quarantine' : 'Release'}</Submit>
      </div>
      {action.error === null ? null : reasonOf(action.error) === 'MAKER_CHECKER' ? (
        <p className="ztax-error-note" role="alert" style={{ gridColumn: '1 / -1' }}>
          You quarantined this event, so another analyst must release it. <span className="mono muted">{errorText(action.error)}</span>
        </p>
      ) : <div style={{ gridColumn: '1 / -1' }}><ErrorNote error={action.error} /></div>}
    </form>
  )
}

// ---------------------------------------------------------------------------
// control totals
// ---------------------------------------------------------------------------

const Summary = ({ period }: { period: string }) => {
  const [month, setMonth] = useState(period)
  const { load, reload } = useLoad((signal) => ztax.getContributionSummary(month, signal), [month])
  return (
    <Loaded load={load}>
      {(s) => (
        <>
          <Tiles tiles={[
            { label: 'Groups', count: s.groups.length },
            { label: 'Accepted events', count: s.groups.reduce((n, g) => n + g.acceptedCount, 0), tone: 'ok' },
            { label: 'Quarantined events', count: s.groups.reduce((n, g) => n + g.quarantinedCount, 0), tone: 'warn' },
            { label: 'Data-incomplete groups', count: s.groups.filter((g) => g.status === 'DATA_INCOMPLETE').length, tone: 'bad' },
          ]} />
          <Card
            title={`Control totals · ${periodLabel(s.period)}`}
            aside={
              <div className="ztax-filter-bar">
                <Field label="Period">
                  <input type="month" value={month} onChange={(e) => { if (e.target.value !== '') setMonth(e.target.value) }} />
                </Field>
                <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
                <button type="button" className="ztax-btn-quiet" disabled={s.groups.length === 0}
                  onClick={() => { downloadSummary(s) }}>
                  Download CSV
                </button>
              </div>
            }
          >
            <p className="ztax-note">
              Per member entity, regime and currency. Reversals count as negatives; quarantined events are counted, never summed.
              {s.dataIncomplete ? ' This period is DATA INCOMPLETE: do not file from these totals yet.' : null}
            </p>
            {s.groups.length === 0 ? <Empty>No contribution is recorded for {periodLabel(s.period)}.</Empty> : (
              <div style={{ overflowX: 'auto' }}>
                <table className="ztax-action-table">
                  <thead>
                    <tr>
                      <th>Entity</th><th>Regime</th><th>Status</th><th>Accepted</th><th>Quarantined</th><th>Reversals</th>
                      <th>Eligible total</th><th>Excluded total</th><th>Reversed</th><th>Period digest</th>
                    </tr>
                  </thead>
                  <tbody>
                    {s.groups.map((g: S<'ContributionPeriodTotals'>) => (
                      <tr key={`${g.legalEntityId}/${g.regime}/${g.currency}`}>
                        <td><Id value={g.legalEntityId} /></td>
                        <td>{words(g.regime)}</td>
                        <td>
                          <Status value={g.status === 'DATA_INCOMPLETE' ? 'DATA_REQUIRED' : 'COMPLETED'} />
                          {g.incompleteReasons.length === 0 ? null : <div className="muted">{g.incompleteReasons.map(words).join(', ')}</div>}
                        </td>
                        <td>{g.acceptedCount}{g.amendmentCount > 0 ? <span className="muted"> ({g.amendmentCount} late)</span> : null}</td>
                        <td>{g.quarantinedCount}</td>
                        <td>{g.reversalCount}</td>
                        <td style={{ whiteSpace: 'nowrap' }}><Amount amount={g.eligibleTotal} currency={g.currency} /></td>
                        <td style={{ whiteSpace: 'nowrap' }}><Amount amount={g.excludedTotal} currency={g.currency} /></td>
                        <td style={{ whiteSpace: 'nowrap' }}><Amount amount={g.reversedTotal} currency={g.currency} /></td>
                        <td><Id value={g.periodDigest} /></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </>
      )}
    </Loaded>
  )
}

// ---------------------------------------------------------------------------
// record
// ---------------------------------------------------------------------------

const VERSION = /^[1-9][0-9]{0,8}$/

const Record = ({ canRecord, onOpen }: { canRecord: boolean; onOpen: (id: string) => void }) => {
  const [form, setForm] = useState({
    sourceEventId: '', sourceVersion: '1', sourceType: 'BSS_INVOICE' as SourceType, sourceChecksum: '',
    regime: 'US_USF' as Regime, period: '', classificationRef: '', situsRef: '', sourceRef: '', componentRef: '',
    ruleVersion: '', currency: 'USD', eligibleBase: '', excludedBase: '0.00', exclusionReason: '',
    reversal: false, reversesId: '', reversesVersion: '1',
  })
  const [invalid, setInvalid] = useState<string | null>(null)
  const [replayed, setReplayed] = useState(false)
  const action = useAction<Contribution>()
  const set = (k: keyof typeof form) => (v: string | boolean) => { setForm((f) => ({ ...f, [k]: v })) }

  if (!canRecord) {
    return <Card><p className="ztax-note">Recording a contribution needs the OPERATOR role.</p></Card>
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setInvalid(null)
    setReplayed(false)
    if (!VERSION.test(form.sourceVersion) || (form.reversal && !VERSION.test(form.reversesVersion))) {
      setInvalid('A version is a whole number, 1 or more.')
      return
    }
    if (!CURRENCY.test(form.currency) || !isDecimal(form.eligibleBase) || !isDecimal(form.excludedBase)) {
      setInvalid('Amounts are decimal strings such as 1250.00 (negative for a reversal), in an ISO currency such as USD.')
      return
    }
    // Only fields with a value are sent: an absent field and an empty one are different requests.
    const opt = <K extends string>(k: K, s: string): Partial<Record<K, string>> =>
      (s.trim() === '' ? {} : ({ [k]: s.trim() } as Record<K, string>))
    const body: S<'ContributionRequest'> = {
      sourceEventId: form.sourceEventId.trim(), sourceVersion: Number.parseInt(form.sourceVersion, 10),
      sourceType: form.sourceType, ...opt('sourceChecksum', form.sourceChecksum), regime: form.regime, period: form.period,
      classificationRef: form.classificationRef.trim(), situsRef: form.situsRef.trim(), sourceRef: form.sourceRef.trim(),
      ...opt('componentRef', form.componentRef), ruleVersion: form.ruleVersion.trim(), currency: form.currency,
      eligibleBase: form.eligibleBase, excludedBase: form.excludedBase, ...opt('exclusionReason', form.exclusionReason),
      ...(form.reversal
        ? { reverses: { sourceEventId: form.reversesId.trim(), sourceVersion: Number.parseInt(form.reversesVersion, 10) } }
        : {}),
    }
    // A retry is safe without a key: the source event and version are the
    // record's identity, so the same payload returns the record already made.
    void action.run(async () => {
      const result = await ztax.recordContribution(body)
      if (result.ok) {
        // 200 and 201 carry the same body, and the SDK does not surface the
        // status: a record made well before this request is the replay.
        setReplayed(result.data.history.length > 1 || Date.parse(result.data.recordedAt) < Date.now() - 60_000)
      }
      return result
    })
  }

  const text = (k: keyof typeof form, label: string, help?: string, required = true) => (
    <Field label={label} {...(help === undefined ? {} : { help })}>
      <input value={String(form[k])} required={required} onChange={(e) => { set(k)(e.target.value) }} />
    </Field>
  )

  return (
    <Columns>
      <Card title="Record a source event">
        <form className="ztax-form" onSubmit={submit}>
          {text('sourceEventId', 'Source event id', 'Its id in the billing, usage or GL system.')}
          {text('sourceVersion', 'Version')}
          <Field label="Source type">
            <select value={form.sourceType} onChange={(e) => { if (isSourceType(e.target.value)) set('sourceType')(e.target.value) }}>
              {SOURCE_TYPES.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
            </select>
          </Field>
          {text('sourceChecksum', 'Source file checksum', 'Optional.', false)}
          <Field label="Regime">
            <select value={form.regime} onChange={(e) => { if (isRegime(e.target.value)) set('regime')(e.target.value) }}>
              {REGIMES.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
            </select>
          </Field>
          <Field label="Period">
            <input type="month" required value={form.period} onChange={(e) => { set('period')(e.target.value) }} />
          </Field>
          {text('classificationRef', 'Classification')}
          {text('situsRef', 'Situs (jurisdiction)')}
          {text('sourceRef', 'Invoice, usage or GL reference')}
          {text('componentRef', 'Component', 'Optional.', false)}
          {text('ruleVersion', 'Rule or obligation version')}
          {text('currency', 'Currency')}
          {text('eligibleBase', 'Eligible base', 'A decimal string, e.g. 1250.00.')}
          {text('excludedBase', 'Excluded base')}
          {text('exclusionReason', 'Exclusion reason', 'Required when the excluded base is not zero.', false)}
          <Field label="This is a reversal">
            <input type="checkbox" checked={form.reversal} onChange={(e) => { set('reversal')(e.target.checked) }} />
          </Field>
          {form.reversal ? (
            <>
              {text('reversesId', 'Reverses source event', 'Bases are negative; the original is never edited.')}
              {text('reversesVersion', 'Reverses version')}
            </>
          ) : null}
          <div className="ztax-form-actions">
            <Submit busy={action.busy}>Record</Submit>
          </div>
          {invalid === null ? null : <p className="ztax-error-note" role="alert" style={{ gridColumn: '1 / -1' }}>{invalid}</p>}
          {action.error === null ? null : (
            <div style={{ gridColumn: '1 / -1' }}>
              {reasonOf(action.error) === 'SOURCE_EVENT_CONFLICT' ? (
                <p className="ztax-error-note" role="alert">
                  This source event and version are already recorded with a different payload. The first record stands —
                  send a new version, or a reversal. <span className="mono muted">{errorText(action.error)}</span>
                </p>
              ) : reasonOf(action.error) === 'ALREADY_EXISTS' ? (
                <p className="ztax-error-note" role="alert">
                  That original is already reversed. <span className="mono muted">{errorText(action.error)}</span>
                </p>
              ) : <ErrorNote error={action.error} />}
            </div>
          )}
        </form>
      </Card>
      <Card title="Outcome">
        {action.done === null ? <Empty>Nothing recorded yet in this session.</Empty> : (
          <>
            <p className="ztax-note">
              {replayed
                ? 'This source event was already recorded with the same payload; the original record is shown, nothing was counted twice.'
                : 'Recorded.'}
            </p>
            <dl className="ztax-kv">
              <dt>Contribution</dt><dd><Id value={action.done.id} /></dd>
              <dt>Status</dt><dd><Status value={action.done.status} /></dd>
              <dt>Eligible</dt><dd><Amount amount={action.done.eligibleBase} currency={action.done.currency} /></dd>
              <dt>Amendment</dt><dd>{action.done.amendment ? 'yes — recorded after its period' : 'no'}</dd>
              <dt>Immutable hash</dt><dd><Id value={action.done.immutableHash} /></dd>
            </dl>
            <button type="button" className="ztax-btn-quiet" onClick={() => { if (action.done !== null) onOpen(action.done.id) }}>
              Trace it
            </button>
          </>
        )}
      </Card>
    </Columns>
  )
}
