import { useCallback, useEffect, useState } from 'react'

import { instant } from '../dashboard/format'
import {
  ApiError,
  EVENT_TYPES,
  api,
  describeError,
  type DeliveryStatus,
  type EventType,
  type Webhook,
  type WebhookDelivery,
  type WebhookDeliveryDetail,
  type WebhookSecret,
  type WebhookStatus,
} from '../platform/api'
import { Confirm } from './Confirm'

/**
 * Integrations & APIs: the tenant's webhook subscriptions, which events leave
 * the cell and to where.
 *
 * Reads are ADMIN's and AUDITOR's; every change is ADMIN's. A signing secret
 * arrives once — in the response that created the subscription or rotated its
 * secret — and is held only in this component's state until the person
 * dismisses it. It is never written to browser storage (C8), never logged,
 * and cannot be fetched again: losing it means rotating.
 */

/** A refusal for want of a role, said as that rather than as a fault. */
function problemText(cause: unknown, need: string): string {
  if (cause instanceof ApiError && cause.isForbidden) {
    return `Your roles do not include this. It needs ${need}.`
  }
  return describeError(cause)
}

function useRead<T>(read: () => Promise<T>, need: string): {
  data: T | null
  error: string
  loading: boolean
  reload: () => void
} {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const reload = useCallback(() => {
    setLoading(true)
    read()
      .then((result) => { setData(result); setError('') })
      .catch((cause: unknown) => { setError(problemText(cause, need)) })
      .finally(() => { setLoading(false) })
  }, [read, need])
  useEffect(reload, [reload])
  return { data, error, loading, reload }
}

const STATUS_TAG: Record<WebhookStatus | DeliveryStatus, string> = {
  ACTIVE: 'tag tag--ok',
  PAUSED: 'tag',
  DISABLED: 'tag tag--warn',
  PENDING: 'tag',
  DELIVERED: 'tag tag--ok',
  DEAD: 'tag tag--warn',
}

const shortEvent = (e: EventType) => e.replace('com.zoikotax.', '')

export function IntegrationsPanel({ canManage, canRead }: { canManage: boolean; canRead: boolean }) {
  if (!canRead) {
    return (
      <section className="panel" aria-labelledby="webhooks-heading">
        <h2 id="webhooks-heading">Webhooks</h2>
        <p className="panel__note">
          Webhook subscriptions are visible to the ADMIN and AUDITOR roles, and changed only by ADMIN. Your roles
          include neither.
        </p>
      </section>
    )
  }
  return <Webhooks canManage={canManage} />
}

