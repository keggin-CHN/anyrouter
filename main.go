package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"anyrouter/pkg/keeper"
	"anyrouter/pkg/web"

	"github.com/jchv/go-webview2"
)

const AppWindowTitle = "AnyRouter 多Key并发自动挂机保活矩阵 v2.6 (Go 原生桌面版)"

var (
	k32 = syscall.NewLazyDLL("kernel32.dll")
	u32 = syscall.NewLazyDLL("user32.dll")
	s32 = syscall.NewLazyDLL("shell32.dll")

	pGetModuleHandle     = k32.NewProc("GetModuleHandleW")
	pCreateMutexW        = k32.NewProc("CreateMutexW")
	pCloseHandle         = k32.NewProc("CloseHandle")
	pSetWindowLongPtr    = u32.NewProc("SetWindowLongPtrW")
	pCallWindowProc      = u32.NewProc("CallWindowProcW")
	pShowWindow          = u32.NewProc("ShowWindow")
	pSetForegroundWindow = u32.NewProc("SetForegroundWindow")
	pFindWindowW         = u32.NewProc("FindWindowW")
	pSendMessage         = u32.NewProc("SendMessageW")
	pCreatePopupMenu     = u32.NewProc("CreatePopupMenu")
	pAppendMenu          = u32.NewProc("AppendMenuW")
	pTrackPopupMenu      = u32.NewProc("TrackPopupMenu")
	pDestroyMenu         = u32.NewProc("DestroyMenu")
	pGetCursorPos        = u32.NewProc("GetCursorPos")
	pLoadIcon            = u32.NewProc("LoadIconW")
	pShellNotifyIcon     = s32.NewProc("Shell_NotifyIconW")

	oldWndProc uintptr
	reallyExit bool
	nidGlobal  notifyIconData
)

const (
	WM_CLOSE         = 0x0010
	WM_SETICON       = 0x0080
	WM_USER          = 0x0400
	WM_TRAYICON      = WM_USER + 100
	WM_LBUTTONUP     = 0x0202
	WM_LBUTTONDBLCLK = 0x0203
	WM_RBUTTONUP     = 0x0205

	NIM_ADD    = 0x00000000
	NIM_MODIFY = 0x00000001
	NIM_DELETE = 0x00000002

	NIF_MESSAGE = 0x00000001
	NIF_ICON    = 0x00000002
	NIF_TIP     = 0x00000004

	SW_HIDE    = 0
	SW_SHOW    = 5
	SW_RESTORE = 9

	ICON_SMALL = 0
	ICON_BIG   = 1

	MF_STRING     = 0x00000000
	MF_SEPARATOR  = 0x00000800
	TPM_RETURNCMD = 0x0100

	IDI_APPLICATION = 32512
)

type point struct {
	X, Y int32
}

type notifyIconData struct {
	cbSize            uint32
	_                 uint32
	hWnd              uintptr
	uID               uint32
	uFlags            uint32
	uCallbackMessage  uint32
	_                 uint32
	hIcon             uintptr
	szTip             [128]uint16
	dwState           uint32
	dwStateMask       uint32
	szInfo            [256]uint16
	uTimeoutOrVersion uint32
	szInfoTitle       [64]uint16
	dwInfoFlags       uint32
}

