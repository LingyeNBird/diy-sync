// Scope-rule matching, mirrored by the Go implementation.
//
// Semantics (docs/requirements.md §4.1):
//  - One pattern per line. Blank lines and lines starting with '#' are ignored.
//  - A leading '!' turns an include rule into an exclude rule.
//  - A trailing '/' makes the pattern match folders only (and so their whole subtree).
//  - A pattern without any other '/' matches a name at any depth ("*.log").
//    A pattern containing '/' is anchored at the sync-folder root.
//  - Wildcards: '*' (within one name), '**' (any number of folders), '?', '[abc]'.
//  - A path is in scope when the LAST rule matching it or one of its parent
//    folders is an include rule.

export type CompiledRule = {
  index: number // line index in the rule list
  source: string
  negate: boolean
  dirOnly: boolean
  re: RegExp
}

function globToRegExp(glob: string): string {
  let re = ''
  for (let i = 0; i < glob.length; i++) {
    const c = glob[i]
    if (c === '*') {
      if (glob[i + 1] === '*') {
        const atSegStart = i === 0 || glob[i - 1] === '/'
        if (glob[i + 2] === '/' && atSegStart) {
          re += '(?:.*/)?'
          i += 2
        } else {
          re += '.*'
          i += 1
        }
      } else {
        re += '[^/]*'
      }
    } else if (c === '?') {
      re += '[^/]'
    } else if (c === '[') {
      const end = glob.indexOf(']', i + 1)
      if (end === -1) {
        re += '\\['
      } else {
        let cls = glob.slice(i + 1, end)
        if (cls.startsWith('!')) cls = '^' + cls.slice(1)
        re += '[' + cls.replace(/\\/g, '\\\\') + ']'
        i = end
      }
    } else if ('\\^$.|+(){}'.includes(c)) {
      re += '\\' + c
    } else {
      re += c
    }
  }
  return re
}

export function compileRules(lines: string[]): CompiledRule[] {
  const out: CompiledRule[] = []
  lines.forEach((raw, index) => {
    let line = raw.trim()
    if (!line || line.startsWith('#')) return
    let negate = false
    if (line.startsWith('!')) {
      negate = true
      line = line.slice(1)
    }
    let dirOnly = false
    if (line.endsWith('/')) {
      dirOnly = true
      line = line.replace(/\/+$/, '')
    }
    const anchored = line.includes('/')
    line = line.replace(/^\/+/, '')
    if (!line) return
    const body = globToRegExp(line)
    const re = new RegExp(anchored ? `^${body}$` : `^(?:.*/)?${body}$`)
    out.push({ index, source: raw.trim(), negate, dirOnly, re })
  })
  return out
}

/** Returns the rule deciding this path, or undefined when no rule matches. */
export function decidingRule(
  rules: CompiledRule[],
  path: string,
  isDir: boolean
): CompiledRule | undefined {
  // Candidates: the path itself and each parent folder.
  const parts = path.split('/')
  const candidates: { p: string; dir: boolean }[] = []
  for (let i = 1; i <= parts.length; i++) {
    candidates.push({
      p: parts.slice(0, i).join('/'),
      dir: i < parts.length || isDir,
    })
  }
  let decided: CompiledRule | undefined
  for (const rule of rules) {
    for (const c of candidates) {
      if (rule.dirOnly && !c.dir) continue
      if (rule.re.test(c.p)) {
        decided = rule
        break
      }
    }
  }
  return decided
}

export function inScope(rules: CompiledRule[], path: string, isDir: boolean) {
  const r = decidingRule(rules, path, isDir)
  return !!r && !r.negate
}
