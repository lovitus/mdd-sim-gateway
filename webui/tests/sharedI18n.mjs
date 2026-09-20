import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'
import { parse } from '@babel/parser'
import { defaultRoot } from '../scripts/source-graph.mjs'

// Use the imports actually mounted by main and its shared controls. This is a
// real React-context test, not an assertion that two dictionaries look alike.
const importedFrom = (file, name) => {
  const ast = parse(fs.readFileSync(path.join(defaultRoot, file), 'utf8'), { sourceType: 'module', plugins: ['jsx'] })
  const node = ast.program.body.find(item => item.type === 'ImportDeclaration' &&
    item.specifiers.some(specifier => specifier.local.name === name))
  assert.ok(node, `${file} must import ${name}`)
  return path.resolve(defaultRoot, path.dirname(file), node.source.value)
}
const previousStorage = globalThis.localStorage
const server = await createServer({ root: defaultRoot, logLevel: 'silent', server: { middlewareMode: true, hmr: false }, appType: 'custom' })
try {
  const { I18nProvider } = await server.ssrLoadModule(importedFrom('src/main.jsx', 'I18nProvider'))
  for (const language of ['zh', 'en']) {
    globalThis.localStorage = { getItem: () => language, setItem() {} }
    for (const consumer of ['src/goCallCoordinator.jsx']) {
      const { useI18n } = await server.ssrLoadModule(importedFrom(consumer, 'useI18n'))
      for (const [key, translated] of Object.entries({ Cancel: '取消', Unknown: '未知', Answer: '接听', Reject: '拒接', 'Hang up': '挂断' })) {
        function Probe() { const value = useI18n(); return React.createElement('span', null, `${value.language}:${value.t(key)}`) }
        const output = renderToStaticMarkup(React.createElement(I18nProvider, null, React.createElement(Probe)))
        assert.equal(output, `<span>${language}:${language === 'zh' ? translated : key}</span>`,
          `${consumer} must use the mounted language context`)
      }
    }
  }
} finally {
  globalThis.localStorage = previousStorage
  await server.close()
}
console.log('Mounted call controls share the actual language provider')
