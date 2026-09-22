import { useId, useState, type ReactNode } from 'react'

/**
 * The consequential-action pattern (ADR-0019 C5).
 *
 * C5 requires explicit confirmation naming the legal or operational effect in
 * plain language, not a generic "Are you sure?". W3 lane N owns the final
 * pattern alongside UX-001; this is the shape of it for the administrative
 * actions that exist today, so the first real screen inherits something rather
 * than inventing it.
 *
 * Two rules it enforces that a generic dialog does not:
 *
 *  - The prompt names what will happen to whom, supplied by the caller. There
 *    is no default text, so a call site cannot get a vague one by omission.
 *  - The confirming control is labelled with the verb, not with "OK". A button
 *    that says "Disable Ada Analyst" cannot be clicked by muscle memory in the
 *    way an "OK" can.
 */
export interface ConfirmProps {
  /** The verb, used on the trigger and the confirming button. */
  readonly action: string
  /** What will happen, in plain language. One or two sentences. */
  readonly effect: ReactNode
  /** Extra weight for an action that cannot be undone or that affects the
   *  person taking it. */
  readonly severe?: boolean
  readonly disabled?: boolean
  readonly onConfirm: () => void | Promise<void>
}

export function Confirm({ action, effect, severe = false, disabled = false, onConfirm }: ConfirmProps) {
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const describedBy = useId()

  if (!open) {
    return (
      <button
        type="button"
        className={severe ? 'btn btn--severe' : 'btn'}
        disabled={disabled}
        onClick={() => { setOpen(true) }}
      >
        {action}
      </button>
    )
  }

  return (
    <div className="confirm" role="group" aria-label={action}>
      <p className="confirm__effect" id={describedBy}>{effect}</p>
      <div className="confirm__actions">
        <button
          type="button"
          className={severe ? 'btn btn--severe' : 'btn btn--primary'}
          aria-describedby={describedBy}
          disabled={busy}
          onClick={() => {
            setBusy(true)
            void Promise.resolve(onConfirm()).finally(() => {
              setBusy(false)
              setOpen(false)
            })
          }}
        >
          {busy ? `${action}…` : action}
        </button>
        <button type="button" className="btn" disabled={busy} onClick={() => { setOpen(false) }}>
          Cancel
        </button>
      </div>
    </div>
  )
}
