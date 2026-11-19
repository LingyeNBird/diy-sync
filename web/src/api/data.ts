// The panel's data, from fdsync-server. useData() returns the same shape
// the mockup's store had, so pages read folders/connections/... the same way.
import { useMemo } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import i18n from '@/i18n'
import { contentKind, languageFor } from '@/lib/content'
import type { ActivityEvent, Binding, Conflict, Connection, PairingRequest, Side, SyncFolder, TreeNode } from '@/lib/types'
import { api, ApiError, apiResponse } from './client'

export type Resolution = 'windows' | 'wsl' | 'both'

type ConflictWire = Omit<Conflict, 'content' | 'language'>

export type Overview = {
  server: { device: string; host: string; listen: string; panelListen: string; version: string }
  folders: SyncFolder[]
  connections: Connection[]
  bindings: Binding[]
  conflicts: ConflictWire[]
  pairing: PairingRequest[]
  /** Pause all: every folder paused, until a time (none = until resumed). */
  pause: { paused: boolean; until?: string }
}

export const overviewKey = ['overview']

// Throughput history for the dashboard chart, sampled with every overview poll.
export type RatePoint = { t: string; up: number; down: number }
const rateHistory: RatePoint[] = []
function sampleRates(o: Overview) {
  const up = o.connections.reduce((n, c) => n + c.upBps, 0)
  const down = o.connections.reduce((n, c) => n + c.downBps, 0)
  rateHistory.push({ t: new Date().toISOString(), up, down })
  if (rateHistory.length > 150) rateHistory.splice(0, rateHistory.length - 150)
}
export const rates = () => rateHistory.slice()

export const overviewQuery = {
  queryKey: overviewKey,
  queryFn: async () => {
    const o = await api<Overview>('GET', '/api/overview')
    // Lists may come as null from older servers; pages expect arrays.
    o.folders = (o.folders ?? []).map((f) => ({ ...f, rules: f.rules ?? [], autoExcluded: f.autoExcluded ?? [] }))
    o.connections ??= []
    o.bindings ??= []
    o.conflicts ??= []
    o.pairing ??= []
    o.pause ??= { paused: false }
    sampleRates(o)
    return o
  },
  staleTime: 1000,
}

export function useOverview() {
  return useQuery({ ...overviewQuery, refetchInterval: 2000 })
}

function withContent(c: ConflictWire): Conflict {
  return { ...c, content: contentKind(c.path), language: languageFor(c.path) }
}

const fail = (e: unknown) => {
  toast.error(i18n.t('errors.actionFailed'), { description: e instanceof Error ? e.message : String(e) })
}

/** Everything the pages show, plus the admin actions. */
export function useData() {
  const qc = useQueryClient()
  const o = useOverview().data
  const activity = useQuery({
    queryKey: ['activity'],
    queryFn: () => api<ActivityEvent[]>('GET', '/api/activity'),
    refetchInterval: 3000,
  }).data

  const folders = o?.folders ?? []
  // Stable while the server's list is unchanged (React Query keeps unchanged
  // parts of a refetch as the same objects), so trees aren't re-annotated
  // on every poll.
  const wireConflicts = o?.conflicts
  const conflicts = useMemo(() => (wireConflicts ?? []).map(withContent), [wireConflicts])
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: overviewKey })
    void qc.invalidateQueries({ queryKey: ['activity'] })
    void qc.invalidateQueries({ queryKey: ['tree'] })
  }
  const run = (p: Promise<unknown>) => p.then(refresh, (e) => (refresh(), fail(e), Promise.reject(e)))

  const setRules = (folderId: string, rules: string[]) =>
    run(api('PUT', `/api/folders/${encodeURIComponent(folderId)}/rules`, { rules }))

  return {
    loaded: !!o,
    server: o?.server,
    folders,
    connections: o?.connections ?? [],
    bindings: o?.bindings ?? [],
    conflicts,
    pairing: o?.pairing ?? [],
    pause: o?.pause ?? { paused: false },
    activity: activity ?? [],
    refresh,

    setRules,
    addRules: (folderId: string, rules: string[]) => {
      const f = folders.find((x) => x.id === folderId)
      return setRules(folderId, [...(f?.rules ?? []), ...rules])
    },
    exclude: (folderId: string, paths: string[]) =>
      run(api('POST', `/api/folders/${encodeURIComponent(folderId)}/exclusions`, { paths })),
    cancelExclusion: (folderId: string, paths: string[]) =>
      run(api('POST', `/api/folders/${encodeURIComponent(folderId)}/exclusions`, { paths, cancel: true })),
    resolve: (ids: string[], how: Resolution) => run(api('POST', '/api/conflicts/resolve', { ids, how })),
    deleteOnSide: (folderId: string, connectionId: string, side: Side, paths: string[]) =>
      run(api('POST', '/api/delete', { folder: folderId, connection: connectionId, side, paths })),
    approve: (code: string, name: string) => run(api('POST', '/api/pair/approve', { code, name })),
    deny: (code: string) => run(api('POST', '/api/pair/deny', { code })),
    revoke: (connectionId: string) => run(api('POST', `/api/connections/${encodeURIComponent(connectionId)}/revoke`)),
  }
}

/** A binding's merged two-sided tree. */
// Tree versions the panel already has: an unchanged tree (tens of MB with
// a big node_modules) is answered with 204 instead of being sent again.
const treeVersions = new Map<string, string>()

export function useTree(folderId: string, connectionId: string) {
  const qc = useQueryClient()
  // qc is the app-wide client; the key is folderId + connectionId.
  // eslint-disable-next-line @tanstack/query/exhaustive-deps
  return useQuery({
    queryKey: ['tree', folderId, connectionId],
    queryFn: async () => {
      const id = `${folderId}|${connectionId}`
      const have = qc.getQueryData<TreeNode[]>(['tree', folderId, connectionId])
      const v = have ? (treeVersions.get(id) ?? '') : ''
      const q = new URLSearchParams({ folder: folderId, connection: connectionId, v })
      const res = await apiResponse(`/api/tree?${q}`)
      if (res.status === 204 && have) return have // same object: nothing re-renders
      treeVersions.set(id, res.headers.get('X-Fdsync-Tree-Version') ?? '')
      return (await res.json()) as TreeNode[]
    },
    refetchInterval: 4000,
    placeholderData: (prev) => prev,
  })
}

export { ApiError }
