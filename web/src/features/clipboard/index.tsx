// Clipboard: in-memory Windows screenshots, panel uploads & WSL-pushed images.
// Users can inspect thumbnails/full previews (with keyboard shortcuts), delete
// sensitive captures, pin ("Select for AI") specific images, attach short notes,
// toggle auto-capture / proportional resize / auto-unselect, or copy the AI Skill.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronUp,
  Copy,
  Download,
  Eye,
  ImagePlus,
  Images,
  Loader2,
  PauseCircle,
  Pencil,
  Pin,
  PinOff,
  RefreshCw,
  Sparkles,
  Terminal,
  Trash2,
  Upload,
  X,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import { ago, formatBytes, timestamp } from '@/lib/format'
import type { ClipboardItem, ClipboardList } from '@/lib/types'
import { api, apiUpload } from '@/api/client'
import { overviewKey } from '@/api/data'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { SegButton, Segmented } from '@/components/ui/segmented'
import { Switch } from '@/components/ui/switch'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { PageShell } from '@/components/page-shell'

const DIRECT_TYPES = new Set(['image/png', 'image/jpeg', 'image/gif'])

const SKILL_TEMPLATE_EN = `---
name: fdsync-clipboard
description: Exchange screenshots and clipboard content between Windows and WSL via \`fdsync clip\`. Use whenever the user asks to view, inspect, compare, or analyze their latest Windows screenshot(s) or panel-selected image(s), or asks to send an image/text back to Windows.
---

# Exchanging Screenshots via \`fdsync clip\`

Run \`fdsync clip\` in WSL to download PNG images from Windows into \`/tmp/fdsync-clip/\` (then read the paths directly), or push generated images/text back to Windows.

## Reading from Windows

- User asks to inspect their screenshot or selected image(s) without giving a count:
  \`\`\`bash
  fdsync clip --json
  \`\`\`
  Downloads images selected in the Windows panel, or the latest 1 screenshot if none are selected.

- User specifies a count (e.g. "look at my latest 2 screenshots"):
  \`\`\`bash
  fdsync clip --latest 2 --json
  \`\`\`

- List available screenshots or fetch specific IDs:
  \`\`\`bash
  fdsync clip list --json
  fdsync clip get --json <id1> <id2>
  \`\`\`

## Sending Back to Windows

- Push a generated image (PNG/JPEG/GIF) to Windows (copies to Windows clipboard and shows a clickable notification that opens the default image viewer):
  \`\`\`bash
  fdsync clip push /path/to/image.png --note "result"
  \`\`\`
- Copy text to the Windows clipboard:
  \`\`\`bash
  printf '%s' "text" | fdsync clip copy
  \`\`\`

## Handling Results

1. Read every local path in \`files[].path\` in parallel.
2. Pay attention to each item's \`note\` and \`capturedAt\` timestamp.
3. If \`mode\` is \`"selected"\` and \`newerUnselected > 0\`, mention briefly that newer unselected screenshots exist on Windows.
4. If \`originalWidth\`/\`originalHeight\` are present and tiny text is hard to read, let the user know they can crop closer or disable Auto-resize in the panel.
5. Keep screenshots in \`/tmp/fdsync-clip/\`; do not copy them into the project repository.
6. Run \`fdsync clip help\` for the full CLI reference.
`

