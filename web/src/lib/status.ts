import { compileRules, decidingRule, type CompiledRule } from './rules'
import type { AutoExclusion, ItemStatus, TreeNode } from './types'

export type Counts = {
  win: number
  wsl: number
  inScope: number
  conflicts: number
  excluded: number
  syncing: number
}

export type AnnotatedNode = Omit<TreeNode, 'children'> & {
  status: ItemStatus
  rule?: CompiledRule
  exclusion?: AutoExclusion
  counts: Counts // files below this node (or the file itself)
  children?: AnnotatedNode[]
}

const zero = (): Counts => ({ win: 0, wsl: 0, inScope: 0, conflicts: 0, excluded: 0, syncing: 0 })

function add(a: Counts, b: Counts) {
  a.win += b.win
  a.wsl += b.wsl
  a.inScope += b.inScope
  a.conflicts += b.conflicts
  a.excluded += b.excluded
  a.syncing += b.syncing
}

/** Works out the status of every item of a binding's merged tree. */
export function annotate(
  roots: TreeNode[],
  ruleLines: string[],
  exclusions: AutoExclusion[],
  conflictPaths: Set<string>
): { nodes: AnnotatedNode[]; totals: Counts } {
  const rules = compileRules(ruleLines)
  const excl = new Map(exclusions.map((e) => [e.path, e]))

  function visit(n: TreeNode, parentExcluded?: AutoExclusion): AnnotatedNode {
    const exclusion = excl.get(n.path) ?? parentExcluded
    const rule = decidingRule(rules, n.path, n.kind === 'dir')
    const scoped = !!rule && !rule.negate

    if (n.kind === 'dir' && n.children) {
      const children = n.children.map((c) => visit(c, exclusion))
      const counts = zero()
      children.forEach((c) => add(counts, c.counts))
      let status: ItemStatus
      if (exclusion) status = 'excluded'
      else if (counts.conflicts) status = 'conflict'
      else if (counts.syncing && scoped) status = 'syncing'
      else if (scoped && children.length && children.every((c) => c.status === 'synced'))
        status = 'synced'
      else if (counts.inScope > 0) status = 'partial'
      else status = privateStatus(n)
      return { ...n, status, rule, exclusion, counts, children }
    }

    const counts = zero()
    counts.win = n.win ? 1 : 0
    counts.wsl = n.wsl ? 1 : 0
    let status: ItemStatus
    if (exclusion || n.kind === 'symlink') {
      status = 'excluded'
      counts.excluded = 1
    } else if (!scoped) {
      status = privateStatus(n)
    } else {
      counts.inScope = 1
      if (conflictPaths.has(n.path)) {
        status = 'conflict'
        counts.conflicts = 1
      } else if (!n.win || !n.wsl) {
        status = 'syncing'
        counts.syncing = 1
      } else status = 'synced'
    }
    const { children: _c, ...rest } = n
    return { ...rest, status, rule, exclusion, counts }
  }

  const nodes = roots.map((r) => visit(r))
  const totals = zero()
  nodes.forEach((n) => add(totals, n.counts))
  return { nodes, totals }
}

function privateStatus(n: TreeNode): ItemStatus {
  if (n.win && n.wsl) return 'private-both'
  return n.win ? 'private-win' : 'private-wsl'
}

/** Paths a rule list matches among the Windows-side files (for the rule preview). */
export function previewMatches(roots: TreeNode[], ruleLines: string[], limit = 500) {
  const rules = compileRules(ruleLines)
  const perRule = new Map<number, number>()
  const matched: { path: string; rule: CompiledRule }[] = []
  let total = 0
  let considered = 0
  const walk = (n: TreeNode) => {
    if (n.kind === 'dir' && n.children) return n.children.forEach(walk)
    if (!n.win || n.kind === 'symlink') return
    considered++
    const r = decidingRule(rules, n.path, false)
    if (r) perRule.set(r.index, (perRule.get(r.index) ?? 0) + 1)
    if (r && !r.negate) {
      total++
      if (matched.length < limit) matched.push({ path: n.path, rule: r })
    }
  }
  roots.forEach(walk)
  return { matched, total, considered, perRule }
}
