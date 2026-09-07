# AnyRouter Python 客户端

专为 `anyrouter.top` 平台定制的 Python 客户端，针对该平台的严格限制与高并发排队机制进行了完整适配与深度伪装。

## 核心特性

1. **双协议深度伪装**：
   - **Codex 协议 (`/v1/responses`)**：适配模型 `gpt-6-astra`。伪装官方 `codex_exec` 客户端特征，注入 `x-openai-internal-codex-responses-lite`、窗口元数据、轮次元数据、客户端指纹与 reasoning 结构。
   - **Claude Code 协议 (`/v1/messages?beta=true`)**：适配模型 `claude-fable-5-1`。伪装官方 `claude-cli/2.1.226 (external, sdk-cli)`，注入计费声明系统块、Claude Agent SDK 身份声明、Stainless 运行时特征与 64 位设备指纹。
2. **智能轮次排队重试机制（默认：每隔 1 分钟挤一轮，每轮 5 次，间隔 5s）**：
   - 针对平台拥挤时常见的 `429 (Service Unavailable / Too Many Requests)`、`520 (Origin Error)`、`502/503/504` 等状态码以及流内限流报错。
   - **分轮重试策略**：同一轮内尝试 5 次（每次间隔 5s）；若 5 次均未挤入，则整轮等待 1 分钟（60s）后自动开启下一轮，持续重试直到排上挤入！
   - 终端提供高可读性的**秒级倒计时读秒与排队轮次实时反馈**。
3. **本地 10808 代理支持**：
   - 默认开箱即用集成 `http://127.0.0.1:10808` 代理通道。
4. **流式打字机输出 & 思考过程展示**：
   - 完整支持 SSE 流式解析，区分展示 Thinking 思考过程与最终输出。

---

## 安装依赖

```bash
pip install -r requirements.txt
```

---

## 使用方法

### 1. 双模型并发一起挤（默认模式，最推荐）

直接启动：
```bash
python main.py
```
- **工作机制**：多线程并发，同时启动两条独立排队通道：
  1. `Codex` (`gpt-6-astra`，基于 Responses 协议)
  2. `Claude Code` (`claude-fable-5-1`，基于 Messages 协议)
- **排队规则**：双模型各自独立执行“每轮 5 次，每次间隔 5s；整轮未挤上冷却 60s 开启下一轮”，直到挤成功。
- **成果保存**：哪个模型先挤成功，立即打印其实时回复并独立保存为 `squeeze_<模型名>.txt`，另一模型继续排队不受影响。

### 2. 单独指定挤某一个模型

只挤 Codex (`gpt-6-astra`)：
```bash
python main.py -m gpt-6-astra
```

只挤 Claude Code (`claude-fable-5-1`)：
```bash
python main.py -m claude-fable-5-1
```
```bash
python main.py -m gpt-6-astra -p "请用 Python 写一个支持泛型的 LRU 缓存类"
```

使用 Claude Code 模型提问：
```bash
python main.py -m claude-fable-5-1 -p "解释一下为什么 TCP 四次挥手需要 TIME_WAIT 状态"
```

指定最大重试次数（如只重试 10 次）：
```bash
python main.py -m gpt-6-astra -p "Hello" --max-retries 10
```

### 3. 作为 Python SDK 导入使用

可以在您自己的 Python 代码中直接调用：

```python
from anyrouter_client import AnyRouterClient

# 初始化客户端（默认使用 10808 代理与内置 API Key）
client = AnyRouterClient(proxy="http://127.0.0.1:10808")

# --- 1. 单次完整调用 ---
response = client.chat(
    model="gpt-6-astra",  # 或 "claude-fable-5-1"
    prompt="请写一段经典的冒泡排序 Python 代码",
)
print(response)

# --- 2. 流式逐字生成 ---
for chunk in client.stream_chat(model="claude-fable-5-1", prompt="写一首赞美春天的短诗"):
    chunk_type = chunk.get("type")
    if chunk_type == "thinking":
        print(f"[思考]: {chunk['delta']}", end="", flush=True)
    elif chunk_type == "text":
        print(chunk["delta"], end="", flush=True)
    elif chunk_type == "status":
        print(f"\n{chunk['message']}")
```

---

## 协议与模型对照表

| 客户端类别 | 目标模型 | 协议端点 | 伪装身份 |
| :--- | :--- | :--- | :--- |
| **Codex** | `gpt-6-astra` | `POST /v1/responses` | `codex_exec/0.144.1` + Responses Lite |
| **Claude Code** | `claude-fable-5-1` | `POST /v1/messages?beta=true` | `claude-cli/2.1.226` + Billing System Block |
