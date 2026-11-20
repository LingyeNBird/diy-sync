// Page memory: each page keeps its view state (selection, open folders,
// filters, tabs, scroll) while you visit other pages, and across a reload
// of the browser tab (sessionStorage). Not shared between tabs or viewers.
import { useCallback, useState, type Dispatch, type SetStateAction } from 'react'

const PREFIX = 'fdsync-page:'
const mem = new Map<string, unknown>()

// Sets (selections, expanded folders) survive JSON as {"$set": [...]}.
const replacer = (_: string, v: unknown) => (v instanceof Set ? { $set: [...v] } : v)
const reviver = (_: string, v: unknown) =>
  v && typeof v === 'object' && '$set' in v && Array.isArray((v as { $set: unknown }).$set) ? new Set((v as { $set: unknown[] }).$set) : v

function load(key: string): { found: boolean; value?: unknown } {
  if (mem.has(key)) return { found: true, value: mem.get(key) }
  try {
    const raw = sessionStorage.getItem(PREFIX + key)
    if (raw !== null) {
      const value = JSON.parse(raw, reviver)
      mem.set(key, value)
      return { found: true, value }
    }
  } catch {
    // storage blocked or a bad entry: start fresh
  }
  return { found: false }
}

const pending = new Map<string, unknown>()
let flushTimer: ReturnType<typeof setTimeout> | undefined
function save(key: string, value: unknown) {
  mem.set(key, value)
  pending.set(key, value)
  // Batched: scrolling and selecting write often.
  flushTimer ??= setTimeout(() => {
    flushTimer = undefined
    for (const [k, v] of pending) {
      try {
        if (v === undefined) sessionStorage.removeItem(PREFIX + k)
        else sessionStorage.setItem(PREFIX + k, JSON.stringify(v, replacer))
      } catch {
        // full or blocked: memory still has it
      }
    }
    pending.clear()
  }, 300)
}

/** Reads a remembered value without a hook (e.g. for a default). */
export function remembered<T>(key: string): T | undefined {
  return load(key).value as T | undefined
}

/** Remembers a value without a hook. */
export function remember(key: string, value: unknown) {
  save(key, value)
}

/**
 * Like useState, but remembered under key: a page that unmounts and comes
 * back starts where it was. Give each page (and each item it shows, e.g. a
 * binding) its own key.
 */
export function usePageState<T>(key: string, initial: T | (() => T)): [T, Dispatch<SetStateAction<T>>] {
  const [state, setState] = useState<{ key: string; value: T }>(() => {
    const got = load(key)
    return { key, value: got.found ? (got.value as T) : typeof initial === 'function' ? (initial as () => T)() : initial }
  })
  // A new key (another binding, another folder): switch to its memory.
  let current = state
  if (state.key !== key) {
    const got = load(key)
    current = { key, value: got.found ? (got.value as T) : typeof initial === 'function' ? (initial as () => T)() : initial }
    setState(current)
  }
  const set: Dispatch<SetStateAction<T>> = useCallback(
    (next) =>
      setState((s) => {
        const value = typeof next === 'function' ? (next as (p: T) => T)(s.value) : next
        save(s.key, value)
        return { key: s.key, value }
      }),
    []
  )
  return [current.value, set]
}
