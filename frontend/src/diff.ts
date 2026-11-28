// 行级 diff。策略文件通常几十到几百行，用最直白的 LCS 就够，
// 不必为了省几毫秒引入一个依赖。

export type DiffKind = 'same' | 'add' | 'del' | 'gap'

export interface DiffLine {
  kind: DiffKind
  text: string
}

// 超过这个规模就退化成「整段替换」，否则二维表会吃掉太多内存
const MAX_CELLS = 1_000_000

export function lineDiff(oldText: string, newText: string): DiffLine[] {
  const a = oldText.replace(/\r\n/g, '\n').split('\n')
  const b = newText.replace(/\r\n/g, '\n').split('\n')
  const n = a.length
  const m = b.length

  if (n * m > MAX_CELLS) {
    return [
      ...a.map((text) => ({ kind: 'del' as const, text })),
      ...b.map((text) => ({ kind: 'add' as const, text })),
    ]
  }

  // dp[i][j] = a[i:] 与 b[j:] 的最长公共子序列长度
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1])
    }
  }

  const out: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push({ kind: 'same', text: a[i] })
      i++
      j++
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      out.push({ kind: 'del', text: a[i] })
      i++
    } else {
      out.push({ kind: 'add', text: b[j] })
      j++
    }
  }
  while (i < n) out.push({ kind: 'del', text: a[i++] })
  while (j < m) out.push({ kind: 'add', text: b[j++] })
  return out
}

export function diffStat(lines: DiffLine[]): { add: number; del: number } {
  let add = 0
  let del = 0
  for (const l of lines) {
    if (l.kind === 'add') add++
    else if (l.kind === 'del') del++
  }
  return { add, del }
}

// 只留改动附近的行，上下文各留两行。整份策略摊开看，改动往往找不到。
export function keepContext(lines: DiffLine[], context = 2): DiffLine[] {
  const keep = new Array<boolean>(lines.length).fill(false)
  lines.forEach((l, i) => {
    if (l.kind === 'same') return
    for (let k = Math.max(0, i - context); k <= Math.min(lines.length - 1, i + context); k++) {
      keep[k] = true
    }
  })
  const out: DiffLine[] = []
  let gap = false
  lines.forEach((l, i) => {
    if (keep[i]) {
      if (gap && out.length) out.push({ kind: 'gap', text: '' })
      out.push(l)
      gap = false
    } else {
      gap = true
    }
  })
  return out
}