function Webhooks({ canManage }: { canManage: boolean }) {
  const read = useCallback(() => api.listWebhooks(), [])
  const { data, error, loading, reload } = useRead(read, 'the ADMIN or AUDITOR role')
  const [selected, setSelected] = useState<string | null>(null)
  const [actionError, setActionError] = useState('')
  const [secret, setSecret] = useState<{ webhook: Webhook; secret: WebhookSecret; note: string } | null>(null)

  const act = (work: Promise<unknown>) => {
    setActionError('')
    work
      .then(() => { reload() })
      .catch((cause: unknown) => { setActionError(problemText(cause, 'the ADMIN role')) })
  }

  const hooks = data?.webhooks ?? []
  const current = hooks.find((h) => h.id === selected)

  return (
    <>
      <section className="panel" aria-labelledby="webhooks-heading">
        <h2 id="webhooks-heading">Webhooks</h2>
        <p className="panel__note">
          Each subscription names the event types it receives — there is no wildcard — and an HTTPS endpoint at a
          public address. Deliveries are signed under the Standard Webhooks scheme and are at least once: deduplicate
          on <span className="mono">webhook-id</span>.
        </p>

        {secret === null ? null : (
          <SecretOnce
            secret={secret.secret}
            webhook={secret.webhook}
            note={secret.note}
            onDismiss={() => { setSecret(null) }}
          />
        )}

        {canManage && (
          <CreateWebhook
            onCreated={(created) => {
              setSecret({
                webhook: created.webhook,
                secret: created.secret,
                note: 'The subscription is active. Configure your receiver with this secret before events arrive.',
              })
              setSelected(created.webhook.id)
              reload()
            }}
          />
        )}

        {error || actionError ? <p className="field__error" role="alert">{error || actionError}</p> : null}
        {loading && data === null ? <p className="muted" aria-live="polite">Loading webhooks…</p> : null}
        {data !== null && hooks.length === 0 ? <p className="muted">No webhook subscriptions.</p> : null}

        {hooks.length > 0 && (
          <div style={{ overflowX: 'auto' }}>
            <table className="table">
              <caption className="table__caption">Subscriptions, oldest first</caption>
              <thead>
                <tr>
                  <th scope="col">Endpoint</th>
                  <th scope="col">Events</th>
                  <th scope="col">Status</th>
                  <th scope="col">Since</th>
                  <th scope="col">Actions</th>
                </tr>
              </thead>
              <tbody>
                {hooks.map((h) => (
                  <tr key={h.id} className={h.id === selected ? 'row--current' : undefined}>
                    <td>
                      <span className="mono" style={{ wordBreak: 'break-all' }}>{h.url}</span>
                      {h.description === undefined ? null : <div className="muted">{h.description}</div>}
                    </td>
                    <td className="mono">{h.eventTypes.map(shortEvent).join(', ')}</td>
                    <td><span className={STATUS_TAG[h.status]}>{h.status}</span></td>
                    <td>{instant(h.statusChangedAt)}</td>
                    <td>
                      <div className="confirm__actions">
                        <button
                          type="button"
                          className="btn"
                          aria-pressed={h.id === selected}
                          onClick={() => { setSelected(h.id) }}
                        >
                          Deliveries
                        </button>
                        {canManage && (
                          <WebhookActions
                            webhook={h}
                            onStatus={(status) => { act(api.setWebhookStatus(h.id, status)) }}
                            onRotate={() => {
                              setActionError('')
                              api
                                .rotateWebhookSecret(h.id)
                                .then((rotation) => {
                                  setSecret({
                                    webhook: h,
                                    secret: rotation.secret,
                                    note: `Until ${instant(rotation.previousRetiresAt)} every delivery is signed with both this secret and the previous one, so the receiver can switch at its own pace.`,
                                  })
                                })
                                .catch((cause: unknown) => { setActionError(problemText(cause, 'the ADMIN role')) })
                            }}
                          />
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {current === undefined ? null : (
        <Deliveries key={current.id} webhook={current} canManage={canManage} onClose={() => { setSelected(null) }} />
      )}
    </>
  )
}

function WebhookActions({ webhook, onStatus, onRotate }: {
  webhook: Webhook
  onStatus: (status: WebhookStatus) => void
  onRotate: () => void
}) {
  if (webhook.status === 'DISABLED') {
    return <span className="muted">Disabled for good</span>
  }
  return (
    <>
      {webhook.status === 'ACTIVE' ? (
        <Confirm
          action="Pause"
          effect={
            <>
              Stop deliveries to {webhook.url}. Events committed while paused are not delivered on resume, and
              deliveries already scheduled are dead-lettered; replay is how the receiver catches up.
            </>
          }
          onConfirm={() => { onStatus('PAUSED') }}
        />
      ) : (
        <Confirm
          action="Resume"
          effect={<>Deliver new events to {webhook.url} again. Nothing missed while paused is sent automatically.</>}
          onConfirm={() => { onStatus('ACTIVE') }}
        />
      )}
      <Confirm
        action="Rotate secret"
        effect={
          <>
            Issue a new signing secret, shown once. For 24 hours deliveries carry signatures under both the new and the
            previous secret; rotating again inside that window retires the oldest at once.
          </>
        }
        onConfirm={onRotate}
      />
      <Confirm
        action="Disable"
        severe
        effect={<>Disable this subscription permanently. A disabled subscription can never be resumed.</>}
        onConfirm={() => { onStatus('DISABLED') }}
      />
    </>
  )
}

/**
 * The secret, shown this once. It lives in React state only; dismissing it
 * drops the last reference.
 */
function SecretOnce({ secret, webhook, note, onDismiss }: {
  secret: WebhookSecret
  webhook: Webhook
  note: string
  onDismiss: () => void
}) {
  const [copied, setCopied] = useState<'no' | 'yes' | 'failed'>('no')
  return (
    <div className="subpanel" role="status" aria-live="polite">
      <h3>Signing secret, version {secret.version}</h3>
      <p className="panel__note">
        For <span className="mono">{webhook.url}</span>. <strong>This is the only time it is shown.</strong> The cell
        stores it sealed and will not return it again; if it is lost, rotate it. {note}
      </p>
      <p className="mono" style={{ wordBreak: 'break-all', userSelect: 'all' }}>{secret.secret}</p>
      <div className="confirm__actions">
        <button
          type="button"
          className="btn btn--primary"
          onClick={() => {
            navigator.clipboard
              .writeText(secret.secret)
              .then(() => { setCopied('yes') })
              .catch(() => { setCopied('failed') })
          }}
        >
          {copied === 'yes' ? 'Copied' : 'Copy secret'}
        </button>
        <button type="button" className="btn" onClick={onDismiss}>I have stored it — hide</button>
      </div>
      {copied === 'failed' ? (
        <p className="field__error" role="alert">The browser refused clipboard access. Select the secret and copy it.</p>
      ) : null}
    </div>
  )
}

function CreateWebhook({ onCreated }: { onCreated: (created: { webhook: Webhook; secret: WebhookSecret }) => void }) {
  const [open, setOpen] = useState(false)
  const [url, setUrl] = useState('')
  const [events, setEvents] = useState<EventType[]>([])
  const [description, setDescription] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  if (!open) {
    return (
      <button type="button" className="btn btn--primary" onClick={() => { setOpen(true) }}>
        Add webhook
      </button>
    )
  }

  return (
    <form
      className="subpanel"
      onSubmit={(event) => {
        event.preventDefault()
        if (events.length === 0) {
          setError('Choose at least one event type.')
          return
        }
        setBusy(true)
        setError('')
        api
          .createWebhook({
            url: url.trim(),
            eventTypes: events,
            ...(description.trim() === '' ? {} : { description: description.trim() }),
          })
          .then((created) => {
            setOpen(false)
            setUrl(''); setEvents([]); setDescription('')
            onCreated(created)
          })
          .catch((cause: unknown) => { setError(problemText(cause, 'the ADMIN role')) })
          .finally(() => { setBusy(false) })
      }}
    >
      <h3>Add webhook</h3>
      <div className="field">
        <label htmlFor="webhook-url">Endpoint URL</label>
        <input
          id="webhook-url"
          type="url"
          value={url}
          required
          maxLength={2048}
          placeholder="https://hooks.example.com/ztax"
          aria-describedby="webhook-url-help"
          onChange={(e) => { setUrl(e.target.value) }}
        />
        <p className="field__help" id="webhook-url-help">
          HTTPS, at a public address, with no credentials in it. An address inside the cell&apos;s network is refused.
        </p>
      </div>
      <fieldset className="field">
        <legend>Event types</legend>
        <div className="roles">
          {EVENT_TYPES.map((t) => (
            <label key={t} className={events.includes(t) ? 'chip chip--on' : 'chip'}>
              <input
                type="checkbox"
                checked={events.includes(t)}
                onChange={() => {
                  setEvents((cur) => (cur.includes(t) ? cur.filter((x) => x !== t) : [...cur, t]))
                }}
              />
              {shortEvent(t)}
            </label>
          ))}
        </div>
      </fieldset>
      <div className="field">
        <label htmlFor="webhook-description">Description (optional)</label>
        <input
          id="webhook-description"
          value={description}
          maxLength={500}
          onChange={(e) => { setDescription(e.target.value) }}
        />
      </div>
      <p className="field__error" role="alert" aria-live="polite">{error}</p>
      <div className="confirm__actions">
        <button type="submit" className="btn btn--primary" disabled={busy}>
          {busy ? 'Creating…' : 'Create webhook'}
        </button>
        <button type="button" className="btn" onClick={() => { setOpen(false) }}>Cancel</button>
      </div>
    </form>
  )
}

const FILTERS: readonly { value: '' | DeliveryStatus; label: string }[] = [
  { value: '', label: 'All' },
  { value: 'PENDING', label: 'Pending' },
  { value: 'DELIVERED', label: 'Delivered' },
  { value: 'DEAD', label: 'Dead-lettered' },
]

function Deliveries({ webhook, canManage, onClose }: { webhook: Webhook; canManage: boolean; onClose: () => void }) {
  const [filter, setFilter] = useState<'' | DeliveryStatus>('')
  const read = useCallback(
    () => api.listWebhookDeliveries(webhook.id, filter === '' ? undefined : filter),
    [webhook.id, filter],
  )
  const { data, error, loading, reload } = useRead(read, 'the ADMIN or AUDITOR role')
  const [open, setOpen] = useState<string | null>(null)
  const deliveries = data?.deliveries ?? []

  return (
    <section className="panel" aria-labelledby="deliveries-heading">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8 }}>
        <h2 id="deliveries-heading">Deliveries</h2>
        <div className="confirm__actions">
          <button type="button" className="btn" onClick={reload}>Refresh</button>
          <button type="button" className="btn" onClick={onClose}>Close</button>
        </div>
      </div>
      <p className="panel__note">
        To <span className="mono">{webhook.url}</span>, newest first. The body sent is not kept here; it is the event,
        which the receiver has.
      </p>
      <div className="field">
        <label htmlFor="delivery-filter">Show</label>
        <select
          id="delivery-filter"
          value={filter}
          onChange={(e) => {
            const v = FILTERS.find((f) => f.value === e.target.value)?.value ?? ''
            setFilter(v)
            setOpen(null)
          }}
        >
          {FILTERS.map((f) => <option key={f.value} value={f.value}>{f.label}</option>)}
        </select>
      </div>

      {error ? <p className="field__error" role="alert">{error}</p> : null}
      {loading && data === null ? <p className="muted" aria-live="polite">Loading deliveries…</p> : null}
      {data !== null && deliveries.length === 0 ? <p className="muted">No deliveries.</p> : null}

      {deliveries.length > 0 && (
        <div style={{ overflowX: 'auto' }}>
          <table className="table">
            <caption className="table__caption">Deliveries of this subscription</caption>
            <thead>
              <tr>
                <th scope="col">Created</th>
                <th scope="col">Event</th>
                <th scope="col">Status</th>
                <th scope="col">Attempts</th>
                <th scope="col">Delivered / next</th>
                <th scope="col">Actions</th>
              </tr>
            </thead>
            <tbody>
              {deliveries.map((d) => (
                <tr key={d.id} className={d.id === open ? 'row--current' : undefined}>
                  <td>{instant(d.createdAt)}</td>
                  <td>
                    <span className="mono">{shortEvent(d.eventType)}</span>
                    <div className="mono muted" title={d.eventId}>{d.eventId}</div>
                    {d.replayOf === undefined ? null : <div className="muted">replay of <span className="mono">{d.replayOf}</span></div>}
                  </td>
                  <td><span className={STATUS_TAG[d.status]}>{d.status}</span></td>
                  <td>{d.attempts}</td>
                  <td>
                    {d.deliveredAt !== undefined
                      ? instant(d.deliveredAt)
                      : d.nextAttemptAt !== undefined ? `next ${instant(d.nextAttemptAt)}` : '—'}
                  </td>
                  <td>
                    <button type="button" className="btn" aria-pressed={d.id === open} onClick={() => { setOpen(d.id) }}>
                      Detail
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {open === null ? null : (
        <DeliveryDetail
          key={open}
          webhook={webhook}
          deliveryId={open}
          canReplay={canManage}
          onReplayed={(d) => { setFilter(''); reload(); setOpen(d.id) }}
        />
      )}
    </section>
  )
}

function DeliveryDetail({ webhook, deliveryId, canReplay, onReplayed }: {
  webhook: Webhook
  deliveryId: string
  canReplay: boolean
  onReplayed: (d: WebhookDelivery) => void
}) {
  const read = useCallback(() => api.getWebhookDelivery(webhook.id, deliveryId), [webhook.id, deliveryId])
  const { data, error, loading } = useRead<WebhookDeliveryDetail>(read, 'the ADMIN or AUDITOR role')
  const [replayError, setReplayError] = useState('')

  return (
    <div className="subpanel">
      <h3>Delivery <span className="mono">{deliveryId}</span></h3>
      {error ? <p className="field__error" role="alert">{error}</p> : null}
      {loading && data === null ? <p className="muted" aria-live="polite">Loading delivery…</p> : null}
      {data === null ? null : (
        <>
          <p>
            <span className={STATUS_TAG[data.delivery.status]}>{data.delivery.status}</span>{' '}
            <span className="mono">{data.delivery.eventType}</span> · event <span className="mono">{data.delivery.eventId}</span>
          </p>
          {data.attempts.length === 0 ? <p className="muted">No attempt yet.</p> : (
            <table className="table">
              <caption className="table__caption">Attempts. The receiver&apos;s response body is never kept.</caption>
              <thead>
                <tr>
                  <th scope="col">#</th>
                  <th scope="col">Started</th>
                  <th scope="col">Duration</th>
                  <th scope="col">Receiver status</th>
                  <th scope="col">Transport error</th>
                </tr>
              </thead>
              <tbody>
                {data.attempts.map((a) => (
                  <tr key={a.attempt}>
                    <td>{a.attempt}</td>
                    <td>{instant(a.startedAt)}</td>
                    <td className="mono">{a.durationMs} ms</td>
                    <td className="mono">{a.statusCode ?? '—'}</td>
                    <td className="mono">{a.error ?? '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          {canReplay ? (
            webhook.status === 'ACTIVE' ? (
              <Confirm
                action="Replay"
                effect={
                  <>
                    Schedule a new delivery of event <span className="mono">{data.delivery.eventId}</span> to{' '}
                    {webhook.url}, with the same bytes and the same <span className="mono">webhook-id</span>. A receiver
                    that deduplicates on it ignores a copy it already processed.
                  </>
                }
                onConfirm={() => {
                  setReplayError('')
                  api
                    .replayWebhookDelivery(webhook.id, deliveryId)
                    .then(onReplayed)
                    .catch((cause: unknown) => { setReplayError(problemText(cause, 'the ADMIN role')) })
                }}
              />
            ) : (
              <p className="muted">Replay needs the subscription to be ACTIVE; it is {webhook.status}.</p>
            )
          ) : null}
          {replayError ? <p className="field__error" role="alert">{replayError}</p> : null}
        </>
      )}
    </div>
  )
}
