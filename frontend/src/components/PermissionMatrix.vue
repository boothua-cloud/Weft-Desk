<script setup lang="ts">
import { computed, watchEffect } from 'vue'
import { accessPorts, groupKey, ownedTokens, portShort, tagKey, tokenLabel, userToken, type AccessPeer as Peer, type AclForm } from '../acl'

// 「谁能访问谁」矩阵：行是人（以及设备标签），列是设备。
// 这里不读规则原文，而是按 headscale 的语义逐格推演，
// 所以规则写宽了、写漏了，一眼就能看出来。
const props = defineProps<{
  form: AclForm
  peers: Peer[]
  members: string[]
}>()

// 规则页把矩阵收起来时只剩一条结论条，数据从这里出：
// 推演只在本组件里做一份，父级不重复实现一套
// <script setup> 里不能 export，父级按同形状自己声明
interface MatrixSummary {
  rows: number
  cols: number
  reachable: number
  deadRows: number
  deadCols: number
  openAll: boolean
}
const emit = defineEmits<{ summary: [MatrixSummary] }>()

interface Row {
  key: string
  kind: 'member' | 'tag'
  name: string
  label: string
  note: string
  owned: Set<string>
}

interface Col {
  key: string
  label: string
  sub: string
  subKind: 'tag' | 'route' | 'none'
  user: string
  online: boolean
  /** 网段列：目标写的是 CIDR，不参与 autogroup:self 判定 */
  route: boolean
  owned: Set<string>
}

const rows = computed<Row[]>(() => {
  const out: Row[] = []
  const names = props.members.length
    ? props.members
    : [...new Set(props.peers.map((p) => p.user).filter(Boolean))]

  for (const name of names) {
    const groups = props.form.groups
      .filter((g) => g.values.includes(name))
      .map((g) => groupKey(g.name))
    out.push({
      key: 'm:' + name,
      kind: 'member',
      name,
      label: tokenLabel(name),
      note: groups.length ? groups.map(tokenLabel).join('、') : '未分组',
      owned: ownedTokens('member', { name, groups }),
    })
  }

  // 标签也能作为来源（「服务器之间可以互相 SSH」这类规则），单独列成行
  for (const t of props.form.tagOwners) {
    const n = tagKey(t.name)
    if (!n) continue
    out.push({
      key: 't:' + n,
      kind: 'tag',
      name: n,
      label: n,
      note: '标签',
      owned: ownedTokens('device', { name: n, tags: [n] }),
    })
  }
  return out
})

const cols = computed<Col[]>(() => {
  const out: Col[] = props.peers.map((p) => ({
    key: 'd:' + (p.name || p.ip),
    label: p.name || p.ip,
    sub: p.tags.length ? p.tags.map(tokenLabel).join(' ') : '无标签',
    subKind: p.tags.length ? 'tag' : 'none',
    user: p.user,
    online: p.online,
    route: false,
    owned: ownedTokens('device', { name: p.name, hostname: p.hostname, tags: p.tags, ip: p.ip }),
  }))
  // 子网路由单独成列：dst 里写的是网段，跟路由器本机的端口是两回事，
  // 混在同一列里会让人以为「能访问 ubuntu 的所有端口」就等于「能进它背后的内网」
  for (const p of props.peers) {
    for (const cidr of p.routes ?? []) {
      if (!cidr || out.some((c) => c.label === cidr)) continue
      out.push({
        key: 'r:' + cidr,
        label: cidr,
        sub: `经 ${p.name}`,
        subKind: 'route',
        user: p.user,
        online: p.online,
        route: true,
        owned: new Set([cidr]),
      })
    }
  }
  return out
})

// 空策略（没写 acls）在 headscale 里是全放行，不能画成一片灰
const openAll = computed(() => !props.form.hasAclsKey && props.form.acls.length === 0)

function cell(row: Row, col: Col): string[] {
  if (openAll.value) return ['*']
  // 设备上的用户是 headscale 里的裸名字，行里存的是「名字@」，比较前统一
  const self = row.kind === 'member' && !col.route && userToken(col.user) === row.name
  return accessPorts(props.form, row.owned, col.owned, self)
}

function cellText(ports: string[]): string {
  if (!ports.length) return ''
  if (ports[0] === '*') return '全部'
  const head = ports.slice(0, 2).map(portShort)
  return head.join(' · ') + (ports.length > 2 ? ` +${ports.length - 2}` : '')
}

function cellTitle(row: Row, col: Col, ports: string[]): string {
  const who = row.kind === 'tag' ? `标签 ${row.name}` : tokenLabel(row.name)
  if (!ports.length) return `${who} 不能访问 ${col.label}`
  return `${who} → ${col.label}：${ports.map(portShort).join('、')}`
}

const grid = computed(() => rows.value.map((r) => cols.value.map((c) => cell(r, c))))

// 每列无人可达 / 每行无处可达，用来标出「这条规则白写了」
const colDead = computed(() => cols.value.map((_, ci) => grid.value.every((line) => !line[ci].length)))
const rowDead = computed(() => grid.value.map((line) => line.every((ports) => !ports.length)))

const reachable = computed(() => {
  let n = 0
  for (const line of grid.value) for (const ports of line) if (ports.length) n++
  return n
})

watchEffect(() => {
  emit('summary', {
    rows: rows.value.length,
    cols: cols.value.length,
    reachable: reachable.value,
    deadRows: rowDead.value.filter(Boolean).length,
    deadCols: colDead.value.filter(Boolean).length,
    openAll: openAll.value,
  })
})
</script>