const SKILL_TEMPLATE_ZH = `---
name: fdsync-clipboard
description: 通过 \`fdsync clip\` 在 Windows 与 WSL 之间收发截图和剪贴板内容。当用户说“看看我刚截的图”“读一下最新的两张截图”“看看我指定的图片”或要把 WSL 里生成的图片/文本发回 Windows 时使用。
---

# 通过 \`fdsync clip\` 收发 Windows 截图

在 WSL 中运行 \`fdsync clip\` 从 Windows 端按需拉取 PNG 到 \`/tmp/fdsync-clip/\` 后直接读取，也可以把 WSL 生成的图片或文本发回 Windows。

## 读取 Windows 截图

- 用户没说具体数量（如“看看我刚截的图”“读一下我指定的图片”）：
  \`\`\`bash
  fdsync clip --json
  \`\`\`
  若 Windows 面板里有勾选「指定给 AI」的图片，自动拉取这些指定图片；否则默认拉取最新 1 张。

- 用户明确说了最新几张（如“读一下我最新截取的这两张图片”）：
  \`\`\`bash
  fdsync clip --latest 2 --json
  \`\`\`

- 查看清单或按 ID 指定：
  \`\`\`bash
  fdsync clip list --json
  fdsync clip get --json <id1> <id2>
  \`\`\`

## 反向发送到 Windows

- 发送 WSL 里的图片到 Windows（自动写入 Windows 剪贴板并弹出系统通知，点击通知直接用默认图片工具打开）：
  \`\`\`bash
  fdsync clip push /path/to/image.png --note "结果图"
  \`\`\`
- 把文本直接复制到 Windows 剪贴板：
  \`\`\`bash
  printf '%s' "内容" | fdsync clip copy
  \`\`\`

## 注意事项

1. 解析 JSON 中的 \`files[].path\`，并行读取全部下载下来的 PNG 图片。
2. 留意每张图的 \`note\`（面板备注）以及 \`capturedAt\` 时间。
3. 若返回中 \`mode\` 为 \`"selected"\` 且 \`newerUnselected > 0\`，顺带提醒用户 Windows 端有更新的未指定截图。
4. 若存在 \`originalWidth\`/\`originalHeight\` 且小字模糊，可提醒用户局部放大截取或在面板关闭大图缩放。
5. 仅从 \`/tmp/fdsync-clip/\` 读取，不要把截图复制到项目代码仓库中；完整参考运行 \`fdsync clip help\`。
`

/** Converts browser-supported images (e.g. WebP/BMP) to PNG when needed. */
async function normalizeForUpload(file: Blob): Promise<Blob> {
  if (DIRECT_TYPES.has(file.type)) return file
  const bitmap = await createImageBitmap(file)
  try {
    const canvas = document.createElement('canvas')
    canvas.width = bitmap.width
    canvas.height = bitmap.height
    const ctx = canvas.getContext('2d')
    if (!ctx) throw new Error('canvas unavailable')
    ctx.drawImage(bitmap, 0, 0)
    const png = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, 'image/png'))
    if (!png) throw new Error('failed to encode PNG')
    return png
  } finally {
    bitmap.close()
  }
}

