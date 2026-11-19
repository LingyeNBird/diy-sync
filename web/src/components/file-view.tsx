// Content views shared by the file browser viewer and the Conflicts page:
// text (Monaco), text diff, image, image compare, hex dump and hex compare.
import { useEffect, useRef, useState } from 'react'
import type * as Monaco from 'monaco-editor/editor/editor.api'
import { ChevronFirst, ChevronLast, ChevronLeft, ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { formatBytes } from '@/lib/format'
import { CodeDiff, CodeEditor } from '@/components/monaco'

export const checkerboard =
  'bg-[conic-gradient(var(--muted)_25%,transparent_0_50%,var(--muted)_0_75%,transparent_0)] bg-[length:16px_16px]'

// ---------------------------------------------------------------- text

export function TextView({
  text,
  language,
  modelPath,
  wrap,
}: {
  text: string
  language?: string
  modelPath: string
  wrap?: boolean
}) {
  return (
    <CodeEditor
      value={text}
      language={language}
      path={modelPath}
      height='100%'
      options={{ readOnly: true, domReadOnly: true, wordWrap: wrap ? 'on' : 'off', lineNumbers: 'on' }}
    />
  )
}

export type DiffStats = { added: number; removed: number; changes: number }
export type DiffNav = { go: (dir: 'next' | 'previous') => void }

/**
 * Read-only diff. `original` is the base (left), `modified` the side being
 * compared against it (right). Remount with a new `id` to swap direction.
 */
export function TextDiff({
  id,
  original,
  modified,
  language,
  inline,
  wrap,
  hideUnchanged,
  onStats,
  navRef,
}: {
  id: string
  original: string
  modified: string
  language?: string
  inline?: boolean
  wrap?: boolean
  hideUnchanged?: boolean
  onStats?: (s: DiffStats) => void
  navRef?: React.RefObject<DiffNav | null>
}) {
  const models = useRef<Monaco.editor.ITextModel[]>([])
  // The diff editor keeps its models alive (see CodeDiff); free them after it is gone.
  useEffect(
    () => () => {
      const ms = models.current
      setTimeout(() => ms.forEach((m) => !m.isDisposed() && m.dispose()), 0)
    },
    []
  )
  return (
    <CodeDiff
      original={original}
      modified={modified}
      language={language}
      originalModelPath={`diff/${id}/original`}
      modifiedModelPath={`diff/${id}/modified`}
      height='100%'
      options={{
        renderSideBySide: !inline,
        wordWrap: wrap ? 'on' : 'off',
        diffWordWrap: wrap ? 'on' : 'off',
        hideUnchangedRegions: { enabled: !!hideUnchanged, contextLineCount: 3, minimumLineCount: 3 },
        renderOverviewRuler: true,
      }}
      onMount={(editor) => {
        const m = editor.getModel()
        if (m) models.current = [m.original, m.modified]
        if (navRef) navRef.current = { go: (dir) => editor.goToDiff(dir) }
        const report = () => {
          const ch = editor.getLineChanges() ?? []
          let added = 0
          let removed = 0
          for (const c of ch) {
            if (c.modifiedEndLineNumber > 0) added += c.modifiedEndLineNumber - c.modifiedStartLineNumber + 1
            if (c.originalEndLineNumber > 0) removed += c.originalEndLineNumber - c.originalStartLineNumber + 1
          }
          onStats?.({ added, removed, changes: ch.length })
        }
        editor.onDidUpdateDiff(report)
      }}
    />
  )
}

// ---------------------------------------------------------------- images

export function ImageView({ src, fit = true }: { src: string; fit?: boolean }) {
  const [dims, setDims] = useState<{ w: number; h: number }>()
  const { t } = useTranslation()
  return (
    <div className={cn('relative h-full overflow-auto', checkerboard)}>
      <div className={cn('flex items-center justify-center p-6', fit ? 'h-full' : 'min-h-full min-w-max')}>
        <img
          src={src}
          alt=''
          onLoad={(e) => setDims({ w: e.currentTarget.naturalWidth, h: e.currentTarget.naturalHeight })}
          className={cn('rounded shadow-sm', fit && 'max-h-full max-w-full object-contain')}
        />
      </div>
      {dims && (
        <span className='absolute end-2 bottom-2 rounded bg-background/80 px-1.5 py-0.5 text-[11px] text-muted-foreground tabular-nums'>
          {t('viewer.image.dims', { w: dims.w, h: dims.h })}
        </span>
      )}
    </div>
  )
}

export type ImageMode = 'side' | 'swipe' | 'onion' | 'difference'
export const imageModes: ImageMode[] = ['side', 'swipe', 'onion', 'difference']

/** `base` is the reference image, `other` the one compared against it. */
export function ImageCompare({
  base,
  other,
  baseLabel,
  otherLabel,
  mode,
}: {
  base: string
  other: string
  baseLabel: string
  otherLabel: string
  mode: ImageMode
}) {
  const { t } = useTranslation()
  const [pos, setPos] = useState(50)
  if (mode === 'side')
    return (
      <div className='grid h-full grid-cols-2'>
        {[
          [base, baseLabel],
          [other, otherLabel],
        ].map(([src, label], i) => (
          <div key={i} className={cn('flex min-h-0 flex-col', i && 'border-s')}>
            <div className='shrink-0 px-3 py-1 text-xs text-muted-foreground'>{label}</div>
            <div className='min-h-0 flex-1'>
              <ImageView src={src} />
            </div>
          </div>
        ))}
      </div>
    )

  const img = 'pointer-events-none absolute inset-0 m-auto max-h-full max-w-full object-contain select-none'
  return (
    <div className='flex h-full flex-col'>
      <div className='flex shrink-0 items-center gap-3 border-b px-3 py-1.5 text-xs text-muted-foreground'>
        <span className='truncate'>{baseLabel}</span>
        {mode !== 'difference' ? (
          <input
            type='range'
            min={0}
            max={100}
            value={pos}
            onChange={(e) => setPos(+e.target.value)}
            aria-label={t(`viewer.image.${mode}`)}
            className='h-1 flex-1 accent-primary'
          />
        ) : (
          <span className='flex-1 text-center'>{t('viewer.image.differenceHint')}</span>
        )}
        <span className='truncate'>{otherLabel}</span>
      </div>
      <div className={cn('relative min-h-0 flex-1 p-6', mode === 'difference' ? 'bg-black' : checkerboard)}>
        <div className='relative h-full w-full' style={mode === 'difference' ? { isolation: 'isolate' } : undefined}>
          <img src={base} alt='' className={img} />
          <img
            src={other}
            alt=''
            className={img}
            style={
              mode === 'swipe'
                ? { clipPath: `inset(0 0 0 ${pos}%)` }
                : mode === 'onion'
                  ? { opacity: pos / 100 }
                  : { mixBlendMode: 'difference' }
            }
          />
          {mode === 'swipe' && (
            <div className='pointer-events-none absolute inset-y-0 w-0.5 bg-primary shadow' style={{ left: `${pos}%` }} />
          )}
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------- binary

const hex2 = (b: number) => b.toString(16).padStart(2, '0')
const ascii = (b: number) => (b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : '·')

/** Hex dump. With `compare`, bytes that differ from it are highlighted. */
export function HexView({
  bytes,
  size,
  compare,
  label,
  offset = 0,
}: {
  bytes: Uint8Array
  size: number
  compare?: Uint8Array
  label?: string
  offset?: number // address of bytes[0] in the file
}) {
  const { t } = useTranslation()
  const rows = Math.ceil(bytes.length / 16)
  const diffs = compare ? bytes.reduce((n, b, i) => n + (b !== compare[i] ? 1 : 0), 0) : 0
  return (
    <div className='h-full overflow-auto p-4 font-mono text-[11px] leading-5'>
      <div className='mb-3 font-sans text-xs text-muted-foreground'>
        {label && <span className='font-medium text-foreground'>{label} · </span>}
        {t('viewer.binaryRange', {
          size: formatBytes(size),
          from: hexAddr(offset),
          to: hexAddr(offset + Math.max(bytes.length, 1) - 1),
        })}
        {compare && ` · ${t('viewer.bytesDiffer', { count: diffs })}`}
      </div>
      {Array.from({ length: rows }, (_, r) => {
        const row = bytes.subarray(r * 16, r * 16 + 16)
        const differs = (i: number) => compare && row[i] !== compare[r * 16 + i]
        return (
          <div key={r} className='flex gap-4 whitespace-pre'>
            <span className='text-muted-foreground'>{hexAddr(offset + r * 16)}</span>
            <span className='flex gap-1.5'>
              {Array.from(row, (b, i) => (
                <span
                  key={i}
                  className={cn(i === 8 && 'ms-1.5', differs(i) && 'rounded-sm bg-amber-500/25 text-amber-700 dark:text-amber-300')}
                >
                  {hex2(b)}
                </span>
              ))}
            </span>
            <span className='text-muted-foreground'>
              {Array.from(row, (b, i) => (
                <span key={i} className={cn(differs(i) && 'text-amber-700 dark:text-amber-300')}>
                  {ascii(b)}
                </span>
              ))}
            </span>
          </div>
        )
      })}
    </div>
  )
}

export function HexCompare({
  base,
  other,
  baseSize,
  otherSize,
  baseLabel,
  otherLabel,
  offset = 0,
}: {
  base: Uint8Array
  other: Uint8Array
  baseSize: number
  otherSize: number
  baseLabel: string
  otherLabel: string
  offset?: number
}) {
  return (
    <div className='grid h-full grid-rows-2 xl:grid-cols-2 xl:grid-rows-1'>
      <div className='min-h-0'>
        <HexView bytes={base} size={baseSize} label={baseLabel} offset={offset} />
      </div>
      <div className='min-h-0 border-t xl:border-t-0 xl:border-s'>
        <HexView bytes={other} size={otherSize} compare={base} label={otherLabel} offset={offset} />
      </div>
    </div>
  )
}

const hexAddr = (n: number) => n.toString(16).padStart(8, '0')
export const HEX_PAGE = 4096

/** Parses "0x1A00", "1a00h" or "6656" (decimal) into a byte offset. */
export function parseOffset(s: string): number | undefined {
  const v = s.trim().toLowerCase().replace(/_/g, '')
  if (!v) return undefined
  if (/^0x[0-9a-f]+$/.test(v)) return parseInt(v.slice(2), 16)
  if (/^[0-9a-f]+h$/.test(v)) return parseInt(v.slice(0, -1), 16)
  if (/^\d+$/.test(v)) return parseInt(v, 10)
  return undefined
}

/** Paging for the hex view: start, back, forward, end, and "go to offset". */
export function HexNav({ offset, size, onChange }: { offset: number; size: number; onChange: (o: number) => void }) {
  const { t } = useTranslation()
  const [text, setText] = useState('')
  const last = Math.max(0, Math.floor((Math.max(size, 1) - 1) / HEX_PAGE) * HEX_PAGE)
  const go = (o: number) => onChange(Math.min(Math.max(0, Math.floor(o / 16) * 16), last))
  const btn = 'inline-flex h-8 min-w-8 items-center justify-center rounded-md border px-2 text-xs hover:bg-muted disabled:pointer-events-none disabled:opacity-40'
  return (
    <div className='flex items-center gap-1'>
      <button type='button' className={btn} disabled={offset === 0} onClick={() => go(0)} aria-label={t('viewer.hexStart')} title={t('viewer.hexStart')}>
        <ChevronFirst className='size-3.5' />
      </button>
      <button type='button' className={btn} disabled={offset === 0} onClick={() => go(offset - HEX_PAGE)} aria-label={t('viewer.hexPrev')} title={t('viewer.hexPrev')}>
        <ChevronLeft className='size-3.5' />
      </button>
      <button type='button' className={btn} disabled={offset >= last} onClick={() => go(offset + HEX_PAGE)} aria-label={t('viewer.hexNext')} title={t('viewer.hexNext')}>
        <ChevronRight className='size-3.5' />
      </button>
      <button type='button' className={btn} disabled={offset >= last} onClick={() => go(last)} aria-label={t('viewer.hexEnd')} title={t('viewer.hexEnd')}>
        <ChevronLast className='size-3.5' />
      </button>
      <form
        className='flex items-center gap-1'
        onSubmit={(e) => {
          e.preventDefault()
          const o = parseOffset(text)
          if (o !== undefined) go(o)
        }}
      >
        <input
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder={t('viewer.hexGoto')}
          aria-label={t('viewer.hexGoto')}
          className={cn('h-8 w-36 rounded-md border bg-transparent px-2 font-mono text-xs', text && parseOffset(text) === undefined && 'border-destructive')}
        />
      </form>
    </div>
  )
}
