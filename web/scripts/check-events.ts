// Every activity event code the server emits (internal/server/*.go) must
// have a message in each language (activity.events.<code>).
// Usage: pnpm test:events
import { readdirSync, readFileSync } from 'node:fs'
import en from '../src/i18n/locales/en.ts'
import zh from '../src/i18n/locales/zh-CN.ts'

const dir = new URL('../../internal/server/', import.meta.url)
const codes = new Set<string>()
for (const f of readdirSync(dir)) {
  if (!f.endsWith('.go') || f.endsWith('_test.go')) continue
  const src = readFileSync(new URL(f, dir), 'utf8')
  for (const m of src.matchAll(/Code:\s*"(\w+)"/g)) codes.add(m[1])
  for (const m of src.matchAll(/\bcode\s*:?=\s*"(\w+)"/g)) codes.add(m[1])
  // code tables such as map[string]string{"symlink": "exclSymlink", …}[x]
  for (const m of src.matchAll(/codes?\s*:?=\s*map\[string\]string\{([^}]*)\}/g))
    for (const v of m[1].matchAll(/:\s*"(\w+)"/g)) codes.add(v[1])
}
let missing = 0
for (const [lang, msgs] of [['en', en], ['zh-CN', zh]] as const) {
  const events = msgs.activity.events as Record<string, string>
  for (const c of codes)
    if (!events[c]) {
      missing++
      console.error(`${lang}: no message for event code "${c}"`)
    }
}
console.log(`${codes.size} event codes, ${missing} missing messages`)
process.exit(missing ? 1 : 0)
