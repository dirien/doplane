import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const pages = [
  ['Getting started', 'guide/getting-started.md'],
  ['Choose an API', 'guide/choose-an-api.md'],
  ['Control loop and state', 'concepts/control-loop.md'],
  ['Dependencies and composites', 'concepts/dependencies.md'],
  ['Day-2 operations', 'operations/day-2.md'],
  ['Security and tenancy', 'operations/security.md'],
  ['Repository context for agents', 'reference/agent-context.md'],
  ['Conditions and failures', 'reference/conditions.md']
]

const site = 'https://dirien.github.io/doplane'
const summary = [
  '# doplane',
  '',
  '> Kubernetes operator that runs Pulumi under the hood: any provider or component from the Pulumi ecosystem becomes a Kubernetes-native API, with external-resource state stored in the object status.',
  '',
  'Use the pages below as task context. Preserve the invariants and verify the success conditions stated on each page.',
  '',
  '## Documentation',
  '',
  ...pages.map(([title, path]) => `- [${title}](${site}/${path.replace(/\.md$/, '')})`),
  '',
  '## Source',
  '',
  '- [Repository](https://github.com/dirien/doplane)',
  '- [Agent instructions](https://github.com/dirien/doplane/blob/main/AGENTS.md)',
  '- [Examples](https://github.com/dirien/doplane/tree/main/examples)',
  ''
].join('\n')

const sections = await Promise.all(pages.map(async ([title, path]) => {
  const markdown = await readFile(join(root, path), 'utf8')
  const body = markdown.replace(/^---[\s\S]*?---\s*/, '')
  return `# ${title}\n\nSource: ${site}/${path.replace(/\.md$/, '')}\n\n${body}`
}))

await mkdir(join(root, 'public'), { recursive: true })
await writeFile(join(root, 'public/llms.txt'), summary)
await writeFile(join(root, 'public/llms-full.txt'), `${summary}\n${sections.join('\n\n---\n\n')}`)
