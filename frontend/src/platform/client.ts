/**
 * The generated SDK, pointed at this origin.
 *
 * ADR-0019 C4: the fiscal surface is reached through the SDK and never by a
 * hand-written fetch. `/api` is the web tier's proxy to the cell (nginx in the
 * image, Vite in development), so the session cookie is same-origin and the
 * client has no token to hold.
 */
import { ZoikoTaxClient } from '@zoikotax/sdk'

/** The screen's request limit. It must stay above the Go gateway deadline (backend config, default 5s). */
export const REQUEST_TIMEOUT_MS = 10000

export const ztax = new ZoikoTaxClient({ baseUrl: '/api', timeoutMs: REQUEST_TIMEOUT_MS })
