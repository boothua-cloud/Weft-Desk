<script setup lang="ts">
import { computed } from 'vue'
import type { PingSample } from '../api'

// 延迟曲线：每条采样就是一次 disco 探测。丢了的那一次断开线段，
// 底部留一道红条——「曲线掉下去」和「根本没回来」是两件事，得分开看。
const props = withDefaults(defineProps<{ samples: PingSample[]; capacity?: number }>(), {
  capacity: 60,
})

const W = 600
const H = 92
const TOP = 8
const BASE = 76 // 基线；下面 16px 留给丢包标记

const n = computed(() => props.samples.length)
const ok = computed(() => props.samples.filter((s) => s.ms >= 0))
const lost = computed(() => props.samples.filter((s) => s.ms < 0).length)
const avg = computed(() => {
  if (!ok.value.length) return -1
  return ok.value.reduce((a, s) => a + s.ms, 0) / ok.value.length
})
const worst = computed(() => (ok.value.length ? Math.max(...ok.value.map((s) => s.ms)) : -1))
const lossPct = computed(() => (n.value ? Math.round((lost.value / n.value) * 100) : 0))
const latest = computed((): PingSample | null => (n.value ? props.samples[n.value - 1] : null))

// 纵轴取「最差一次」再抬 25%，向上取整到 10ms，不然曲线一直贴着顶
const yMax = computed(() => {
  const w = worst.value
  if (w < 0) return 50
  return Math.max(50, Math.ceil((w * 1.25) / 10) * 10)
})

const step = computed(() => W / Math.max(1, props.capacity - 1))

interface P { x: number; y: number }

// 新的点从右边长出来，旧的往左推；采样没攒够时曲线贴右边缘，
// 不会先占满整宽再往左挤。
const geom = computed(() => {
  const st = step.value
  const total = props.samples.length
  const pts: (P & { loss: boolean })[] = props.samples.map((s, i) => ({
    x: W - (total - 1 - i) * st,
    y: s.ms < 0 ? BASE : BASE - Math.min(1, s.ms / yMax.value) * (BASE - TOP),
    loss: s.ms < 0,
  }))

  const segs: { line: string; area: string }[] = []
  const dots: P[] = []
  let run: P[] = []
  const flush = () => {
    if (run.length >= 2) {
      const line = 'M' + run.map((p) => `${p.x.toFixed(1)},${p.y.toFixed(1)}`).join('L')
      const area = `M${run[0].x.toFixed(1)},${BASE}L` +
        run.map((p) => `${p.x.toFixed(1)},${p.y.toFixed(1)}`).join('L') +
        `L${run[run.length - 1].x.toFixed(1)},${BASE}Z`
      segs.push({ line, area })
    } else if (run.length === 1) {
      // 前后都断了只剩一个点，画条线没意义，改成一个小圆点
      dots.push(run[0])
    }
    run = []
  }
  for (const p of pts) {
    if (p.loss) { flush(); continue }
    run.push({ x: p.x, y: p.y })
  }
  flush()

  const last = ok.value.length ? pts.filter((p) => !p.loss).at(-1) ?? null : null
  return { pts, segs, dots, marks: pts.filter((p) => p.loss), last }
})

const gridY = computed(() => [TOP, (TOP + BASE) / 2, BASE])

function fmt(v: number): string {
  if (v < 0) return '—'
  return v < 10 ? v.toFixed(1) : String(Math.round(v))
}
</script>

<template>
  <div class="lat">
    <div class="lat-head">
      <div class="lat-stats">
        <span class="lat-stat">
          <i>当前</i>
          <b :class="{ bad: !latest || latest.ms < 0, warn: !!latest && latest.ms > yMax * 0.8 }">
            {{ latest ? (latest.ms < 0 ? '超时' : fmt(latest.ms) + ' ms') : '—' }}
          </b>
        </span>
        <span class="lat-stat"><i>平均</i><b>{{ fmt(avg) }}{{ avg >= 0 ? ' ms' : '' }}</b></span>
        <span class="lat-stat"><i>最差</i><b>{{ fmt(worst) }}{{ worst >= 0 ? ' ms' : '' }}</b></span>
        <span class="lat-stat">
          <i>丢包</i>
          <b :class="{ bad: lossPct > 0 }">{{ lossPct }}%</b>
          <small v-if="lost">（{{ lost }}/{{ n }}）</small>
        </span>
      </div>
      <span class="lat-count">{{ n }} 次 · 每 2 秒</span>
    </div>

    <div class="lat-plot">
      <span class="lat-scale top">{{ yMax }} ms</span>
      <span class="lat-scale bottom">0</span>
      <svg :viewBox="`0 0 ${W} ${H}`" preserveAspectRatio="none" class="lat-svg">
        <line v-for="y in gridY" :key="y" x1="0" :y1="y" :x2="W" :y2="y" class="lat-grid" />
        <path v-for="(s, i) in geom.segs" :key="'a' + i" :d="s.area" class="lat-area" />
        <path v-for="(s, i) in geom.segs" :key="'l' + i" :d="s.line" class="lat-line" />
        <circle v-for="(d, i) in geom.dots" :key="'d' + i" :cx="d.x" :cy="d.y" r="2" class="lat-dot" />
        <rect v-for="(m, i) in geom.marks" :key="'m' + i" :x="m.x - 1.6" y="78" width="3.2" height="7"
              rx="1" class="lat-loss" />
        <circle v-if="geom.last" :cx="geom.last.x" :cy="geom.last.y" r="2.6" class="lat-now" />
      </svg>
      <span v-if="!n" class="lat-empty">正在采样…</span>
    </div>
  </div>
</template>

<style scoped>
.lat {
  margin-top: 12px;
  padding-top: 11px;
  border-top: 1px dashed var(--line-soft);
}
.lat-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.lat-stats { display: flex; align-items: baseline; gap: 14px; flex-wrap: wrap; font-size: 11px; }
.lat-stat { display: flex; align-items: baseline; gap: 5px; }
.lat-stat i { font-style: normal; color: var(--faint); }
.lat-stat b { font-weight: 600; font-variant-numeric: tabular-nums; }
.lat-stat b.bad { color: var(--danger); }
.lat-stat b.warn { color: var(--warn); }
.lat-stat small { color: var(--faint); font-size: 10px; }
.lat-count { font-size: 10.5px; color: var(--faint); }

.lat-plot { position: relative; }
.lat-svg { display: block; width: 100%; height: 92px; overflow: visible; }
.lat-grid { stroke: var(--line); stroke-width: 1; vector-effect: non-scaling-stroke; opacity: .8; }
.lat-area { fill: var(--brand-soft); opacity: .55; }
.lat-line {
  fill: none;
  stroke: var(--brand);
  stroke-width: 1.6;
  stroke-linejoin: round;
  stroke-linecap: round;
  vector-effect: non-scaling-stroke;
}
.lat-dot { fill: var(--brand); }
.lat-now { fill: var(--brand); stroke: var(--surface); stroke-width: 1.5; vector-effect: non-scaling-stroke; }
.lat-loss { fill: var(--danger); }
.lat-scale {
  position: absolute;
  left: 0;
  font-size: 9.5px;
  color: var(--faint);
  pointer-events: none;
  background: var(--surface);
  padding: 0 3px;
  border-radius: 3px;
}
.lat-scale.top { top: -2px; }
.lat-scale.bottom { bottom: 14px; }
.lat-empty {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 11px;
  color: var(--faint);
}
</style>
