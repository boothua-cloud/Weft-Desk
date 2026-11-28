import type { PeerInfo, State } from './api'

// 网络变化提示：前后两次 state 一对比就知道有没有变，不用后端另开通道。
//
// 只挑两类说：本机网络换了、到某台设备的连接方式变了。设备上下线不提示，
// 手机锁屏就离线，一晚上能刷几十条；那种变化在设备列表里能看见，不值得打断人。

export type NetChange = {
  // net: 本机网络；path: 到某台设备的连接方式
  kind: 'net' | 'path'
  text: string
}

function pathOf(p: PeerInfo): string {
  if (!p.online) return ''
  return p.curAddr ? 'direct' : 'relay:' + (p.relay ?? '')
}

function names(list: string[]): string {
  const head = list.slice(0, 2).join('、')
  return list.length > 2 ? `${head} 等 ${list.length} 台` : head
}

// diffNetwork 返回要说的话。空数组表示没什么好说的。
export function diffNetwork(prev: State, next: State): NetChange[] {
  // 刚启动（还没上一份）或换了服务端：两批设备不是一回事，别当成网络变了
  if (!prev.endpoint || prev.endpoint !== next.endpoint) return []
  // 没连上时网络怎么变都无所谓，等连上再说
  if (!next.connected && !prev.connected) return []

  const out: NetChange[] = []

  const before = prev.localIPs ?? []
  const after = next.localIPs ?? []
  if (before.length && after.length && before.join() !== after.join()) {
    const added = after.filter((ip) => !before.includes(ip))
    const removed = before.filter((ip) => !after.includes(ip))
    if (added.length === 1 && removed.length === 1) {
      out.push({ kind: 'net', text: `本机网络变了：${removed[0]} → ${added[0]}` })
    } else if (added.length === 1) {
      out.push({ kind: 'net', text: `本机网络变了，多了地址 ${added[0]}` })
    } else if (removed.length) {
      out.push({ kind: 'net', text: `本机网络变了，${removed.join('、')} 已断开` })
    } else {
      out.push({ kind: 'net', text: '本机网络变了' })
    }
  }

  const was = new Map((prev.peers ?? []).map((p) => [p.ip, p]))
  const toRelay: string[] = []
  const toDirect: string[] = []
  let relay = ''
  for (const p of next.peers ?? []) {
    const old = was.get(p.ip)
    if (!old) continue
    const a = pathOf(old)
    const b = pathOf(p)
    // 离线/上线不算路径变化；同一个方向反复跳由调用方限流
    if (!a || !b || a === b) continue
    const who = p.name || p.ip
    if (b.startsWith('relay:')) {
      toRelay.push(who)
      relay = relay || b.slice('relay:'.length)
    } else if (b === 'direct') {
      toDirect.push(who)
    }
  }
  if (toRelay.length) {
    out.push({
      kind: 'path',
      text: `${names(toRelay)} 切到了中继${relay ? '（' + relay + '）' : ''}，速度会变慢`,
    })
  }
  if (toDirect.length) {
    out.push({ kind: 'path', text: `${names(toDirect)} 恢复直连` })
  }
  return out
}
