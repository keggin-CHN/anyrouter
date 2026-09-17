# AnyRouter Go 客户端与挂机保活系统 (Go 纯净轻量版)

专为 `anyrouter.top` 平台定制的 **Go 语言高性能轻量客户端与全自动化挂机排队保活工具**。
纯 Go 标准库驱动，**免 Cgo 依赖，零第三方网络库，单文件独立二进制仅约 7MB**（较原 Python 版 38.5MB 缩小 80%+，内存占用由 120MB+ 降至约 15MB）。

---

## 核心特性

1. **双协议深度伪装**：
   - **Codex 协议 (`/v1/responses`)**：适配 `gpt-6-astra`、`gpt-5-codex`、`o*` 等模型。深度伪装官方 `codex_exec/0.144.1 (Linux; x86_64)`，自动注入 Responses Lite 特征、window/turn 动态元数据及 reasoning 推理参数。
   - **Claude Code 协议 (`/v1/messages?beta=true`)**：适配 `claude-fable-5-1` 等。深度伪装官方 `claude-cli/2.1.226 (external, sdk-cli)`，注入 8 项 Anthropic beta 特性头、计费声明系统块（`x-anthropic-billing-header`）与 64 位设备硬件指纹。
2. **两阶段智能挂机保活状态机**：
   - **阶段一【用不了】**：分轮循环排队挤入（默认每轮 5 次，间隔 5s；整轮未果冷却 30s）。秒级动态倒计时显示，首字出字即视为挤入成功。
   - **阶段二【能用】**：保持可用状态，开启 30 分钟巡航倒计时，“能用就不管”；到期自动轻量测活，失效则自动跌回阶段一重新排队。
3. **精选 4 大核心模型**：
   - 精准收敛支持且仅保留：`gpt-6-astra`、`claude-opus-4-8`、`claude-fable-5-1`、`gemini-2.5-pro`。
4. **100 道极简预设测活题库 & 随机轮换**：
   - 内置包含算术、单字回复、常识问答、微翻译、极简编程与逻辑推理等 100 道超低 Token 消耗的高速响应题，每次随机抽取，杜绝平台缓存与自动化判定。
5. **多 Key × 多模型并发矩阵**：
   - 支持凭据池配置多个 API Key，并发为每个 Key 与模型建立独立守护通道。
6. **专属应用图标与后台静默守护**：
   - 内置专属路由器脉冲图标（嵌入 `.exe` 与任务栏、托盘）。
   - 点击窗口右上角关闭按钮（✕）自动隐藏并转入后台**静默保活**，**彻底去除流氓通知弹窗与系统提示音**，安静守护不打扰。
   - 双击托盘小图标可随时重新唤出控制面板；右键菜单支持【彻底退出程序】。

---

## 快速上手

### 1. 运行挂机保活桌面程序 (原生桌面 GUI，双击即用)

直接双击运行：
```cmd
anyrouter-keeper.exe
```

- **自带专属图标与原生独立窗口**：双击直接弹出 1120×840 独立桌面窗口，无任何黑框命令行控制台，界面交互体验与原 Python 版本 1:1 像素级对齐。
- **状态栏托盘常驻与静默模式**：关闭窗口自动进入后台静默保活状态，右下角托盘区常驻小图标，无任何弹窗通知骚扰。
- **开放式前端源码**：前端源码位于根目录下 [`web/index.html`](file:///c:/code/anyrouter/web/index.html)，修改保存后在窗口中刷新即可即时生效。
- **脱机单文件保障**：即使分发单文件 `anyrouter-keeper.exe` 到没有 `web/` 目录的新电脑，程序也会自动回退至内置的嵌入式前端，真正实现单文件零依赖。
- **跨平台 Web 访问**：启动后同时在本地提供 Web 面板：`http://127.0.0.1:28888`，可在任意浏览器中直接访问。

---

## 作为 Go SDK 导入项目

在您自己的 Go 代码中直接调用：

```go
package main

import (
	"context"
	"fmt"
	"time"

	"anyrouter/pkg/client"
)

func main() {
	// 初始化客户端 (默认使用 10808 代理与内置/环境变量 Key)
	c, err := client.NewClient(client.ClientConfig{
		APIKey:  "sk-your-api-key",
		Proxy:   "http://127.0.0.1:10808",
		Timeout: 60 * time.Second,
	})
	if err != nil {
		panic(err)
	}

	ctx := context.Background()

	// 1. 同步非流式调用
	reply, err := c.Chat(ctx, "gpt-6-astra", "请用 Go 写一个并发安全的缓存类")
	if err != nil {
		fmt.Printf("调用失败: %v\n", err)
	} else {
		fmt.Println(reply)
	}

	// 2. 流式打字机调用 (区分思考过程与正文)
	events, err := c.StreamChat(ctx, "claude-fable-5-1", "写一首短诗", nil, "", "", 2048)
	if err != nil {
		panic(err)
	}

	for ev := range events {
		if ev.Type == "thinking" {
			fmt.Printf("[思考]: %s", ev.Delta)
		} else if ev.Type == "text" {
			fmt.Print(ev.Delta)
		}
	}
}
```

---

## 编译指南 (如需重新构建)

本项目采用 100% 纯 Go 实现，**无需 gcc，无需 Cgo**：

```bash
# 编译带内置原生应用图标、零黑框的独立桌面应用程序
go build -ldflags="-H windowsgui -s -w" -o anyrouter-keeper.exe .
```
