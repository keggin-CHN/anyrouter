#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Auto-squeeze worker for AnyRouter
Keeps running the round-based retry loop:
  - 5 tries per round, 5 seconds interval
  - 1 minute (60s) between rounds
  - Squeezes until success, then logs the output and terminates.
"""

import sys
import os
import time
import json

if sys.platform == "win32":
    import io
    if hasattr(sys.stdout, "buffer"):
        sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    if hasattr(sys.stderr, "buffer"):
        sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")

from anyrouter_client import AnyRouterClient

def main():
    target_model = sys.argv[1] if len(sys.argv) > 1 else "claude-fable-5-1"
    prompt = "你好！请用中文回答，确认你已成功接收到请求，并用一句话介绍你自己。"
    
    print(f"=== 开始执行自动排队挤入任务 ===")
    print(f"目标模型: {target_model}")
    print(f"提问内容: {prompt}")
    print(f"策略: 一轮 5 次，间隔 5 秒，每隔 1 分钟一轮，不设上限直到挤成功！\n", flush=True)
    
    client = AnyRouterClient(
        proxy="http://127.0.0.1:10808",
        max_retries=None,  # 持续重试直到排上挤入
        attempts_per_round=5,
        intra_round_delay=5.0,
        inter_round_delay=60.0,
    )
    
    start_time = time.time()
    full_text = []
    full_thinking = []
    
    for chunk in client.stream_chat(model=target_model, prompt=prompt):
        c_type = chunk.get("type")
        
        if c_type == "status":
            stage = chunk.get("stage")
            round_num = chunk.get("round", 1)
            attempt_in_round = chunk.get("attempt_in_round", 1)
            max_in_round = chunk.get("max_in_round", 5)
            
            if stage == "connecting":
                print(f"\r[>] [第 {round_num} 轮 #{attempt_in_round}/{max_in_round}] 正在发起连接...{' ' * 20}", end="", flush=True)
            elif stage == "countdown":
                rem = chunk.get("remaining", 0)
                is_round_end = chunk.get("is_round_end", False)
                if is_round_end:
                    print(f"\r[⏳] [第 {round_num} 轮结束 (已挤5次)] 按照策略每隔 1 分钟开启下一轮: 倒计时 {rem:02d}s...{' ' * 10}", end="", flush=True)
                else:
                    print(f"\r[⏳] [第 {round_num} 轮 #{attempt_in_round}/{max_in_round}] 遇到 429 拥挤排队，间隔 5s 再次尝试: 倒计时 {rem:02d}s...{' ' * 10}", end="", flush=True)
            elif stage == "connected":
                print(f"\r\n[+] [第 {round_num} 轮 #{attempt_in_round}/{max_in_round}] 成功挤入通道！开始接收流式回复...\n", flush=True)
                
        elif c_type == "thinking":
            delta = chunk.get("delta", "")
            full_thinking.append(delta)
            print(f"[Thinking] {delta}", end="", flush=True)
            
        elif c_type == "text":
            delta = chunk.get("delta", "")
            full_text.append(delta)
            print(delta, end="", flush=True)
            
        elif c_type == "done":
            elapsed = time.time() - start_time
            ans = "".join(full_text)
            thinking = "".join(full_thinking)
            print(f"\n\n==========================================")
            print(f"🎉 挤入成功！总耗时: {elapsed:.1f} 秒")
            print(f"完整回复:\n{ans}")
            print(f"==========================================")
            
            result_data = {
                "model": target_model,
                "elapsed_seconds": elapsed,
                "answer": ans,
                "thinking": thinking,
                "timestamp": time.strftime("%Y-%m-%d %H:%M:%S"),
            }
            with open(f"squeeze_{target_model}.json", "w", encoding="utf-8") as f:
                json.dump(result_data, f, ensure_ascii=False, indent=2)
            return

if __name__ == "__main__":
    main()