export function Clipboard() {
  const { t, i18n } = useTranslation()
  const qc = useQueryClient()
  const fileInput = useRef<HTMLInputElement>(null)
  const [filter, setFilter] = useState<'all' | 'selected'>('all')
  const [previewId, setPreviewId] = useState<string>()
  const [confirmClear, setConfirmClear] = useState(false)
  const [dragging, setDragging] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [editingNoteId, setEditingNoteId] = useState<string>()
  const [noteDraft, setNoteDraft] = useState('')
  const [copiedCmd, setCopiedCmd] = useState<string>()
  const [skillOpen, setSkillOpen] = useState(false)
  const [copiedSkill, setCopiedSkill] = useState(false)
  const skillText = i18n.language === 'zh-CN' ? SKILL_TEMPLATE_ZH : SKILL_TEMPLATE_EN

  const list = useQuery({
    queryKey: ['clipboard'],
    queryFn: () => api<ClipboardList>('GET', '/api/clipboard'),
    refetchInterval: 2000,
  })

  const items = useMemo(() => list.data?.items ?? [], [list.data?.items])
  const paused = list.data?.paused ?? false
  const compress = list.data?.compress ?? true
  const autoUnselect = list.data?.autoUnselect ?? false
  const maxEdge = list.data?.maxEdge ?? 2048
  const maxItems = list.data?.max ?? 20
  const selectedCount = useMemo(() => items.filter((it) => it.selected).length, [items])
  const visible = useMemo(() => (filter === 'selected' ? items.filter((it) => it.selected) : items), [items, filter])
  const previewIndex = useMemo(() => (previewId ? visible.findIndex((it) => it.id === previewId) : -1), [visible, previewId])
  const preview = previewIndex >= 0 ? visible[previewIndex] : items.find((it) => it.id === previewId)

  const invalidate = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ['clipboard'] })
    void qc.invalidateQueries({ queryKey: overviewKey })
  }, [qc])

  const onError = useCallback(
    (e: unknown) => {
      toast.error(t('errors.actionFailed'), { description: e instanceof Error ? e.message : String(e) })
    },
    [t]
  )

  const uploadFiles = useCallback(
    async (files: FileList | Blob[]) => {
      const listFiles = [...files].filter((f) => f.type.startsWith('image/'))
      if (listFiles.length === 0) return
      setUploading(true)
      try {
        for (const f of listFiles) {
          const blob = await normalizeForUpload(f)
          await apiUpload<ClipboardItem>('/api/clipboard/upload?select=1', blob)
        }
        invalidate()
        toast.success(t('clipboard.added'))
      } catch (e) {
        onError(e)
      } finally {
        setUploading(false)
      }
    },
    [invalidate, onError, t]
  )

  const toggleSelect = useCallback(
    async (item: ClipboardItem) => {
      try {
        await api<ClipboardItem>('PATCH', `/api/clipboard/${encodeURIComponent(item.id)}`, { selected: !item.selected })
        invalidate()
      } catch (e) {
        onError(e)
      }
    },
    [invalidate, onError]
  )

  const removeItem = useCallback(
    async (id: string) => {
      try {
        await api('DELETE', `/api/clipboard/${encodeURIComponent(id)}`)
        if (previewId === id) {
          const idx = visible.findIndex((x) => x.id === id)
          const next = visible[idx + 1] ?? visible[idx - 1]
          setPreviewId(next?.id)
        }
        invalidate()
        toast.success(t('clipboard.deleted'))
      } catch (e) {
        onError(e)
      }
    },
    [invalidate, onError, previewId, t, visible]
  )

  // Allow pasting an image directly into the Clipboard panel with Ctrl+V.
  useEffect(() => {
    const onPaste = (e: ClipboardEvent) => {
      const target = e.target as HTMLElement | null
      if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return
      const files = [...(e.clipboardData?.files ?? [])].filter((f) => f.type.startsWith('image/'))
      if (files.length === 0) return
      e.preventDefault()
      void uploadFiles(files)
    }
    window.addEventListener('paste', onPaste)
    return () => window.removeEventListener('paste', onPaste)
  }, [uploadFiles])

  // Keyboard shortcuts inside the full-size preview lightbox: ← → Space Delete.
  useEffect(() => {
    if (!preview) return
    const onKeyDown = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null
      if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return
      if (e.key === 'ArrowLeft' && previewIndex > 0) {
        e.preventDefault()
        setPreviewId(visible[previewIndex - 1].id)
      } else if (e.key === 'ArrowRight' && previewIndex >= 0 && previewIndex < visible.length - 1) {
        e.preventDefault()
        setPreviewId(visible[previewIndex + 1].id)
      } else if (e.key === ' ') {
        e.preventDefault()
        void toggleSelect(preview)
      } else if (e.key === 'Delete') {
        e.preventDefault()
        void removeItem(preview.id)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [preview, previewIndex, removeItem, toggleSelect, visible])

  const saveNote = async (id: string, note: string) => {
    try {
      await api<ClipboardItem>('PATCH', `/api/clipboard/${encodeURIComponent(id)}`, { note })
      setEditingNoteId(undefined)
      invalidate()
    } catch (e) {
      onError(e)
    }
  }

  const clearSelection = async () => {
    try {
      await api('POST', '/api/clipboard/unselect')
      invalidate()
      toast.success(t('clipboard.selectionCleared'))
    } catch (e) {
      onError(e)
    }
  }

  const clearAll = async () => {
    setBusy(true)
    try {
      await api('DELETE', '/api/clipboard')
      setConfirmClear(false)
      setPreviewId(undefined)
      invalidate()
      toast.success(t('clipboard.cleared'))
    } catch (e) {
      onError(e)
    } finally {
      setBusy(false)
    }
  }

  const setCapturePaused = async (nextPaused: boolean) => {
    try {
      await api('PUT', '/api/clipboard/pause', { paused: nextPaused })
      invalidate()
      toast.success(nextPaused ? t('clipboard.captureOff') : t('clipboard.captureOn'))
    } catch (e) {
      onError(e)
    }
  }

  const setCompressMode = async (nextCompress: boolean) => {
    try {
      await api('PUT', '/api/clipboard/options', { compress: nextCompress })
      invalidate()
      toast.success(nextCompress ? t('clipboard.compressOn', { edge: maxEdge }) : t('clipboard.compressOff'))
    } catch (e) {
      onError(e)
    }
  }

  const setAutoUnselectMode = async (nextOn: boolean) => {
    try {
      await api('PUT', '/api/clipboard/options', { autoUnselect: nextOn })
      invalidate()
      toast.success(nextOn ? t('clipboard.autoUnselectOn') : t('clipboard.autoUnselectOff'))
    } catch (e) {
      onError(e)
    }
  }

  const copyCommand = (cmd: string) => {
    void navigator.clipboard?.writeText(cmd)
    setCopiedCmd(cmd)
    setTimeout(() => setCopiedCmd((cur) => (cur === cmd ? undefined : cur)), 1500)
  }

  const copySkill = () => {
    void navigator.clipboard?.writeText(skillText)
    setCopiedSkill(true)
    toast.success(t('clipboard.skillCopied'))
    setTimeout(() => setCopiedSkill(false), 1800)
  }

  const startEditNote = (item: ClipboardItem) => {
    setEditingNoteId(item.id)
    setNoteDraft(item.note ?? '')
  }

  const sourceLabel = (src: ClipboardItem['source']) => {
    if (src === 'wsl') return t('clipboard.sourceWsl')
    if (src === 'upload') return t('clipboard.sourceUpload')
    return t('clipboard.sourceClipboard')
  }

  return (
    <PageShell
      title={t('clipboard.title')}
      description={t('clipboard.description')}
      breadcrumb={`${t('nav.sync')} / ${t('nav.clipboard')}`}
      actions={
        <>
          <label className='flex cursor-pointer items-center gap-2 rounded-md border bg-card px-3 py-1.5 text-sm'>
            <Switch checked={!paused} onCheckedChange={(on) => void setCapturePaused(!on)} aria-label={t('clipboard.capturing')} />
            <span>{t('clipboard.capturing')}</span>
          </label>
          <label
            className='flex cursor-pointer items-center gap-2 rounded-md border bg-card px-3 py-1.5 text-sm'
            title={t('clipboard.compressHint', { edge: maxEdge })}
          >
            <Switch
              checked={compress}
              onCheckedChange={(on) => void setCompressMode(on)}
              aria-label={t('clipboard.compress', { edge: maxEdge })}
            />
            <span>{t('clipboard.compress', { edge: maxEdge })}</span>
          </label>
          <label
            className='flex cursor-pointer items-center gap-2 rounded-md border bg-card px-3 py-1.5 text-sm'
            title={t('clipboard.autoUnselectHint')}
          >
            <Switch
              checked={autoUnselect}
              onCheckedChange={(on) => void setAutoUnselectMode(on)}
              aria-label={t('clipboard.autoUnselect')}
            />
            <span>{t('clipboard.autoUnselect')}</span>
          </label>
          <input
            ref={fileInput}
            type='file'
            accept='image/*'
            multiple
            className='hidden'
            onChange={(e) => {
              if (e.target.files?.length) void uploadFiles(e.target.files)
              e.target.value = ''
            }}
          />
          <Button variant='outline' size='sm' onClick={() => fileInput.current?.click()} disabled={uploading}>
            {uploading ? <Loader2 className='animate-spin' /> : <ImagePlus />} {t('clipboard.addImage')}
          </Button>
          <Button variant='outline' size='sm' onClick={() => setConfirmClear(true)} disabled={items.length === 0}>
            <Trash2 /> {t('clipboard.clearAll')}
          </Button>
          <Button variant='outline' size='sm' onClick={() => void list.refetch()} disabled={list.isFetching}>
            <RefreshCw className={cn(list.isFetching && 'animate-spin')} /> {t('clipboard.refresh')}
          </Button>
        </>
      }
    >
      <div
        className='relative flex flex-col gap-4'
        onDragOver={(e) => {
          if (e.dataTransfer.types.includes('Files')) {
            e.preventDefault()
            setDragging(true)
          }
        }}
        onDragLeave={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false)
        }}
        onDrop={(e) => {
          e.preventDefault()
          setDragging(false)
          if (e.dataTransfer.files?.length) void uploadFiles(e.dataTransfer.files)
        }}
      >
        {dragging && (
          <div className='pointer-events-none absolute inset-0 z-20 flex flex-col items-center justify-center gap-2 rounded-xl border-2 border-dashed border-primary bg-background/85 backdrop-blur-xs'>
            <Upload className='size-8 text-primary' />
            <p className='text-sm font-medium'>{t('clipboard.dropHint')}</p>
          </div>
        )}

        {paused && (
          <div className='flex items-center gap-2 rounded-lg border border-amber-500/30 bg-amber-500/5 px-3.5 py-2.5 text-sm'>
            <PauseCircle className='size-4 shrink-0 text-amber-500' />
            <span>{t('clipboard.pausedHint')}</span>
          </div>
        )}

        {selectedCount > 0 && (
          <div className='flex flex-wrap items-center justify-between gap-2 rounded-lg border border-primary/30 bg-primary/5 px-3.5 py-2.5 text-sm'>
            <div className='flex items-center gap-2 font-medium'>
              <Pin className='size-4 shrink-0 text-primary' />
              <span>{t('clipboard.selectedBanner', { count: selectedCount })}</span>
            </div>
            <Button variant='outline' size='sm' className='h-7' onClick={() => void clearSelection()}>
              <PinOff /> {t('clipboard.clearSelection')}
            </Button>
          </div>
        )}

        <div className='flex flex-wrap items-center justify-between gap-3'>
          <Segmented>
            <SegButton on={filter === 'all'} onClick={() => setFilter('all')}>
              {t('clipboard.filterAll', { count: items.length })}
            </SegButton>
            <SegButton on={filter === 'selected'} onClick={() => setFilter('selected')}>
              <Pin /> {t('clipboard.filterSelected', { count: selectedCount })}
            </SegButton>
          </Segmented>
          <p className='text-xs text-muted-foreground'>{t('clipboard.memoryHint', { max: maxItems })}</p>
        </div>

        {list.isLoading ? (
          <div className='flex items-center justify-center gap-2 py-20 text-sm text-muted-foreground'>
            <Loader2 className='size-4 animate-spin' /> {t('viewer.loading')}
          </div>
        ) : visible.length === 0 ? (
          <div className='flex flex-col items-center justify-center gap-2 rounded-xl border border-dashed bg-card/50 px-6 py-16 text-center'>
            <Images className='size-10 text-muted-foreground/50' />
            <p className='text-sm font-medium'>{filter === 'selected' ? t('clipboard.emptySelected') : t('clipboard.empty')}</p>
            <p className='max-w-md text-xs text-muted-foreground'>
              {filter === 'selected' ? t('clipboard.emptySelectedHint') : t('clipboard.emptyHint')}
            </p>
            {filter === 'all' && (
              <Button variant='outline' size='sm' className='mt-2' onClick={() => fileInput.current?.click()}>
                <ImagePlus /> {t('clipboard.addImage')}
              </Button>
            )}
          </div>
        ) : (
          <div className='grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4'>
            {visible.map((item) => {
              const imgURL = `/api/clipboard/${encodeURIComponent(item.id)}?h=${item.hash}`
              const isEditing = editingNoteId === item.id
              return (
                <div
                  key={item.id}
                  className={cn(
                    'group flex flex-col overflow-hidden rounded-xl border bg-card transition-shadow',
                    item.selected ? 'border-primary/60 ring-2 ring-primary/25' : 'hover:border-foreground/20'
                  )}
                >
                  {/* Thumbnail */}
                  <div className='relative flex h-44 items-center justify-center overflow-hidden bg-muted/50'>
                    <button
                      type='button'
                      onClick={() => setPreviewId(item.id)}
                      className='flex size-full cursor-zoom-in items-center justify-center p-2'
                      aria-label={t('clipboard.preview')}
                    >
                      <img src={imgURL} alt={item.note || item.id} loading='lazy' className='max-h-full max-w-full rounded object-contain shadow-xs' />
                    </button>
                    <div className='absolute start-2 top-2 flex items-center gap-1'>
                      <Button
                        size='sm'
                        variant={item.selected ? 'default' : 'secondary'}
                        className='h-7 gap-1 px-2 text-xs shadow-xs'
                        onClick={() => void toggleSelect(item)}
                      >
                        <Pin className={cn('size-3.5', item.selected && 'fill-current')} />
                        {item.selected ? t('clipboard.selectedForAi') : t('clipboard.selectForAi')}
                      </Button>
                    </div>
                    <Button
                      size='icon'
                      variant='secondary'
                      className='absolute end-2 top-2 size-7 opacity-85 shadow-xs hover:bg-destructive hover:text-destructive-foreground'
                      onClick={() => void removeItem(item.id)}
                      title={t('clipboard.remove')}
                      aria-label={t('clipboard.remove')}
                    >
                      <Trash2 className='size-3.5' />
                    </Button>
                  </div>

                  {/* Metadata & note */}
                  <div className='flex flex-1 flex-col gap-2 p-3'>
                    <div className='flex items-center justify-between gap-2 text-xs'>
                      <span className='font-mono text-muted-foreground'>{item.id}</span>
                      <div className='flex items-center gap-1'>
                        {item.lastReadAt && (
                          <Badge
                            variant='secondary'
                            className='h-5 gap-1 px-1.5 text-[10px] font-normal text-emerald-700 dark:text-emerald-300'
                            title={timestamp(item.lastReadAt)}
                          >
                            <Eye className='size-3' />
                            {t('clipboard.readByAi', { ago: ago(item.lastReadAt) })}
                          </Badge>
                        )}
                        <Badge
                          variant='outline'
                          className={cn(
                            'h-5 px-1.5 text-[10px] font-normal',
                            item.source === 'wsl' && 'border-sky-500/40 text-sky-600 dark:text-sky-400'
                          )}
                        >
                          {sourceLabel(item.source)}
                        </Badge>
                      </div>
                    </div>
                    <div className='flex items-center justify-between gap-2 text-xs text-muted-foreground tabular-nums'>
                      <span
                        title={
                          item.originalWidth && item.originalHeight
                            ? t('clipboard.resizedFrom', { w: item.originalWidth, h: item.originalHeight })
                            : undefined
                        }
                      >
                        {item.width} × {item.height}
                        {item.originalWidth && item.originalHeight ? ' ↘' : ''} · {formatBytes(item.size)}
                      </span>
                      <span title={timestamp(item.capturedAt)}>{ago(item.capturedAt)}</span>
                    </div>

                    {isEditing ? (
                      <form
                        className='flex items-center gap-1'
                        onSubmit={(e) => {
                          e.preventDefault()
                          void saveNote(item.id, noteDraft)
                        }}
                      >
                        <Input
                          value={noteDraft}
                          onChange={(e) => setNoteDraft(e.target.value)}
                          placeholder={t('clipboard.notePlaceholder')}
                          maxLength={120}
                          autoFocus
                          className='h-7 text-xs'
                        />
                        <Button type='submit' size='icon' variant='ghost' className='size-7 shrink-0' aria-label={t('clipboard.saveNote')}>
                          <Check className='size-3.5' />
                        </Button>
                        <Button
                          type='button'
                          size='icon'
                          variant='ghost'
                          className='size-7 shrink-0'
                          onClick={() => setEditingNoteId(undefined)}
                          aria-label={t('common.cancel')}
                        >
                          <X className='size-3.5' />
                        </Button>
                      </form>
                    ) : (
                      <button
                        type='button'
                        onClick={() => startEditNote(item)}
                        className='flex items-center gap-1.5 rounded px-1.5 py-1 text-start text-xs text-muted-foreground hover:bg-muted/60 hover:text-foreground'
                      >
                        <Pencil className='size-3 shrink-0 opacity-60' />
                        <span className={cn('truncate', item.note && 'text-foreground')}>{item.note || t('clipboard.notePlaceholder')}</span>
                      </button>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        )}

        {/* WSL / AI CLI quick reference & copyable Skill */}
        <div className='space-y-4 rounded-lg border bg-card/60 p-4'>
          <div className='flex items-center gap-2 text-sm font-medium'>
            <Terminal className='size-4 text-muted-foreground' />
            <span>{t('clipboard.cliTitle')}</span>
          </div>
          <div className='grid gap-2 sm:grid-cols-2 lg:grid-cols-3'>
            {[
              { cmd: 'fdsync clip', desc: t('clipboard.cliDefault') },
              { cmd: 'fdsync clip --latest 2', desc: t('clipboard.cliLatest') },
              { cmd: 'fdsync clip list', desc: t('clipboard.cliList') },
              { cmd: 'fdsync clip push image.png', desc: t('clipboard.cliPush') },
              { cmd: 'echo "hi" | fdsync clip copy', desc: t('clipboard.cliCopy') },
              { cmd: 'fdsync clip help', desc: t('clipboard.cliHelp') },
            ].map((row) => (
              <div key={row.cmd} className='flex items-start justify-between gap-2 rounded-md border bg-background px-3 py-2'>
                <div className='min-w-0'>
                  <code className='block truncate font-mono text-xs font-medium'>{row.cmd}</code>
                  <p className='mt-0.5 text-[11px] text-muted-foreground'>{row.desc}</p>
                </div>
                <Button
                  size='icon'
                  variant='ghost'
                  className='size-7 shrink-0'
                  onClick={() => copyCommand(row.cmd)}
                  aria-label={t('browser.menu.copy')}
                >
                  {copiedCmd === row.cmd ? <Check className='size-3.5 text-emerald-500' /> : <Copy className='size-3.5' />}
                </Button>
              </div>
            ))}
          </div>

          <div className='border-t pt-3'>
            <div className='flex flex-wrap items-center justify-between gap-2'>
              <div className='min-w-0'>
                <div className='flex items-center gap-1.5 text-sm font-medium'>
                  <Sparkles className='size-4 text-primary' />
                  <span>{t('clipboard.skillTitle')}</span>
                </div>
                <p className='mt-0.5 text-xs text-muted-foreground'>{t('clipboard.skillDesc')}</p>
              </div>
              <div className='flex items-center gap-2'>
                <Button size='sm' variant='outline' onClick={() => setSkillOpen((v) => !v)}>
                  {skillOpen ? <ChevronUp /> : <ChevronDown />}
                  {skillOpen ? t('clipboard.skillHide') : t('clipboard.skillShow')}
                </Button>
                <Button size='sm' onClick={copySkill}>
                  {copiedSkill ? <Check /> : <Copy />}
                  {copiedSkill ? t('common.copied') : t('clipboard.skillCopy')}
                </Button>
              </div>
            </div>
            {skillOpen && (
              <pre className='mt-3 max-h-72 overflow-auto rounded-md border bg-background p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap'>
                {skillText}
              </pre>
            )}
          </div>
        </div>
      </div>

      {/* Full-size preview lightbox with keyboard navigation */}
      <Dialog open={!!preview} onOpenChange={(open) => !open && setPreviewId(undefined)}>
        <DialogContent className='sm:max-w-4xl'>
          {preview && (
            <>
              <DialogHeader>
                <DialogTitle className='flex flex-wrap items-center gap-2 font-mono text-base'>
                  <span>{preview.id}</span>
                  {previewIndex >= 0 && (
                    <span className='text-xs font-normal text-muted-foreground tabular-nums'>
                      ({previewIndex + 1} / {visible.length})
                    </span>
                  )}
                  {preview.selected && <Badge>{t('clipboard.selectedForAi')}</Badge>}
                  {preview.lastReadAt && (
                    <Badge variant='secondary' className='gap-1 font-sans text-xs font-normal text-emerald-700 dark:text-emerald-300'>
                      <Eye className='size-3' />
                      {t('clipboard.readByAi', { ago: ago(preview.lastReadAt) })}
                    </Badge>
                  )}
                </DialogTitle>
                <DialogDescription className='tabular-nums'>
                  {preview.width} × {preview.height}
                  {preview.originalWidth && preview.originalHeight
                    ? ` (${t('clipboard.resizedFrom', { w: preview.originalWidth, h: preview.originalHeight })})`
                    : ''}{' '}
                  · {formatBytes(preview.size)} · {timestamp(preview.capturedAt)}
                  {preview.note ? ` · ${preview.note}` : ''}
                </DialogDescription>
              </DialogHeader>
              <div className='relative flex max-h-[65vh] items-center justify-center overflow-auto rounded-lg border bg-muted/40 p-3'>
                {previewIndex > 0 && (
                  <Button
                    size='icon'
                    variant='secondary'
                    className='absolute start-2 top-1/2 z-10 size-8 -translate-y-1/2 opacity-85 shadow-sm'
                    onClick={() => setPreviewId(visible[previewIndex - 1].id)}
                    aria-label={t('clipboard.prev')}
                  >
                    <ChevronLeft className='size-4' />
                  </Button>
                )}
                <img
                  src={`/api/clipboard/${encodeURIComponent(preview.id)}?h=${preview.hash}`}
                  alt={preview.note || preview.id}
                  className='max-h-[60vh] max-w-full rounded object-contain'
                />
                {previewIndex >= 0 && previewIndex < visible.length - 1 && (
                  <Button
                    size='icon'
                    variant='secondary'
                    className='absolute end-2 top-1/2 z-10 size-8 -translate-y-1/2 opacity-85 shadow-sm'
                    onClick={() => setPreviewId(visible[previewIndex + 1].id)}
                    aria-label={t('clipboard.next')}
                  >
                    <ChevronRight className='size-4' />
                  </Button>
                )}
              </div>
              <div className='flex flex-wrap items-center justify-between gap-2'>
                <div className='flex flex-wrap items-center gap-2'>
                  <Button
                    size='sm'
                    variant={preview.selected ? 'default' : 'outline'}
                    onClick={() => void toggleSelect(preview)}
                  >
                    <Pin className={cn(preview.selected && 'fill-current')} />
                    {preview.selected ? t('clipboard.selectedForAi') : t('clipboard.selectForAi')}
                  </Button>
                  <Button size='sm' variant='outline' asChild>
                    <a href={`/api/clipboard/${encodeURIComponent(preview.id)}`} download={`${preview.id}.png`}>
                      <Download /> {t('clipboard.download')}
                    </a>
                  </Button>
                </div>
                <span className='hidden text-xs text-muted-foreground sm:inline'>{t('clipboard.lightboxKeys')}</span>
                <Button size='sm' variant='destructive' onClick={() => void removeItem(preview.id)}>
                  <Trash2 /> {t('clipboard.remove')}
                </Button>
              </div>
            </>
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={confirmClear}
        onOpenChange={setConfirmClear}
        title={t('clipboard.clearTitle')}
        desc={t('clipboard.clearDesc', { count: items.length })}
        confirmText={t('clipboard.clearConfirm')}
        destructive
        isLoading={busy}
        handleConfirm={clearAll}
      />
    </PageShell>
  )
}
