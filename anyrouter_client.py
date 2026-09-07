"""
AnyRouter Client: Dedicated Python Client for anyrouter.top
Supports:
  1. Codex Protocol (Responses API): model 'gpt-6-astra'
  2. Claude Code Protocol (Messages API): model 'claude-fable-5-1'
Features:
  - Deep client masquerading (official headers, SDK billing blocks, device fingerprinting, session tracing)
  - Full local proxy support (default http://127.0.0.1:10808)
  - Persistent queue retry mechanism (handles 429, 520, 502, 503, 504 and stream failures)
  - Streaming SSE generator yielding thinking/reasoning and output text
  - Multi-turn conversation management
"""

import json
import os
import random
import re
import secrets
import time
import uuid
from typing import Any, Callable, Dict, Generator, List, Optional, Tuple, Union

import requests
from requests.adapters import HTTPAdapter
from urllib3.util.retry import Retry

# --- Constants & Signatures for Client Masquerade ---

DEFAULT_BASE_URL = "https://anyrouter.top"
DEFAULT_PROXY = "http://127.0.0.1:10808"

# Claude Code 官方客户端特征 (Aligned with Claude Code 2.1.226)
CLAUDE_CODE_VERSION = "2.1.226"
CLAUDE_CODE_VERSION_BUILD = "2.1.226.b94"
STAINLESS_PACKAGE_VERSION = "0.94.0"
ANTHROPIC_BETA = (
    "claude-code-20250219,"
    "context-1m-2025-08-07,"
    "interleaved-thinking-2025-05-14,"
    "thinking-token-count-2026-05-13,"
    "context-management-2025-06-27,"
    "prompt-caching-scope-2026-01-05,"
    "mid-conversation-system-2026-04-07,"
    "effort-2025-11-24"
)

# Codex 官方客户端特征 (Aligned with Codex Responses Lite 0.144.1)
CODEX_VERSION = "0.144.1"

# 排队及网络重试状态码
RETRYABLE_STATUS_CODES = {408, 409, 429, 500, 502, 503, 504, 520, 522, 524}


