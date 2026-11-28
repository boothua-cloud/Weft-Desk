<script setup lang="ts">
import { computed } from 'vue'
import type { State } from '../api'

const props = defineProps<{ kind: 'about' | 'help'; state?: State | null }>()
const emit = defineEmits(['close', 'check-update'])

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
            <div class="faint" style="font-size:11.5px;margin-top:4px">Headscale 桌面客户端 · v{{ state?.appVersion || '—' }}</div>
          </div>
        </div>
        <p class="about-desc">
          不装 Tailscale，节点跑在应用进程里。接管系统网络后，<span class="mono">ssh</span>、
          <span class="mono">mysql</span>、<span class="mono">psql</span>、<span class="mono">redis-cli</span>
          直接填 100.x 或内网 IP 就能连，不用配代理。
        </p>
        <div class="kv"><span>名字来历</span><b>李白《蜀道难》「天梯石栈相钩连」</b></div>
        <div class="kv"><span>服务端</span><b>Headscale（自建）</b></div>
        <div class="kv"><span>数据目录</span><b class="mono" style="font-size:10px">{{ dataDir }}</b></div>
        <div class="kv"><span>收起窗口</span><b>{{ closeHint }}</b></div>
        <div class="kv"><span>退出应用</span><b>{{ quitHint }}</b></div>
        <button class="btn ghost sm upd-entry" @click="emit('check-update')">检查更新</button>
        <div class="about-foot faint">© 2026 Boothua</div>
      </template>

      <!-- 帮助手册 -->
      <template v-else>
        <h2 class="help-title">帮助手册</h2>
        <p class="help-lead">按页面顺序写，每个按钮都在这。遇到问题直接跳到第 13 节。</p>

        <section class="help-sec">
          <h4><span class="step">1</span>填配置，连上第一个服务端</h4>
          <p>
            「连接设置 → 服务端」点<span class="hl">编辑</span>（或「+ 新增服务端」）打开表单：
            填服务端地址，比如 <span class="mono">https://hs.example.com</span>；Hostname 是本机在 tailnet 里的名字，可留空。
          </p>
          <figure class="shot">
            <img src="/help/settings-form.png" alt="服务端表单" />
            <figcaption>服务端表单：地址、Hostname、两把 Key 都在这</figcaption>
          </figure>
          <p style="margin-top:10px">
            两把 Key 分工不同，别填反：
          </p>
          <ul>
            <li><span class="hl">节点注册密钥 Auth Key</span>：让这台机器加入 tailnet。在服务端跑 <span class="mono">headscale preauthkeys create --user 1</span> 生成；留空则点连接时走浏览器授权，总览页会给出登录链接。</li>
            <li><span class="hl">管理密钥 API Key</span>：调管理接口用，设备、人员、密钥、ACL 四个页面都依赖它。在服务端跑 <span class="mono">headscale apikeys create</span> 生成，格式是 <span class="mono">hskey-api-</span> 开头。</li>
          </ul>
          <p style="margin-top:8px">
            填完点保存，回「总览」点<span class="hl">连接</span>。
            <template v-if="isWin">
              Windows 版启动时申请一次管理员权限（UAC），用来建虚拟网卡；不想每次弹窗，在连接设置里装一次「系统助手」。
            </template>
            <template v-else>
              首次要输管理员密码建虚拟网卡（TUN），这是接管系统路由必须的权限，装一次系统助手之后不再问。
            </template>
          </p>
        </section>

        <section class="help-sec">
          <h4><span class="step">2</span>总览：连接状态和本机信息</h4>
          <p>
            最上面那张大卡片是当前状态：<span class="hl">连接</span> / <span class="hl">重连</span> /
            <span class="hl">断开连接</span>都在右上角，下面一行是本机在 tailnet 的地址（<span class="mono">100.x</span>）。
            连接失败时，卡片上会直接写原因。
          </p>
          <figure class="shot">
            <img src="/help/overview.png" alt="总览" />
            <figcaption>总览：状态卡、四项指标、本节点、已接入网段、快捷应用</figcaption>
          </figure>
          <p style="margin-top:10px">四张指标分别是：</p>
          <ul>
            <li><b>在线设备</b>：tailnet 内在线 / 全部节点数。</li>
            <li><b>已接入网段</b>：本机接受的子网路由条数，全按最长前缀生效。</li>
            <li><b>运行时长</b>：本次隧道保持的时间。</li>
            <li><b>管理密钥</b>：API Key 剩余天数与到期时间，低于 15 天会变黄。点「续期」会新建一把并写进钥匙串，旧 Key 立即失效。</li>
          </ul>
          <p style="margin-top:8px">
            下面依次是「本节点」「已接入网段」「快捷应用」和「最近事件」。快捷应用就是「应用管理」里的书签，点一下直接打开，右侧「管理」跳过去维护。
            没填 Auth Key 时，这里会出现「复制登录链接」，粘到浏览器里完成授权即可注册本机。
            节点快到期时顶部会多一张提醒卡，点「取消到期」可续期。
          </p>
        </section>

        <section class="help-sec">
          <h4><span class="step">3</span>设备管理</h4>
          <p>
            列出 tailnet 里所有节点：设备名、归属成员、IP、连接方式（直连 / 中继）、标签、最后在线、在线状态。
            上面一排可以按名字 / IP / 标签搜索，也能按状态、成员、标签筛选，右侧显示「在线 / 离线 / 过期」计数。
          </p>
          <figure class="shot">
            <img src="/help/manage.png" alt="设备管理" />
            <figcaption>设备管理：搜索、筛选、注册与审批都在顶部</figcaption>
          </figure>
          <p style="margin-top:10px">顶部四个按钮的用途：</p>
          <ul>
            <li><b>补分配 IP</b>：给没有地址的老节点补 IP。正常注册的节点都有，一般不用点。</li>
            <li><b>手动添加节点</b>：调试用，直接按 IP 造一条记录。</li>
            <li><b>待审批请求</b>：节点用 auth ID 等待批准时，在这里填 ID 批准或拒绝。</li>
            <li><b>注册设备</b>：选成员 + 填 Auth Key，替另一台机器完成注册。</li>
          </ul>
          <p style="margin-top:8px">
            每行右侧「管理」打开详情抽屉：改主机名、打标签（<span class="mono">tag:server</span>）、勾选批准该节点通告的子网路由、
            让节点立即到期或取消到期、删除设备。删除会连同它的路由一起消失，正在连的客户端会断开。
          </p>
        </section>

        <section class="help-sec">
          <h4><span class="step">4</span>人员管理</h4>
          <p>
            成员就是 headscale 的用户命名空间，设备归属在成员下，ACL 里引用成员也用这里的名字。
            表格给出成员名、显示名称、邮箱、设备数（在线 / 总数）和创建时间；点设备数字会跳到设备页并按该成员筛好。
          </p>
          <figure class="shot">
            <img src="/help/manage-users.png" alt="人员管理" />
            <figcaption>人员管理：成员名即 headscale 用户名</figcaption>
          </figure>
          <p style="margin-top:10px">
            「+ 新建成员」填名称（必填）、显示名称、邮箱、头像地址，后三项可空。行内「编辑」可改名，
            改名会同步到 headscale，设备归属和 ACL 引用跟着走；「删除」要求先把该成员名下的设备清空。
            服务端不支持创建用户的版本，这两个按钮是灰的。
          </p>
        </section>

        <section class="help-sec">
          <h4><span class="step">5</span>授权 Key</h4>
          <p>
            授权 Key 用来让设备加入 tailnet，等价于 <span class="mono">headscale preauthkeys</span>。
            列表显示 Key 前缀、归属成员、类型（一次性 / 可复用）、到期时间、创建时间和状态（有效 / 已使用 / 已过期）。
          </p>
          <figure class="shot">
            <img src="/help/manage-keys.png" alt="授权 Key" />
            <figcaption>授权 Key：新建时选成员、有效期和标签</figcaption>
          </figure>
          <p style="margin-top:10px">新建时可以设：</p>
          <ul>
            <li><b>归属成员</b>：用这把 Key 注册的节点归到谁名下。</li>
            <li><b>有效期</b>：1 / 7 / 30 / 90 / 180 / 365 天，过期后不能再注册。</li>
            <li><b>可复用</b>：允许多台设备共用同一把 Key；关掉就是一次性。</li>
            <li><b>临时节点</b>：节点离线后自动从 tailnet 删除，适合临时容器。</li>
            <li><b>ACL 标签</b>：注册的节点自动带上这些标签（<span class="mono">tag:server,tag:db</span>），省得事后手动打。</li>
          </ul>
          <p style="margin-top:8px">
            生成后明文只显示一次，关掉就查不到了，点「复制」先存好。列表里「吊销」让 Key 立即作废（用过它的设备不受影响），「删除」抹掉记录。
          </p>
        </section>

        <section class="help-sec">
          <h4><span class="step">6</span>API Key</h4>
          <p>
            API Key 是给管理接口用的，云栈自己也要用它才能读设备、人员、密钥和 ACL。
            列表只显示前缀：<span class="mono">hskey-api-3f9c4a</span>，附带到期时间、创建时间、最后使用和状态。
          </p>
          <figure class="shot">
            <img src="/help/manage-api.png" alt="API Key" />
            <figcaption>API Key：新建时只选有效期，明文只出现一次</figcaption>
          </figure>
          <p style="margin-top:10px">
            新建只要选有效期（1 到 365 天）。「吊销」立即作废，正在用它的客户端当场失去管理权限；
            「删除」只是清掉这条记录，想留痕就用吊销。当前这把被吊销后，去连接设置换成新的才能继续管理。
          </p>
        </section>

        <section class="help-sec">
          <h4><span class="step">7</span>ACL 策略</h4>
          <p>
            三个页签共用同一份策略，顺序是先定义「谁」和「哪些设备」，再写「谁能访问谁」，最后才是源码。
          </p>
          <figure class="shot">
            <img src="/help/acl-scope.png" alt="用户组与标签" />
            <figcaption>用户组与标签：组里填成员，标签填谁能打这个标签</figcaption>
          </figure>
          <p style="margin-top:10px">
            <span class="hl">用户组与标签</span>：组是一批人，规则里写组名，人员变动只改这里；标签是一批设备，
            服务器、NAS 这类固定角色打上标签，换设备不用改规则。成员名保存时会自动补 <span class="mono">@</span>。
          </p>
          <figure class="shot">
            <img src="/help/acl.png" alt="规则" />
            <figcaption>规则：谁 → 哪些设备 → 哪些端口，下方钉着权限矩阵</figcaption>
          </figure>
          <p style="margin-top:10px">
            <span class="hl">规则</span>：一条规则读作「谁 → 哪些设备 → 哪些端口」。目标按端口分组，端口能从常用服务（SSH、HTTPS、MySQL…）里选，
            也能手写 <span class="mono">8000-8100</span> 或「全部端口」；一条规则可以加多组目标。
            策略为空时页面会给三个起点模板：只访问自己的设备、管理员全通其他人各管各的、保持全部互通。
          </p>
          <p style="margin-top:8px">
            矩阵按当前规则推演：行是人（和标签），列是设备，每格显示实际能通的端口。
            某个成员一行全空说明写漏了，矩阵会自动展开；某列没人访问只提示，那多半是故意的隔离。
            改完点「校验」看本地和服务端的检查结果，再点「保存并应用」。
          </p>
          <figure class="shot">
            <img src="/help/acl-source.png" alt="源码" />
            <figcaption>源码：直接改整份 HuJSON，保存前可以格式化和校验</figcaption>
          </figure>
          <p style="margin-top:10px">
            <span class="hl">源码</span>切过去前会先解析一遍，解析不了就不让切，避免把策略改坏。
            源码里能写 Grants、Tailscale SSH、autoApprovers 这些表单不碰的字段，保存时原样写回；
            用表单保存会把 JSON 注释（HuJSON）去掉。服务端把策略存在文件里（<span class="mono">policy.mode: file</span>）时，这里只能读，页面上会写明改法。
          </p>
        </section>

        <section class="help-sec">
          <h4><span class="step">8</span>应用管理</h4>
          <p>
            把内网 Web 服务（Nacos、Jenkins、路由器后台）存成卡片。地址支持 <span class="mono">100.x</span>、
            MagicDNS 名称和自定义域名；可以分组、挑图标颜色，搜索框按名称或地址过滤。
          </p>
          <figure class="shot">
            <img src="/help/apps.png" alt="应用管理" />
            <figcaption>应用管理：卡片上直接打开，右上角可编辑或删</figcaption>
          </figure>
          <p style="margin-top:10px">
            每张卡片三个动作：<span class="hl">应用内打开</span>（在本地起反向代理，用独立窗口加载，浏览器按钮同样走它）、
            <span class="mono">↗</span> 用系统浏览器打开、<span class="mono">✎</span> 编辑。
            表单里打开「用系统浏览器打开」后，卡片主按钮会变成「浏览器打开」。
            应用是自签 HTTPS 时应用内窗口会白屏，去连接设置打开「信任自签 HTTPS 证书」。
            反代只在云栈运行期间有效，关掉应用链接就断了。
          </p>
        </section>

        <section class="help-sec">
          <h4><span class="step">9</span>端口转发</h4>
          <p>
            把 tailnet 里的服务映射到本地端口，比如「本地 3306 → 192.168.10.20:3306」，之后
            <span class="mono">mysql -h 127.0.0.1 -P 3306</span> 就能连过去。顶部是总开关、SOCKS5 开关（默认 <span class="mono">1080</span>）和「连接后自动启动」。
          </p>
          <figure class="shot">
            <img src="/help/forwards.png" alt="端口转发" />
            <figcaption>端口转发：引擎、SOCKS5、监听范围、规则表</figcaption>
          </figure>
          <p style="margin-top:10px">
            注意<span class="hl">监听范围</span>：默认「仅本机」只监听 <span class="mono">127.0.0.1</span>，同网段的设备连不进来；
            要让别人也能访问，切到「同网段可达」，再用 <span class="mono">本机IP:端口</span> 连。
            规则表每行可以禁用、编辑、删除，点「命令」给出这段映射对应的连接命令；流量列是这条规则累计转发量。
          </p>
        </section>

        <section class="help-sec">
          <h4><span class="step">10</span>连接设置</h4>
          <p>
            上半部分是服务端列表：可以放多个 Headscale，点「切换」切过去，也支持编辑、重命名、删除。
            每个服务端有独立的节点身份和凭证，切换时断开隧道再重连，不会把上一个 tailnet 的登录态带过去。
          </p>
          <figure class="shot">
            <img src="/help/settings.png" alt="连接设置" />
            <figcaption>连接设置：服务端、系统助手、外观和启动项</figcaption>
          </figure>
          <p style="margin-top:10px">下面的分组：</p>
          <ul>
            <li><b>系统助手</b>：装一次之后建虚拟网卡、改路由都不再弹管理员密码；不需要就点「移除」，之后恢复每次授权。</li>
            <li><b>外观主题</b>：跟随系统 / 亮色 / 暗色，顶栏那个太阳/月亮按钮也能切。</li>
            <li><b>开机自动连接</b>：登录后自动拉起并连上隧道。</li>
            <li><b>断线自动重连</b>：网络恢复后自己重连，不用手动点。</li>
            <li><b>系统托盘显示图标</b>：关窗口不退出，只在托盘常驻。</li>
          </ul>
        </section>

        <section class="help-sec">
          <h4><span class="step">11</span>顶栏、菜单与快捷键</h4>
          <p>
            顶栏右边依次是：<span class="hl">服务端胶囊</span>（点开切换服务端，底部有「管理服务端…」）、
            主题按钮、刷新、<span class="hl">更多</span>菜单（刷新数据、切换主题、帮助手册、检查更新、关于云栈）。
          </p>
          <p style="margin-top:8px">
            窗口内 <span class="mono">F12</span> / <span class="mono">⌘⌥I</span> 打开 WebView 调试器。
            <template v-if="isMac">快捷键：<span class="mono">⌘R</span> 刷新数据，<span class="mono">⌘W</span> 收起窗口，<span class="mono">⌘Q</span> 退出。</template>
            <template v-else>刷新、主题、退出都在「更多」菜单和托盘菜单里。</template>
          </p>
        </section>

        <section class="help-sec">
          <h4><span class="step">12</span>托盘和退出</h4>
          <p>
            关窗口只是收起，程序还在托盘里跑，隧道不断。托盘菜单从上到下是：当前状态、连接 / 断开连接、显示主窗口、
            帮助手册、设置、关于云栈、退出云栈。要真正退出用「退出云栈」{{ isMac ? '或 ⌘Q' : '' }}。
          </p>
        </section>

        <section class="help-sec">
          <h4><span class="step">13</span>连不上怎么排查</h4>
          <ul>
            <li>先看总览页大卡片：「上次启动失败：…」那句就是原因。</li>
            <li>确认地址可达、两把 Key 没填反。API Key 用错成 Auth Key 时，管理页会直接提示。</li>
            <li>管理页整块消失或提示没配 Key：说明 API Key 无效或过期，去服务端 <span class="mono">headscale apikeys create</span> 换一把。</li>
            <li>ACL 改完不生效：策略是整份替换，确认没覆盖掉别人的规则；保存前先点「校验」。文件模式的策略只能读，改服务器上的文件后执行 <span class="mono">systemctl reload headscale</span>。</li>
            <li>
              看详细日志：<template v-if="isMac">终端里跑 <span class="mono">/Applications/云栈.app/Contents/MacOS/weft --diag</span></template><template v-else>在安装目录里用命令行启动 <span class="mono">weft.exe --diag</span></template>，<span class="mono">[system]</span> 开头的是节点日志，会打印每个对端的在线状态和 DERP。
            </li>
            <li>应用内窗口白屏且地址是 https：打开「信任自签 HTTPS 证书」，或改用系统浏览器打开。</li>
          </ul>
        </section>

        <section class="help-sec">
          <h4><span class="step">14</span>已知边界</h4>
          <ul>
            <li>只对接 Headscale 的 <span class="mono">/api/v1</span>，不兼容 Tailscale 官方接口。</li>
            <li>不做 MagicDNS（统一用 IP）、不做 Exit Node、不做 Taildrop。</li>
            <li>子网路由要在服务端批准后才生效；本机也可以在设备详情里勾选批准。</li>
            <li>应用内窗口用的是系统 WebView，装不了浏览器扩展（Bitwarden 之类）；需要扩展就改用系统浏览器。</li>
            <li>Windows 的提权粒度是整个进程，WinTun 没有「把句柄传给非提权进程」的路径。</li>
          </ul>
        </section>
      </template>
    </div>
  </div>
</template>
