// Monaco on demand: it is large (several MB of script), so pages load it
// only when an editor or diff is actually shown, not when they open.
import { lazy, Suspense } from 'react'
import type { DiffEditorProps, EditorProps } from '@monaco-editor/react'
import { Loader2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

type MonacoModule = typeof import('./monaco')
let loaded: MonacoModule | undefined
const load = () =>
  import('./monaco').then((m) => {
    loaded = m
    return m
  })
// Until Monaco is here the editors suspend; once it is (e.g. preloaded),
// they render it directly: no Suspense round, whose fallback React holds
// for a moment even when the module is ready.
const LazyEditor = lazy(() => load().then((m) => ({ default: m.CodeEditor })))
const LazyDiff = lazy(() => load().then((m) => ({ default: m.CodeDiff })))

/** Fetches Monaco in the background (when the browser is idle), so the first editor opens fast. */
export function preloadEditor() {
  const go = () => void load()
  if ('requestIdleCallback' in window) window.requestIdleCallback(go, { timeout: 5000 })
  else setTimeout(go, 1500)
}

function Loading() {
  const { t } = useTranslation()
  return (
    <div className='flex h-full items-center justify-center gap-2 text-sm text-muted-foreground'>
      <Loader2 className='size-4 animate-spin' /> {t('viewer.loading')}
    </div>
  )
}

export function CodeEditor(props: EditorProps) {
  if (loaded) return <loaded.CodeEditor {...props} />
  return (
    <Suspense fallback={<Loading />}>
      <LazyEditor {...props} />
    </Suspense>
  )
}

export function CodeDiff(props: DiffEditorProps) {
  if (loaded) return <loaded.CodeDiff {...props} />
  return (
    <Suspense fallback={<Loading />}>
      <LazyDiff {...props} />
    </Suspense>
  )
}