func showContextMenu(hwnd uintptr, matrix *keeper.Matrix, w webview2.WebView) {
	hMenu, _, _ := pCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer pDestroyMenu.Call(hMenu)

	titleOpen, _ := syscall.UTF16PtrFromString("🖥 打开主控制面板")
	titleExit, _ := syscall.UTF16PtrFromString("🛑 彻底退出程序")

	pAppendMenu.Call(hMenu, uintptr(MF_STRING), 1, uintptr(unsafe.Pointer(titleOpen)))
	pAppendMenu.Call(hMenu, uintptr(MF_SEPARATOR), 0, 0)
	pAppendMenu.Call(hMenu, uintptr(MF_STRING), 2, uintptr(unsafe.Pointer(titleExit)))

	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(hwnd)

	cmd, _, _ := pTrackPopupMenu.Call(hMenu, uintptr(TPM_RETURNCMD), uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	switch cmd {
	case 1:
		pShowWindow.Call(hwnd, uintptr(SW_RESTORE))
		pSetForegroundWindow.Call(hwnd)
	case 2:
		reallyExit = true
		pShellNotifyIcon.Call(uintptr(NIM_DELETE), uintptr(unsafe.Pointer(&nidGlobal)))
		w.Dispatch(func() {
			w.Terminate()
		})
	}
}

var globalMatrix *keeper.Matrix
var globalWebView webview2.WebView

func customWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_TRAYICON:
		switch lParam {
		case WM_LBUTTONUP, WM_LBUTTONDBLCLK:
			pShowWindow.Call(hwnd, uintptr(SW_RESTORE))
			pSetForegroundWindow.Call(hwnd)
		case WM_RBUTTONUP:
			if globalMatrix != nil && globalWebView != nil {
				showContextMenu(hwnd, globalMatrix, globalWebView)
			}
		}
		return 0
	case WM_CLOSE:
		if !reallyExit {
			// Window close button clicked -> silently hide to background instead of exiting!
			// No intrusive popups or beeps as requested by user.
			pShowWindow.Call(hwnd, uintptr(SW_HIDE))
			return 0
		}
	}
	r, _, _ := pCallWindowProc.Call(oldWndProc, hwnd, uintptr(msg), wParam, lParam)
	return r
}

// attachParentConsole attaches to the parent console if available.
func attachParentConsole() {
	if runtime.GOOS == "windows" {
		mod := syscall.NewLazyDLL("kernel32.dll")
		proc := mod.NewProc("AttachConsole")
		r1, _, _ := proc.Call(uintptr(0xFFFFFFFF))
		if r1 != 0 {
			stdout, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0644)
			if err == nil {
				os.Stdout = stdout
				os.Stderr = stdout
			}
		}
	}
}

// openAppWindow fallback if WebView2 native window fails to initialize.
func openAppWindow(url string) {
	if runtime.GOOS == "windows" {
		edgePaths := []string{
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		}
		for _, p := range edgePaths {
			if _, err := os.Stat(p); err == nil {
				cmd := exec.Command(p,
					fmt.Sprintf("--app=%s", url),
					"--window-size=1120,840",
					"--no-first-run",
					"--no-default-browser-check",
				)
				if err := cmd.Start(); err == nil {
					return
				}
			}
		}
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
		return
	}
	if runtime.GOOS == "darwin" {
		_ = exec.Command("open", url).Start()
		return
	}
	_ = exec.Command("xdg-open", url).Start()
}

