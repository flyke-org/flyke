// Monaco is bundled (not loaded from a CDN) so the dashboard embedded in the
// flyke binary works offline. Only the Playground route imports this module.

import { loader } from "@monaco-editor/react";
import * as monaco from "monaco-editor/esm/vs/editor/editor.api";
import "monaco-editor/esm/vs/editor/editor.all";
import "monaco-editor/esm/vs/basic-languages/python/python.contribution";
import "monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution";
import "monaco-editor/esm/vs/basic-languages/markdown/markdown.contribution";
import "monaco-editor/esm/vs/basic-languages/ini/ini.contribution";
import "monaco-editor/esm/vs/language/json/monaco.contribution";
import EditorWorker from "monaco-editor/esm/vs/editor/editor.worker?worker";
import JsonWorker from "monaco-editor/esm/vs/language/json/json.worker?worker";

self.MonacoEnvironment = {
  getWorker: (_id: string, label: string) => (label === "json" ? new JsonWorker() : new EditorWorker()),
};

// Editor themes that sit on the dashboard's surfaces (see styles.css tokens).
monaco.editor.defineTheme("flyke-light", {
  base: "vs", inherit: true, rules: [],
  colors: { "editor.background": "#ffffff", "editor.lineHighlightBackground": "#f2f2f9", "editorLineNumber.foreground": "#a3a3b8", "editorCursor.foreground": "#5b4bf5", "editor.selectionBackground": "#d9d4ff" },
});
monaco.editor.defineTheme("flyke-dark", {
  base: "vs-dark", inherit: true, rules: [],
  colors: { "editor.background": "#11111c", "editor.lineHighlightBackground": "#181826", "editorLineNumber.foreground": "#66667f", "editorCursor.foreground": "#7c6cff", "editor.selectionBackground": "#3a3180", "editorWidget.background": "#181826", "editorWidget.border": "#36364f" },
});

loader.config({ monaco });

export { monaco };

export function languageOf(path: string): string {
  const ext = path.slice(path.lastIndexOf(".") + 1).toLowerCase();
  return ({ py: "python", json: "json", yaml: "yaml", yml: "yaml", md: "markdown", toml: "ini", cfg: "ini", ini: "ini", txt: "plaintext" } as Record<string, string>)[ext] ?? "plaintext";
}

type Snippet = { label: string; detail: string; insert: string };

// Monaco has no Python language server; a few Flyke-aware snippets help.
const SNIPPETS: Snippet[] = [
  {
    label: "workflow", detail: "Flyke: new @workflow class",
    insert: "@workflow\nclass ${1:MyWorkflow}:\n    ${2:name}: ${3:str} = ${4:\"world\"}\n\n    async def run(self) -> ${5:str}:\n        ${0:return await self.step()}",
  },
  {
    label: "task", detail: "Flyke: new @task method",
    insert: "@task(retry=Retry(attempts=${1:3}))\nasync def ${2:step}(self${3}) -> ${4:str}:\n    self.log.info(\"${2:step}\")\n    ${0:return \"done\"}",
  },
  { label: "fimport", detail: "Flyke: import the SDK", insert: "from flyke import Retry, task, workflow\n" },
  { label: "self.wait_for", detail: "Durable wait for an event", insert: "await self.wait_for(\"${1:event.name}\", where={${2}}, timeout=\"${3:1h}\")" },
  { label: "self.sleep", detail: "Durable timer", insert: "await self.sleep(\"${1:10s}\")" },
  { label: "self.emit", detail: "Publish an event when the step commits", insert: "self.emit(\"${1:event.name}\", ${2:key=value})" },
  { label: "self.log.info", detail: "Structured log line", insert: "self.log.info(\"${1:message}\"${2:, key=value})" },
  { label: "gather", detail: "Run tasks in parallel", insert: "await asyncio.gather(${1:self.a(), self.b()})" },
];

let registered = false;

export function registerFlykeLanguageFeatures() {
  if (registered) return;
  registered = true;
  monaco.languages.registerCompletionItemProvider("python", {
    provideCompletionItems(model, position) {
      const word = model.getWordUntilPosition(position);
      const range = {
        startLineNumber: position.lineNumber, endLineNumber: position.lineNumber,
        startColumn: word.startColumn, endColumn: word.endColumn,
      };
      return {
        suggestions: SNIPPETS.map((s) => ({
          label: s.label,
          kind: monaco.languages.CompletionItemKind.Snippet,
          detail: s.detail,
          insertText: s.insert,
          insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet,
          range,
        })),
      };
    },
  });
}
