import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { spawnSync } from 'node:child_process'

const file = new URL('../go-runtime/release/install-macos-agent.sh', import.meta.url)
const source = fs.readFileSync(file, 'utf8')
const definition = name => {
  const match = source.match(new RegExp(`^${name}\\(\\) \\{\\n[\\s\\S]*?^\\}`, 'm'))
  assert.ok(match, `missing shipped function ${name}`)
  return match[0]
}
const root = fs.mkdtempSync(path.join(os.tmpdir(), 'mdd-cutover-test-'))
try {
  fs.writeFileSync(path.join(root, 'clock'), '10')
  const run = body => {
    const script = `set -eu\nroot=${JSON.stringify(root)}\n` + body
    const result = spawnSync('sh', ['-c', script], { encoding: 'utf8', timeout: 2000 })
    assert.equal(result.status, 0, result.stderr || result.stdout)
    return result.stdout
  }
  const output = run(`${definition('wait_cutover_delay')}
  date() { cat "$root/clock"; }
  sleep() { echo "$1"; echo $(( $(cat "$root/clock") + $1 )) > "$root/clock"; }
  deadline=18; delay=1
  while wait_cutover_delay; do :; done
  test "$(cat "$root/clock")" -eq 18
  `)
  assert.equal(output.trim(), '1\n2\n4\n1', 'bounded backoff must not overshoot deadline')
  assert.ok(!definition('agent_pids').includes('ps -'))
  assert.ok(definition('agent_pids').includes('/com.mdd.agent'))
  assert.ok(!definition('stop_launch_agent').includes('kill -TERM'))
  const start = definition('start_launch_agent')
  assert.ok(start.indexOf('status --config') < start.indexOf('wait_cutover_delay'))
  const stop = definition('stop_launch_agent')
  assert.ok(stop.indexOf('running_pid=$(agent_pids)') < stop.indexOf('/bin/launchctl bootout'))
  assert.match(source, /stop_launch_agent \|\| \{[^\n]*rollback blocked/)
  console.log('macOS cutover: exact launchd label, immediate readiness, deadline-capped backoff, confirmed rollback stop passed')
} finally { fs.rmSync(root, { recursive: true, force: true }) }
