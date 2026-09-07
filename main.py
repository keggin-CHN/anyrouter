#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
AnyRouter Concurrent Auto-Squeezer (main.py)
自动排队挤入工具：开箱即排，支持 GPT (Codex) 与 Claude (Claude Code) 双模型并发一起挤！
- 规则：每隔 1 分钟一轮，一轮挤 5 次，每次间隔 5s
- 判定：只有真正收到模型生成内容才算成功挤入，绝无误报
- 机制：双模型多线程并发并行挤，挤成功一个完成一个，直到全部挤成功
"""

import argparse
import os
import sys
import threading
import time
from datetime import datetime

# Windows 终端 UTF-8 兼容
if sys.platform == "win32":
    import io
    if hasattr(sys.stdout, "buffer"):
        sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    if hasattr(sys.stderr, "buffer"):
        sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")

from anyrouter_client import AnyRouterClient, DEFAULT_BASE_URL, DEFAULT_PROXY

PRINT_LOCK = threading.Lock()


def now_str():
    return datetime.now().strftime("%H:%M:%S")


def safe_print(*args, **kwargs):
    with PRINT_LOCK:
        print(*args, **kwargs)


def squeeze_worker(model: str, args, proxy_val: str, results_dict: dict):
    """单模型排队挤入工作线程"""
    is_codex = AnyRouterClient.is_codex_model(model)
    tag = "Codex " if is_codex else "Claude"
    prefix = f"[{tag} | {model}]"

    client = AnyRouterClient(
        api_key=args.key,
        proxy=proxy_val,
        max_retries=args.max_retries,
        attempts_per_round=args.round_tries,
        intra_round_delay=args.interval,
        inter_round_delay=args.round_interval,
    )

    start_time = time.time()
    full_text = []
    full_thinking = []
    has_connected = False

    try:
        for chunk in client.stream_chat(model=model, prompt=args.prompt, system_prompt=args.system):
            c_type = chunk.get("type")

            if c_type == "status":
                stage = chunk.get("stage")
                round_num = chunk.get("round", 1)
                attempt_in_round = chunk.get("attempt_in_round", 1)
                max_in_round = chunk.get("max_in_round", 5)

                if stage == "connecting":
                    safe_print(f"[{now_str()}] {prefix} [第 {round_num} 轮 {attempt_in_round}/{max_in_round}] 发起连接...", flush=True)

                elif stage == "retrying":
                    reason = chunk.get("reason", "排队拥挤")
                    if "429" in reason:
                        short_reason = "429 (Service Unavailable)"
                    elif "520" in reason:
                        short_reason = "520 (Origin Error)"
                    elif any(c in reason for c in ("502", "503", "504")):
                        short_reason = "服务网关暂不可用"
                    elif "exceeded rate limit" in reason.lower() or "quota" in reason.lower():
                        short_reason = "流内限流拦截 (Rate limit)"
                    else:
                        short_reason = reason[:30]

                    wait_sec = int(chunk.get("wait_seconds", 5))
                    is_round_end = chunk.get("is_round_end", False)

                    if is_round_end:
                        safe_print(f"[{now_str()}] {prefix} [第 {round_num} 轮结束] 5次尝试未挤上 ({short_reason})，冷却 1 分钟后进入第 {round_num + 1} 轮...", flush=True)
                    else:
                        safe_print(f"[{now_str()}] {prefix} [第 {round_num} 轮 {attempt_in_round}/{max_in_round}] 未挤上 ({short_reason}) -> 等待 {wait_sec}s", flush=True)

                elif stage == "connected":
                    if not has_connected:
                        has_connected = True
                        safe_print(f"\n[{now_str()}] {prefix} ★★★ 挤入成功！正在接收回复... ★★★\n", flush=True)

            elif c_type == "thinking":
                delta = chunk.get("delta", "")
                full_thinking.append(delta)

            elif c_type == "text":
                delta = chunk.get("delta", "")
                full_text.append(delta)

            elif c_type == "done":
                elapsed = time.time() - start_time
                ans = "".join(full_text)
                thinking = "".join(full_thinking)

                with PRINT_LOCK:
                    print(f"\n" + "=" * 70)
                    print(f"🎉 {prefix} 排队挤入成功！总耗时: {elapsed:.1f} 秒")
                    if thinking.strip():
                        print(f"[思考过程]:\n{thinking.strip()}\n")
                    print(f"[模型回复]:\n{ans.strip()}")
                    print("=" * 70 + "\n", flush=True)

                # 保存该模型的专属结果文件
                result_file = f"squeeze_{model}.txt"
                with open(result_file, "w", encoding="utf-8") as f:
                    f.write(f"模型: {model}\n耗时: {elapsed:.1f}s\n时间: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}\n\n回答:\n{ans}\n")

                results_dict[model] = {
                    "status": "success",
                    "elapsed": elapsed,
                    "answer": ans,
                    "file": result_file,
                }
                return

    except TimeoutError as e:
        safe_print(f"[{now_str()}] {prefix} [X] 达到最大重试限制: {e}")
        results_dict[model] = {"status": "timeout", "error": str(e)}
    except Exception as e:
        safe_print(f"[{now_str()}] {prefix} [X] 发生异常: {e}")
        results_dict[model] = {"status": "error", "error": str(e)}


def main():
    parser = argparse.ArgumentParser(description="AnyRouter 自动排队挤入工具 (支持 GPT 与 Claude 并发一起挤)")
    parser.add_argument(
        "-m", "--model",
        default="all",
        choices=["all", "both", "claude-fable-5-1", "gpt-6-astra"],
        help="目标模型：all (默认：GPT 与 Claude 一起并发挤), 或单独指定 claude-fable-5-1 / gpt-6-astra",
    )
    parser.add_argument(
        "-p", "--prompt",
        type=str,
        default="你好！请用中文回答，确认你已成功接收到请求，并用一句话介绍你自己。",
        help="挤入成功后的测试提问",
    )
    parser.add_argument(
        "--proxy",
        type=str,
        default=DEFAULT_PROXY,
        help=f"本地代理地址 (默认: {DEFAULT_PROXY})",
    )
    parser.add_argument(
        "--key",
        type=str,
        default=None,
        help="API Key (默认自动使用内置 Key)",
    )
    parser.add_argument(
        "--max-retries",
        type=int,
        default=None,
        help="最大总尝试次数 (默认 None，直到挤成功)",
    )
    parser.add_argument(
        "--round-tries",
        type=int,
        default=5,
        help="每轮尝试次数 (默认 5 次)",
    )
    parser.add_argument(
        "--interval",
        type=float,
        default=5.0,
        help="同一轮内尝试间隔秒数 (默认 5.0 秒)",
    )
    parser.add_argument(
        "--round-interval",
        type=float,
        default=60.0,
        help="轮与轮之间的冷却秒数 (默认 60.0 秒)",
    )
    parser.add_argument(
        "--system",
        type=str,
        default=None,
        help="系统提示词 (可选)",
    )

    args = parser.parse_args()
    proxy_val = None if args.proxy.lower() in ("none", "off", "no", "") else args.proxy

    if args.model in ("all", "both"):
        target_models = ["gpt-6-astra", "claude-fable-5-1"]
    else:
        target_models = [args.model]

    print("=" * 70)
    print("  AnyRouter 自动并发排队挤入工具")
    print(f"  挤入目标模型 ({len(target_models)} 个):")
    for idx, m in enumerate(target_models, 1):
        proto = "OpenAI Responses 协议" if AnyRouterClient.is_codex_model(m) else "Anthropic Messages 协议"
        print(f"    {idx}. {m} ({proto})")
    print(f"  代理通道: {proxy_val or '直连'}")
    print(f"  排队策略: 每轮 {args.round_tries} 次 (间隔 {args.interval}s) | 轮间等待 {args.round_interval}s | 不设上限直到挤成功")
    print(f"  测试提问: {args.prompt}")
    print("=" * 70)
    print(flush=True)

    results = {}
    threads = []

    for m in target_models:
        t = threading.Thread(
            target=squeeze_worker,
            args=(m, args, proxy_val, results),
            daemon=True,
        )
        t.start()
        threads.append(t)

    # 等待所有线程完成（或按 Ctrl+C 中断）
    try:
        while any(t.is_alive() for t in threads):
            time.sleep(0.5)
    except KeyboardInterrupt:
        print(f"\n[{now_str()}] 用户手动终止排队任务。")
        sys.exit(0)

    print(f"[{now_str()}] ★★★ 所有目标模型排队挤入流程已结束！ ★★★")
    for m, res in results.items():
        if res.get("status") == "success":
            print(f"  [✓] {m}: 挤入成功！耗时 {res['elapsed']:.1f}s -> 已保存至 {res['file']}")
        else:
            print(f"  [X] {m}: 未成功 ({res.get('error', '未知错误')})")


if __name__ == "__main__":
    main()
