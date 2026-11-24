import { useEffect, useRef, useState } from 'react'

const COPY_RESET_MS = 2000

/** The evidence id and a Copy button. The id is never put in any message. */
export const EvidenceIdValue = ({ id }: { id: string }) => {
  const [label, setLabel] = useState('Copy')
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(() => () => { if (timer.current !== null) clearTimeout(timer.current) }, [])
  const show = (text: string) => {
    setLabel(text)
    if (timer.current !== null) clearTimeout(timer.current)
    timer.current = setTimeout(() => { setLabel('Copy') }, COPY_RESET_MS)
  }
  const copy = () => {
    try {
      navigator.clipboard.writeText(id).then(() => { show('Copied') }, () => { show('Could not copy') })
    } catch {
      show('Could not copy')
    }
  }
  return (
    <>
      {id}{' '}
      <button type="button" className="ztax-btn-quiet" onClick={copy}>{label}</button>
    </>
  )
}
