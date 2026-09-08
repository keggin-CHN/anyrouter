#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
AnyRouter 多 Key 自动挂机排队保活工具 v2.6 (高颜值全配置 + 系统托盘常驻版)
- 核心功能：
  1. 支持多个 API Key 并发保活（笛卡尔积多 Key × 多模型并发矩阵）
  2. 内置 10 大极简预设测活题库，支持随机轮换提问（杜绝平台缓存与固定特征识别）
  3. 所有配置参数在 EXE 界面完全可视化调节，并自动持久化到 keeper_config.json
  4. 窗口关闭行为选择：点击右上角叉掉可选择【最小化到系统托盘后台运行】或【彻底退出程序】
  5. 系统托盘常驻支持：托盘气泡、右键菜单（打开面板、查看状态、退出）、双击重新唤出
  6. 两阶段自动化保活工作流：
     - 阶段一【用不了】：单次显式驱动，30s 一轮，一轮 5 次排队挤入。出字立即在桌面右下角弹窗提示！
     - 阶段二【能用】：30 分钟测活一次。“能用就不管，不能用就继续进入第一阶段”。
  7. 屏幕右下角通知浮窗，自适应垂直堆叠不重叠。
"""

import json
import os
import random
import sys
import threading
import time
import uuid
from datetime import datetime
import tkinter as tk
from tkinter import ttk, messagebox, scrolledtext

# 托盘与图标库
from PIL import Image, ImageDraw
import pystray

# Windows 终端 UTF-8 编码兼容
if sys.platform == "win32":
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    if hasattr(sys.stderr, "reconfigure"):
        sys.stderr.reconfigure(encoding="utf-8", errors="replace")

# 引入底层客户端库
from anyrouter_client import (
    AnyRouterClient,
    DEFAULT_BASE_URL,
    DEFAULT_PROXY,
)

CONFIG_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "keeper_config.json")

# 10 大经典极简预设测活题库 (超低 Token 消耗、极速首字响应)
DEFAULT_HEARTBEAT_PROMPTS = [
    "1+1",
    "用一个字回答：好",
    "输出一个数字: 8",
    "2*3等于几",
    "Hi, reply 1 word.",
    "请回复'pong'",
    "3+5等于几",
    "ping",
    "100-1等于多少",
    "从1数到3",
]

# 默认内置全量可用模型候选列表
DEFAULT_MODEL_CANDIDATES = [
    {"id": "gpt-6-astra", "type": "Codex / Responses", "desc": "OpenAI Responses 协议 (核心推荐)"},
    {"id": "claude-fable-5-1", "type": "Claude Code", "desc": "Anthropic Claude Code 协议 (核心推荐)"},
    {"id": "claude-3-7-sonnet-20250219", "type": "Claude Code", "desc": "Claude 3.7 Sonnet 协议"},
    {"id": "claude-3-5-sonnet-20241022", "type": "Claude Code", "desc": "Claude 3.5 Sonnet 协议"},
    {"id": "claude-3-5-haiku-20241022", "type": "Claude Code", "desc": "Claude 3.5 Haiku 极速版"},
    {"id": "claude-opus-4-7", "type": "Claude Code", "desc": "Claude Opus 4.7 协议"},
    {"id": "claude-opus-4-6", "type": "Claude Code", "desc": "Claude Opus 4.6 协议"},
    {"id": "gemini-2.5-pro", "type": "OpenAI 标准", "desc": "Gemini 2.5 Pro 协议"},
    {"id": "gpt-5-codex", "type": "Codex / Responses", "desc": "GPT-5 Codex 协议"},
]

# 状态枚举
STATUS_IDLE = "idle"
STATUS_SQUEEZING = "squeezing"      # 阶段一：用不了 (30s一轮/5次挤入中)
STATUS_AVAILABLE = "available"      # 阶段二：能用 (30分钟测活中，能用就不管)
STATUS_STOPPED = "stopped"          # 已停止

# 活跃通知浮窗队列，防止重叠
_active_toasts = []


def mask_key(key: str) -> str:
    """生成 Key 遮罩缩写，如 sk-BZMS...p9T2"""
    if not key:
        return ""
    key = key.strip()
    if len(key) <= 12:
        return key
    return f"{key[:7]}...{key[-4:]}"


def format_key_tag(index: int, key: str) -> str:
    """生成 Key 标识标签"""
    return f"Key-{index} ({mask_key(key)})"


def create_tray_icon_image():
    """动态生成高分辨率系统托盘图标 (绿色常驻脉冲路由器图标)"""
    img = Image.new("RGBA", (64, 64), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)
    # 底座背景圆盘
    draw.ellipse((4, 4, 60, 60), fill="#1e2235", outline="#10b981", width=4)
    # 核心呼吸绿灯
    draw.ellipse((22, 22, 42, 42), fill="#10b981")
    # 上方信号弧线
    draw.arc((12, 10, 52, 50), start=215, end=325, fill="#34d399", width=3)
    draw.arc((17, 15, 47, 45), start=215, end=325, fill="#6ee7b7", width=2)
    return img


class CloseActionDialog(tk.Toplevel):
    """窗口关闭确认对话框：选择退出进程还是最小化到系统托盘"""

    def __init__(self, parent, is_running: bool, callback):
        super().__init__(parent)
        self.callback = callback
        self.title("关闭操作确认")
        self.configure(bg="#181926")
        self.resizable(False, False)
        self.transient(parent)
        self.grab_set()

        dlg_w, dlg_h = 470, 275
        self.update_idletasks()
        pw = parent.winfo_width()
        ph = parent.winfo_height()
        px = parent.winfo_x()
        py = parent.winfo_y()
        cx = px + (pw - dlg_w) // 2
        cy = py + (ph - dlg_h) // 2
        self.geometry(f"{dlg_w}x{dlg_h}+{max(0, cx)}+{max(0, cy)}")

        card = tk.Frame(self, bg="#1e2235", padx=20, pady=16)
        card.pack(fill=tk.BOTH, expand=True)

        header = tk.Frame(card, bg="#1e2235")
        header.pack(fill=tk.X, pady=(0, 10))

        tk.Label(header, text="⚙", font=("Segoe UI", 16), bg="#1e2235", fg="#818cf8").pack(side=tk.LEFT, padx=(0, 8))
        tk.Label(
            header,
            text="您点击了关闭按钮，请选择接下来的操作：",
            font=("Segoe UI", 11, "bold"),
            fg="#f1f5f9",
            bg="#1e2235",
        ).pack(side=tk.LEFT)

        status_text = (
            "🟢 挂机保活当前正在后台运行中！\n建议选择【最小化到系统托盘】，通道将在后台持续保持存活与测活；\n若选择【彻底退出程序】，将立即终止全部保活守护并关闭进程。"
            if is_running
            else "⚪ 当前未开启挂机保活。\n您可以选择【最小化到托盘】以便后台待命随时唤出，或【彻底退出程序】关闭应用。"
        )
        status_color = "#34d399" if is_running else "#94a3b8"

        tk.Label(
            card,
            text=status_text,
            font=("Segoe UI", 9),
            fg=status_color,
            bg="#1e2235",
            justify="left",
            wraplength=420,
        ).pack(fill=tk.X, pady=(0, 12))

        self.remember_var = tk.BooleanVar(value=False)
        tk.Checkbutton(
            card,
            text="记住我的选择 (后续可在【全参数高级配置】中随时修改)",
            variable=self.remember_var,
            font=("Segoe UI", 8),
            bg="#1e2235",
            fg="#94a3b8",
            selectcolor="#161724",
            activebackground="#1e2235",
            activeforeground="#f1f5f9",
        ).pack(anchor="w", pady=(0, 14))

        btn_box = tk.Frame(card, bg="#1e2235")
        btn_box.pack(fill=tk.X)

        # 按钮 1：最小化到系统托盘 (推荐)
        min_btn = tk.Button(
            btn_box,
            text="📥 最小化到系统托盘",
            command=self._on_minimize,
            bg="#10b981",
            fg="#ffffff",
            activebackground="#059669",
            activeforeground="#ffffff",
            font=("Segoe UI", 9, "bold"),
            relief=tk.FLAT,
            padx=14,
            pady=6,
        )
        min_btn.pack(side=tk.LEFT, padx=(0, 10))

        # 按钮 2：彻底退出程序
        exit_btn = tk.Button(
            btn_box,
            text="🛑 彻底退出程序",
            command=self._on_exit,
            bg="#ef4444",
            fg="#ffffff",
            activebackground="#dc2626",
            activeforeground="#ffffff",
            font=("Segoe UI", 9, "bold"),
            relief=tk.FLAT,
            padx=14,
            pady=6,
        )
        exit_btn.pack(side=tk.LEFT, padx=(0, 10))

        # 按钮 3：取消
        tk.Button(
            btn_box,
            text="取消",
            command=self.destroy,
            bg="#374151",
            fg="#e2e8f0",
            relief=tk.FLAT,
            padx=10,
            pady=6,
            font=("Segoe UI", 9),
        ).pack(side=tk.RIGHT)

    def _on_minimize(self):
        rem = self.remember_var.get()
        self.destroy()
        self.callback("minimize", rem)

    def _on_exit(self):
        rem = self.remember_var.get()
        self.destroy()
        self.callback("exit", rem)


class DesktopToast(tk.Toplevel):
    """Windows 屏幕右下角高颜值悬浮通知卡片，支持多通知自适应垂直向上堆叠"""

    def __init__(self, parent, title: str, message: str, duration_sec: int = 8):
        super().__init__(parent)
        self.overrideredirect(True)
        self.attributes("-topmost", True)
        self.configure(bg="#13141f")

        global _active_toasts
        _active_toasts = [t for t in _active_toasts if t.winfo_exists()]

        toast_w, toast_h = 380, 122
        screen_w = self.winfo_screenwidth()
        screen_h = self.winfo_screenheight()
        pos_x = screen_w - toast_w - 24

        # 多气泡垂直向上堆叠，避免遮挡覆盖
        stack_idx = min(len(_active_toasts), 4)
        pos_y = screen_h - (toast_h + 12) * (stack_idx + 1) - 50
        if pos_y < 20:
            pos_y = 20

        self.geometry(f"{toast_w}x{toast_h}+{pos_x}+{pos_y}")
        _active_toasts.append(self)

        card = tk.Frame(
            self,
            bg="#1e2235",
            highlightbackground="#10b981",
            highlightthickness=2,
            padx=12,
            pady=10,
        )
        card.pack(fill=tk.BOTH, expand=True)

        top = tk.Frame(card, bg="#1e2235")
        top.pack(fill=tk.X)

        tk.Label(top, text="🟢", font=("Segoe UI", 12), bg="#1e2235").pack(side=tk.LEFT, padx=(0, 6))
        tk.Label(
            top,
            text=title,
            font=("Segoe UI", 11, "bold"),
            fg="#34d399",
            bg="#1e2235",
        ).pack(side=tk.LEFT)

        tk.Button(
            top,
            text="✕",
            font=("Segoe UI", 9, "bold"),
            fg="#94a3b8",
            bg="#1e2235",
            activebackground="#2d3748",
            activeforeground="#ffffff",
            relief=tk.FLAT,
            command=self.destroy,
            bd=0,
            padx=4,
        ).pack(side=tk.RIGHT)

        tk.Label(
            card,
            text=message,
            font=("Segoe UI", 9),
            fg="#f1f5f9",
            bg="#1e2235",
            justify="left",
            wraplength=340,
        ).pack(fill=tk.BOTH, expand=True, pady=(6, 0))

        # Windows 蜂鸣提示音
        try:
            import winsound
            winsound.MessageBeep(winsound.MB_ICONASTERISK)
        except Exception:
            pass

        self.after(duration_sec * 1000, self._auto_close)

    def _auto_close(self):
        try:
            self.destroy()
        except Exception:
            pass


class ModelKeeperWorker(threading.Thread):
    """
    单通道独立守护线程（单个 Key + 单个模型）：
    - 阶段一【用不了】：单次显式驱动，30s 一轮，一轮 5 次排队挤入。出字立即在桌面右下角弹窗提示！
    - 阶段二【能用】：30 分钟测活一次。“能用就不管，不能用就继续进入第一阶段”。
    - 随机题库：每次测活或挤入从 10 大题库中随机抽取，防止单一固定请求被平台识别！
    """

    def __init__(
        self,
        key: str,
        key_label: str,
        model_id: str,
        model_type: str,
        proxy: str,
        check_interval_min: int,
        round_cooldown_sec: int,
        tries_per_round: int,
        intra_round_delay_sec: int,
        max_retries: int,
        heartbeat_prompts: list,
        random_heartbeat: bool,
        log_cb,
        status_cb,
        notify_cb,
    ):
        super().__init__(daemon=True)
        self.key = key
        self.key_label = key_label
        self.model_id = model_id
        self.model_type = model_type
        self.proxy = proxy
        self.task_id = f"{self.key_label}::{self.model_id}"

        self.check_interval_sec = max(60, check_interval_min * 60)
        self.round_cooldown_sec = max(5, round_cooldown_sec)
        self.tries_per_round = max(1, tries_per_round)
        self.intra_round_delay_sec = max(1, intra_round_delay_sec)
        self.max_retries = max(1, max_retries)

        self.heartbeat_prompts = heartbeat_prompts or list(DEFAULT_HEARTBEAT_PROMPTS)
        self.random_heartbeat = random_heartbeat

        self.log_cb = log_cb
        self.status_cb = status_cb
        self.notify_cb = notify_cb
        self.stop_event = threading.Event()

        self.session_id = str(uuid.uuid4())
        self.check_count = 0
        self.status = STATUS_IDLE
        self.last_reply = ""
        self.last_prompt = ""

    def stop(self):
        self.stop_event.set()

    def log(self, text: str, tag: str = "normal"):
        self.log_cb(f"[{self.key_label} | {self.model_id}] {text}", tag)

    def _pick_prompt(self) -> str:
        """从预设题库中抽取问题"""
        if self.random_heartbeat and self.heartbeat_prompts:
            return random.choice(self.heartbeat_prompts)
        elif self.heartbeat_prompts:
            return self.heartbeat_prompts[0]
        return "1+1"

    def _make_client(self) -> AnyRouterClient:
        return AnyRouterClient(
            api_key=self.key,
            proxy=self.proxy,
            timeout=(15, 60),
        )

    def run(self):
        self.log(
            f"🚀 启动守护 | 阶段一(用不了: {self.round_cooldown_sec}s一轮/{self.tries_per_round}次) -> 阶段二(能用: {self.check_interval_sec//60}分钟测活) | 题库量: {len(self.heartbeat_prompts)} (随机: {'是' if self.random_heartbeat else '否'})",
            "info",
        )

        while not self.stop_event.is_set():
            # =======================================================
            # 阶段一：【用不了】 30s 一轮，一轮 5 次挤入排队
            # =======================================================
            self.status = STATUS_SQUEEZING
            self.status_cb(self.task_id, self.status, "🟡 用不了 (正在排队挤入...)", "", "")

            squeezed = self._squeeze_in()
            if not squeezed:
                if self.stop_event.is_set():
                    break
                time.sleep(2.0)
                continue

            # =======================================================
            # 挤入成功！触发右下角弹窗通知提示
            # =======================================================
            self.status = STATUS_AVAILABLE
            title = f"[{self.key_label}] {self.model_id} 可用！"
            msg = f"🎉 挤入提问: '{self.last_prompt}' -> 回复: '{self.last_reply[:30]}'\n已进入【能用】状态，开启 {self.check_interval_sec//60} 分钟定期测活。"
            self.notify_cb(title, msg)
            self.log(f"★★★ 挤入成功！提问: '{self.last_prompt}' -> 响应: '{self.last_reply}' -> 触发桌面右下角弹窗！★★★", "success")

            # =======================================================
            # 阶段二：【能用】 30 分钟测活，能用就不管，不能用回阶段一
            # =======================================================
            while not self.stop_event.is_set():
                # 倒计时 30 分钟（能用就不管）
                slept = 0
                while slept < self.check_interval_sec and not self.stop_event.is_set():
                    remaining = self.check_interval_sec - slept
                    rem_min = remaining // 60
                    rem_sec = remaining % 60
                    time_str = f"{rem_min}分{rem_sec:02d}秒" if rem_min > 0 else f"{rem_sec}秒"

                    if remaining % 10 == 0 or remaining <= 5:
                        self.status_cb(
                            self.task_id,
                            self.status,
                            f"🟢 能用 (距测活: {time_str} | 已保活 {self.check_count} 次)",
                            self.last_prompt,
                            self.last_reply[:25],
                        )
                    time.sleep(1.0)
                    slept += 1

                if self.stop_event.is_set():
                    break

                # 30 分钟到：执行测活 Ping (随机选一题)
                test_prompt = self._pick_prompt()
                self.log(f"🔍 触发 {self.check_interval_sec//60} 分钟定时测活 (随机提问: '{test_prompt}')...", "heartbeat")
                self.status_cb(self.task_id, self.status, "🟢 正在执行 30 分钟测活...", test_prompt, self.last_reply[:25])

                ping_ok = self._send_heartbeat(test_prompt)
                if ping_ok:
                    # 能用就不管，保持阶段二！
                    self.check_count += 1
                    self.log(f"✅ 测活通过 (提问: '{test_prompt}' -> 响应: '{self.last_reply}'，能用就不管，重置 30 分钟倒计时)", "success")
                else:
                    # 不能用就继续进入第一阶段！
                    self.log("⚠️ 测活失败或通道失效！模型不可用，立即重新进入阶段一排队挤入...", "warn")
                    break

        self.status = STATUS_STOPPED
        self.status_cb(self.task_id, self.status, "⚪ 已停止", self.last_prompt, self.last_reply[:25])
        self.log("⏹ 任务已安全停止", "info")

    def _squeeze_in(self) -> bool:
        """阶段一：30s 一轮，一轮 5 次循环排队挤入 (轮内间隔 5s，轮末冷却 30s，每次随机提问)"""
        self.session_id = str(uuid.uuid4())

        attempt = 0
        while not self.stop_event.is_set():
            attempt += 1
            round_num = ((attempt - 1) // self.tries_per_round) + 1
            attempt_in_round = ((attempt - 1) % self.tries_per_round) + 1
            is_round_end = (attempt_in_round == self.tries_per_round)

            # 每一次尝试都重新随机抽取问题
            prompt = self._pick_prompt()
            self.last_prompt = prompt

            self.status_cb(
                self.task_id,
                STATUS_SQUEEZING,
                f"🟡 用不了 (第 {round_num} 轮 #{attempt_in_round}/{self.tries_per_round})",
                prompt,
                "",
            )
            self.log(f"[第 {round_num} 轮 #{attempt_in_round}/{self.tries_per_round}] 正在尝试挤入 (随机提问: '{prompt}')...", "queue")

            has_real_content = False
            full_text = []
            failure_reason = ""

            try:
                client = self._make_client()
                client.max_retries = 1  # 关键：单次尝试，由外层循环精确控制轮次、倒计时与随机题轮换！

                for chunk in client.stream_chat(
                    model=self.model_id,
                    prompt=prompt,
                    session_id=self.session_id,
                    max_tokens=64,
                ):
                    if self.stop_event.is_set():
                        return False

                    c_type = chunk.get("type")
                    if c_type in ("text", "thinking"):
                        t = chunk.get("delta", "")
                        if t:
                            has_real_content = True
                            if c_type == "text":
                                full_text.append(t)
                    elif c_type == "status" and chunk.get("stage") == "retrying":
                        failure_reason = chunk.get("reason", "")

                if has_real_content:
                    self.last_reply = "".join(full_text).strip().replace("\n", " ")
                    return True

            except Exception as e:
                err_msg = str(e)
                if "最后报错: " in err_msg:
                    err_msg = err_msg.split("最后报错: ", 1)[1]
                failure_reason = err_msg

            if failure_reason:
                self.log(f"[第 {round_num} 轮 #{attempt_in_round}/{self.tries_per_round}] 排队未挤上: {failure_reason[:50]}", "warn")

            if self.stop_event.is_set():
                return False

            # 等待与冷却处理
            if is_round_end:
                wait_time = int(self.round_cooldown_sec)
                self.log(f"第 {round_num} 轮 ({self.tries_per_round}/{self.tries_per_round}) 结束未挤上，等待冷却 {wait_time}s 进入第 {round_num + 1} 轮...", "queue")

                # 轮末冷却倒计时，UI 实时跳秒显示！
                slept = 0
                while slept < wait_time and not self.stop_event.is_set():
                    rem = wait_time - slept
                    self.status_cb(
                        self.task_id,
                        STATUS_SQUEEZING,
                        f"🟡 轮末冷却中 ({rem}s 后开启第 {round_num + 1} 轮)",
                        prompt,
                        "",
                    )
                    time.sleep(1.0)
                    slept += 1
            else:
                wait_time = int(self.intra_round_delay_sec)
                slept = 0
                while slept < wait_time and not self.stop_event.is_set():
                    time.sleep(1.0)
                    slept += 1

        return False

    def _send_heartbeat(self, prompt: str) -> bool:
        """阶段二：执行测活 Ping (带重试与随机题)"""
        client = self._make_client()
        client.max_retries = self.max_retries
        start_t = time.time()
        received_content = []

        try:
            for chunk in client.stream_chat(
                model=self.model_id,
                prompt=prompt,
                session_id=self.session_id,
                max_tokens=32,
            ):
                if self.stop_event.is_set():
                    return False
                c_type = chunk.get("type")
                if c_type in ("text", "thinking"):
                    delta = chunk.get("delta", "")
                    if delta and c_type == "text":
                        received_content.append(delta)
                elif c_type == "status" and chunk.get("stage") == "retrying":
                    self.log(f"测活等待中: {chunk.get('reason','')[:35]}", "queue")

            cost_ms = int((time.time() - start_t) * 1000)
            if received_content:
                reply_str = "".join(received_content).strip().replace("\n", " ")
                self.last_reply = reply_str
                self.last_prompt = prompt
                self.log(f"✅ 测活成功 ({cost_ms}ms) -> 提问: '{prompt}' | 回复: {reply_str}", "success")
                return True
            else:
                self.log("❌ 测活建立连接但未收到有效回复", "warn")
                return False

        except Exception as e:
            self.log(f"❌ 测活请求报错: {e}", "warn")
            return False


class KeeperApp:
    """主界面窗口应用程序 (高颜值 + 全参数配置 + 系统托盘常驻版)"""

    def __init__(self, root: tk.Tk):
        self.root = root
        self.root.title("AnyRouter 多Key并发保活挂机工具 v2.6")
        self.root.geometry("1060x780")
        self.root.minsize(960, 680)
        self.root.configure(bg="#13141f")

        self.workers = {}                   # task_id -> ModelKeeperWorker
        self.model_check_vars = {}          # model_id -> tk.BooleanVar
        self.model_metadata = {}            # model_id -> dict(id, type, desc)
        self.channel_status_labels = {}     # task_id -> tk.Label
        self.channel_prompt_labels = {}     # task_id -> tk.Label
        self.channel_reply_labels = {}      # task_id -> tk.Label
        self.is_running = False

        self.tray_icon = None
        self.close_behavior = "ask"         # ask, minimize, exit

        self._apply_styles()
        self._build_ui()
        self._load_config()

        threading.Thread(target=self._check_proxy_status, daemon=True).start()

    def _apply_styles(self):
        self.style = ttk.Style()
        self.style.theme_use("clam")

        self.BG_MAIN = "#13141f"
        self.BG_HEADER = "#1a1b2b"
        self.BG_CARD = "#1f2235"
        self.BG_INPUT = "#161724"
        self.BORDER_COLOR = "#2a2d45"
        self.FG_TEXT = "#f1f5f9"
        self.FG_MUTED = "#94a3b8"

        self.COLOR_ACCENT = "#6366f1"
        self.COLOR_SUCCESS = "#10b981"
        self.COLOR_WARN = "#f59e0b"
        self.COLOR_DANGER = "#ef4444"

        self.style.configure(".", background=self.BG_MAIN, foreground=self.FG_TEXT, font=("Segoe UI", 9))
        self.style.configure("Card.TFrame", background=self.BG_CARD)

        # 现代化 Tab 样式
        self.style.configure(
            "TNotebook",
            background=self.BG_MAIN,
            borderwidth=0,
            tabmargins=[2, 6, 2, 0],
        )
        self.style.configure(
            "TNotebook.Tab",
            background="#1a1c2b",
            foreground="#94a3b8",
            font=("Segoe UI", 9, "bold"),
            padding=[16, 6],
            borderwidth=0,
        )
        self.style.map(
            "TNotebook.Tab",
            background=[("selected", "#4f46e5")],
            foreground=[("selected", "#ffffff")],
        )

    def _build_ui(self):
        # 1. 顶部 Header 状态栏
        top_bar = tk.Frame(self.root, bg=self.BG_HEADER, height=48)
        top_bar.pack(fill=tk.X, side=tk.TOP)

        title_frame = tk.Frame(top_bar, bg=self.BG_HEADER)
        title_frame.pack(side=tk.LEFT, padx=16, pady=8)

        tk.Label(
            title_frame,
            text="AnyRouter 多Key自动挂机排队保活工具",
            font=("Segoe UI", 12, "bold"),
            bg=self.BG_HEADER,
            fg="#ffffff",
        ).pack(side=tk.LEFT)

        tk.Label(
            title_frame,
            text="v2.6 托盘常驻版",
            font=("Segoe UI", 8, "bold"),
            bg="#312e81",
            fg="#a5b4fc",
            padx=7,
            pady=1,
        ).pack(side=tk.LEFT, padx=8)

        self.proxy_status_lbl = tk.Label(
            top_bar,
            text="[● 正在检测 10808 代理...]",
            font=("Segoe UI", 9, "bold"),
            bg=self.BG_HEADER,
            fg="#f59e0b",
        )
        self.proxy_status_lbl.pack(side=tk.RIGHT, padx=16)

        main_container = tk.Frame(self.root, bg=self.BG_MAIN)
        main_container.pack(fill=tk.BOTH, expand=True, padx=14, pady=8)

        # =================================================================
        # 2. 顶部卡片：API Keys 多 Key 输入与快捷状态
        # =================================================================
        key_card = tk.LabelFrame(
            main_container,
            text=" 🔑 API Keys 凭据池 (支持多 Key 一行一个，自动去重与并发) ",
            bg=self.BG_CARD,
            fg="#818cf8",
            font=("Segoe UI", 10, "bold"),
            padx=12,
            pady=6,
        )
        key_card.pack(fill=tk.X, pady=(0, 8))

        key_box_row = tk.Frame(key_card, bg=self.BG_CARD)
        key_box_row.pack(fill=tk.X, pady=2)

        self.keys_text = tk.Text(
            key_box_row,
            height=3,
            bg=self.BG_INPUT,
            fg="#e2e8f0",
            insertbackground="#ffffff",
            font=("Consolas", 9),
            relief=tk.FLAT,
            padx=8,
            pady=6,
            wrap=tk.NONE,
        )
        key_scroll = ttk.Scrollbar(key_box_row, orient="vertical", command=self.keys_text.yview)
        self.keys_text.configure(yscrollcommand=key_scroll.set)
        self.keys_text.pack(side=tk.LEFT, fill=tk.X, expand=True)
        key_scroll.pack(side=tk.LEFT, fill=tk.Y, padx=(0, 10))

        self.keys_text.bind("<KeyRelease>", lambda e: self._update_key_count_label())
        self.keys_text.bind("<FocusOut>", lambda e: self._update_key_count_label())

        # 右侧快捷按钮与计数
        btn_side_frame = tk.Frame(key_box_row, bg=self.BG_CARD)
        btn_side_frame.pack(side=tk.RIGHT, fill=tk.Y)

        self.key_count_lbl = tk.Label(
            btn_side_frame,
            text="已识别 0 个 Key",
            font=("Segoe UI", 9, "bold"),
            bg=self.BG_CARD,
            fg="#34d399",
        )
        self.key_count_lbl.pack(fill=tk.X, pady=(0, 3))

        tk.Button(
            btn_side_frame,
            text="📋 粘贴剪贴板",
            command=self._paste_keys_from_clipboard,
            bg="#374151",
            fg="#e2e8f0",
            relief=tk.FLAT,
            padx=8,
            pady=2,
            font=("Segoe UI", 8),
        ).pack(fill=tk.X, pady=1)

        tk.Button(
            btn_side_frame,
            text="🔍 获取模型列表",
            command=self._on_fetch_models,
            bg="#4338ca",
            fg="#ffffff",
            activebackground="#3730a3",
            activeforeground="#ffffff",
            font=("Segoe UI", 9, "bold"),
            relief=tk.FLAT,
            padx=10,
            pady=3,
        ).pack(fill=tk.X, pady=1)

        # =================================================================
        # 3. 中部三大面板 Notebook (模型选择 / 并发矩阵监控 / 全配置参数)
        # =================================================================
        self.notebook = ttk.Notebook(main_container)
        self.notebook.pack(fill=tk.BOTH, expand=True, pady=(0, 8))

        # -----------------------------------------------------------------
        # Tab 1: 📋 模型选择
        # -----------------------------------------------------------------
        self.tab_models = tk.Frame(self.notebook, bg=self.BG_CARD)
        self.notebook.add(self.tab_models, text=" 📋 模型选择 (可多选) ")

        tab1_tb = tk.Frame(self.tab_models, bg=self.BG_CARD, padx=10, pady=6)
        tab1_tb.pack(fill=tk.X)

        tk.Button(
            tab1_tb,
            text="全选",
            command=self._select_all_models,
            bg="#374151",
            fg="#ffffff",
            relief=tk.FLAT,
            padx=8,
            pady=1,
            font=("Segoe UI", 8),
        ).pack(side=tk.LEFT, padx=(0, 4))

        tk.Button(
            tab1_tb,
            text="全不选",
            command=self._deselect_all_models,
            bg="#374151",
            fg="#ffffff",
            relief=tk.FLAT,
            padx=8,
            pady=1,
            font=("Segoe UI", 8),
        ).pack(side=tk.LEFT, padx=(0, 8))

        self.model_count_lbl = tk.Label(
            tab1_tb,
            text="共加载 0 个模型 (已勾选 0 个)",
            bg=self.BG_CARD,
            fg=self.FG_MUTED,
            font=("Segoe UI", 8),
        )
        self.model_count_lbl.pack(side=tk.LEFT)

        model_canvas_frame = tk.Frame(self.tab_models, bg=self.BG_CARD)
        model_canvas_frame.pack(fill=tk.BOTH, expand=True, padx=8, pady=(0, 8))

        self.model_canvas = tk.Canvas(model_canvas_frame, bg="#161724", highlightthickness=0)
        m_scroll = ttk.Scrollbar(model_canvas_frame, orient="vertical", command=self.model_canvas.yview)
        self.model_scroll_frame = tk.Frame(self.model_canvas, bg="#161724")

        self.model_scroll_frame.bind(
            "<Configure>",
            lambda e: self.model_canvas.configure(scrollregion=self.model_canvas.bbox("all")),
        )
        self.model_canvas.create_window((0, 0), window=self.model_scroll_frame, anchor="nw")
        self.model_canvas.configure(yscrollcommand=m_scroll.set)

        self.model_canvas.pack(side=tk.LEFT, fill=tk.BOTH, expand=True)
        m_scroll.pack(side=tk.RIGHT, fill=tk.Y)

        # -----------------------------------------------------------------
        # Tab 2: 📊 多 Key 并发监控矩阵
        # -----------------------------------------------------------------
        self.tab_monitor = tk.Frame(self.notebook, bg=self.BG_CARD)
        self.notebook.add(self.tab_monitor, text=" 📊 多 Key 并发监控矩阵 (实时状态) ")

        tab2_tb = tk.Frame(self.tab_monitor, bg=self.BG_CARD, padx=10, pady=6)
        tab2_tb.pack(fill=tk.X)

        self.monitor_summary_lbl = tk.Label(
            tab2_tb,
            text="尚未开始保活。配置好参数并勾选模型后，点击下方【🚀 开始并发挂机保活】即可启动。",
            bg=self.BG_CARD,
            fg="#94a3b8",
            font=("Segoe UI", 9),
        )
        self.monitor_summary_lbl.pack(side=tk.LEFT)

        monitor_canvas_frame = tk.Frame(self.tab_monitor, bg=self.BG_CARD)
        monitor_canvas_frame.pack(fill=tk.BOTH, expand=True, padx=8, pady=(0, 8))

        self.monitor_canvas = tk.Canvas(monitor_canvas_frame, bg="#161724", highlightthickness=0)
        mon_scroll = ttk.Scrollbar(monitor_canvas_frame, orient="vertical", command=self.monitor_canvas.yview)
        self.monitor_scroll_frame = tk.Frame(self.monitor_canvas, bg="#161724")

        self.monitor_scroll_frame.bind(
            "<Configure>",
            lambda e: self.monitor_canvas.configure(scrollregion=self.monitor_canvas.bbox("all")),
        )
        self.monitor_canvas.create_window((0, 0), window=self.monitor_scroll_frame, anchor="nw")
        self.monitor_canvas.configure(yscrollcommand=mon_scroll.set)

        self.monitor_canvas.pack(side=tk.LEFT, fill=tk.BOTH, expand=True)
        mon_scroll.pack(side=tk.RIGHT, fill=tk.Y)

        # -----------------------------------------------------------------
        # Tab 3: ⚙ 全参数高级配置 (所有配置文件参数在界面全部可调)
        # -----------------------------------------------------------------
        self.tab_settings = tk.Frame(self.notebook, bg=self.BG_CARD)
        self.notebook.add(self.tab_settings, text=" ⚙ 全参数高级配置与题库 ")

        self._build_settings_tab()

        # =================================================================
        # 4. 操作控制条
        # =================================================================
        action_bar = tk.Frame(main_container, bg=self.BG_MAIN)
        action_bar.pack(fill=tk.X, pady=(0, 8))

        self.start_btn = tk.Button(
            action_bar,
            text="🚀 开始并发挂机保活",
            command=self._on_start_keepalive,
            bg=self.COLOR_SUCCESS,
            fg="#ffffff",
            activebackground="#059669",
            activeforeground="#ffffff",
            font=("Segoe UI", 11, "bold"),
            relief=tk.FLAT,
            padx=18,
            pady=6,
        )
        self.start_btn.pack(side=tk.LEFT, padx=(0, 10))

        self.stop_btn = tk.Button(
            action_bar,
            text="⏹ 停止挂机",
            command=self._on_stop_keepalive,
            state=tk.DISABLED,
            bg="#4b5563",
            fg="#ffffff",
            activebackground="#dc2626",
            activeforeground="#ffffff",
            font=("Segoe UI", 11, "bold"),
            relief=tk.FLAT,
            padx=18,
            pady=6,
        )
        self.stop_btn.pack(side=tk.LEFT, padx=(0, 10))

        self.matrix_summary_lbl = tk.Label(
            action_bar,
            text="",
            bg=self.BG_MAIN,
            fg="#818cf8",
            font=("Segoe UI", 9, "bold"),
        )
        self.matrix_summary_lbl.pack(side=tk.LEFT, padx=6)

        tk.Button(
            action_bar,
            text="🧹 清空日志",
            command=self._clear_logs,
            bg="#33374f",
            fg="#e2e8f0",
            relief=tk.FLAT,
            padx=12,
            pady=6,
            font=("Segoe UI", 9),
        ).pack(side=tk.RIGHT, padx=4)

        tk.Button(
            action_bar,
            text="💾 保存配置",
            command=self._save_config,
            bg="#33374f",
            fg="#e2e8f0",
            relief=tk.FLAT,
            padx=12,
            pady=6,
            font=("Segoe UI", 9),
        ).pack(side=tk.RIGHT, padx=4)

        # =================================================================
        # 5. 底部实时日志监控窗口
        # =================================================================
        log_box = tk.LabelFrame(
            main_container,
            text=" 🖥 实时两阶段并发挂机日志 ",
            bg=self.BG_CARD,
            fg="#818cf8",
            font=("Segoe UI", 9, "bold"),
            padx=8,
            pady=6,
        )
        log_box.pack(fill=tk.BOTH, expand=True)

        self.log_text = scrolledtext.ScrolledText(
            log_box,
            wrap=tk.WORD,
            height=7,
            bg="#0f1019",
            fg="#f8f8f2",
            insertbackground="#ffffff",
            font=("Consolas", 9),
            relief=tk.FLAT,
        )
        self.log_text.pack(fill=tk.BOTH, expand=True)

        self.log_text.tag_config("timestamp", foreground="#64748b")
        self.log_text.tag_config("normal", foreground="#e2e8f0")
        self.log_text.tag_config("info", foreground="#60a5fa")
        self.log_text.tag_config("queue", foreground="#facc15")
        self.log_text.tag_config("success", foreground="#34d399")
        self.log_text.tag_config("warn", foreground="#fb923c")
        self.log_text.tag_config("error", foreground="#f87171")
        self.log_text.tag_config("heartbeat", foreground="#38bdf8")

    def _build_settings_tab(self):
        """在 Tab 3 中构建全部参数的调节面板"""
        container = tk.Frame(self.tab_settings, bg=self.BG_CARD, padx=14, pady=10)
        container.pack(fill=tk.BOTH, expand=True)

        # 1. 题库设置 Card
        prompt_card = tk.LabelFrame(
            container,
            text=" 🎲 随机测活题库设置 (内置 10 大极简预设题，支持自由增删改) ",
            bg=self.BG_CARD,
            fg="#818cf8",
            font=("Segoe UI", 9, "bold"),
            padx=10,
            pady=6,
        )
        prompt_card.pack(fill=tk.X, pady=(0, 10))

        prompt_top_row = tk.Frame(prompt_card, bg=self.BG_CARD)
        prompt_top_row.pack(fill=tk.X, pady=(0, 4))

        self.random_prompt_var = tk.BooleanVar(value=True)
        tk.Checkbutton(
            prompt_top_row,
            text="启用随机测活问题轮换 (防止平台缓存与固定单一特征识别)",
            variable=self.random_prompt_var,
            font=("Segoe UI", 9, "bold"),
            bg=self.BG_CARD,
            fg="#34d399",
            selectcolor="#161724",
            activebackground=self.BG_CARD,
            activeforeground="#34d399",
        ).pack(side=tk.LEFT)

        tk.Button(
            prompt_top_row,
            text="↺ 恢复 10 大经典预设题库",
            command=self._restore_default_prompts,
            bg="#374151",
            fg="#e2e8f0",
            relief=tk.FLAT,
            padx=8,
            pady=1,
            font=("Segoe UI", 8),
        ).pack(side=tk.RIGHT)

        prompt_box_row = tk.Frame(prompt_card, bg=self.BG_CARD)
        prompt_box_row.pack(fill=tk.X, pady=2)

        self.prompts_text = tk.Text(
            prompt_box_row,
            height=4,
            bg=self.BG_INPUT,
            fg="#e2e8f0",
            insertbackground="#ffffff",
            font=("Consolas", 9),
            relief=tk.FLAT,
            padx=8,
            pady=4,
            wrap=tk.NONE,
        )
        p_scroll = ttk.Scrollbar(prompt_box_row, orient="vertical", command=self.prompts_text.yview)
        self.prompts_text.configure(yscrollcommand=p_scroll.set)
        self.prompts_text.pack(side=tk.LEFT, fill=tk.X, expand=True)
        p_scroll.pack(side=tk.LEFT, fill=tk.Y)

        # 2. 阶段一与阶段二参数双列 Card
        cols_frame = tk.Frame(container, bg=self.BG_CARD)
        cols_frame.pack(fill=tk.X, pady=(0, 10))

        # 阶段一排队参数
        p1_card = tk.LabelFrame(
            cols_frame,
            text=" ⏱ 阶段一【用不了】排队挤入参数 ",
            bg=self.BG_CARD,
            fg="#facc15",
            font=("Segoe UI", 9, "bold"),
            padx=10,
            pady=8,
        )
        p1_card.pack(side=tk.LEFT, fill=tk.BOTH, expand=True, padx=(0, 6))

        p1_r1 = tk.Frame(p1_card, bg=self.BG_CARD)
        p1_r1.pack(fill=tk.X, pady=3)
        tk.Label(p1_r1, text="每轮尝试次数:", width=14, anchor="w", bg=self.BG_CARD, fg=self.FG_TEXT).pack(side=tk.LEFT)
        self.tries_entry = tk.Entry(p1_r1, width=6, bg=self.BG_INPUT, fg="#ffffff", insertbackground="#ffffff")
        self.tries_entry.pack(side=tk.LEFT, padx=4)
        tk.Label(p1_r1, text="次 (默认 5 次)", bg=self.BG_CARD, fg=self.FG_MUTED).pack(side=tk.LEFT)

        p1_r2 = tk.Frame(p1_card, bg=self.BG_CARD)
        p1_r2.pack(fill=tk.X, pady=3)
        tk.Label(p1_r2, text="轮内单次间隔:", width=14, anchor="w", bg=self.BG_CARD, fg=self.FG_TEXT).pack(side=tk.LEFT)
        self.intra_delay_entry = tk.Entry(p1_r2, width=6, bg=self.BG_INPUT, fg="#ffffff", insertbackground="#ffffff")
        self.intra_delay_entry.pack(side=tk.LEFT, padx=4)
        tk.Label(p1_r2, text="秒 (默认 5 秒)", bg=self.BG_CARD, fg=self.FG_MUTED).pack(side=tk.LEFT)

        p1_r3 = tk.Frame(p1_card, bg=self.BG_CARD)
        p1_r3.pack(fill=tk.X, pady=3)
        tk.Label(p1_r3, text="轮末冷却等待:", width=14, anchor="w", bg=self.BG_CARD, fg=self.FG_TEXT).pack(side=tk.LEFT)
        self.cooldown_entry = tk.Entry(p1_r3, width=6, bg=self.BG_INPUT, fg="#ffffff", insertbackground="#ffffff")
        self.cooldown_entry.pack(side=tk.LEFT, padx=4)
        tk.Label(p1_r3, text="秒 (默认 30 秒)", bg=self.BG_CARD, fg=self.FG_MUTED).pack(side=tk.LEFT)

        # 阶段二测活与窗口行为参数
        p2_card = tk.LabelFrame(
            cols_frame,
            text=" 🛡️ 阶段二【能用】测活与窗口行为 ",
            bg=self.BG_CARD,
            fg="#34d399",
            font=("Segoe UI", 9, "bold"),
            padx=10,
            pady=8,
        )
        p2_card.pack(side=tk.RIGHT, fill=tk.BOTH, expand=True, padx=(6, 0))

        p2_r1 = tk.Frame(p2_card, bg=self.BG_CARD)
        p2_r1.pack(fill=tk.X, pady=3)
        tk.Label(p2_r1, text="测活巡航周期:", width=14, anchor="w", bg=self.BG_CARD, fg=self.FG_TEXT).pack(side=tk.LEFT)
        self.check_min_entry = tk.Entry(p2_r1, width=6, bg=self.BG_INPUT, fg="#ffffff", insertbackground="#ffffff")
        self.check_min_entry.pack(side=tk.LEFT, padx=4)
        tk.Label(p2_r1, text="分钟 (默认 30 分钟，能用就不管)", bg=self.BG_CARD, fg=self.FG_MUTED).pack(side=tk.LEFT)

        p2_r2 = tk.Frame(p2_card, bg=self.BG_CARD)
        p2_r2.pack(fill=tk.X, pady=3)
        tk.Label(p2_r2, text="测活重试次数:", width=14, anchor="w", bg=self.BG_CARD, fg=self.FG_TEXT).pack(side=tk.LEFT)
        self.max_retries_entry = tk.Entry(p2_r2, width=6, bg=self.BG_INPUT, fg="#ffffff", insertbackground="#ffffff")
        self.max_retries_entry.pack(side=tk.LEFT, padx=4)
        tk.Label(p2_r2, text="次 (默认 3 次)", bg=self.BG_CARD, fg=self.FG_MUTED).pack(side=tk.LEFT)

        p2_r3 = tk.Frame(p2_card, bg=self.BG_CARD)
        p2_r3.pack(fill=tk.X, pady=3)
        tk.Label(p2_r3, text="本地代理通道:", width=14, anchor="w", bg=self.BG_CARD, fg=self.FG_TEXT).pack(side=tk.LEFT)
        self.proxy_entry = tk.Entry(p2_r3, width=22, bg=self.BG_INPUT, fg="#ffffff", insertbackground="#ffffff")
        self.proxy_entry.pack(side=tk.LEFT, padx=4)

        # 窗口关闭行为配置
        p2_r4 = tk.Frame(p2_card, bg=self.BG_CARD)
        p2_r4.pack(fill=tk.X, pady=3)
        tk.Label(p2_r4, text="点击关闭行为:", width=14, anchor="w", bg=self.BG_CARD, fg="#818cf8", font=("Segoe UI", 9, "bold")).pack(side=tk.LEFT)
        self.close_action_var = tk.StringVar(value="每次询问 (默认)")
        self.close_cb = ttk.Combobox(
            p2_r4,
            textvariable=self.close_action_var,
            values=["每次询问 (默认)", "最小化到系统托盘", "彻底退出程序"],
            state="readonly",
            width=18,
        )
        self.close_cb.pack(side=tk.LEFT, padx=4)

    # =====================================================================
    # Key 处理与多 Key 识别
    # =====================================================================
    def _get_keys(self) -> list:
        """从多行文本框中解析出所有有效 API Key"""
        raw = self.keys_text.get("1.0", tk.END)
        lines = [k.strip() for k in raw.splitlines()]
        seen = set()
        keys = []
        for k in lines:
            if k and not k.startswith("#") and k not in seen:
                seen.add(k)
                keys.append(k)
        return keys

    def _get_prompts(self) -> list:
        """从多行文本框中解析出所有题库问题"""
        raw = self.prompts_text.get("1.0", tk.END)
        lines = [p.strip() for p in raw.splitlines()]
        prompts = [p for p in lines if p and not p.startswith("#")]
        if not prompts:
            prompts = list(DEFAULT_HEARTBEAT_PROMPTS)
        return prompts

    def _restore_default_prompts(self):
        self.prompts_text.delete("1.0", tk.END)
        self.prompts_text.insert("1.0", "\n".join(DEFAULT_HEARTBEAT_PROMPTS) + "\n")
        self.random_prompt_var.set(True)
        messagebox.showinfo("提示", "已成功恢复 10 大经典预设题库！")

    def _update_key_count_label(self):
        keys = self._get_keys()
        n = len(keys)
        self.key_count_lbl.configure(
            text=f"已识别 {n} 个 Key",
            fg="#34d399" if n > 0 else "#f87171",
        )
        self._update_matrix_summary()

    def _paste_keys_from_clipboard(self):
        try:
            content = self.root.clipboard_get()
            if content:
                current = self.keys_text.get("1.0", tk.END).strip()
                if current:
                    self.keys_text.insert(tk.END, f"\n{content.strip()}\n")
                else:
                    self.keys_text.insert(tk.END, f"{content.strip()}\n")
                self._update_key_count_label()
        except Exception as e:
            messagebox.showwarning("粘贴失败", f"无法读取剪贴板: {e}")

    def _update_matrix_summary(self):
        keys = self._get_keys()
        models = [m for m, v in self.model_check_vars.items() if v.get()]
        prompts = self._get_prompts()
        total = len(keys) * len(models)
        if total > 0:
            rand_str = "10题随机轮换" if self.random_prompt_var.get() else "固定首题"
            self.matrix_summary_lbl.configure(
                text=f"⚙ 当前规模: {len(keys)} 个 Key × {len(models)} 个模型 = {total} 个并发通道 | 题库: {len(prompts)} 题 ({rand_str})"
            )
        else:
            self.matrix_summary_lbl.configure(text="")

    def _append_log(self, text: str, tag: str = "normal"):
        now = datetime.now().strftime("%H:%M:%S")

        def _do_append():
            self.log_text.insert(tk.END, f"[{now}] ", "timestamp")
            self.log_text.insert(tk.END, f"{text}\n", tag)
            self.log_text.see(tk.END)

        self.root.after(0, _do_append)

    def show_toast(self, title: str, message: str, duration_sec: int = 8):
        """主线程安全地在屏幕右下角弹出悬浮通知卡片"""
        self.root.after(0, lambda: DesktopToast(self.root, title, message, duration_sec))

    def _update_channel_status(self, task_id: str, status: str, status_desc: str, prompt: str, last_reply: str):
        color_map = {
            STATUS_IDLE: "#94a3b8",
            STATUS_SQUEEZING: "#facc15",
            STATUS_AVAILABLE: "#34d399",
            STATUS_STOPPED: "#94a3b8",
        }
        color = color_map.get(status, "#ffffff")

        def _do_update():
            if task_id in self.channel_status_labels:
                self.channel_status_labels[task_id].configure(text=status_desc, fg=color)
            if task_id in self.channel_prompt_labels and prompt:
                self.channel_prompt_labels[task_id].configure(text=f"题: '{prompt}'")
            if task_id in self.channel_reply_labels and last_reply:
                self.channel_reply_labels[task_id].configure(text=f"响应: {last_reply}")

        self.root.after(0, _do_update)

    def _clear_logs(self):
        self.log_text.delete("1.0", tk.END)

    def _check_proxy_status(self):
        proxy = self.proxy_entry.get().strip() or DEFAULT_PROXY
        try:
            import urllib.request
            proxy_support = urllib.request.ProxyHandler({"http": proxy, "https": proxy})
            opener = urllib.request.build_opener(proxy_support)
            req = urllib.request.Request("https://anyrouter.top/health", headers={"User-Agent": "curl/8.0"})
            with opener.open(req, timeout=8) as resp:
                if resp.status == 200:
                    self.root.after(0, lambda: self.proxy_status_lbl.configure(text="[● 10808 代理连接畅通]", fg="#10b981"))
                    return
        except Exception:
            pass
        self.root.after(0, lambda: self.proxy_status_lbl.configure(text="[● 10808 代理异常/检测中]", fg="#f59e0b"))

    # =====================================================================
    # 系统托盘管理
    # =====================================================================
    def _init_tray_icon(self):
        """初始化并启动系统托盘常驻图标"""
        if self.tray_icon is not None:
            return
        icon_img = create_tray_icon_image()
        menu = pystray.Menu(
            pystray.MenuItem("打开主控制面板", lambda icon, item: self._show_from_tray(), default=True),
            pystray.MenuItem("查看挂机状态", lambda icon, item: self._tray_show_status()),
            pystray.Menu.SEPARATOR,
            pystray.MenuItem("彻底退出程序", lambda icon, item: self._quit_from_tray()),
        )
        self.tray_icon = pystray.Icon("anyrouter_keeper", icon_img, "AnyRouter 挂机保活工具", menu)
        threading.Thread(target=self.tray_icon.run, daemon=True).start()

    def _minimize_to_tray(self):
        """最小化到系统托盘"""
        self._init_tray_icon()
        self.root.withdraw()
        self.show_toast(
            "已最小化到系统托盘",
            "AnyRouter 挂机保活在后台持续运行中。\n双击屏幕右下角托盘图标可重新打开主面板。",
            duration_sec=6,
        )

    def _show_from_tray(self):
        """从托盘恢复显示主窗口"""
        def _do():
            self.root.deiconify()
            self.root.lift()
            self.root.focus_force()
        self.root.after(0, _do)

    def _tray_show_status(self):
        """托盘气泡提示当前状态"""
        running_str = "🟢 正在并发挂机守护中" if self.is_running else "⚪ 空闲待命中"
        msg = f"当前状态: {running_str}\n活动守护通道数: {len(self.workers)}\n双击托盘图标打开主面板。"
        if self.tray_icon:
            try:
                self.tray_icon.notify(msg, "AnyRouter 挂机状态")
            except Exception:
                pass

    def _quit_from_tray(self):
        """从托盘菜单彻底退出"""
        self.root.after(0, self._force_quit_app)

    def _force_quit_app(self):
        """安全停止守护线程并彻底退出应用进程"""
        if self.is_running:
            for worker in self.workers.values():
                worker.stop()
            self.workers.clear()
            self.is_running = False
        if self.tray_icon is not None:
            try:
                self.tray_icon.stop()
            except Exception:
                pass
            self.tray_icon = None
        self.root.destroy()
        sys.exit(0)

    def on_window_close_clicked(self):
        """处理窗口右上角叉掉事件"""
        b_map = {
            "每次询问 (默认)": "ask",
            "最小化到系统托盘": "minimize",
            "彻底退出程序": "exit",
        }
        current_setting = b_map.get(self.close_action_var.get(), "ask")
        if current_setting == "minimize":
            self._minimize_to_tray()
        elif current_setting == "exit":
            self._force_quit_app()
        else:
            CloseActionDialog(self.root, self.is_running, self._handle_close_decision)

    def _handle_close_decision(self, action: str, remember: bool):
        if remember:
            rev_map = {
                "ask": "每次询问 (默认)",
                "minimize": "最小化到系统托盘",
                "exit": "彻底退出程序",
            }
            self.close_action_var.set(rev_map.get(action, "每次询问 (默认)"))
            self._save_config_silent()
        if action == "minimize":
            self._minimize_to_tray()
        elif action == "exit":
            self._force_quit_app()

    def _save_config_silent(self):
        keys = self._get_keys()
        prompts = self._get_prompts()
        selected = [m for m, var in self.model_check_vars.items() if var.get()]
        b_map = {
            "每次询问 (默认)": "ask",
            "最小化到系统托盘": "minimize",
            "彻底退出程序": "exit",
        }
        try:
            cfg = {
                "api_keys": keys,
                "api_key": keys[0] if keys else "",
                "proxy": self.proxy_entry.get().strip(),
                "check_interval_min": int(self.check_min_entry.get().strip() or 30),
                "round_cooldown_sec": int(self.cooldown_entry.get().strip() or 30),
                "tries_per_round": int(self.tries_entry.get().strip() or 5),
                "intra_round_delay_sec": int(self.intra_delay_entry.get().strip() or 5),
                "max_retries": int(self.max_retries_entry.get().strip() or 3),
                "random_heartbeat": self.random_prompt_var.get(),
                "heartbeat_prompts": prompts,
                "heartbeat_prompt": prompts[0] if prompts else "1+1",
                "selected_models": selected,
                "close_behavior": b_map.get(self.close_action_var.get(), "ask"),
            }
            with open(CONFIG_FILE, "w", encoding="utf-8") as f:
                json.dump(cfg, f, ensure_ascii=False, indent=2)
        except Exception:
            pass

    # =====================================================================
    # 配置持久化 (所有参数双向读写)
    # =====================================================================
    def _load_config(self):
        default_key = "sk-BZMSVilf0BiRvEnvymd3KflwY1xr45wmXjw5Ewg2fsIkp9T2"
        cfg = {}
        if os.path.exists(CONFIG_FILE):
            try:
                with open(CONFIG_FILE, "r", encoding="utf-8") as f:
                    cfg = json.load(f)
            except Exception:
                pass

        # 1. API Keys
        keys_list = cfg.get("api_keys")
        if isinstance(keys_list, list) and keys_list:
            keys_text = "\n".join(keys_list)
        elif cfg.get("api_key"):
            keys_text = cfg.get("api_key")
        else:
            keys_text = default_key

        self.keys_text.delete("1.0", tk.END)
        self.keys_text.insert("1.0", keys_text.strip() + "\n")
        self._update_key_count_label()

        # 2. 题库列表与随机开关
        prompts_list = cfg.get("heartbeat_prompts")
        if not isinstance(prompts_list, list) or not prompts_list:
            single_p = cfg.get("heartbeat_prompt")
            prompts_list = [single_p] if single_p else list(DEFAULT_HEARTBEAT_PROMPTS)
            for dp in DEFAULT_HEARTBEAT_PROMPTS:
                if dp not in prompts_list:
                    prompts_list.append(dp)
                if len(prompts_list) >= 10:
                    break

        self.prompts_text.delete("1.0", tk.END)
        self.prompts_text.insert("1.0", "\n".join(prompts_list) + "\n")
        self.random_prompt_var.set(cfg.get("random_heartbeat", True))

        # 3. 阶段一排队参数
        tries = str(cfg.get("tries_per_round", 5))
        intra_delay = str(cfg.get("intra_round_delay_sec", 5))
        cooldown = str(cfg.get("round_cooldown_sec", 30))

        self.tries_entry.insert(0, tries)
        self.intra_delay_entry.insert(0, intra_delay)
        self.cooldown_entry.insert(0, cooldown)

        # 4. 阶段二测活与网络参数
        check_min = str(cfg.get("check_interval_min", 30))
        max_retries = str(cfg.get("max_retries", 3))
        proxy = cfg.get("proxy") or os.environ.get("HTTP_PROXY") or DEFAULT_PROXY

        self.check_min_entry.insert(0, check_min)
        self.max_retries_entry.insert(0, max_retries)
        self.proxy_entry.insert(0, proxy)

        # 5. 关闭行为
        behavior = cfg.get("close_behavior", "ask")
        rev_map = {
            "ask": "每次询问 (默认)",
            "minimize": "最小化到系统托盘",
            "exit": "彻底退出程序",
        }
        self.close_action_var.set(rev_map.get(behavior, "每次询问 (默认)"))

        # 6. 模型勾选项
        selected = cfg.get("selected_models", ["gpt-6-astra", "claude-fable-5-1"])
        self._render_models_list(DEFAULT_MODEL_CANDIDATES, selected)

        self._append_log("初始化就绪 | 架构: 多 Key 并发矩阵 | 题库: 10 题随机轮换 | 已支持系统托盘常驻", "info")

    def _save_config(self):
        keys = self._get_keys()
        prompts = self._get_prompts()
        selected = [m for m, var in self.model_check_vars.items() if var.get()]

        try:
            tries = int(self.tries_entry.get().strip() or 5)
            intra_delay = int(self.intra_delay_entry.get().strip() or 5)
            cooldown = int(self.cooldown_entry.get().strip() or 30)
            check_min = int(self.check_min_entry.get().strip() or 30)
            max_retries = int(self.max_retries_entry.get().strip() or 3)
        except ValueError:
            messagebox.showerror("错误", "数值参数必须为有效正整数！")
            return

        b_map = {
            "每次询问 (默认)": "ask",
            "最小化到系统托盘": "minimize",
            "彻底退出程序": "exit",
        }

        cfg = {
            "api_keys": keys,
            "api_key": keys[0] if keys else "",
            "proxy": self.proxy_entry.get().strip(),
            "check_interval_min": check_min,
            "round_cooldown_sec": cooldown,
            "tries_per_round": tries,
            "intra_round_delay_sec": intra_delay,
            "max_retries": max_retries,
            "random_heartbeat": self.random_prompt_var.get(),
            "heartbeat_prompts": prompts,
            "heartbeat_prompt": prompts[0] if prompts else "1+1",
            "selected_models": selected,
            "close_behavior": b_map.get(self.close_action_var.get(), "ask"),
        }
        try:
            with open(CONFIG_FILE, "w", encoding="utf-8") as f:
                json.dump(cfg, f, ensure_ascii=False, indent=2)
            self._append_log(f"全量配置已保存至 keeper_config.json (Key: {len(keys)}个, 题库: {len(prompts)}道, 模型: {len(selected)}个)", "success")
            messagebox.showinfo("保存成功", f"所有配置已成功保存！\n- Key 数量: {len(keys)} 个\n- 测活题库: {len(prompts)} 道 (随机: {'开' if self.random_prompt_var.get() else '关'})\n- 关闭行为: {self.close_action_var.get()}\n下次启动软件自动生效。")
        except Exception as e:
            messagebox.showerror("保存失败", f"保存配置出错: {e}")

    # =====================================================================
    # 模型列表渲染与拉取
    # =====================================================================
    def _render_models_list(self, model_list, selected_ids=None):
        for widget in self.model_scroll_frame.winfo_children():
            widget.destroy()
        self.model_check_vars.clear()
        self.model_metadata.clear()

        selected_set = set(selected_ids or ["gpt-6-astra", "claude-fable-5-1"])

        for idx, m in enumerate(model_list):
            m_id = m["id"] if isinstance(m, dict) else m
            m_type = m.get("type", "自动检测") if isinstance(m, dict) else ""
            m_desc = m.get("desc", "") if isinstance(m, dict) else ""
            self.model_metadata[m_id] = {"id": m_id, "type": m_type, "desc": m_desc}

            row = tk.Frame(self.model_scroll_frame, bg="#1a1c2d" if idx % 2 == 0 else "#161724", padx=12, pady=6)
            row.pack(fill=tk.X, expand=True, pady=1)

            is_checked = m_id in selected_set
            var = tk.BooleanVar(value=is_checked)
            self.model_check_vars[m_id] = var
            var.trace_add("write", lambda *args: self._on_model_selection_changed())

            cb = tk.Checkbutton(
                row,
                text=m_id,
                variable=var,
                font=("Segoe UI", 10, "bold"),
                bg=row["bg"],
                fg="#ffffff",
                selectcolor="#0f1019",
                activebackground=row["bg"],
                activeforeground="#ffffff",
            )
            cb.pack(side=tk.LEFT)

            if m_type:
                is_codex = "Codex" in m_type
                tag_bg = "#23334d" if is_codex else "#36264d"
                tag_fg = "#93c5fd" if is_codex else "#c084fc"
                tag_lbl = tk.Label(
                    row,
                    text=f" {m_type} ",
                    font=("Segoe UI", 8, "bold"),
                    bg=tag_bg,
                    fg=tag_fg,
                    padx=4,
                    pady=1,
                )
                tag_lbl.pack(side=tk.LEFT, padx=6)

            if m_desc:
                desc_lbl = tk.Label(
                    row,
                    text=m_desc,
                    font=("Segoe UI", 8),
                    bg=row["bg"],
                    fg="#64748b",
                )
                desc_lbl.pack(side=tk.LEFT, padx=6)

        self._on_model_selection_changed()

    def _on_model_selection_changed(self):
        total = len(self.model_metadata)
        selected = len([v for v in self.model_check_vars.values() if v.get()])
        self.model_count_lbl.configure(text=f"共加载 {total} 个模型 (已勾选 {selected} 个)")
        self._update_matrix_summary()

    def _select_all_models(self):
        for var in self.model_check_vars.values():
            var.set(True)

    def _deselect_all_models(self):
        for var in self.model_check_vars.values():
            var.set(False)

    def _on_fetch_models(self):
        keys = self._get_keys()
        if not keys:
            messagebox.showwarning("提示", "请先在上方输入框填入至少一个有效的 API Key！")
            return

        proxy = self.proxy_entry.get().strip() or DEFAULT_PROXY
        self._append_log("正在通过 10808 代理从 anyrouter.top 拉取线上实时模型列表...", "info")

        def _fetch_thread():
            import requests
            last_err = ""
            for key in keys:
                try:
                    headers = {
                        "Authorization": f"Bearer {key}",
                        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
                        "Accept": "application/json",
                    }
                    proxies = {"http": proxy, "https": proxy} if proxy else None
                    resp = requests.get(f"{DEFAULT_BASE_URL}/v1/models", headers=headers, proxies=proxies, timeout=10)
                    if resp.status_code == 200:
                        data = resp.json().get("data", [])
                        models = []
                        for item in data:
                            m_id = item.get("id")
                            if not m_id:
                                continue
                            if AnyRouterClient.is_codex_model(m_id):
                                m_type = "Codex / Responses"
                            else:
                                m_type = "Claude Code"
                            models.append({"id": m_id, "type": m_type, "desc": ""})

                        self.root.after(0, lambda: self._on_fetch_success(models))
                        return
                    else:
                        last_err = f"HTTP {resp.status_code}: {resp.text[:80]}"
                except Exception as e:
                    last_err = str(e)

            self.root.after(0, lambda: self._on_fetch_fail(last_err))

        threading.Thread(target=_fetch_thread, daemon=True).start()

    def _on_fetch_success(self, models):
        self._append_log(f"✅ 成功获取并刷新 {len(models)} 个线上实时模型！", "success")
        current_selected = [m for m, v in self.model_check_vars.items() if v.get()]
        self._render_models_list(models, current_selected)

    def _on_fetch_fail(self, err):
        self._append_log(f"拉取模型失败 ({err})，继续使用内置全量模型列表", "warn")
        messagebox.showwarning("提示", f"线上拉取模型超时或鉴权失败 ({err})，将继续使用内置候选列表。")

    # =====================================================================
    # 并发保活通道渲染与启动控制
    # =====================================================================
    def _render_monitor_channels(self, task_specs):
        """在 Tab 2 中构筑多 Key × 模型的实时监控通道卡片"""
        for widget in self.monitor_scroll_frame.winfo_children():
            widget.destroy()
        self.channel_status_labels.clear()
        self.channel_prompt_labels.clear()
        self.channel_reply_labels.clear()

        for idx, spec in enumerate(task_specs):
            task_id = spec["task_id"]
            key_label = spec["key_label"]
            model_id = spec["model_id"]
            model_type = spec["model_type"]

            card = tk.Frame(
                self.monitor_scroll_frame,
                bg="#1c1e30" if idx % 2 == 0 else "#161725",
                padx=12,
                pady=7,
            )
            card.pack(fill=tk.X, expand=True, pady=1)

            key_badge = tk.Label(
                card,
                text=key_label,
                font=("Consolas", 8, "bold"),
                bg="#4338ca",
                fg="#ffffff",
                padx=6,
                pady=1,
            )
            key_badge.pack(side=tk.LEFT, padx=(0, 8))

            m_lbl = tk.Label(
                card,
                text=model_id,
                font=("Segoe UI", 10, "bold"),
                bg=card["bg"],
                fg="#ffffff",
            )
            m_lbl.pack(side=tk.LEFT, padx=(0, 6))

            if model_type:
                t_lbl = tk.Label(
                    card,
                    text=f"[{model_type}]",
                    font=("Segoe UI", 8),
                    bg=card["bg"],
                    fg="#94a3b8",
                )
                t_lbl.pack(side=tk.LEFT, padx=(0, 10))

            status_lbl = tk.Label(
                card,
                text="🟡 准备启动...",
                font=("Segoe UI", 9),
                bg=card["bg"],
                fg="#facc15",
            )
            status_lbl.pack(side=tk.LEFT, padx=6)
            self.channel_status_labels[task_id] = status_lbl

            prompt_lbl = tk.Label(
                card,
                text="",
                font=("Segoe UI", 8),
                bg=card["bg"],
                fg="#60a5fa",
            )
            prompt_lbl.pack(side=tk.LEFT, padx=8)
            self.channel_prompt_labels[task_id] = prompt_lbl

            reply_lbl = tk.Label(
                card,
                text="",
                font=("Segoe UI", 8),
                bg=card["bg"],
                fg="#cbd5e1",
                anchor="e",
            )
            reply_lbl.pack(side=tk.RIGHT, padx=6)
            self.channel_reply_labels[task_id] = reply_lbl

    def _on_start_keepalive(self):
        keys = self._get_keys()
        if not keys:
            messagebox.showwarning("提示", "请在上方输入框填入至少一个有效的 API Key（一行一个）！")
            return

        selected_models = [m for m, var in self.model_check_vars.items() if var.get()]
        if not selected_models:
            messagebox.showwarning("提示", "请在【模型选择】标签页至少勾选一个需要挂机保活的目标模型！")
            return

        try:
            tries = int(self.tries_entry.get().strip() or 5)
            intra_delay = int(self.intra_delay_entry.get().strip() or 5)
            cooldown_sec = int(self.cooldown_entry.get().strip() or 30)
            check_min = int(self.check_min_entry.get().strip() or 30)
            max_retries = int(self.max_retries_entry.get().strip() or 3)
        except ValueError:
            messagebox.showerror("错误", "参数数值必须为有效正整数！")
            return

        prompts = self._get_prompts()
        random_hb = self.random_prompt_var.get()
        proxy = self.proxy_entry.get().strip() or DEFAULT_PROXY

        task_specs = []
        for k_idx, key in enumerate(keys, start=1):
            k_label = format_key_tag(k_idx, key)
            for m_id in selected_models:
                m_meta = self.model_metadata.get(m_id, {})
                task_id = f"{k_label}::{m_id}"
                task_specs.append({
                    "task_id": task_id,
                    "key": key,
                    "key_label": k_label,
                    "model_id": m_id,
                    "model_type": m_meta.get("type", ""),
                })

        total_tasks = len(task_specs)

        self._render_monitor_channels(task_specs)
        self.notebook.select(self.tab_monitor)
        rand_desc = f"启用 {len(prompts)} 题随机轮换" if random_hb else "使用固定首题"
        self.monitor_summary_lbl.configure(
            text=f"🟢 挂机运行中: {len(keys)} 个 Key × {len(selected_models)} 个模型 = {total_tasks} 个通道并发守护中 | 题库策略: {rand_desc}"
        )

        self.is_running = True
        self.start_btn.configure(state=tk.DISABLED, bg="#4b5563")
        self.stop_btn.configure(state=tk.NORMAL, bg=self.COLOR_DANGER)

        self._append_log("==================================================================", "info")
        self._append_log(f"🚀 开始多 Key 并发挂机保活！总通道数: {total_tasks} (Key: {len(keys)}, 模型: {len(selected_models)})", "success")
        self._append_log(f"⚙ 策略: 阶段一(用不了: {cooldown_sec}s一轮/{tries}次) -> 阶段二(能用: {check_min}分钟测活，能用就不管)", "info")
        self._append_log(f"🎲 测活题库: 共 {len(prompts)} 道题 ({rand_desc})", "info")
        self._append_log("==================================================================", "info")

        for spec in task_specs:
            task_id = spec["task_id"]
            worker = ModelKeeperWorker(
                key=spec["key"],
                key_label=spec["key_label"],
                model_id=spec["model_id"],
                model_type=spec["model_type"],
                proxy=proxy,
                check_interval_min=check_min,
                round_cooldown_sec=cooldown_sec,
                tries_per_round=tries,
                intra_round_delay_sec=intra_delay,
                max_retries=max_retries,
                heartbeat_prompts=prompts,
                random_heartbeat=random_hb,
                log_cb=self._append_log,
                status_cb=self._update_channel_status,
                notify_cb=self.show_toast,
            )
            self.workers[task_id] = worker
            worker.start()

    def _on_stop_keepalive(self):
        self._append_log("⏹ 收到停止指令，正在安全停止全部并发保活线程...", "warn")
        self.stop_btn.configure(state=tk.DISABLED)

        def _stop_thread():
            for worker in self.workers.values():
                worker.stop()
            self.workers.clear()
            self.is_running = False

            def _ui_reset():
                self.start_btn.configure(state=tk.NORMAL, bg=self.COLOR_SUCCESS)
                self.stop_btn.configure(state=tk.DISABLED, bg="#4b5563")
                self.monitor_summary_lbl.configure(text="全部并发通道已停止。可重新在配置页调节后再次启动。")
                self._append_log("全部挂机任务已安全停止。", "info")

            self.root.after(0, _ui_reset)

        threading.Thread(target=_stop_thread, daemon=True).start()


def main():
    root = tk.Tk()
    app = KeeperApp(root)

    # 捕获窗口关闭事件
    root.protocol("WM_DELETE_WINDOW", app.on_window_close_clicked)
    root.mainloop()


if __name__ == "__main__":
    main()