class AnyRouterClient:
    """AnyRouter 平台的专用客户端，支持 Codex 和 Claude Code 双协议与自动排队重试"""

    def __init__(
        self,
        api_key: Optional[str] = None,
        base_url: Optional[str] = None,
        proxy: Optional[str] = DEFAULT_PROXY,
        max_retries: Optional[int] = None,
        attempts_per_round: int = 5,
        intra_round_delay: float = 5.0,
        inter_round_delay: float = 60.0,
        timeout: Tuple[int, int] = (15, 90),
    ):
        """
        初始化 AnyRouter 客户端

        :param api_key: API 密钥，若未提供则从环境变量 ANYROUTER_API_KEY 获取，默认内置用户 Key
        :param base_url: 平台基础 URL，默认 https://anyrouter.top
        :param proxy: 本地代理地址，默认 http://127.0.0.1:10808 (设为 None 或 "" 可关闭代理)
        :param max_retries: 最大总重试次数，None 表示持续重试直到成功排队挤上
        :param attempts_per_round: 每轮尝试次数 (默认 5 次)
        :param intra_round_delay: 同一轮内每次尝试的间隔秒数 (默认 5.0 秒)
        :param inter_round_delay: 轮与轮之间的等待秒数 (默认 60.0 秒，即 1 分钟)
        :param timeout: (connect_timeout, read_timeout)
        """
        self.api_key = (
            api_key
            or os.environ.get("ANYROUTER_API_KEY")
            or "sk-BZMSVilf0BiRvEnvymd3KflwY1xr45wmXjw5Ewg2fsIkp9T2"
        ).strip()
        self.base_url = (base_url or os.environ.get("ANYROUTER_BASE_URL") or DEFAULT_BASE_URL).rstrip("/")
        self.proxy = proxy if proxy is not None else os.environ.get("HTTP_PROXY") or os.environ.get("HTTPS_PROXY")
        self.max_retries = max_retries
        self.attempts_per_round = attempts_per_round
        self.intra_round_delay = intra_round_delay
        self.inter_round_delay = inter_round_delay
        self.timeout = timeout

        # 生成固定的设备指纹（在会话生命周期内保持稳定）
        self.claude_device_id = secrets.token_hex(32)
        self.codex_installation_id = str(uuid.uuid4())

        # 初始化会话与代理配置
        self.session = requests.Session()
        if self.proxy:
            self.session.proxies = {
                "http": self.proxy,
                "https": self.proxy,
            }

    @staticmethod
    def is_codex_model(model_id: str) -> bool:
        """判断模型是否属于 Codex / Responses 协议体系"""
        model_lower = model_id.lower()
        if "astra" in model_lower or "codex" in model_lower or "gpt" in model_lower:
            return True
        if re.match(r"^o\d", model_lower):
            return True
        return False

    # --- Codex 伪装层 (Responses Protocol) ---

    def _create_codex_headers(self, session_id: str, turn_id: str) -> Tuple[Dict[str, str], Dict[str, Any]]:
        """构建完整的 Codex 官方请求头与元数据"""
        window_id = f"{session_id}:0"
        turn_metadata_dict = {
            "installation_id": self.codex_installation_id,
            "session_id": session_id,
            "thread_id": session_id,
            "turn_id": turn_id,
            "window_id": window_id,
            "request_kind": "turn",
            "thread_source": "user",
            "turn_started_at_unix_ms": int(time.time() * 1000),
        }
        turn_metadata_json = json.dumps(turn_metadata_dict, separators=(",", ":"))

        headers = {
            "authorization": f"Bearer {self.api_key}",
            "accept": "text/event-stream",
            "content-type": "application/json",
            "originator": "codex_exec",
            "user-agent": f"codex_exec/{CODEX_VERSION} (Linux; x86_64) (codex_exec; {CODEX_VERSION})",
            "x-openai-internal-codex-responses-lite": "true",
            "x-codex-beta-features": "remote_compaction_v2",
            "x-codex-window-id": window_id,
            "x-codex-turn-metadata": turn_metadata_json,
            "x-client-request-id": session_id,
            "session-id": session_id,
            "thread-id": session_id,
        }

        client_metadata = {
            "session_id": session_id,
            "thread_id": session_id,
            "turn_id": turn_id,
            "x-codex-installation-id": self.codex_installation_id,
            "x-codex-window-id": window_id,
            "x-codex-turn-metadata": turn_metadata_json,
        }

        return headers, client_metadata

    def _build_codex_body(
        self,
        model: str,
        messages: List[Dict[str, str]],
        system_prompt: Optional[str],
        session_id: str,
        turn_id: str,
        client_metadata: Dict[str, Any],
        max_tokens: int = 4096,
        reasoning_effort: str = "medium",
    ) -> Dict[str, Any]:
        """构建符合 OpenAI Responses 协议的输入请求体"""
        input_items = []

        # Developer / System 指令
        if system_prompt:
            input_items.append({
                "type": "message",
                "role": "developer",
                "content": [{"type": "input_text", "text": system_prompt}],
            })

        # 历史对话转换
        for msg in messages:
            role = msg.get("role", "user")
            content = msg.get("content", "")
            if role == "user":
                input_items.append({
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": content}],
                })
            elif role == "assistant":
                input_items.append({
                    "type": "message",
                    "role": "assistant",
                    "status": "completed",
                    "content": [{"type": "output_text", "text": content, "annotations": []}],
                })

        return {
            "model": model,
            "input": input_items,
            "tool_choice": "auto",
            "parallel_tool_calls": False,
            "reasoning": {
                "effort": reasoning_effort,
                "context": "all_turns",
            },
            "store": False,
            "stream": True,
            "text": {"verbosity": "low"},
            "max_output_tokens": max_tokens,
            "include": ["reasoning.encrypted_content"],
            "prompt_cache_key": session_id,
            "client_metadata": client_metadata,
        }

    # --- Claude Code 伪装层 (Messages Protocol) ---

    def _create_claude_headers(self, session_id: str, retry_count: int = 0) -> Dict[str, str]:
        """构建完整的 Claude Code 官方请求头"""
        return {
            "content-type": "application/json",
            "accept": "application/json",
            "authorization": f"Bearer {self.api_key}",
            "anthropic-version": "2023-06-01",
            "anthropic-dangerous-direct-browser-access": "true",
            "anthropic-beta": ANTHROPIC_BETA,
            "user-agent": f"claude-cli/{CLAUDE_CODE_VERSION} (external, sdk-cli)",
            "x-app": "cli",
            "x-claude-code-session-id": session_id,
            "x-stainless-retry-count": str(retry_count),
            "x-stainless-timeout": "600",
            "x-stainless-lang": "js",
            "x-stainless-package-version": STAINLESS_PACKAGE_VERSION,
            "x-stainless-os": "Linux",
            "x-stainless-arch": "x64",
            "x-stainless-runtime": "node",
            "x-stainless-runtime-version": "v26.3.0",
        }

    def _build_claude_body(
        self,
        model: str,
        messages: List[Dict[str, str]],
        system_prompt: Optional[str],
        session_id: str,
        max_tokens: int = 4096,
    ) -> Dict[str, Any]:
        """构建符合 Anthropic Claude Code 规范的请求体（必须包含计费系统头与元数据）"""
        # 严格伪装系统块：包含计费声明和 Agent SDK 身份（否则 AnyRouter 拦截）
        system_blocks = [
            {"type": "text", "text": f"x-anthropic-billing-header: cc_version={CLAUDE_CODE_VERSION_BUILD}; cc_entrypoint=sdk-cli;"},
            {"type": "text", "text": "You are a Claude agent, built on Anthropic's Claude Agent SDK.", "cache_control": {"type": "ephemeral"}},
        ]
        if system_prompt:
            system_blocks.append({"type": "text", "text": system_prompt, "cache_control": {"type": "ephemeral"}})

        # 转换对话消息
        formatted_messages = []
        for msg in messages:
            formatted_messages.append({
                "role": msg["role"],
                "content": [{"type": "text", "text": msg["content"]}],
            })

        # 标记最后一条用户消息开启临时缓存
        if formatted_messages and formatted_messages[-1]["role"] == "user":
            formatted_messages[-1]["content"][-1]["cache_control"] = {"type": "ephemeral"}

        return {
            "model": model,
            "max_tokens": max_tokens,
            "stream": True,
            "system": system_blocks,
            "messages": formatted_messages,
            "metadata": {
                "user_id": json.dumps({
                    "device_id": self.claude_device_id,
                    "account_uuid": "",
                    "session_id": session_id,
                }, separators=(",", ":"))
            },
        }

    # --- 统一排队重试与流式请求执行 ---

    def stream_chat(
        self,
        model: str,
        prompt: Optional[str] = None,
        history: Optional[List[Dict[str, str]]] = None,
        system_prompt: Optional[str] = None,
        session_id: Optional[str] = None,
        max_tokens: int = 4096,
        on_retry: Optional[Callable[[int, int, str, float], None]] = None,
    ) -> Generator[Dict[str, Any], None, None]:
        """
        发送对话并实时以生成器流式返回结果。
        遇到排队拥挤（429 / 520 等报错）自动无限/持续重试直到排上挤入！

        :param model: 模型标识符，如 'gpt-6-astra' 或 'claude-fable-5-1'
        :param prompt: 当前用户输入文本（若与 history 一起传，将自动作为最后一条 user 消息追加）
        :param history: 历史对话列表 [{"role": "user"|"assistant", "content": "..."}]
        :param system_prompt: 自定义系统提示词
        :param session_id: 会话标识符，若无则自动生成
        :param max_tokens: 最大输出 token 数量
        :param on_retry: 重试回调函数 (attempt, status_code, error_msg, wait_seconds)
        :yield: 包含事件字典，例如：
            {"type": "status", "stage": "...", "message": "..."}
            {"type": "thinking", "delta": "思考过程片段"}
            {"type": "text", "delta": "回答文本片段"}
            {"type": "done", "full_text": "...", "full_thinking": "..."}
        """
        session_id = session_id or str(uuid.uuid4())
        messages = list(history) if history else []
        if prompt:
            messages.append({"role": "user", "content": prompt})

        is_codex = self.is_codex_model(model)
        url = f"{self.base_url}/v1/responses" if is_codex else f"{self.base_url}/v1/messages?beta=true"

        attempt = 0
        while True:
            attempt += 1
            round_num = ((attempt - 1) // self.attempts_per_round) + 1
            attempt_in_round = ((attempt - 1) % self.attempts_per_round) + 1
            is_round_end = (attempt_in_round == self.attempts_per_round)
            turn_id = str(uuid.uuid4())

            if is_codex:
                headers, client_metadata = self._create_codex_headers(session_id, turn_id)
                body = self._build_codex_body(
                    model, messages, system_prompt, session_id, turn_id, client_metadata, max_tokens
                )
            else:
                headers = self._create_claude_headers(session_id, retry_count=0)
                body = self._build_claude_body(
                    model, messages, system_prompt, session_id, max_tokens
                )

            retry_reason = ""
            status_code = 0
            retry_after_sec = None

            try:
                yield {
                    "type": "status",
                    "stage": "connecting",
                    "round": round_num,
                    "attempt_in_round": attempt_in_round,
                    "max_in_round": self.attempts_per_round,
                    "attempt": attempt,
                    "message": f"正在发起连接 (第 {round_num} 轮 #{attempt_in_round}/{self.attempts_per_round}, 模型: {model})...",
                }

                resp = self.session.post(
                    url,
                    headers=headers,
                    json=body,
                    timeout=self.timeout,
                    stream=True,
                )
                status_code = resp.status_code

                # 检查 Retry-After 响应头
                if "retry-after" in resp.headers:
                    try:
                        retry_after_sec = float(resp.headers["retry-after"])
                    except ValueError:
                        pass

                if status_code == 200:
                    stream_generator = (
                        self._parse_codex_sse(resp) if is_codex else self._parse_claude_sse(resp)
                    )

                    has_content = False
                    for event in stream_generator:
                        e_type = event.get("type")

                        if e_type == "stream_error":
                            err_data = event.get("error", {})
                            err_msg = err_data.get("message") or str(err_data)
                            retry_reason = f"流内排队拦截: {err_msg}"
                            break

                        if e_type in ("text", "thinking"):
                            if not has_content:
                                has_content = True
                                # 只有真正收到文本内容时，才算真正挤入成功！
                                yield {
                                    "type": "status",
                                    "stage": "connected",
                                    "round": round_num,
                                    "attempt_in_round": attempt_in_round,
                                    "attempt": attempt,
                                    "message": "成功挤入通道！",
                                }
                            yield event

                        elif e_type == "done":
                            if has_content:
                                yield event
                                return
                            else:
                                retry_reason = "流连接建立但未返回有效内容"

                    if has_content:
                        return

                elif status_code in RETRYABLE_STATUS_CODES:
                    resp_text = resp.text[:300].strip()
                    retry_reason = f"HTTP {status_code}: {resp_text or '服务排队中/暂时过载'}"
                else:
                    # 非可重试错误（如 401 密钥失效）直接抛出
                    resp_text = resp.text[:400]
                    raise RuntimeError(f"不可重试的平台报错 (HTTP {status_code}): {resp_text}")

            except (requests.exceptions.RequestException, requests.exceptions.Timeout) as net_err:
                retry_reason = f"网络连接波动: {str(net_err)}"

            # 检查最大尝试次数
            if self.max_retries is not None and attempt >= self.max_retries:
                raise TimeoutError(f"已达到最大重试次数 ({self.max_retries})，最后报错: {retry_reason}")

            # 策略：一轮 5 次，每次间隔 5s；每轮结束等待 1 分钟 (60s)
            if retry_after_sec is not None and retry_after_sec > 0:
                wait_sec = min(retry_after_sec, 60.0)
            elif is_round_end:
                wait_sec = self.inter_round_delay  # 60s
            else:
                wait_sec = self.intra_round_delay  # 5s

            if on_retry:
                on_retry(attempt, status_code, retry_reason, wait_sec)

            yield {
                "type": "status",
                "stage": "retrying",
                "round": round_num,
                "attempt_in_round": attempt_in_round,
                "max_in_round": self.attempts_per_round,
                "attempt": attempt,
                "status_code": status_code,
                "reason": retry_reason,
                "wait_seconds": wait_sec,
                "is_round_end": is_round_end,
                "message": (
                    f"第 {round_num} 轮完成 ({self.attempts_per_round}/{self.attempts_per_round})，等待 1 分钟 ({int(wait_sec)}s) 开启下一轮..."
                    if is_round_end
                    else f"第 {round_num} 轮 ({attempt_in_round}/{self.attempts_per_round}) 未挤上，等待 {int(wait_sec)}s 尝试下一次..."
                ),
            }

            # 执行等待休眠
            start_sleep = time.time()
            while True:
                elapsed = time.time() - start_sleep
                if elapsed >= wait_sec:
                    break
                remaining = max(0, int(round(wait_sec - elapsed)))
                yield {
                    "type": "status",
                    "stage": "waiting",
                    "round": round_num,
                    "attempt_in_round": attempt_in_round,
                    "max_in_round": self.attempts_per_round,
                    "remaining": remaining,
                    "wait_seconds": wait_sec,
                    "is_round_end": is_round_end,
                }
                time.sleep(1.0)

    # --- SSE 流解析器 ---

    def _parse_codex_sse(self, response: requests.Response) -> Generator[Dict[str, Any], None, None]:
        """解析 OpenAI Responses Lite 的 SSE 事件流"""
        full_text = []
        full_thinking = []

        for line in response.iter_lines():
            if not line:
                continue
            decoded = line.decode("utf-8", errors="replace")
            if decoded.startswith("data:"):
                data_str = decoded[5:].strip()
                if not data_str or data_str == "[DONE]":
                    break

                try:
                    payload = json.loads(data_str)
                except json.JSONDecodeError:
                    continue

                event_type = payload.get("type")

                if event_type == "error":
                    yield {"type": "stream_error", "error": payload.get("error", {})}
                    return

                if event_type == "response.failed":
                    err = payload.get("response", {}).get("error", {})
                    yield {"type": "stream_error", "error": err}
                    return

                if event_type == "response.output_text.delta":
                    delta = payload.get("delta", "")
                    full_text.append(delta)
                    yield {"type": "text", "delta": delta}

                elif event_type in ("response.reasoning_text.delta", "response.reasoning_summary_text.delta"):
                    delta = payload.get("delta", "")
                    full_thinking.append(delta)
                    yield {"type": "thinking", "delta": delta}

                elif event_type == "response.completed":
                    break

        yield {
            "type": "done",
            "full_text": "".join(full_text),
            "full_thinking": "".join(full_thinking),
        }

    def _parse_claude_sse(self, response: requests.Response) -> Generator[Dict[str, Any], None, None]:
        """解析 Anthropic Claude Code Messages 的 SSE 事件流"""
        full_text = []
        full_thinking = []
        current_block_type = "text"

        for line in response.iter_lines():
            if not line:
                continue
            decoded = line.decode("utf-8", errors="replace")
            if decoded.startswith("data:"):
                data_str = decoded[5:].strip()
                if not data_str or data_str == "[DONE]":
                    break

                try:
                    payload = json.loads(data_str)
                except json.JSONDecodeError:
                    continue

                event_type = payload.get("type")

                if event_type == "error":
                    yield {"type": "stream_error", "error": payload.get("error", {})}
                    return

                if event_type == "content_block_start":
                    block = payload.get("content_block", {})
                    current_block_type = block.get("type", "text")

                elif event_type == "content_block_delta":
                    delta_obj = payload.get("delta", {})
                    delta_type = delta_obj.get("type")

                    if delta_type == "text_delta":
                        text_delta = delta_obj.get("text", "")
                        full_text.append(text_delta)
                        yield {"type": "text", "delta": text_delta}
                    elif delta_type == "thinking_delta":
                        thinking_delta = delta_obj.get("thinking", "")
                        full_thinking.append(thinking_delta)
                        yield {"type": "thinking", "delta": thinking_delta}

                elif event_type == "message_stop":
                    break

        yield {
            "type": "done",
            "full_text": "".join(full_text),
            "full_thinking": "".join(full_thinking),
        }

    # --- 便捷调用接口 ---

    def chat(
        self,
        model: str,
        prompt: str,
        history: Optional[List[Dict[str, str]]] = None,
        system_prompt: Optional[str] = None,
    ) -> str:
        """非流式调用，直接返回完整文本回答（自动处理排队重试）"""
        full_result = ""
        for chunk in self.stream_chat(model=model, prompt=prompt, history=history, system_prompt=system_prompt):
            if chunk.get("type") == "done":
                full_result = chunk.get("full_text", "")
        return full_result
