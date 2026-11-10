# herdr-autopin
[![ci](https://github.com/Tomyail/herdr-autopin/actions/workflows/ci.yml/badge.svg)](https://github.com/Tomyail/herdr-autopin/actions/workflows/ci.yml)

[English](README.md) | 简体中文

盯着 [Herdr](https://herdr.dev) 里的每个 agent，每次有 agent 停下来就问一句：
**这一轮是不是在等我决定？** 是的话给它的终端打上标签 `herdr.pinned=1`，
让它一直留在 Herdr 列表的最上面，直到你处理它。

Herdr 在你看一眼 agent 之后就会把它从 `done` 变成 `idle`，不管你有没有做决定。pin 是那个会留下来的记号。

回答这个问题的是 Jev，[TypeSafe AI](https://docs.typesafe.ai/introduction/quickstart) 的 System One 模型：
便宜，每次调用大约一秒。herdr-autopin 里没有任何针对某个 agent 界面的规则，
所以任何向 Herdr 报告状态的 agent 都能用（实测过 pi 和 codex）。

```text
herdr-core ──unix socket──> herdr-autopin ──HTTPS──> api.typesafe.ai/v1/systemone
   status 事件             这一轮前后的两屏          blocked_on_user、asked_question
```

> **哪些内容会离开你的机器。** 每次 agent 停下，它的终端在这一轮前后各自最后 60 行非空内容
> 会被发到 `api.typesafe.ai`，其中可能有屏幕上的代码或密钥。
> 用 `-only` 限定要看的终端，用 `-dry` 先看看它会怎么判。

## 用法

需要 Go 1.25+、正在运行的 herdr-core，以及 TypeSafe 的 API key。

```sh
export TYPESAFE_API_KEY=...
go build -o herdr-autopin ./cmd/herdr-autopin
./herdr-autopin            # 监听并 pin
./herdr-autopin -dry       # 监听并判断，但不 pin
./herdr-autopin -judge t7  # 只判断一个终端一次，不 pin（没有"之前那一屏"，分数偏高）
./herdr-autopin -judge - < screens.json  # 判断两屏快照：{"base":[行],"now":[行]}
```

日志：

```text
22:17:49 t8z  working -> done  "当前目录文件数量"
22:17:54 t8z  jev blocked=0.07 asked=0.04  not mine
22:18:07 t8z  working -> done  "合并方案"
22:18:10 t8z  jev blocked=0.74 asked=0.94  PINNED
```

`make dev` 会构建并在后台重启它，日志写到 `/tmp/herdr-autopin.log`。

## 装成服务

macOS。安装脚本把二进制放到 `~/.local/bin`，并注册成 LaunchAgent
（`~/Library/LaunchAgents/com.tomyail.herdr-autopin.plist`）：登录自启，退出会自动拉起。

```sh
curl -fsSL https://raw.githubusercontent.com/Tomyail/herdr-autopin/main/install.sh \
  | TYPESAFE_API_KEY=... sh
```

在仓库里直接 `./install.sh --key ...` 效果相同。要传给服务的参数写在 `--` 之后：
`./install.sh --dry -- --only herdr.space=<id>`。

```sh
./install.sh --uninstall            # 停掉服务，保留二进制
./install.sh --uninstall --purge    # 连二进制一起删
launchctl print gui/$(id -u)/com.tomyail.herdr-autopin   # 看是否在跑
tail -f ~/Library/Logs/herdr-autopin.log                 # 看它做了什么
```

plist 里存着 API key，所以权限是 600。重装会覆盖它。预编译包（darwin arm64/amd64）来自
GitHub Releases；还没有 release 时，只要装了 Go，脚本会本地编译。

## 参数

| 参数 | 默认 | 作用 |
|---|---|---|
| `-blocked` | `0.5` | `blocked_on_user` 达到这个值就 pin |
| `-asked` / `-asked-floor` | `0.6` / `0.25` | 或者 `asked_question` 达到 `-asked`，同时 `blocked_on_user` 至少 `-asked-floor` |
| `-label k=v` | `herdr.pinned=1` | 判定在等你时打的标签；换成自己的标签就不占用 pin |
| `-only k=v` | 全部终端 | 只看带这个标签的终端，可重复（`-only herdr.space=<id>`）。由 core 过滤，其它终端的屏幕不会被读取 |
| `-tail` | `60` | 每屏发给 Jev 的行数（去掉空行后的最后 N 行） |
| `-jev-url` / `-jev-model` | `https://api.typesafe.ai/v1/systemone` / `jev-latest` | Jev 的地址和模型 |
| `-dry` | 关 | 监听并判断，只写日志，不打标签 |

环境变量：`TYPESAFE_API_KEY`（必需），`HC_SESSION`（herdr-core 的 session，默认 `default`）。

默认阈值是用很少的样本定的。先用 `-dry` 跑一段时间，把日志里的分数和你自己的判断对一对，再调。

## 它怎么判断

1. 订阅 herdr-core 的事件，agent 的状态变成 `done` 或 `blocked` 时开始处理。
2. agent 自己说了为什么卡住（要权限、在提问：`status.kind`）就直接 pin，不读屏幕，也不调用 Jev。
3. 否则读终端，把两屏发给 Jev：`screen_before` 是这一轮开始前终端的样子，`screen_after` 是现在的样子。
   Jev 自己分辨哪些是这一轮写的、哪些是界面。只给一屏不行：页脚、横幅、更新提示会被当成在等你。
4. 一次调用问两个是非题，各返回一个概率：

| 问题 | 含义 |
|---|---|
| `blocked_on_user` | 用户不回答或不决定，工作就没法继续 |
| `asked_question` | agent 的最后一条消息在问用户，或者给了选项 |

"之前那一屏"按终端存在内存里：agent 每次停下（`idle`、`done`、`blocked`）时读一次，
间隔 0.4 秒的两次读取除数字外一致才算数。一启动就带任务、从没停过的 agent，用它刚进入 `working` 时的屏幕。
两者都没有（herdr-autopin 在这一轮中途才启动）的话，这一轮不判。

## 其它机器上的 agent

不用额外配置。本机 core 会列出它连着的机器上的终端，地址是 `machine/t7`；
事件、读屏、打标签都直接用这种地址，标签写在那台机器的 core 上，所以每个 Herdr 客户端都看得到。

```text
17:37:47 mini/t3t  working -> done  "Exact OK reply"
17:37:51 mini/t3t  jev blocked=0.26 asked=0.05  not mine
```

## herdr-core socket 的两个注意点

1. **订阅过的连接不再回答请求。** herdr-autopin 开两条：一条收事件，一条读屏和打标签。
2. **状态是对象**，`{"state":"done","app":"pi","msg":"..."}`，不是字符串。

core 不在的时候（更新、重启），herdr-autopin 每 3 秒重试一次，直到它回来。

## 限制

- 它从不取消 pin。你做完决定后在 Herdr 里自己取消。
- herdr-autopin 启动时已经在跑的那一轮没有"之前那一屏"，不判。
- 同一轮可能被判断两次。
- core 的 `missed` 事件（发给落后的订阅者）没有处理，之后可能漏掉一次状态变化。
- 没测过超过 `-tail` 行的长回复，也没测过 pi 和 codex 以外的 agent。

## 许可

MIT
