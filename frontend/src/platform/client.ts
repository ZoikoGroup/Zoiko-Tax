/**
 * The generated SDK, pointed at this origin.
 *
 * ADR-0019 C4: the fiscal surface is reached through the SDK and never by a
 * hand-written fetch. `/api` is the web tier's proxy to the cell (nginx in the
 * image, Vite in development), so the session cookie is same-origin and the
 * client has no token to hold.
 */
import { ZoikoTaxClient } from '@zoikotax/sdk'

export const ztax = new ZoikoTaxClient({ baseUrl: '/api', timeoutMs: 10000 })
