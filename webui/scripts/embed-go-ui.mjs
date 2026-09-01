import fs from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const webRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const repository = path.resolve(webRoot, '..')
const source = process.argv[2] ? path.resolve(webRoot, process.argv[2]) : path.join(webRoot, 'dist')
const cleanSource = process.argv.includes('--clean')
const destination = path.join(repository, 'go-runtime', 'internal', 'webui', 'assets')
const jsqrLicense = path.join(webRoot, 'node_modules', 'jsqr', 'LICENSE')
const allowed = new Set(['.html', '.js', '.css', '.svg', '.ttf', '.woff', '.woff2', '.png', '.ico'])

async function files(root, prefix = '') {
  const result = []
  for (const entry of await fs.readdir(path.join(root, prefix), { withFileTypes: true })) {
    if (entry.isSymbolicLink()) throw new Error(`embedded UI contains a symlink: ${path.join(prefix, entry.name)}`)
    const relative = path.join(prefix, entry.name)
    if (entry.isDirectory()) result.push(...await files(root, relative))
    else if (entry.isFile()) result.push(relative)
    else throw new Error(`embedded UI contains an unsupported entry: ${relative}`)
  }
  return result.sort()
}

const entries = await files(source)
if (!entries.includes('index.html') || !entries.includes(path.join('assets', 'app.js')) ||
    !entries.includes(path.join('assets', 'app.css'))) throw new Error('Vite output is missing its fixed entry assets')
for (const relative of entries) {
  const extension = path.extname(relative).toLowerCase()
  if (!allowed.has(extension)) throw new Error(`unsupported embedded UI asset: ${relative}`)
  const info = await fs.stat(path.join(source, relative))
  if (info.size < 1 || info.size > 8 * 1024 * 1024) throw new Error(`invalid embedded UI asset size: ${relative}`)
}
const html = await fs.readFile(path.join(source, 'index.html'), 'utf8')
if (!html.includes('/assets/app.js') || !html.includes('/assets/app.css') || /<script(?![^>]*\bsrc=)/i.test(html))
  throw new Error('Vite index does not satisfy the external-script CSP contract')

await fs.rm(destination, { recursive: true, force: true })
await fs.mkdir(destination, { recursive: true, mode: 0o755 })
for (const relative of entries) {
  const target = path.join(destination, relative)
  await fs.mkdir(path.dirname(target), { recursive: true, mode: 0o755 })
  await fs.copyFile(path.join(source, relative), target)
}
await fs.mkdir(path.join(destination, 'licenses'), { recursive: true, mode: 0o755 })
const license = (await fs.readFile(jsqrLicense, 'utf8')).replace(/\s+$/, '') + '\n'
await fs.writeFile(path.join(destination, 'licenses', 'jsqr-Apache-2.0.txt'), license, { mode: 0o644 })
if (cleanSource) await fs.rm(source, { recursive: true, force: true })

console.log(`Embedded ${entries.length} deterministic React assets for the Go runtime`)
