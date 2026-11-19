// Monaco is bundled locally (no CDN): the panel must work offline,
// served from the server exe.
import * as monaco from 'monaco-editor/editor/editor.api'
import Editor, { DiffEditor, loader, type EditorProps, type DiffEditorProps } from '@monaco-editor/react'
import editorWorker from 'monaco-editor/editor/editor.worker?worker'
// Icon font for the gutter +/- markers, folding arrows and widgets.
import 'monaco-editor/features/codicon/register'
// Syntax highlighting only (no language services) keeps the bundle small.
import 'monaco-editor/languages/definitions/go/register'
import 'monaco-editor/languages/definitions/typescript/register'
import 'monaco-editor/languages/definitions/javascript/register'
import 'monaco-editor/languages/definitions/markdown/register'
import 'monaco-editor/languages/definitions/yaml/register'
import 'monaco-editor/languages/definitions/python/register'
import 'monaco-editor/languages/definitions/shell/register'
import 'monaco-editor/languages/definitions/powershell/register'
import 'monaco-editor/languages/definitions/rust/register'
import 'monaco-editor/languages/definitions/ini/register'
import 'monaco-editor/languages/definitions/dockerfile/register'
import 'monaco-editor/languages/definitions/html/register'
import 'monaco-editor/languages/definitions/css/register'
import { useTheme } from '@/context/theme-provider'

self.MonacoEnvironment = {
  getWorker() {
    return new editorWorker()
  },
}
loader.config({ monaco })

// Syntax highlighting for scope rules.
monaco.languages.register({ id: 'fdsync-rules' })
monaco.languages.setMonarchTokensProvider('fdsync-rules', {
  tokenizer: {
    root: [
      [/^\s*#.*$/, 'comment'],
      [/^\s*!/, 'keyword'],
      [/\*\*|\*|\?/, 'number'],
      [/\[[^\]]*\]/, 'number'],
      [/\/\s*$/, 'type'],
      [/[^*?[\]/!]+/, 'identifier'],
      [/\//, 'delimiter'],
    ],
  },
})

const shared = {
  fontFamily: "'JetBrains Mono Variable', ui-monospace, monospace",
  fontSize: 13,
  minimap: { enabled: false },
  scrollBeyondLastLine: false,
  renderLineHighlight: 'none' as const,
  automaticLayout: true,
}

function useMonacoTheme() {
  const { resolvedTheme } = useTheme()
  return resolvedTheme === 'dark' ? 'vs-dark' : 'light'
}

export function CodeEditor(props: EditorProps) {
  const theme = useMonacoTheme()
  return <Editor theme={theme} {...props} options={{ ...shared, ...props.options }} />
}

export function CodeDiff(props: DiffEditorProps) {
  const theme = useMonacoTheme()
  return (
    <DiffEditor
      theme={theme}
      // Avoids "TextModel got disposed before DiffEditorWidget model got reset"
      // on unmount; models are reused per path, so they don't pile up.
      keepCurrentOriginalModel
      keepCurrentModifiedModel
      {...props}
      options={{
        ...shared,
        readOnly: true,
        originalEditable: false,
        renderSideBySide: true,
        ...props.options,
      }}
    />
  )
}
