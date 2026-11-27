<script setup lang="ts">
import { computed } from 'vue'
import type { State } from '../api'

const props = defineProps<{ kind: 'about' | 'help'; state?: State | null }>()
const emit = defineEmits(['close'])

const isMac = computed(() => (props.state?.platform ?? 'darwin') === 'darwin')
const isWin = computed(() => props.state?.platform === 'windows')

// 数据目录直接读后端给的绝对路径，不同系统各自不同
const dataDir = computed(() =>
  props.state?.dataDir || (isMac.value ? '~/Library/Application Support/Weft' : '%AppData%\\Weft'),
)

const closeHint = computed(() => (isMac.value ? '⌘W' : '标题栏关闭按钮'))
const quitHint = computed(() => (isMac.value ? '⌘Q' : '托盘菜单「退出云栈」'))
</script>

<template>
  <div class="modal-mask" @click.self="emit('close')">
    <div class="modal" :class="{ 'modal-wide': kind === 'help' }">
      <button class="modal-x" @click="emit('close')">✕</button>

      <!-- 关于 -->
      <template v-if="kind === 'about'">
        <div class="about-head">
          <img src="/appicon.png" alt="云栈" class="about-logo" draggable="false" />
          <div>
            <div class="about-name">云栈</div>
            <div class="faint" style="font-size:11.5px;margin-top:4px">Headscale 桌面客户端 · v0.1.3</div>
          </div>
        </div>
        <p class="about-desc">
          不安装 Tailscale，节点跑在应用进程内。接管系统网络之后，<span class="mono">ssh</span>、
          <span class="mono">mysql</span>、<span class="mono">psql</span>、<span class="mono">redis-cli</span>
          这些命令行工具直接填 100.x 或内网 IP 就能连，不需要任何代理。
        </p>
        <div class="kv"><span>名字来历</span><b>李白《蜀道难》「天梯石栈相钩连」</b></div>
        <div class="kv"><span>服务端</span><b>Headscale（自建）</b></div>
        <div class="kv"><span>数据目录</span><b class="mono" style="font-size:10px">{{ dataDir }}</b></div>
        <div class="kv"><span>收起窗口</span><b>{{ closeHint }}</b></div>
        <div class="kv"><span>退出应用</span><b>{{ quitHint }}</b></div>
        <div class="about-foot faint">© 2026 Boothua</div>
      </template>

      <!-- 帮助手册 -->
      <template v-else>
        <h2 class="help-title">帮助手册</h2>
        <p class="help-lead">下面每一步都配了界面截图，对着点就行。</p>

        <section class="help-sec">
          <h4><span class="step">1</span>填配置，然后连接</h4>
          <p>
            打开「连接设置」，填 Headscale 地址和 API Key。API Key 在服务器上执行
            <span class="mono">headscale apikeys create</span> 生成，用来管理设备、成员和 ACL；
            Auth Key 只用于节点注册，留空的话会走浏览器授权。
          </p>
          <figure class="shot">
            <img src="/help/settings.png" alt="连接设置" />
            <figcaption>连接设置：地址、凭证都填在这里</figcaption>
          </figure>
          <p style="margin-top:10px">
            填完回到「总览」点右边的<span class="hl">连接</span>。
            <template v-if="isWin">
              Windows 版在双击打开时就会请求管理员权限（UAC），用来创建虚拟网卡（TUN）；
              允许之后这里点一下直接连上。嫌每次弹窗的话，去「连接设置」装一次登录自启，
              之后开机由计划任务以最高权限拉起，不再询问。
            </template>
            <template v-else>
              首次会要求输入管理员密码，用来创建虚拟网卡（TUN）——这是接管系统路由必须的权限，一次授权后不再询问。
            </template>
          </p>
          <figure class="shot">
            <img src="/help/overview.png" alt="总览" />
            <figcaption>总览：右上角是连接按钮，下方显示本节点地址与已接入网段</figcaption>
          </figure>
        </section>

        <section class="help-sec">
          <h4><span class="step">2</span>端口转发</h4>
          <p>
            把 tailnet 里的服务映射到本地端口，比如「本地 3306 → 192.168.31.10:3306」，
            之后用 <span class="mono">mysql -h 127.0.0.1 -P 3306</span> 就能连过去。
          </p>
          <figure class="shot">
            <img src="/help/forwards.png" alt="端口转发" />
            <figcaption>端口转发：上面是引擎开关和监听范围，下面是规则表</figcaption>
          </figure>
          <p style="margin-top:10px">
            注意<span class="hl">监听范围</span>：默认「仅本机」只允许 127.0.0.1，
            同网段的其他设备连不进来。要让别人也能访问，切到「同网段可达」，
            然后用 <span class="mono">本机IP:端口</span> 连。
          </p>
        </section>

        <section class="help-sec">
          <h4><span class="step">3</span>应用管理</h4>
          <p>
            把内网 Web 服务（Nacos、Jenkins、路由器后台）存成卡片。点「应用内打开」会在本地起一个
            反向代理，在独立窗口里加载页面，不用手动拼地址，也不用管证书。
          </p>
          <figure class="shot">
            <img src="/help/apps.png" alt="应用管理" />
            <figcaption>应用管理：每张卡片右上角可以复制地址或跳系统浏览器</figcaption>
          </figure>
        </section>

        <section class="help-sec">
          <h4><span class="step">4</span>多个 Headscale 怎么切换</h4>
          <p>
            顶栏右边那个胶囊点开就是服务端切换器；也可以在「连接设置 → 服务端」里管理。
            点「+ 新增服务端」会弹出一个表单，填地址和 Key 就是另一套环境；每行还能编辑、重命名、删除。
          </p>
          <p style="margin-top:6px">
            每个服务端有独立的节点身份目录和凭证，切换时会断开隧道再重连，
            不会把上一个 tailnet 的登录态带过去。
          </p>
        </section>

        <section class="help-sec">
          <h4><span class="step">5</span>连不上怎么排查</h4>
          <ul>
            <li>总览页大卡片会显示「上次启动失败：…」，先看那句具体错误。</li>
            <li>「连接设置」里点「测试连接」，能确认地址和凭证是否有效。</li>
            <li>
              看详细日志：<template v-if="isMac">终端里跑 <span class="mono">bin/Weft.app/Contents/MacOS/weft --diag</span></template><template v-else>在安装目录里用命令行启动 <span class="mono">weft.exe --diag</span></template>，<span class="mono">[system]</span> 开头的是节点日志，会打印每个对端的在线状态和 DERP。
            </li>
            <li>ACL 改完不生效：Headscale 的策略是整份替换，注意别把别人的规则覆盖掉，可以先点「校验」。</li>
          </ul>
        </section>

        <section class="help-sec">
          <h4><span class="step">6</span>关掉窗口去哪了</h4>
          <p>点关闭只是收起窗口，程序还在托盘里跑，隧道不断。真正退出用托盘菜单的「退出云栈」{{ isMac ? '或 ⌘Q' : '' }}。</p>
        </section>
      </template>
    </div>
  </div>
</template>
