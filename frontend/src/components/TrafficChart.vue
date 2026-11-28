<script setup lang="ts">
import { computed } from 'vue'

// 流量曲线：两条线，上行（本机发出）与下行（本机收到）。
// 传进来的是已经算好的速率（字节/秒），组件不碰时间差，
// 采样节奏由页面控制——页面知道什么时候该暂停。
interface TrafficPoint {
  up: number
  down: number
}

const props = withDefaults(defineProps<{ samples: TrafficPoint[]; capacity?: number; idle?: string }>(), {
  capacity: 60,
  idle: '正在采样…',
})

const W = 600
const H = 96
const TOP = 8
const BASE = 84

const n = computed(() => props.samples.length)
const curUp = computed(() => (n.value ? props.samples[n.value - 1].up : 0))
const curDown = computed(() => (n.value ? props.samples[n.value - 1].down : 0))
const peak = computed(() =>
  props.samples.reduce((m, s) => Math.max(m, s.up, s.down), 0),
)

// 轴上限取峰值往上抬一档，台阶按 1024 走：刻度要落在整数 KB/MB 上，
// 否则会出现「9.8 KB/s」这种读数——一个每秒都在变的刻度比曲线还晃眼。
// 最低给 1 KB/s，纯空转时不至于把几个字节的噪声放大成锯齿。
const yMax = computed(() => {
  const p = peak.value * 1.2
  let unit = 1
  while (p / unit >= 1024) unit *= 1024
  const v = p / unit
  // 1/2/3/5/8 之后每 10 一档往上取整，保证轴永远在峰值之上：
  // 写死到 10 的话，20 KB/s 的尖峰会被裁到「10 KB/s」的框外面
  const nice =
    v <= 1 ? 1 : v <= 2 ? 2 : v <= 3 ? 3 : v <= 5 ? 5 : v <= 8 ? 8 : Math.ceil(v / 10) * 10
  return Math.max(1024, nice * unit)
})

const step = computed(() => W / Math.max(1, props.capacity - 1))

function y(v: number): number {
  return BASE - Math.min(1, v / yMax.value) * (BASE - TOP)
}

// 新点从右边长出来，没攒够时贴右边缘
function geom(key: 'up' | 'down') {
  const total = props.samples.length
  const st = step.value
  const pts = props.samples.map((s, i) => ({
    x: W - (total - 1 - i) * st,
    y: y(s[key]),
  }))
  if (!pts.length) return { line: '', area: '', last: null as { x: number; y: number } | null }
  const line = 'M' + pts.map((p) => `${p.x.toFixed(1)},${p.y.toFixed(1)}`).join('L')
  const area =
    `M${pts[0].x.toFixed(1)},${BASE}L` +
    pts.map((p) => `${p.x.toFixed(1)},${p.y.toFixed(1)}`).join('L') +
    `L${pts[pts.length - 1].x.toFixed(1)},${BASE}Z`
  return { line, area, last: pts[pts.length - 1] }
}

const upPath = computed(() => geom('up'))
const downPath = computed(() => geom('down'))
const gridY = computed(() => [TOP, (TOP + BASE) / 2, BASE])

function rate(v: number): string {
  if (v >= 1024 * 1024) {
    const n = v / 1024 / 1024
    return (n >= 10 ? n.toFixed(0) : n.toFixed(1)) + ' MB/s'
  }
  if (v >= 1024) {
    const n = v / 1024
    return (n >= 10 ? n.toFixed(0) : n.toFixed(1)) + ' KB/s'
  }
  return Math.round(v) + ' B/s'
}
</script>

<template>
  <div class="tr">
    <div class="tr-head">
      <div class="tr-stats">
        <span class="tr-stat">
          <i class="lg up"></i>上行<b class="up">{{ rate(curUp) }}</b>
        </span>
        <span class="tr-stat">
          <i class="lg down"></i>下行<b class="down">{{ rate(curDown) }}</b>
        </span>
        <span class="tr-stat">
          <i>峰值</i><b>{{ peak ? rate(peak) : '—' }}</b>
        </span>
      </div>
      <span class="tr-count">{{ n }} 个点 · 每 2 秒</span>
    </div>

    <div class="tr-plot">
      <span class="tr-scale top">{{ rate(yMax) }}</span>
      <span class="tr-scale bottom">0</span>
      <svg :viewBox="`0 0 ${W} ${H}`" preserveAspectRatio="none" class="tr-svg">
        <line v-for="y in gridY" :key="y" x1="0" :y1="y" :x2="W" :y2="y" class="tr-grid" />
        <path v-if="downPath.area" :d="downPath.area" class="tr-area down" />
        <path v-if="upPath.area" :d="upPath.area" class="tr-area up" />
        <path v-if="downPath.line" :d="downPath.line" class="tr-line down" />
        <path v-if="upPath.line" :d="upPath.line" class="tr-line up" />
        <circle v-if="upPath.last" :cx="upPath.last.x" :cy="upPath.last.y" r="2.4" class="tr-now up" />
        <circle v-if="downPath.last" :cx="downPath.last.x" :cy="downPath.last.y" r="2.4" class="tr-now down" />
      </svg>
      <span v-if="!n" class="tr-empty">{{ idle }}</span>
    </div>
  </div>
</template>

<style scoped>
.tr {
  margin-top: 2px;
}
.tr-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.tr-stats { display: flex; align-items: baseline; gap: 16px; flex-wrap: wrap; font-size: 11px; }
.tr-stat { display: flex; align-items: baseline; gap: 5px; color: var(--faint); }
.tr-stat b { font-weight: 600; font-variant-numeric: tabular-nums; color: var(--text); }
.tr-stat b.up { color: var(--brand); }
.tr-stat b.down { color: var(--ok); }
.tr-count { font-size: 10.5px; color: var(--faint); }

/* 图例小方点，跟线上颜色对齐 */
.tr-stat i.lg { width: 7px; height: 2px; border-radius: 1px; display: inline-block; }
.tr-stat i.lg.up { background: var(--brand); }
.tr-stat i.lg.down { background: var(--ok); }
.tr-stat i { font-style: normal; }

.tr-plot { position: relative; }
.tr-svg { display: block; width: 100%; height: 96px; overflow: visible; }
.tr-grid { stroke: var(--line); stroke-width: 1; vector-effect: non-scaling-stroke; opacity: .8; }
.tr-area { opacity: .12; }
.tr-area.up { fill: var(--brand); }
.tr-area.down { fill: var(--ok); }
.tr-line {
  fill: none;
  stroke-width: 1.6;
  stroke-linejoin: round;
  stroke-linecap: round;
  vector-effect: non-scaling-stroke;
}
.tr-line.up { stroke: var(--brand); }
.tr-line.down { stroke: var(--ok); }
.tr-now { stroke: var(--surface); stroke-width: 1.5; vector-effect: non-scaling-stroke; }
.tr-now.up { fill: var(--brand); }
.tr-now.down { fill: var(--ok); }
.tr-scale {
  position: absolute;
  left: 0;
  font-size: 9.5px;
  color: var(--faint);
  pointer-events: none;
  background: var(--surface);
  padding: 0 3px;
  border-radius: 3px;
}
.tr-scale.top { top: -2px; }
.tr-scale.bottom { bottom: 8px; }
.tr-empty {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 11px;
  color: var(--faint);
}
</style>
