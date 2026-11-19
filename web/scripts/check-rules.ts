// Runs the shared rule cases (internal/rules/testdata/cases.json) against the
// panel's matcher, so the Go and TypeScript matchers can't drift apart.
// Usage: pnpm test:rules   (Node strips the types itself, no build step)
import { readFileSync } from 'node:fs'
import { compileRules, decidingRule, inScope } from '../src/lib/rules.ts'

type Group = {
  name: string
  rules: string[]
  cases: { path: string; dir?: boolean; in: boolean; rule: number }[]
}

const file = new URL('../../internal/rules/testdata/cases.json', import.meta.url)
const groups = JSON.parse(readFileSync(file, 'utf8')) as Group[]
let failed = 0
let total = 0
for (const g of groups) {
  const rules = compileRules(g.rules)
  for (const c of g.cases) {
    total++
    const got = inScope(rules, c.path, !!c.dir)
    const rule = decidingRule(rules, c.path, !!c.dir)?.index ?? -1
    if (got !== c.in || rule !== c.rule) {
      failed++
      console.error(`${g.name}: ${c.path} dir=${!!c.dir}: in=${got} rule=${rule}, want in=${c.in} rule=${c.rule}`)
    }
  }
}
console.log(`${total - failed}/${total} rule cases pass`)
process.exit(failed ? 1 : 0)