func main() {
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "-") {
			attachParentConsole()
			break
		}
	}

	configPathFlag := flag.String("config", "keeper_config.json", "配置文件路径")
	portFlag := flag.Int("port", 28888, "Web 监控面板端口 (默认 28888)")
	cliModeFlag := flag.Bool("cli", false, "纯命令行无头模式")
	noWebFlag := flag.Bool("no-web", false, "关闭本地 Web 服务")
	autoStartFlag := flag.Bool("auto-start", true, "程序启动后是否立即开启保活")
	flag.Parse()

	// Single Instance Guard (防止重复启动产生多个窗口或 WebView2 端口冲突死锁)
	if runtime.GOOS == "windows" && !*cliModeFlag {
		mutexName, _ := syscall.UTF16PtrFromString("Local\\AnyRouterKeeperSingleInstanceMutex_v2")
		hMutex, _, mutexErr := pCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(mutexName)))
		// Proc.Call captures GetLastError on the calling thread.
		isAlreadyRunning := hMutex != 0 && mutexErr == syscall.Errno(183)

		// 额外兜底探测：如果本地 28888 端口已被占用且响应 status，说明已有实例在运行
		if !isAlreadyRunning {
			testClient := &http.Client{Timeout: 300 * time.Millisecond}
			if resp, err := testClient.Get(fmt.Sprintf("http://127.0.0.1:%d/api/status", *portFlag)); err == nil && resp != nil {
				_ = resp.Body.Close()
				if resp.StatusCode == 200 {
					isAlreadyRunning = true
				}
			}
		}

		if isAlreadyRunning {
			// 已有实例在后台运行：通过 REST API 与 Win32 唤醒并置顶窗口
			client := &http.Client{Timeout: 600 * time.Millisecond}
			if resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/api/show", *portFlag), "application/json", nil); err == nil {
				_ = resp.Body.Close()
			}

			titlePtr, _ := syscall.UTF16PtrFromString(AppWindowTitle)
			existingHwnd, _, _ := pFindWindowW.Call(0, uintptr(unsafe.Pointer(titlePtr)))
			if existingHwnd != 0 {
				pShowWindow.Call(existingHwnd, uintptr(SW_RESTORE))
				pSetForegroundWindow.Call(existingHwnd)
			}
			if hMutex != 0 {
				pCloseHandle.Call(hMutex)
			}
			return
		}

		if hMutex != 0 {
			defer pCloseHandle.Call(hMutex)
		}
	}

	if *cliModeFlag {
		attachParentConsole()
	}

	// Locate config
	configPath := *configPathFlag
	if !filepath.IsAbs(configPath) {
		execDir, err := os.Executable()
		if err == nil {
			potential := filepath.Join(filepath.Dir(execDir), configPath)
			if _, err := os.Stat(potential); err == nil {
				configPath = potential
			}
		}
	}

	cfg, err := keeper.LoadConfig(configPath)
	if err != nil {
		fmt.Printf("⚠️ 加载配置文件失败，将使用默认配置: %v\n", err)
		cfg = keeper.DefaultConfig()
	}

	matrix := keeper.NewMatrix(configPath, cfg)
	globalMatrix = matrix

	fmt.Printf("===============================================================\n")
	fmt.Printf("   🚀 AnyRouter Keeper (Go 原生桌面版 v2.6)\n")
	fmt.Printf("===============================================================\n")
	fmt.Printf(" 配置文件: %s\n", configPath)
	fmt.Printf(" 凭据池  : %d 个 API Key (%s 等)\n", len(cfg.APIKeys), keeper.MaskKey(cfg.APIKey))
	fmt.Printf(" 目标模型: %v\n", cfg.SelectedModels)
	fmt.Printf(" 本地代理: %s\n", cfg.Proxy)
	fmt.Printf(" 保活策略: 阶段一 (%ds一轮/%d次) -> 阶段二 (%d分钟巡航测活)\n",
		cfg.RoundCooldownSec, cfg.TriesPerRound, cfg.CheckIntervalMin)
	fmt.Printf("===============================================================\n")

	// Start Web dashboard & API backend
	var webServer *web.Server
	webURL := fmt.Sprintf("http://127.0.0.1:%d", *portFlag)
	if !*noWebFlag {
		webServer = web.NewServer(*portFlag, matrix)
		if err := webServer.Start(); err != nil {
			fmt.Printf("⚠️ Web 控制台启动失败: %v\n", err)
			return
		} else {
			webURL = webServer.Addr()
			fmt.Printf(" 🌐 控制面板服务已就绪: %s\n", webURL)
		}
	}

	// Auto-start keeper matrix
	if *autoStartFlag {
		if err := matrix.Start(); err != nil {
			fmt.Printf("❌ 启动保活矩阵失败: %v\n", err)
		}
	}

	// Real-time log listener for console / stdout
	logChan := matrix.SubscribeLogs()
	go func() {
		for entry := range logChan {
			timeStr := entry.Timestamp.Format("15:04:05")
			icon := "ℹ️ "
			switch entry.Level {
			case "warn":
				icon = "⚠️ "
			case "success":
				icon = "🎉 "
			case "queue":
				icon = "🔄 "
			case "heartbeat":
				icon = "💓 "
			}
			fmt.Printf("[%s] %s%s\n", timeStr, icon, entry.Text)
		}
	}()

	// Signal handling for graceful exit
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	// CLI and browser fallback modes also need a graceful exit endpoint.
	if webServer != nil {
		webServer.SetWindowControls(nil, nil, func() {
			select {
			case sigChan <- os.Interrupt:
			default:
			}
		})
	}

	if *cliModeFlag || *noWebFlag {
		fmt.Printf("\n[提示] 命令行服务模式已启动，按 Ctrl+C 可安全退出...\n\n")
		<-sigChan
		shutdown(matrix, webServer, logChan)
		return
	}

	// Native Windows Desktop GUI Mode (WebView2)
	opts := webview2.WebViewOptions{
		Debug: false,
		WindowOptions: webview2.WindowOptions{
			Title:  AppWindowTitle,
			Width:  1120,
			Height: 840,
			Center: true,
		},
	}

	var w webview2.WebView
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("⚠️ 初始化原生桌面窗口失败 (将降级为系统浏览器打开): %v\n", r)
				w = nil
			}
		}()
		w = webview2.NewWithOptions(opts)
	}()

	if w != nil {
		globalWebView = w
		defer w.Destroy()
		w.Navigate(webURL)

		hwnd := uintptr(w.Window())

		// Subclass window to intercept 'X' close button -> hide to silent background state
		procCallback := syscall.NewCallback(customWndProc)
		r, _, _ := pSetWindowLongPtr.Call(hwnd, ^uintptr(3), procCallback)
		oldWndProc = r

		// Setup System Tray Icon in taskbar notification area with embedded app icon
		hInst, _, _ := pGetModuleHandle.Call(0)
		hAppIcon, _, _ := pLoadIcon.Call(hInst, uintptr(1))
		if hAppIcon == 0 {
			hAppIcon, _, _ = pLoadIcon.Call(0, uintptr(IDI_APPLICATION))
		}

		// Set window titlebar and taskbar icons
		pSendMessage.Call(hwnd, uintptr(WM_SETICON), uintptr(ICON_BIG), hAppIcon)
		pSendMessage.Call(hwnd, uintptr(WM_SETICON), uintptr(ICON_SMALL), hAppIcon)

		nidGlobal = notifyIconData{
			cbSize:           uint32(unsafe.Sizeof(nidGlobal)),
			hWnd:             hwnd,
			uID:              1001,
			uFlags:           NIF_MESSAGE | NIF_ICON | NIF_TIP,
			uCallbackMessage: WM_TRAYICON,
			hIcon:            hAppIcon,
		}
		tipStr, _ := syscall.UTF16FromString("AnyRouter Keeper (静默挂机保活中)")
		copy(nidGlobal.szTip[:], tipStr)
		pShellNotifyIcon.Call(uintptr(NIM_ADD), uintptr(unsafe.Pointer(&nidGlobal)))

		// Link Web server window control endpoints (/api/hide, /api/show, /api/exit)
		if webServer != nil {
			webServer.SetWindowControls(func() {
				// onHide: hide window to background tray
				w.Dispatch(func() { pShowWindow.Call(hwnd, uintptr(SW_HIDE)) })
			}, func() {
				// onShow: restore window from background tray to foreground
				w.Dispatch(func() {
					pShowWindow.Call(hwnd, uintptr(SW_RESTORE))
					pSetForegroundWindow.Call(hwnd)
				})
			}, func() {
				// onExit: completely exit
				w.Dispatch(func() {
					reallyExit = true
					pShellNotifyIcon.Call(uintptr(NIM_DELETE), uintptr(unsafe.Pointer(&nidGlobal)))
					w.Terminate()
				})
			})
		}

		// Terminate window if Ctrl+C or SIGTERM received
		go func() {
			<-sigChan
			w.Dispatch(func() {
				reallyExit = true
				pShellNotifyIcon.Call(uintptr(NIM_DELETE), uintptr(unsafe.Pointer(&nidGlobal)))
				w.Terminate()
			})
		}()

		// Run native Win32 message loop (blocks until reallyExit)
		w.Run()

		pShellNotifyIcon.Call(uintptr(NIM_DELETE), uintptr(unsafe.Pointer(&nidGlobal)))
		shutdown(matrix, webServer, logChan)
	} else {
		// Fallback to browser
		openAppWindow(webURL)
		fmt.Printf("\n[提示] 监控面板已在浏览器中打开，按 Ctrl+C 可安全退出...\n\n")
		<-sigChan
		shutdown(matrix, webServer, logChan)
	}
}

func shutdown(matrix *keeper.Matrix, webServer *web.Server, logChan chan keeper.LogEntry) {
	fmt.Printf("\n🛑 正在平滑安全停止所有保活通道...\n")
	if webServer != nil {
		webServer.Stop()
	}
	matrix.Stop()
	matrix.UnsubscribeLogs(logChan)
	fmt.Printf("✅ 程序已安全退出。\n")
}
