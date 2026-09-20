import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { parse } from '@babel/parser'

const scriptFile = fileURLToPath(import.meta.url)
export const defaultRoot = path.resolve(path.dirname(scriptFile), '..')
const modulePattern = /\.(?:[cm]?js|jsx)$/
export function visit(node, fn) {
  if (!node || typeof node !== 'object') return
  if (typeof node.type === 'string') fn(node)
  for (const [key, value] of Object.entries(node)) {
    if (['loc', 'start', 'end', 'comments', 'tokens', 'extra'].includes(key)) continue
    if (Array.isArray(value)) value.forEach(item => visit(item, fn))
    else if (value && typeof value === 'object') visit(value, fn)
  }
}
function filesBelow(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap(item => {
    const file = path.join(dir, item.name)
    if (item.isSymbolicLink()) throw new Error(`Source symlink is not auditable: ${file}`)
    return item.isDirectory() ? filesBelow(file) : [file]
  })
}
function tree(file) {
  return parse(fs.readFileSync(file, 'utf8'), { sourceType: 'module', plugins: ['jsx'], createImportExpressions: true })
}
export function moduleReferences(ast, { moduleURLs = true } = {}) {
  const references = []
  visit(ast, node => {
    if (['ImportDeclaration', 'ExportAllDeclaration', 'ExportNamedDeclaration'].includes(node.type) && node.source)
      references.push(node.source.value)
    if (node.type === 'ImportExpression') {
      if (node.source.type !== 'StringLiteral') throw new Error('Nonliteral dynamic import needs explicit graph support')
      references.push(node.source.value)
    }
    if (node.type === 'CallExpression' && node.callee.type === 'Identifier' && node.callee.name === 'require') {
      if (node.arguments[0]?.type !== 'StringLiteral') throw new Error('Nonliteral require needs explicit graph support')
      references.push(node.arguments[0].value)
    }
    if (node.type === 'CallExpression' && node.callee.type === 'MemberExpression' &&
        node.callee.object.type === 'MetaProperty')
      throw new Error('import.meta loader needs explicit graph support')
    if (moduleURLs && node.type === 'NewExpression' && node.callee.type === 'Identifier' && node.callee.name === 'URL' &&
        node.arguments[1]?.object?.type === 'MetaProperty') {
      if (node.arguments[0]?.type !== 'StringLiteral') throw new Error('Nonliteral module URL needs explicit graph support')
      references.push(node.arguments[0].value)
    }
  })
  return references
}
export function analyze(root = defaultRoot) {
  root = path.resolve(root)
  const source = path.join(root, 'src')
  const sourceFiles = filesBelow(source)
  if (sourceFiles.some(file => /\.tsx?$/.test(file))) throw new Error('TypeScript needs explicit graph/parser support')
  const files = sourceFiles.filter(file => modulePattern.test(file)).sort()
  const errors = [], edges = new Map(), all = new Set(files)
  const relative = file => path.relative(root, file).replaceAll(path.sep, '/')
  const resolve = (from, specifier) => {
    if (specifier.startsWith('/') || specifier.startsWith('@/')) throw new Error(`Root/alias import needs explicit graph support: ${specifier}`)
    if (!specifier.startsWith('.')) return null
    const base = path.resolve(path.dirname(from), specifier.split('?')[0])
    const target = [base, ...['.js', '.jsx', '.mjs', '.cjs'].flatMap(ext => [base + ext, path.join(base, 'index' + ext)])]
      .find(file => fs.existsSync(file) && fs.statSync(file).isFile())
    if (!target) throw new Error(`Missing local import: ${relative(from)} -> ${specifier}`)
    return modulePattern.test(target) ? target : null
  }
  for (const file of files) {
    try {
      edges.set(file, moduleReferences(tree(file)).map(spec => resolve(file, spec)).filter(Boolean))
    } catch (error) { errors.push(`${relative(file)}: ${error.message}`) }
  }
  const live = new Set(), pending = [path.join(source, 'main.jsx')]
  while (pending.length) {
    const file = pending.pop()
    if (!all.has(file)) { errors.push(`Entrypoint dependency outside src: ${relative(file)}`); continue }
    if (live.has(file)) continue
    live.add(file)
    pending.push(...(edges.get(file) || []))
  }
  const unreachable = files.filter(file => !live.has(file))
  const dead = new Set(unreachable)
  const testOnlyReferences = []
  const testDir = path.join(root, 'tests')
  if (fs.existsSync(testDir)) for (const file of filesBelow(testDir).filter(file => /\.(?:mjs|js|jsx)$/.test(file))) {
    try {
      const ast = tree(file)
      const references = new Set(moduleReferences(ast, { moduleURLs: false }).map(spec => resolve(file, spec)).filter(Boolean))
      // Source-reading contract tests must not hide orphaned production modules.
      // Inspect literals too: new URL('../src/...'), read('mdd/...'), path.join(root,'src/...').
      visit(ast, node => {
        if (node.type !== 'StringLiteral') return
        for (const base of [root, source, path.dirname(file)]) {
          const target = path.resolve(base, node.value)
          if (all.has(target)) references.add(target)
        }
      })
      for (const target of references) if (dead.has(target))
        testOnlyReferences.push({ test: relative(file), source: relative(target) })
    } catch (error) { errors.push(`${relative(file)}: ${error.message}`) }
  }
  return {
    entry: 'src/main.jsx', totalModules: files.length, reachableModules: live.size,
    unreachableLines: unreachable.reduce((n, file) => n + fs.readFileSync(file, 'utf8').split('\n').length - 1, 0),
    unreachable: unreachable.map(relative), testOnlyReferences,
    reachable: [...live].sort().map(relative), errors,
  }
}
if (process.argv[1] && path.resolve(process.argv[1]) === scriptFile) {
  const report = analyze(process.argv.find(arg => arg.startsWith('--root='))?.slice(7) || defaultRoot)
  console.log(JSON.stringify(report, null, 2))
  if (process.argv.includes('--check') && (report.errors.length || report.unreachable.length || report.testOnlyReferences.length))
    process.exitCode = 1
}
