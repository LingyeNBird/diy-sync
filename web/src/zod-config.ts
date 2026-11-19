// Imported first in main.tsx, before any schema is created: the panel's CSP
// forbids eval, so zod must not probe for it (the probe logs a CSP error).
import { z } from 'zod'

z.config({ jitless: true })