<template>
  <div class="mtx">
    <div v-if="!peers.length" class="mtx-empty">
      还没有设备数据。连上服务端、有设备注册后会自动出现。
    </div>
    <div v-else-if="!rows.length" class="mtx-empty">
      还没有成员或标签。有第一个成员后，这里会显示各自能访问哪些设备。
    </div>

    <template v-else>
      <div class="mtx-scroll">
        <table class="mtx-table">
          <thead>
            <tr>
              <th class="mtx-corner">谁 ↓ / 访问 →</th>
              <th v-for="(c, ci) in cols" :key="c.key" class="mtx-col" :class="{ dead: colDead[ci] }">
                <div class="mtx-col-name">{{ c.label }}</div>
                <div class="mtx-col-sub">
                  <span v-if="c.subKind === 'tag'" class="mtx-tag">{{ c.sub }}</span>
                  <span v-else-if="c.subKind === 'route'" class="mtx-route">{{ c.sub }}</span>
                  <span v-else class="mtx-notag">{{ c.sub }}</span>
                </div>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(r, ri) in rows" :key="r.key" :class="{ dead: rowDead[ri] }">
              <th class="mtx-row">
                <span class="mtx-row-name" :class="{ 'is-tag': r.kind === 'tag' }">{{ r.label }}</span>
                <span class="mtx-row-note">{{ r.note }}</span>
              </th>
              <td
                v-for="(c, ci) in cols"
                :key="c.key"
                class="mtx-cell"
                :class="{ on: grid[ri][ci].length, all: grid[ri][ci][0] === '*' }"
                :title="cellTitle(r, c, grid[ri][ci])"
              >
                <span v-if="grid[ri][ci].length">{{ cellText(grid[ri][ci]) }}</span>
                <span v-else class="mtx-no">·</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="mtx-foot">
        <span class="mtx-legend"><i class="lg on"></i>可以访问</span>
        <span class="mtx-legend"><i class="lg all"></i>全部端口</span>
        <span class="mtx-legend"><i class="lg off"></i>不通</span>
        <span class="mtx-stat">{{ reachable }} 条可达</span>
        <span v-if="openAll" class="mtx-warn">策略里没有 acls，默认全部互通</span>
        <span v-else-if="rowDead.some(Boolean)" class="mtx-warn">
          {{ rowDead.filter(Boolean).length }} 行完全不通（这个成员哪台设备都去不了）
        </span>
      </div>
    </template>
  </div>
</template>

<style scoped>
.mtx-empty {
  padding: 18px 2px;
  font-size: 11.5px;
  color: var(--faint);
  line-height: 1.8;
}

.mtx-scroll {
  overflow-x: auto;
  /* 表格自己不透明：外层不再是白卡，若不铺底，行名列（墙面色）会和透明的数据格割开 */
  background: var(--surface);
  border: 1px solid var(--line-soft);
  border-radius: 8px;
}

.mtx-table {
  border-collapse: separate;
  border-spacing: 0;
  width: 100%;
  font-size: 11px;
}

.mtx-corner,
.mtx-row {
  position: sticky;
  left: 0;
  z-index: 2;
  background: var(--surface);
  text-align: left;
  border-right: 1px solid var(--line);
  border-bottom: 1px solid var(--line-soft);
  padding: 8px 10px;
  min-width: 132px;
  max-width: 180px;
}

.mtx-corner {
  font-size: 9.5px;
  font-weight: 500;
  color: var(--faint);
  letter-spacing: .02em;
  border-bottom: 1px solid var(--line);
}

.mtx-col {
  padding: 7px 6px 6px;
  border-bottom: 1px solid var(--line);
  border-left: 1px solid var(--line-soft);
  text-align: center;
  vertical-align: bottom;
  min-width: 74px;
}
.mtx-col.dead { background: color-mix(in srgb, var(--danger) 5%, transparent); }

.mtx-col-name {
  font-size: 10.5px;
  font-weight: 600;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 110px;
  margin: 0 auto;
}
.mtx-col-sub { margin-top: 2px; }
.mtx-tag { font-size: 9px; color: var(--info); font-family: var(--mono); }
.mtx-route { font-size: 9px; color: var(--brand); }
.mtx-notag { font-size: 9px; color: var(--faint); }

.mtx-row-name {
  display: block;
  font-size: 11.5px;
  font-weight: 500;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mtx-row-name.is-tag { color: var(--info); font-family: var(--mono); font-size: 11px; }
.mtx-row-note { display: block; font-size: 9px; color: var(--faint); margin-top: 1px; }
tr.dead .mtx-row-name { color: var(--faint); }

.mtx-cell {
  text-align: center;
  padding: 0 4px;
  height: 30px;
  border-left: 1px solid var(--line-soft);
  border-bottom: 1px solid var(--line-soft);
  font-size: 9.5px;
  font-family: var(--mono);
  color: var(--faint);
  cursor: default;
}
.mtx-cell.on { color: var(--brand); background: var(--brand-soft); font-weight: 600; }
.mtx-cell.all { color: var(--warn); background: var(--warn-soft); }
.mtx-no { opacity: .45; }

.mtx-foot {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  margin-top: 9px;
  font-size: 10px;
  color: var(--faint);
}
.mtx-legend { display: inline-flex; align-items: center; gap: 5px; }
.lg { width: 10px; height: 10px; border-radius: 3px; display: inline-block; border: 1px solid var(--line); }
.lg.on { background: var(--brand-soft); border-color: var(--brand-line); }
.lg.all { background: var(--warn-soft); border-color: color-mix(in srgb, var(--warn) 30%, transparent); }
.lg.off { background: var(--surface-2); }
.mtx-stat { margin-left: auto; }
.mtx-warn { color: var(--warn); }
</style>
