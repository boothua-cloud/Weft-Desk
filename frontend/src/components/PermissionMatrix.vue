<script setup lang="ts">
import { computed, ref, watchEffect } from 'vue'
import { accessPorts, groupKey, ownedTokens, parseDstEntry, portShort, tagKey, tokenLabel, userToken, type AccessPeer as Peer, type AclForm } from '../acl'

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
  /** hosts 里给这个地址起的名字。规则直接写 nas:5000 时靠它对上号 */
  alias: string
  /** 别名指向的地址，鼠标悬停时能看到它到底是谁 */
  addr: string
  sub: string
  subKind: 'tag' | 'route' | 'none' | 'alias'
  user: string
  online: boolean
  /** 网段列：目标写的是 CIDR，不参与 autogroup:self 判定 */
  route: boolean
  /** 纯别名列：地址不是 tailnet 里的设备，只是策略里的一个 host */
  aliasOnly: boolean
  owned: Set<string>
}

const allRows = computed<Row[]>(() => {
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

// 别名表：hosts 里是「名字 → 地址」，规则里可以直接写 nas:5000。
// 矩阵必须把 nas 认成那个地址，否则这条规则在矩阵里等于没写。
const aliases = computed(() =>
  props.form.hosts
    .map((h) => ({ name: h.name.trim(), addr: h.addr.trim() }))
    .filter((h) => h.name && h.addr),
)
const sameAddr = (a: string, b: string) => !!a && !!b && a.toLowerCase() === b.toLowerCase()

const allCols = computed<Col[]>(() => {
  const out: Col[] = []
  const claimed = new Set<string>()
  const namesFor = (addr: string) =>
    addr ? aliases.value.filter((a) => sameAddr(a.addr, addr)).map((a) => a.name) : []

  for (const p of props.peers) {
    const names = namesFor(p.ip)
    const owned = ownedTokens('device', { name: p.name, hostname: p.hostname, tags: p.tags, ip: p.ip })
    for (const n of names) { owned.add(n); claimed.add(n) }
    out.push({
      key: 'd:' + (p.name || p.ip),
      label: p.name || p.ip,
      alias: names[0] ?? '',
      addr: p.ip,
      sub: p.tags.length ? p.tags.map(tokenLabel).join(' ') : '无标签',
      subKind: p.tags.length ? 'tag' : 'none',
      user: p.user,
      online: p.online,
      route: false,
      aliasOnly: false,
      owned,
    })
  }
  // 子网路由单独成列：dst 里写的是网段，跟路由器本机的端口是两回事，
  // 混在同一列里会让人以为「能访问 ubuntu 的所有端口」就等于「能进它背后的内网」
  for (const p of props.peers) {
    for (const cidr of p.routes ?? []) {
      if (!cidr || out.some((c) => c.label === cidr)) continue
      const names = namesFor(cidr)
      for (const n of names) claimed.add(n)
      out.push({
        key: 'r:' + cidr,
        label: cidr,
        alias: names[0] ?? '',
        addr: cidr,
        sub: `经 ${p.name}`,
        subKind: 'route',
        user: p.user,
        online: p.online,
        route: true,
        aliasOnly: false,
        owned: new Set([cidr, ...names]),
      })
    }
  }
  // 剩下的别名：地址既不是某台设备的 IP，也不是某条路由的网段，
  // 典型是内网里一台没装 tailscale 的机器。给它单独一列，
  // 否则「谁能访问 nas:5000」在矩阵里根本看不出来。
  for (const a of aliases.value) {
    if (claimed.has(a.name)) continue
    claimed.add(a.name)
    out.push({
      key: 'a:' + a.name,
      label: a.name,
      // 列名本身就是别名，再挂一行只会重复
      alias: '',
      addr: a.addr,
      sub: a.addr,
      subKind: 'alias',
      user: '',
      online: false,
      route: true,
      aliasOnly: true,
      owned: new Set([a.name]),
    })
  }
  return out
})

// 空策略（没写 acls）在 headscale 里是全放行，不能画成一片灰
const openAll = computed(() => !props.form.hasAclsKey && props.form.acls.length === 0)

// 设备一多，矩阵就成了一面墙。默认只留「规则里提到过」的人和设备：
// 没被任何规则提到的那些，对谁都不可达，折起来不影响结论，
// 想看全的还有一个开关。
const onlyUsed = ref(true)
const dense = computed(() => allCols.value.length > 10 || allRows.value.length > 12)

const colUsed = computed(() => allCols.value.map((c) =>
  openAll.value || props.form.acls.some((r) =>
    r.dst.some((d) => {
      const h = parseDstEntry(d).host
      return h === '*' || c.owned.has(h)
    }),
  ),
))
const rowUsed = computed(() => allRows.value.map((r) =>
  openAll.value || props.form.acls.some((x) =>
    x.src.some((s) => s === '*' || s.startsWith('autogroup:') || r.owned.has(s)),
  ),
))
const folded = computed(() =>
  dense.value && onlyUsed.value && !openAll.value
  && colUsed.value.some(Boolean) && rowUsed.value.some(Boolean),
)
const hiddenRows = computed(() => (folded.value ? rowUsed.value.filter((u) => !u).length : 0))
const hiddenCols = computed(() => (folded.value ? colUsed.value.filter((u) => !u).length : 0))

const rows = computed<Row[]>(() => (folded.value ? allRows.value.filter((_, i) => rowUsed.value[i]) : allRows.value))
const cols = computed<Col[]>(() => (folded.value ? allCols.value.filter((_, i) => colUsed.value[i]) : allCols.value))

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
    rows: allRows.value.length,
    cols: allCols.value.length,
    reachable: reachable.value,
    // 折起来的那些对谁都不可达，算进「完全不通」里，结论条才跟总数对得上
    deadRows: rowDead.value.filter(Boolean).length + hiddenRows.value,
    deadCols: colDead.value.filter(Boolean).length + hiddenCols.value,
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
      <div v-if="dense" class="mtx-tools">
        <span>{{ allRows.length }} 行 · {{ allCols.length }} 列</span>
        <button class="link-btn" @click="onlyUsed = !onlyUsed">
          {{ onlyUsed ? '显示全部' : '只看规则提到的' }}
        </button>
      </div>
      <div class="mtx-scroll">
        <table class="mtx-table">
          <thead>
            <tr>
              <th class="mtx-corner">谁 ↓ / 访问 →</th>
              <th v-for="(c, ci) in cols" :key="c.key" class="mtx-col" :class="{ dead: colDead[ci] }">
                <div class="mtx-col-name" :title="c.label">{{ c.label }}</div>
                <div v-if="c.alias" class="mtx-col-alias" :title="`别名 ${c.alias} = ${c.addr}`">{{ c.alias }}</div>
                <div class="mtx-col-sub">
                  <span v-if="c.subKind === 'tag'" class="mtx-tag">{{ c.sub }}</span>
                  <span v-else-if="c.subKind === 'route'" class="mtx-route">{{ c.sub }}</span>
                  <span v-else-if="c.subKind === 'alias'" class="mtx-addr">{{ c.sub }}</span>
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
        <span v-if="folded" class="mtx-fold">
          另有 {{ hiddenRows }} 行、{{ hiddenCols }} 列没被任何规则提到，已折叠
        </span>
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
/* 组件根铺满父级：外层按 36vh 限高的高度要能传下来，表格框才滚得起来 */
.mtx { flex: 1; min-height: 0; display: flex; flex-direction: column; }

.mtx-empty {
  padding: 18px 2px;
  font-size: 11.5px;
  color: var(--faint);
  line-height: 1.8;
}

.mtx-tools {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-bottom: 7px;
  font-size: 10px;
  color: var(--faint);
}

.mtx-scroll {
  /* 两个方向都在这个框里滚：外层 .mtx-body 已按 36vh 限高并撑成 flex，
     这里占满剩余高度，表头和行名才能吸在这个框的边上 */
  flex: 1;
  min-height: 0;
  overflow: auto;
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

/* 表头跟着滚，滚到第 20 行还知道这一列是哪台设备 */
.mtx-table thead th {
  position: sticky;
  top: 0;
  z-index: 3;
  background: var(--surface);
}
/* 左上角那一格要同时压住表头和行名，层级得比两者都高。
   这里必须带上 thead 提高优先级，否则会被上面那条同层级的规则盖回去 */
.mtx-table thead th.mtx-corner {
  left: 0;
  top: 0;
  z-index: 4;
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
  top: 0;
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
  /* 表头是 sticky 的，底色必须不透明，否则滚动时下面的行会透上来 */
  background: var(--surface);
}
.mtx-col.dead { background: color-mix(in srgb, var(--danger) 5%, var(--surface)); }

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
.mtx-col-alias {
  margin-top: 1px;
  font-size: 9px;
  font-weight: 600;
  color: var(--brand);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 110px;
  margin-left: auto;
  margin-right: auto;
}
.mtx-tag { font-size: 9px; color: var(--info); font-family: var(--mono); }
.mtx-route { font-size: 9px; color: var(--brand); }
.mtx-addr { font-size: 8.5px; color: var(--faint); font-family: var(--mono); }
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
.mtx-fold { color: var(--faint); }
.mtx-warn { color: var(--warn); }
</style>
