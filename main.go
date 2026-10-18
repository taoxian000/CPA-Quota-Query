//go:build windows

package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

const (
	defaultServerURL      = "https://kot.xkcht.eu.org"
	quotaRoutePath        = "/v0/resource/plugins/user-routing/quota"
	authRelPath           = ".codex\\auth.json"
	authModeAPIKey        = "apikey"
	authModeChatGPT       = "chatgpt"
	quotaRequestRetries   = 5
	maxQuotaResponseBytes = 4 << 20

	classMain  = "QuotaTrayMonitorMainWindow"
	classPanel = "QuotaTrayMonitorPanelWindow"
	windowName = "Codex额度监控"

	wmApp              = 0x8000
	wmTray             = wmApp + 10
	wmStateChanged     = wmApp + 11
	wmResetCompleted   = wmApp + 12
	wmShowAlert        = wmApp + 13
	wmShowNotice       = wmApp + 14
	wmShowPanelRequest = wmApp + 15
	wmActivate         = 0x0006
	wmPaint            = 0x000F
	wmClose            = 0x0010
	wmEraseBackground  = 0x0014
	wmSettingChange    = 0x001A
	wmThemeChanged     = 0x031A
	wmCommand          = 0x0111
	wmTimer            = 0x0113
	wmLButtonDown      = 0x0201
	wmExitSizeMove     = 0x0232
	wmMoving           = 0x0216
	vkMenu             = 0x12 // Alt
	wmNCLButtonDown    = 0x00A1
	wmNull             = 0x0000
	wmLButtonUp        = 0x0202
	wmRButtonUp        = 0x0205
	wmContextMenu      = 0x007B
	waInactive         = 0
	htCaption          = 2

	wsPopup          = 0x80000000
	wsExToolWindow   = 0x00000080
	wsExTopmost      = 0x00000008
	swHide           = 0
	swShow           = 5
	swpNoMove        = 0x0002
	swpNoSize        = 0x0001
	swpNoZOrder      = 0x0004
	swpNoActivate    = 0x0010
	swpShowWindow    = 0x0040
	tpmRightButton   = 0x0002
	tpmReturnCommand = 0x0100

	nimAdd            = 0
	nimModify         = 1
	nimDelete         = 2
	nifMessage        = 1
	nifIcon           = 2
	nifTip            = 4
	nifInfo           = 0x10
	niifInfo          = 1
	iconID            = 1
	menuRefresh       = 1001
	menuOpenPanel     = 1002
	menuTopmost       = 1003
	menuStartup       = 1004
	menuExit          = 1005
	menuRefresh10     = 1010
	menuRefresh20     = 1011
	menuRefresh30     = 1012
	menuRefresh5      = 1013
	menuCheckUpdates  = 1014
	menuInstallUpdate = 1015
	menuRefresh15     = 1016
	menuProviderBase  = 1020 // one item per entry of providerOrder

	mbOK              = 0x00000000
	mbYesNo           = 0x00000004
	mbIconError       = 0x00000010
	mbIconInformation = 0x00000040
	mbIconWarning     = 0x00000030
	mbDefaultButton2  = 0x00000100
	mbSetForeground   = 0x00010000
	mbTopmost         = 0x00040000
	idYes             = 6

	dibRGBColors        = 0
	biRGB               = 0
	monDefaultToNearest = 2
	smCxSmIcon          = 49
	logPixelsX          = 88
	mmAnisotropic       = 8
	idcArrow            = 32512
	baseDPI             = 96
	dtSingleLine        = 0x0020
	dtVCenter           = 0x0004
	dtCenter            = 0x0001
	dtEndEllipsis       = 0x8000

	hwndTop       = 0
	hwndTopmost   = ^uintptr(0)
	hwndNotopmost = ^uintptr(1)

	// DPI_AWARENESS_CONTEXT_SYSTEM_AWARE is the pseudo-handle -2.
	dpiAwarenessContextSystemAware = ^uintptr(1)
	asfwAny                        = uintptr(^uint32(0))

	hkeyCurrentUser    = 0x80000001
	keySetValue        = 0x0002
	keyQueryValue      = 0x0001
	regSZ              = 1
	regDword           = 4
	errorFileNotFound  = 2
	errorAlreadyExists = 183
	errorCancelled     = 1223
	panelTimerID       = 1
	panelTimerInterval = 60_000
	trayRetryTimerID   = 2
	trayRetryInterval  = 3_000

	// A tray click that lands within this window after the panel hid itself on
	// deactivation is the click that caused the deactivation, not a new open.
	trayToggleGrace = 500 * time.Millisecond
)

const (
	themeSystem = "system"
	themeLight  = "light"
	themeDark   = "dark"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")

	procRegisterClassEx        = user32.NewProc("RegisterClassExW")
	procCreateWindowEx         = user32.NewProc("CreateWindowExW")
	procDefWindowProc          = user32.NewProc("DefWindowProcW")
	procMessageBox             = user32.NewProc("MessageBoxW")
	procGetMessage             = user32.NewProc("GetMessageW")
	procTranslateMessage       = user32.NewProc("TranslateMessage")
	procDispatchMessage        = user32.NewProc("DispatchMessageW")
	procPostMessage            = user32.NewProc("PostMessageW")
	procPostQuitMessage        = user32.NewProc("PostQuitMessage")
	procShowWindow             = user32.NewProc("ShowWindow")
	procSetTimer               = user32.NewProc("SetTimer")
	procKillTimer              = user32.NewProc("KillTimer")
	procSetWindowPos           = user32.NewProc("SetWindowPos")
	procSendMessage            = user32.NewProc("SendMessageW")
	procReleaseCapture         = user32.NewProc("ReleaseCapture")
	procGetAsyncKeyState       = user32.NewProc("GetAsyncKeyState")
	procGetWindowRect          = user32.NewProc("GetWindowRect")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procGetCursorPos           = user32.NewProc("GetCursorPos")
	procMonitorFromPoint       = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfo         = user32.NewProc("GetMonitorInfoW")
	procBeginPaint             = user32.NewProc("BeginPaint")
	procEndPaint               = user32.NewProc("EndPaint")
	procDrawText               = user32.NewProc("DrawTextW")
	procSetTextColor           = gdi32.NewProc("SetTextColor")
	procSetBkMode              = gdi32.NewProc("SetBkMode")
	procCreateFont             = gdi32.NewProc("CreateFontW")
	procCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	procCreatePen              = gdi32.NewProc("CreatePen")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procFillRect               = user32.NewProc("FillRect")
	procRoundRect              = gdi32.NewProc("RoundRect")
	procMoveToEx               = gdi32.NewProc("MoveToEx")
	procLineTo                 = gdi32.NewProc("LineTo")
	procShellNotifyIcon        = shell32.NewProc("Shell_NotifyIconW")
	procShellNotifyIconGetRect = shell32.NewProc("Shell_NotifyIconGetRect")
	procRegisterWindowMessage  = user32.NewProc("RegisterWindowMessageW")
	procGetModuleHandle        = kernel32.NewProc("GetModuleHandleW")
	procCreateMutex            = kernel32.NewProc("CreateMutexW")
	procCloseHandle            = kernel32.NewProc("CloseHandle")
	procCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap           = gdi32.NewProc("CreateBitmap")
	procCreateIconIndirect     = user32.NewProc("CreateIconIndirect")
	procDestroyIcon            = user32.NewProc("DestroyIcon")
	procGetDC                  = user32.NewProc("GetDC")
	procReleaseDC              = user32.NewProc("ReleaseDC")
	procCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	procAppendMenu             = user32.NewProc("AppendMenuW")
	procCheckMenuItem          = user32.NewProc("CheckMenuItem")
	procTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	procDestroyMenu            = user32.NewProc("DestroyMenu")
	procRegCreateKeyEx         = advapi32.NewProc("RegCreateKeyExW")
	procRegOpenKeyEx           = advapi32.NewProc("RegOpenKeyExW")
	procRegSetValueEx          = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValue         = advapi32.NewProc("RegDeleteValueW")
	procRegQueryValueEx        = advapi32.NewProc("RegQueryValueExW")
	procRegCloseKey            = advapi32.NewProc("RegCloseKey")
	procInvalidateRect         = user32.NewProc("InvalidateRect")
	procLoadCursor             = user32.NewProc("LoadCursorW")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procFindWindow             = user32.NewProc("FindWindowW")
	procAllowSetForeground     = user32.NewProc("AllowSetForegroundWindow")
	procSetProcessDPIAware     = user32.NewProc("SetProcessDPIAware")
	procSetProcessDpiAwareness = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetDeviceCaps          = gdi32.NewProc("GetDeviceCaps")
	procSetMapMode             = gdi32.NewProc("SetMapMode")
	procSetWindowExtEx         = gdi32.NewProc("SetWindowExtEx")
	procSetViewportExtEx       = gdi32.NewProc("SetViewportExtEx")
)

type point struct {
	X int32
	Y int32
}

type rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type paintStruct struct {
	HDC       syscall.Handle
	Erase     int32
	Paint     rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   syscall.Handle
	Icon       syscall.Handle
	Cursor     syscall.Handle
	Background syscall.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  syscall.Handle
}

type notifyIconData struct {
	Size             uint32
	Window           syscall.Handle
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             syscall.Handle
	Tooltip          [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUID             [16]byte
	BalloonIcon      syscall.Handle
}

type notifyIconIdentifier struct {
	Size   uint32
	Window syscall.Handle
	ID     uint32
	GUID   [16]byte
}

type bitmapInfoHeader struct {
	Size            uint32
	Width           int32
	Height          int32
	Planes          uint16
	BitCount        uint16
	Compression     uint32
	SizeImage       uint32
	XPixelsPerM     int32
	YPixelsPerM     int32
	ColorsUsed      uint32
	ColorsImportant uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Color  [1]uint32
}

type iconInfo struct {
	Icon     int32
	HotspotX uint32
	HotspotY uint32
	Mask     syscall.Handle
	Color    syscall.Handle
}

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

type quotaBucket struct {
	Percent int
	Known   bool
	ResetAt time.Time
}

type accountQuota struct {
	Email     string
	Primary   quotaBucket
	Secondary quotaBucket
	Reset     resetCreditInfo
}

type resetCreditInfo struct {
	AvailableCount         int      `json:"available_count"`
	ExpiresAt              []string `json:"expires_at"`
	WithoutExpiry          int      `json:"without_expiry"`
	ExpiryDetailsAvailable bool     `json:"expiry_details_available"`
	ExpiryDetailsComplete  bool     `json:"expiry_details_complete"`
}

// quotaSnapshot holds every account returned by the server, in response order.
type quotaSnapshot struct {
	Nominal        []accountQuota
	Actual         []accountQuota
	DirectMode     bool
	ResetSupported bool
}

func (q quotaSnapshot) singleAccount() bool {
	return len(q.Nominal)+len(q.Actual) == 1
}

type appSettings struct {
	AlwaysOnTop    bool
	PositionSet    bool
	PanelX         int32
	PanelY         int32
	RefreshMinutes int
	ThemeMode      string
	// ServerURL is the CPA base URL (scheme + host, optional path prefix).
	// Empty means defaultServerURL.
	ServerURL string `json:",omitempty"`
	// Providers holds the per-provider switch and dialog values, keyed by the
	// lowercase provider id. A missing Codex entry means enabled.
	Providers map[string]providerConfig `json:",omitempty"`
}

type alertMessage struct {
	text  string
	flags uintptr
}

type appState struct {
	mu              sync.RWMutex
	quota           quotaSnapshot
	codexOK         bool                // the latest Codex sync succeeded
	ext             map[string]extQuota // Antigravity / Claude, by provider id
	status          string
	updated         time.Time
	settings        appSettings
	startup         bool
	resetResult     string
	updateStatus    string
	updateAvailable bool
	updateRelease   updateRelease
	fetchFailing    bool
	alerts          []alertMessage
	notices         []quotaRecoveryNotice
}

type monitorApp struct {
	state             appState
	mainWindow        syscall.Handle
	panelWindow       syscall.Handle
	instance          syscall.Handle
	mutex             syscall.Handle
	dpi               int32
	iconSize          int
	trayAdded         bool
	panelVisible      bool
	trayIcon          syscall.Handle
	taskbarMsg        uint32
	fetching          atomic.Bool
	manualRefresh     atomic.Bool
	resetPending      atomic.Bool
	checkingUpdate    atomic.Bool
	manualUpdateCheck atomic.Bool
	installingUpdate  atomic.Bool
	intervalChange    chan time.Duration
	quotaChanged      chan struct{}
	panelButton       string
	menuOpen          bool
	modalDepth        int
	alertOpen         bool
	panelAutoHiddenAt time.Time
	trayPressed       bool
	trayPressHides    bool
	closing           bool
	dialog            *providerDialog
}

var currentApp *monitorApp

func main() {
	if err := run(); err != nil {
		showError(err.Error())
	}
}

func run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	enableDPIAwareness()

	mutex, _, createErr := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(utf16Ptr("Local\\CodexQuotaTrayMonitor"))))
	if mutex == 0 {
		return errors.New("无法创建单实例互斥锁")
	}
	if createErr == syscall.Errno(errorAlreadyExists) {
		procCloseHandle.Call(mutex)
		showExistingInstance()
		return nil
	}

	app := &monitorApp{
		mutex:          syscall.Handle(mutex),
		dpi:            systemDPI(),
		iconSize:       trayIconSize(),
		intervalChange: make(chan time.Duration, 1),
		quotaChanged:   make(chan struct{}, 1),
	}
	currentApp = app
	app.state.settings = loadSettings()
	app.state.startup = startupEnabled()
	app.state.status = "等待首次同步"
	updateFailure := ""
	if executable, err := os.Executable(); err == nil {
		updateFailure = consumeUpdateFailure(executable)
	}

	instance, _, _ := procGetModuleHandle.Call(0)
	if instance == 0 {
		return winError("GetModuleHandleW")
	}
	app.instance = syscall.Handle(instance)

	mainClass := utf16Ptr(classMain)
	panelClass := utf16Ptr(classPanel)
	callback := syscall.NewCallback(windowProc)
	cursor, _, _ := procLoadCursor.Call(0, idcArrow)
	if err := registerWindowClass(mainClass, app.instance, callback, syscall.Handle(cursor)); err != nil {
		return err
	}
	if err := registerWindowClass(panelClass, app.instance, callback, syscall.Handle(cursor)); err != nil {
		return err
	}
	if err := registerDialogClass(app.instance, callback, syscall.Handle(cursor)); err != nil {
		return err
	}

	initial := app.snapshot()
	title := utf16Ptr(windowName)
	hwnd, _, _ := procCreateWindowEx.Call(
		wsExToolWindow,
		uintptr(unsafe.Pointer(mainClass)),
		uintptr(unsafe.Pointer(title)),
		wsPopup,
		0, 0, 0, 0,
		0, 0,
		uintptr(app.instance),
		0,
	)
	if hwnd == 0 {
		return winError("CreateWindowExW (tray)")
	}
	app.mainWindow = syscall.Handle(hwnd)

	panelEx := uintptr(wsExToolWindow)
	if app.state.settings.AlwaysOnTop {
		panelEx |= wsExTopmost
	}
	panel, _, _ := procCreateWindowEx.Call(
		panelEx,
		uintptr(unsafe.Pointer(panelClass)),
		uintptr(unsafe.Pointer(title)),
		wsPopup,
		0, 0, uintptr(app.scale(panelWidth)), uintptr(app.scale(panelHeightForState(&initial))),
		hwnd, 0,
		uintptr(app.instance),
		0,
	)
	if panel == 0 {
		return winError("CreateWindowExW (panel)")
	}
	app.panelWindow = syscall.Handle(panel)

	taskbarMsg, _, _ := procRegisterWindowMessage.Call(uintptr(unsafe.Pointer(utf16Ptr("TaskbarCreated"))))
	app.taskbarMsg = uint32(taskbarMsg)

	if err := app.addTrayIcon(); err != nil {
		// At logon Explorer's notification area may not be ready yet. Keep
		// running (the panel is shown below) and retry until it accepts the icon.
		app.scheduleTrayRetry()
	}
	if updateFailure != "" {
		app.queueAlert("上次更新未能完成。\n\n"+updateFailure, mbIconWarning)
	}
	app.showPanel()
	app.startFetch(false)
	go app.refreshLoop()
	go app.quotaExpiryLoop()
	go app.updateCheckLoop()
	return app.messageLoop()
}

func registerWindowClass(className *uint16, instance syscall.Handle, callback uintptr, cursor syscall.Handle) error {
	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:   callback,
		Instance:  instance,
		Cursor:    cursor,
		ClassName: className,
	}
	r, _, _ := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	if r == 0 {
		return winError("RegisterClassExW")
	}
	return nil
}

// enableDPIAwareness must run before any window is created. System-DPI
// awareness lets the tray icon be drawn at the shell's real small-icon size
// and keeps panel text sharp instead of being bitmap-stretched by Windows.
func enableDPIAwareness() {
	if procSetProcessDpiAwareness.Find() == nil {
		if r, _, _ := procSetProcessDpiAwareness.Call(dpiAwarenessContextSystemAware); r != 0 {
			return
		}
	}
	if procSetProcessDPIAware.Find() == nil {
		procSetProcessDPIAware.Call()
	}
}

func systemDPI() int32 {
	dc, _, _ := procGetDC.Call(0)
	if dc == 0 {
		return baseDPI
	}
	defer procReleaseDC.Call(0, dc)
	dpi, _, _ := procGetDeviceCaps.Call(dc, logPixelsX)
	if dpi == 0 {
		return baseDPI
	}
	return int32(dpi)
}

func trayIconSize() int {
	size, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	return min(max(int(size), 16), 128)
}

// showExistingInstance asks an already running instance to open its panel, so
// launching the program again behaves like opening it.
func showExistingInstance() {
	hwnd, _, _ := procFindWindow.Call(uintptr(unsafe.Pointer(utf16Ptr(classMain))), 0)
	if hwnd == 0 {
		return
	}
	procAllowSetForeground.Call(asfwAny)
	procPostMessage.Call(hwnd, wmShowPanelRequest, 0, 0)
}

func scaleForDPI(value, dpi int32) int32 {
	if dpi <= 0 {
		dpi = baseDPI
	}
	return int32((int64(value)*int64(dpi) + baseDPI/2) / baseDPI)
}

// scale converts a panel layout value (designed at 96 DPI) to device pixels.
func (a *monitorApp) scale(value int32) int32 {
	return scaleForDPI(value, a.dpi)
}

// logicalPoint converts a device-pixel client point to panel layout units.
func (a *monitorApp) logicalPoint(pt point) point {
	dpi := int64(a.dpi)
	if dpi <= 0 {
		dpi = baseDPI
	}
	return point{X: int32(int64(pt.X) * baseDPI / dpi), Y: int32(int64(pt.Y) * baseDPI / dpi)}
}

func (a *monitorApp) messageLoop() error {
	var msg struct {
		Window  syscall.Handle
		Message uint32
		WParam  uintptr
		LParam  uintptr
		Time    uint32
		Point   point
		Private uint32
	}
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) == -1 {
			return winError("GetMessageW")
		}
		if r == 0 {
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func windowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	a := currentApp
	if a == nil {
		r, _, _ := procDefWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
		return r
	}
	handle := syscall.Handle(hwnd)

	if a.dialog != nil && handle == a.dialog.hwnd {
		switch message {
		case wmCommand:
			a.handleDialogCommand(a.dialog, uint16(wParam))
			return 0
		case wmClose:
			a.handleDialogCommand(a.dialog, idDialogCancel)
			return 0
		}
	}

	if message == a.taskbarMsg && a.taskbarMsg != 0 && handle == a.mainWindow {
		a.trayAdded = false
		if a.addTrayIcon() != nil {
			a.scheduleTrayRetry()
		} else {
			a.drainNotices()
		}
		return 0
	}
	if handle == a.mainWindow {
		switch message {
		case wmTray:
			switch uint32(lParam) {
			case wmLButtonDown:
				a.trayPressed = true
				a.trayPressHides = a.trayClickHidesPanel()
				return 0
			case wmLButtonUp:
				hide := a.trayClickHidesPanel()
				if a.trayPressed {
					hide = a.trayPressHides
				}
				a.trayPressed = false
				if hide {
					a.hidePanel()
				} else {
					a.showPanel()
				}
				return 0
			case wmRButtonUp, wmContextMenu:
				a.showMenu()
				return 0
			}
		case wmStateChanged:
			a.updateTray()
			// Quota refreshes can add or remove account cards. Keep the hidden
			// window's geometry current too, then repaint immediately if it is
			// visible; showPanel also uses this latest snapshot.
			a.resizePanel()
			procInvalidateRect.Call(uintptr(a.panelWindow), 0, 1)
			return 0
		case wmResetCompleted:
			a.state.mu.Lock()
			result := a.state.resetResult
			a.state.resetResult = ""
			a.state.mu.Unlock()
			flags := uintptr(mbOK | mbIconInformation)
			if strings.Contains(result, "未能") || strings.Contains(result, "不确定") || strings.Contains(result, "失败") || strings.Contains(result, "未报告") {
				flags = mbOK | mbIconWarning
			}
			a.messageBox(a.dialogOwner(), result, flags)
			a.resetPending.Store(false)
			a.fetching.Store(false)
			a.startFetch(false)
			return 0
		case wmShowAlert:
			a.drainAlerts()
			return 0
		case wmShowNotice:
			a.drainNotices()
			return 0
		case wmShowPanelRequest:
			a.showPanel()
			procSetForegroundWindow.Call(uintptr(a.panelWindow))
			return 0
		case wmTimer:
			if wParam == trayRetryTimerID {
				if a.addTrayIcon() == nil {
					procKillTimer.Call(uintptr(a.mainWindow), trayRetryTimerID)
					a.drainNotices()
				}
				return 0
			}
		case wmCommand:
			a.handleMenuCommand(uint16(wParam))
			return 0
		case wmClose:
			a.shutdown()
			return 0
		}
	}
	if handle == a.panelWindow {
		switch message {
		case wmLButtonDown:
			pt := a.logicalPoint(pointFromLParam(lParam))
			a.panelButton = a.panelButtonAt(pt)
			if a.panelButton != "" {
				return 0
			}
			procReleaseCapture.Call()
			procSendMessage.Call(hwnd, wmNCLButtonDown, htCaption, 0)
			return 0
		case wmLButtonUp:
			button := a.panelButton
			a.panelButton = ""
			if button != "" && button == a.panelButtonAt(a.logicalPoint(pointFromLParam(lParam))) {
				a.handlePanelButton(button)
			}
			return 0
		case wmRButtonUp, wmContextMenu:
			a.showMenu()
			return 0
		case wmMoving:
			if lParam != 0 {
				a.snapMovingPanel(*(**rect)(unsafe.Pointer(&lParam)))
			}
			return 1
		case wmExitSizeMove:
			a.savePanelPosition()
			return 0
		case wmTimer:
			if wParam == panelTimerID {
				procInvalidateRect.Call(uintptr(a.panelWindow), 0, 1)
				return 0
			}
		case wmSettingChange, wmThemeChanged:
			procInvalidateRect.Call(uintptr(a.panelWindow), 0, 1)
			return 0
		case wmPaint:
			a.paintPanel(handle)
			return 0
		case wmEraseBackground:
			return 1
		case wmActivate:
			// Menus and message boxes owned by the panel take activation from
			// it; only a real switch to another window should auto-hide it.
			if uint16(wParam) == waInactive && !a.menuOpen && a.modalDepth == 0 && !a.snapshot().settings.AlwaysOnTop {
				a.hidePanel()
				a.panelAutoHiddenAt = time.Now()
				return 0
			}
		case wmClose:
			a.hidePanel()
			return 0
		}
	}
	r, _, _ := procDefWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
	return r
}

// startFetch refreshes the quota in the background. A failure pops up an alert
// when the user asked for the refresh, or when it is the first failure after a
// success; repeated automatic failures only update the status line.
func (a *monitorApp) startFetch(manual bool) {
	if manual {
		a.manualRefresh.Store(true)
	}
	if !a.fetching.CompareAndSwap(false, true) {
		return
	}
	a.state.mu.Lock()
	a.state.status = "正在刷新额度…"
	settings := a.state.settings
	a.state.mu.Unlock()
	procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)
	go func() {
		defer func() {
			a.fetching.Store(false)
			select {
			case a.quotaChanged <- struct{}{}:
			default:
			}
		}()
		// Every enabled provider is refreshed together, each on its own goroutine.
		type outcome struct {
			ran   bool
			codex quotaSnapshot
			ext   extQuota
			err   error
		}
		outcomes := make([]outcome, len(providerOrder))
		var wg sync.WaitGroup
		for i, id := range providerOrder {
			if !settings.providerEnabled(id) {
				continue
			}
			outcomes[i].ran = true
			wg.Add(1)
			go func() {
				defer wg.Done()
				if id == providerCodex {
					outcomes[i].codex, outcomes[i].err = fetchQuota(settings.ServerURL)
				} else {
					outcomes[i].ext, outcomes[i].err = fetchExtQuota(id, settings.providerConfigFor(id))
				}
			}()
		}
		wg.Wait()

		manual := a.manualRefresh.Swap(false)
		var failures, details []string
		ran := false
		a.state.mu.Lock()
		if a.state.ext == nil {
			a.state.ext = map[string]extQuota{}
		}
		for i, id := range providerOrder {
			result := outcomes[i]
			if !result.ran {
				continue
			}
			ran = true
			switch {
			case result.err != nil && id == providerCodex:
				a.state.codexOK = false
			case result.err != nil:
				previous := a.state.ext[id]
				previous.Failed, previous.Err = true, quotaFailureCause(result.err).Error()
				a.state.ext[id] = previous
			case id == providerCodex:
				a.state.quota, a.state.codexOK = result.codex, true
			default:
				a.state.ext[id] = result.ext
			}
			if result.err != nil {
				failures = append(failures, providerTitle(id)+"："+shortError(quotaFailureCause(result.err).Error()))
				details = append(details, providerTitle(id)+"：\n"+result.err.Error())
			}
		}
		alert := false
		switch {
		case !ran:
			a.state.status = "未启用任何 Provider"
			a.state.fetchFailing = false
		case len(failures) > 0:
			a.state.status = "同步失败：" + strings.Join(failures, "；")
			alert = manual || !a.state.fetchFailing
			a.state.fetchFailing = true
		default:
			a.state.updated = time.Now()
			a.state.status = "更新于 " + a.state.updated.Format("15:04:05")
			a.state.fetchFailing = false
		}
		a.state.mu.Unlock()
		procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)
		if alert {
			a.queueAlert("额度刷新失败。\n\n"+strings.Join(details, "\n\n"), mbIconWarning)
		}
	}()
}

func (a *monitorApp) refreshLoop() {
	interval := time.Duration(a.snapshot().settings.RefreshMinutes) * time.Minute
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			a.startFetch(false)
			timer.Reset(time.Duration(a.snapshot().settings.RefreshMinutes) * time.Minute)
		case interval = <-a.intervalChange:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(interval)
		}
	}
}

// quotaExpiryLoop forces a refresh whenever a quota window or reset credit
// expires, and announces nominal-account windows that recover from 0%.
func (a *monitorApp) quotaExpiryLoop() {
	var timer *time.Timer
	var timerC <-chan time.Time
	var handledThrough time.Time
	recoveries := newQuotaRecoveryTracker()
	for {
		select {
		case <-a.quotaChanged:
		case <-timerC:
			handledThrough = time.Now()
			a.startFetch(false)
		}
		if timer != nil {
			timer.Stop()
		}
		timer, timerC = nil, nil
		now := time.Now()
		state := a.snapshot()
		quota := state.quota
		if !state.settings.providerEnabled(providerCodex) {
			quota = quotaSnapshot{}
		}
		for _, notice := range recoveries.update(quota.Nominal, now) {
			a.queueNotice(notice)
		}
		next, expired := nextQuotaExpiry(quota, now, handledThrough, extResetTimes(&state)...)
		if due := recoveries.nextDue(); !due.IsZero() && (next.IsZero() || due.Before(next)) {
			next = due
		}
		switch {
		case expired:
			timer = time.NewTimer(0)
		case !next.IsZero():
			timer = time.NewTimer(time.Until(next))
		}
		if timer != nil {
			timerC = timer.C
		}
	}
}

func (a *monitorApp) scheduleTrayRetry() {
	procSetTimer.Call(uintptr(a.mainWindow), trayRetryTimerID, trayRetryInterval, 0)
}

func (a *monitorApp) addTrayIcon() error {
	current := a.snapshot()
	_, trayData := trayTarget(&current)
	icon := createTrayIcon(trayData, a.iconSize)
	if icon == 0 {
		return winError("CreateIconIndirect")
	}
	nid := a.notifyData(icon)
	ok := false
	if a.trayAdded {
		r, _, _ := procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
		ok = r != 0
	}
	if !ok {
		// Also covers an icon that vanished without a TaskbarCreated broadcast.
		r, _, _ := procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
		ok = r != 0
	}
	if !ok {
		err := winError("Shell_NotifyIconW")
		procDestroyIcon.Call(uintptr(icon))
		return err
	}
	a.trayAdded = true
	old := a.trayIcon
	a.trayIcon = icon
	if old != 0 {
		procDestroyIcon.Call(uintptr(old))
	}
	return nil
}

func (a *monitorApp) updateTray() {
	if !a.trayAdded {
		return
	}
	_ = a.addTrayIcon()
}

func (a *monitorApp) notifyData(icon syscall.Handle) notifyIconData {
	nid := notifyIconData{
		Size:            uint32(unsafe.Sizeof(notifyIconData{})),
		Window:          a.mainWindow,
		ID:              iconID,
		Flags:           nifMessage | nifIcon | nifTip,
		CallbackMessage: wmTray,
		Icon:            icon,
	}
	copyUTF16(nid.Tooltip[:], a.tooltip())
	return nid
}

func copyUTF16(dst []uint16, s string) {
	u16 := utf16.Encode([]rune(strings.ReplaceAll(s, "\x00", "")))
	if len(u16) > len(dst)-1 {
		u16 = u16[:len(dst)-1]
	}
	copy(dst, u16)
}

// showTrayNotification shows a Windows notification (toast) from the tray icon.
func (a *monitorApp) showTrayNotification(title, text string) bool {
	if !a.trayAdded {
		return false
	}
	nid := notifyIconData{
		Size:      uint32(unsafe.Sizeof(notifyIconData{})),
		Window:    a.mainWindow,
		ID:        iconID,
		Flags:     nifInfo,
		InfoFlags: niifInfo,
	}
	copyUTF16(nid.InfoTitle[:], title)
	copyUTF16(nid.Info[:], text)
	r, _, _ := procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
	return r != 0
}

func (a *monitorApp) queueNotice(notice quotaRecoveryNotice) {
	a.state.mu.Lock()
	a.state.notices = append(a.state.notices, notice)
	a.state.mu.Unlock()
	procPostMessage.Call(uintptr(a.mainWindow), wmShowNotice, 0, 0)
}

// drainNotices runs on the UI thread. Notices wait while the tray icon is
// missing and are shown once it has been added.
func (a *monitorApp) drainNotices() {
	if !a.trayAdded {
		return
	}
	a.state.mu.Lock()
	notices := a.state.notices
	a.state.notices = nil
	a.state.mu.Unlock()
	for _, notice := range notices {
		a.showTrayNotification(notice.title, notice.text)
	}
}

// queueAlert shows a message box from any goroutine. Alerts are shown one at a
// time on the UI thread; an identical alert that is already waiting is dropped.
func (a *monitorApp) queueAlert(text string, flags uintptr) {
	a.state.mu.Lock()
	for _, pending := range a.state.alerts {
		if pending.text == text {
			a.state.mu.Unlock()
			return
		}
	}
	a.state.alerts = append(a.state.alerts, alertMessage{text: text, flags: flags})
	a.state.mu.Unlock()
	procPostMessage.Call(uintptr(a.mainWindow), wmShowAlert, 0, 0)
}

func (a *monitorApp) drainAlerts() {
	if a.alertOpen {
		// The running loop below picks up alerts queued while a box is open.
		return
	}
	a.alertOpen = true
	defer func() { a.alertOpen = false }()
	for {
		a.state.mu.Lock()
		if len(a.state.alerts) == 0 {
			a.state.mu.Unlock()
			return
		}
		next := a.state.alerts[0]
		a.state.alerts = a.state.alerts[1:]
		a.state.mu.Unlock()
		a.messageBox(a.dialogOwner(), next.text, mbOK|next.flags|mbSetForeground|mbTopmost)
	}
}

// messageBox shows a modal box while telling the panel's activation handler
// that losing focus to it must not auto-hide the panel.
func (a *monitorApp) messageBox(owner syscall.Handle, text string, flags uintptr) int32 {
	a.modalDepth++
	defer func() { a.modalDepth-- }()
	return showMessageBox(owner, text, flags)
}

func (a *monitorApp) dialogOwner() syscall.Handle {
	if a.panelVisible {
		return a.panelWindow
	}
	return a.mainWindow
}

func (a *monitorApp) tooltip() string {
	s := a.snapshot()
	title, account := trayTarget(&s)
	name := account.Email
	if name == "" {
		name = "实际账户"
	}
	if title != providerTitle(providerCodex) {
		name = title + " · " + name
	}
	result := fmt.Sprintf("%s\n5小时剩余：%s  周额度剩余：%s", name, bucketText(account.Primary), bucketText(account.Secondary))
	if strings.HasPrefix(s.status, "同步失败") {
		result += "\n" + s.status
	}
	if s.updateAvailable {
		result += "\n可更新到 " + s.updateRelease.Tag
	} else if s.updateStatus != "" {
		result += "\n" + s.updateStatus
	}
	return result
}

// trayAccount picks the account shown in the tray: the first actual account
// when the server reports one, otherwise the first nominal account.
func trayAccount(quota quotaSnapshot) accountQuota {
	if len(quota.Actual) > 0 {
		return quota.Actual[0]
	}
	if len(quota.Nominal) > 0 {
		return quota.Nominal[0]
	}
	return accountQuota{}
}

func (a *monitorApp) snapshot() appState {
	a.state.mu.RLock()
	defer a.state.mu.RUnlock()
	ext := make(map[string]extQuota, len(a.state.ext))
	for id, q := range a.state.ext {
		ext[id] = q
	}
	return appState{
		quota:           a.state.quota,
		codexOK:         a.state.codexOK,
		ext:             ext,
		status:          a.state.status,
		updated:         a.state.updated,
		settings:        a.state.settings,
		startup:         a.state.startup,
		updateStatus:    a.state.updateStatus,
		updateAvailable: a.state.updateAvailable,
		updateRelease:   a.state.updateRelease,
	}
}

// trayClickHidesPanel reports whether a tray click should close the panel. In
// non-topmost mode, pressing the tray icon first activates the taskbar, which
// auto-hides the panel before the click arrives; that click must not reopen it.
func (a *monitorApp) trayClickHidesPanel() bool {
	return a.panelVisible || time.Since(a.panelAutoHiddenAt) < trayToggleGrace
}

func (a *monitorApp) panelSize(s *appState) (int32, int32) {
	return a.scale(panelWidth), a.scale(panelHeightForState(s))
}

func (a *monitorApp) showPanel() {
	if a.panelWindow == 0 {
		return
	}
	snapshot := a.snapshot()
	settings := snapshot.settings
	width, height := a.panelSize(&snapshot)
	var x, y int32
	if settings.PositionSet {
		x, y = clampPanelPosition(settings.PanelX, settings.PanelY, width, height)
	} else {
		x, y = panelPosition(a.panelAnchor(), width, height, a.scale(8))
	}
	insertAfter := uintptr(hwndTop)
	if settings.AlwaysOnTop {
		insertAfter = hwndTopmost
	}
	procSetWindowPos.Call(
		uintptr(a.panelWindow), insertAfter,
		uintptr(x), uintptr(y), uintptr(width), uintptr(height),
		swpShowWindow,
	)
	procShowWindow.Call(uintptr(a.panelWindow), swShow)
	procSetTimer.Call(uintptr(a.panelWindow), panelTimerID, panelTimerInterval, 0)
	a.panelVisible = true
	procInvalidateRect.Call(uintptr(a.panelWindow), 0, 1)
}

func (a *monitorApp) resizePanel() {
	if a.panelWindow == 0 {
		return
	}
	bounds := rect{}
	if r, _, _ := procGetWindowRect.Call(uintptr(a.panelWindow), uintptr(unsafe.Pointer(&bounds))); r == 0 {
		return
	}
	current := a.snapshot()
	width, height := a.panelSize(&current)
	x, y := bounds.Left, bounds.Top
	if work, ok := panelWorkArea(bounds); ok {
		x, y = keepSnappedEdges(bounds, width, height, work)
	}
	x, y = clampPanelPosition(x, y, width, height)
	procSetWindowPos.Call(uintptr(a.panelWindow), 0, uintptr(x), uintptr(y), uintptr(width), uintptr(height), swpNoActivate|swpNoZOrder)
}

// panelAnchor is the point the panel opens next to when no position has been
// saved: the tray icon when the shell can locate it, otherwise the cursor.
func (a *monitorApp) panelAnchor() point {
	if a.trayAdded {
		id := notifyIconIdentifier{Size: uint32(unsafe.Sizeof(notifyIconIdentifier{})), Window: a.mainWindow, ID: iconID}
		bounds := rect{}
		if r, _, _ := procShellNotifyIconGetRect.Call(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&bounds))); r == 0 && bounds.Right > bounds.Left {
			return point{X: (bounds.Left + bounds.Right) / 2, Y: (bounds.Top + bounds.Bottom) / 2}
		}
	}
	pt := point{}
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	return pt
}

func (a *monitorApp) savePanelPosition() {
	bounds := rect{}
	r, _, _ := procGetWindowRect.Call(uintptr(a.panelWindow), uintptr(unsafe.Pointer(&bounds)))
	if r == 0 {
		return
	}
	a.state.mu.Lock()
	a.state.settings.PanelX = bounds.Left
	a.state.settings.PanelY = bounds.Top
	a.state.settings.PositionSet = true
	settings := a.state.settings
	a.state.mu.Unlock()
	saveSettings(settings)
}

func (a *monitorApp) hidePanel() {
	if a.panelWindow != 0 {
		procKillTimer.Call(uintptr(a.panelWindow), panelTimerID)
		procShowWindow.Call(uintptr(a.panelWindow), swHide)
	}
	a.panelVisible = false
}

func panelPosition(anchor point, width, height, gap int32) (int32, int32) {
	monitor, _, _ := procMonitorFromPoint.Call(*(*uintptr)(unsafe.Pointer(&anchor)), monDefaultToNearest)
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	procGetMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&mi)))
	x := anchor.X - width/2
	y := anchor.Y - height - gap
	if y < mi.Work.Top {
		y = anchor.Y + gap
	}
	return clampToWorkArea(x, y, width, height, mi.Work)
}

func clampPanelPosition(x, y, width, height int32) (int32, int32) {
	work, ok := panelWorkArea(rect{Left: x, Top: y, Right: x + width, Bottom: y + height})
	if !ok {
		return x, y
	}
	return clampToWorkArea(x, y, width, height, work)
}

// panelWorkArea is the work area (screen minus taskbar) of the monitor that
// holds the centre of bounds.
func panelWorkArea(bounds rect) (rect, bool) {
	center := point{X: (bounds.Left + bounds.Right) / 2, Y: (bounds.Top + bounds.Bottom) / 2}
	monitor, _, _ := procMonitorFromPoint.Call(*(*uintptr)(unsafe.Pointer(&center)), monDefaultToNearest)
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	if r, _, _ := procGetMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return rect{}, false
	}
	return mi.Work, true
}

// panelSnapDistance is how close (in 96-DPI units) a dragged panel edge must
// come to the work-area edge to be pulled onto it.
const panelSnapDistance = 16

// snapDelta returns how far to shift the span [lo, hi) so that an end lands on
// the matching end of [workLo, workHi), or 0 when neither end is within
// distance. The nearer end wins.
func snapDelta(lo, hi, workLo, workHi, distance int32) int32 {
	toLo, toHi := workLo-lo, workHi-hi
	abs := func(v int32) int32 { return max(v, -v) }
	switch {
	case abs(toLo) <= distance && abs(toLo) <= abs(toHi):
		return toLo
	case abs(toHi) <= distance:
		return toHi
	}
	return 0
}

// snapToWorkArea pulls bounds onto the work-area edges (and so the corners)
// it is within distance of, each axis independently.
func snapToWorkArea(bounds, work rect, distance int32) rect {
	dx := snapDelta(bounds.Left, bounds.Right, work.Left, work.Right, distance)
	dy := snapDelta(bounds.Top, bounds.Bottom, work.Top, work.Bottom, distance)
	return rect{Left: bounds.Left + dx, Top: bounds.Top + dy, Right: bounds.Right + dx, Bottom: bounds.Bottom + dy}
}

// snapMovingPanel adjusts the rectangle Windows is about to move the panel to.
// Holding Alt drags freely.
func (a *monitorApp) snapMovingPanel(bounds *rect) {
	if state, _, _ := procGetAsyncKeyState.Call(vkMenu); state&0x8000 != 0 {
		return
	}
	if work, ok := panelWorkArea(*bounds); ok {
		*bounds = snapToWorkArea(*bounds, work, a.scale(panelSnapDistance))
	}
}

// keepSnappedEdges keeps a panel that sits flush against the right or bottom
// work-area edge on that edge when its size changes, instead of letting the
// growing or shrinking panel drift away from it.
func keepSnappedEdges(old rect, width, height int32, work rect) (int32, int32) {
	x, y := old.Left, old.Top
	if old.Right == work.Right {
		x = work.Right - width
	}
	if old.Bottom == work.Bottom {
		y = work.Bottom - height
	}
	return x, y
}

// clampToWorkArea keeps the panel inside the work area. If the panel is taller
// than the work area its top edge stays visible.
func clampToWorkArea(x, y, width, height int32, work rect) (int32, int32) {
	if x+width > work.Right {
		x = work.Right - width
	}
	if x < work.Left {
		x = work.Left
	}
	if y+height > work.Bottom {
		y = work.Bottom - height
	}
	if y < work.Top {
		y = work.Top
	}
	return x, y
}

func (a *monitorApp) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	a.menuOpen = true
	snapshot := a.snapshot()
	appendMenu(menu, 0, menuRefresh, "立即刷新")
	appendMenu(menu, 0, menuCheckUpdates, "检查更新")
	if snapshot.updateAvailable {
		flags := uintptr(0)
		label := "更新到 " + snapshot.updateRelease.Tag
		if a.installingUpdate.Load() {
			flags = 0x0001
			label = "正在更新…"
		}
		appendMenu(menu, flags, menuInstallUpdate, label)
	}
	appendMenu(menu, 0x00000800, 0, "")
	for i, id := range providerOrder {
		appendMenu(menu, 0, uintptr(menuProviderBase+i), providerTitle(id))
		checkMenu(menu, uint16(menuProviderBase+i), snapshot.settings.providerEnabled(id))
	}
	appendMenu(menu, 0x00000800, 0, "")
	intervalMenu, _, _ := procCreatePopupMenu.Call()
	if intervalMenu != 0 {
		minutes := snapshot.settings.RefreshMinutes
		for _, option := range refreshIntervalOptions {
			appendMenu(intervalMenu, 0, uintptr(option.menuID), refreshIntervalLabel(option.minutes))
			checkMenu(intervalMenu, option.menuID, minutes == option.minutes)
		}
		appendMenu(menu, 0x00000010, intervalMenu, "刷新间隔")
	}
	appendMenu(menu, 0x00000800, 0, "")
	appendMenu(menu, 0, menuOpenPanel, "窗口打开")
	appendMenu(menu, 0, menuTopmost, "面板始终置顶")
	appendMenu(menu, 0, menuStartup, "登录 Windows 时启动")
	appendMenu(menu, 0x00000800, 0, "")
	appendMenu(menu, 0, menuExit, "退出")
	checkMenu(menu, menuTopmost, snapshot.settings.AlwaysOnTop)
	checkMenu(menu, menuStartup, snapshot.startup)

	pt := point{}
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(uintptr(a.mainWindow))
	selected, _, _ := procTrackPopupMenu.Call(
		menu,
		tpmRightButton|tpmReturnCommand,
		uintptr(pt.X), uintptr(pt.Y),
		0, uintptr(a.mainWindow), 0,
	)
	procPostMessage.Call(uintptr(a.mainWindow), wmNull, 0, 0)
	if selected != 0 {
		a.handleMenuCommand(uint16(selected))
	}
	a.menuOpen = false
	if uint16(selected) != menuOpenPanel && !isProviderCommand(uint16(selected)) && !a.snapshot().settings.AlwaysOnTop && a.panelVisible {
		a.hidePanel()
	}
}

func (a *monitorApp) handlePanelButton(button string) {
	switch button {
	case "theme":
		a.state.mu.Lock()
		switch normalizedThemeMode(a.state.settings.ThemeMode) {
		case themeSystem:
			a.state.settings.ThemeMode = themeLight
		case themeLight:
			a.state.settings.ThemeMode = themeDark
		default:
			a.state.settings.ThemeMode = themeSystem
		}
		settings := a.state.settings
		a.state.mu.Unlock()
		saveSettings(settings)
		procInvalidateRect.Call(uintptr(a.panelWindow), 0, 1)
	case "minimize":
		a.hidePanel()
	case "exit":
		a.shutdown()
	case "reset":
		a.resetQuota()
	case "topmost":
		a.toggleAlwaysOnTop()
	case "update":
		a.installUpdate()
	}
}

// panelButtonAt hit-tests a point in panel layout units.
func (a *monitorApp) panelButtonAt(pt point) string {
	s := a.snapshot()
	sections, _ := sectionLayout(&s)
	for _, section := range sections {
		for _, card := range section.cards {
			if card.resetButton && containsPoint(panelResetButtonRect(card.top, card.height), pt) {
				return "reset"
			}
		}
	}
	switch {
	case containsPoint(panelUpdateButtonRect(), pt) && s.updateAvailable && !a.installingUpdate.Load() && !strings.HasPrefix(s.updateStatus, "更新失败"):
		return "update"
	case containsPoint(panelThemeButtonRect(), pt):
		return "theme"
	case containsPoint(panelTopmostButtonRect(), pt):
		return "topmost"
	case containsPoint(panelMinimizeButtonRect(), pt):
		return "minimize"
	case containsPoint(panelExitButtonRect(), pt):
		return "exit"
	default:
		return ""
	}
}

func panelResetButtonRect(top, height int32) rect {
	sectionTop := top + 95
	sectionBottom := top + height - 10
	buttonTop := (sectionTop+sectionBottom)/2 - 17
	return rect{Left: panelWidth - 58, Top: buttonTop, Right: panelWidth - 24, Bottom: buttonTop + 34}
}

func panelThemeButtonRect() rect {
	return rect{Left: panelWidth - 204, Top: 6, Right: panelWidth - 112, Bottom: 36}
}

func panelUpdateButtonRect() rect {
	return rect{Left: 196, Top: 6, Right: panelWidth - 210, Bottom: 36}
}

func panelMinimizeButtonRect() rect {
	return rect{Left: panelWidth - 66, Top: 6, Right: panelWidth - 38, Bottom: 36}
}

func panelExitButtonRect() rect {
	return rect{Left: panelWidth - 30, Top: 6, Right: panelWidth - 8, Bottom: 36}
}

func panelTopmostButtonRect() rect {
	return rect{Left: panelWidth - 102, Top: 6, Right: panelWidth - 74, Bottom: 36}
}

func containsPoint(bounds rect, pt point) bool {
	return pt.X >= bounds.Left && pt.X < bounds.Right && pt.Y >= bounds.Top && pt.Y < bounds.Bottom
}

func pointFromLParam(lParam uintptr) point {
	return point{X: int32(int16(uint16(lParam))), Y: int32(int16(uint16(lParam >> 16)))}
}

func appendMenu(menu uintptr, flags, id uintptr, text string) {
	procAppendMenu.Call(menu, flags, id, uintptr(unsafe.Pointer(utf16Ptr(text))))
}

func checkMenu(menu uintptr, id uint16, checked bool) {
	flags := uintptr(0)
	if checked {
		flags = 0x0008
	}
	procCheckMenuItem.Call(menu, uintptr(id), 0x0000|flags)
}

func (a *monitorApp) handleMenuCommand(command uint16) {
	switch command {
	case menuRefresh:
		a.startFetch(true)
	case menuCheckUpdates:
		a.checkForUpdate(true)
	case menuInstallUpdate:
		a.installUpdate()
	case menuRefresh5, menuRefresh10, menuRefresh15, menuRefresh20, menuRefresh30:
		for _, option := range refreshIntervalOptions {
			if option.menuID == command {
				a.setRefreshInterval(option.minutes)
			}
		}
	case menuOpenPanel:
		a.showPanel()
	case menuTopmost:
		a.toggleAlwaysOnTop()
	case menuStartup:
		a.state.mu.RLock()
		enabled := !a.state.startup
		a.state.mu.RUnlock()
		if err := setStartup(enabled); err == nil {
			a.state.mu.Lock()
			a.state.startup = enabled
			a.state.mu.Unlock()
		} else {
			a.state.mu.Lock()
			a.state.status = "开机启动设置失败：" + shortError(err.Error())
			a.state.mu.Unlock()
			procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)
			a.queueAlert("开机启动设置失败。\n\n"+err.Error(), mbIconWarning)
		}
	case menuExit:
		a.shutdown()
	default:
		if isProviderCommand(command) {
			a.editProvider(providerOrder[command-menuProviderBase])
		}
	}
}

func isProviderCommand(command uint16) bool {
	return command >= menuProviderBase && int(command) < menuProviderBase+len(providerOrder)
}

// refreshIntervalOptions lists the selectable auto-refresh periods.
var refreshIntervalOptions = []struct {
	minutes int
	menuID  uint16
}{{5, menuRefresh5}, {10, menuRefresh10}, {15, menuRefresh15}, {20, menuRefresh20}, {30, menuRefresh30}}

func (a *monitorApp) toggleAlwaysOnTop() {
	a.state.mu.Lock()
	a.state.settings.AlwaysOnTop = !a.state.settings.AlwaysOnTop
	topmost := a.state.settings.AlwaysOnTop
	settings := a.state.settings
	a.state.mu.Unlock()
	saveSettings(settings)
	insertAfter := uintptr(hwndNotopmost)
	if topmost {
		insertAfter = hwndTopmost
	}
	procSetWindowPos.Call(uintptr(a.panelWindow), insertAfter, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	if a.panelVisible {
		procInvalidateRect.Call(uintptr(a.panelWindow), 0, 1)
	}
}

func (a *monitorApp) setRefreshInterval(minutes int) {
	if !validRefreshInterval(minutes) {
		return
	}
	a.state.mu.Lock()
	a.state.settings.RefreshMinutes = minutes
	a.state.status = "刷新间隔：" + refreshIntervalLabel(minutes)
	settings := a.state.settings
	a.state.mu.Unlock()
	saveSettings(settings)
	select {
	case <-a.intervalChange:
	default:
	}
	a.intervalChange <- time.Duration(minutes) * time.Minute
	procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)
}

func (a *monitorApp) resetQuota() {
	if a.resetPending.Load() {
		a.messageBox(a.panelWindow, "上一次重置结果仍在处理中，请稍候。", mbOK|mbIconInformation)
		return
	}
	snapshot := a.snapshot()
	if !snapshot.quota.ResetSupported {
		a.messageBox(a.panelWindow, "当前认证方式不支持此重置操作。", mbOK|mbIconInformation)
		return
	}
	if availableResetCredits(snapshot.quota.Nominal) < 1 {
		a.messageBox(a.panelWindow, "当前没有可用的重置次数，因此没有发送请求。", mbOK|mbIconInformation)
		return
	}
	if a.messageBox(a.panelWindow, resetConfirmation(snapshot.quota), mbYesNo|mbIconWarning|mbDefaultButton2) != idYes {
		return
	}
	if !a.fetching.CompareAndSwap(false, true) {
		a.messageBox(a.panelWindow, "当前正在刷新额度或执行其他操作，请稍后再试。", mbOK|mbIconInformation)
		return
	}
	a.resetPending.Store(true)
	a.state.mu.Lock()
	a.state.status = "正在消耗重置额度…"
	serverURL := a.state.settings.ServerURL
	a.state.mu.Unlock()
	procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)
	go func() {
		result, err := requestQuotaReset(serverURL)
		if err != nil {
			detail := err.Error()
			if strings.Contains(detail, "读取 Codex 凭据") {
				detail = "读取本机 Codex 凭据失败，请检查 auth.json。"
			} else if strings.Contains(detail, "解析 Codex 凭据") {
				detail = "本机 auth.json 格式无效。"
			}
			result = "重置请求未能确认结果。\n\n" + detail + "\n\n请立即刷新剩余次数；程序不会自动重试。"
		}
		a.state.mu.Lock()
		a.state.resetResult = result
		if err != nil {
			a.state.status = "重置结果待核实"
		} else {
			a.state.status = "重置操作已返回"
		}
		a.state.mu.Unlock()
		procPostMessage.Call(uintptr(a.mainWindow), wmResetCompleted, 0, 0)
	}()
}

func availableResetCredits(accounts []accountQuota) int {
	total := 0
	for _, account := range accounts {
		total += account.Reset.AvailableCount
	}
	return total
}

func resetConfirmation(quota quotaSnapshot) string {
	first := accountQuota{}
	if len(quota.Nominal) > 0 {
		first = quota.Nominal[0]
	}
	if quota.DirectMode {
		return fmt.Sprintf(
			"即将使用当前 Codex 认证为账户 %s 消耗 1 次可用重置额度。\n\n该操作会直接请求插件的账户重置接口，可能无法撤销；请求不会自动重试。是否继续？",
			accountEmail(first),
		)
	}
	if len(quota.Nominal) <= 1 {
		return fmt.Sprintf(
			"即将为名义账户 %s 消耗 1 次可用重置额度。\n\n重置可能无法撤销；请求不会自动重试。是否继续？",
			accountEmail(first),
		)
	}
	emails := make([]string, 0, len(quota.Nominal))
	for _, account := range quota.Nominal {
		emails = append(emails, accountEmail(account))
	}
	return fmt.Sprintf(
		"服务端会对该 API Key 名义前缀下符合条件的每个账户各消耗最多 1 次可用重置额度，共 %d 个账户：\n\n%s\n\n重置可能无法撤销；请求不会自动重试。是否继续？",
		len(quota.Nominal), strings.Join(emails, "\n"),
	)
}

func (a *monitorApp) shutdown() {
	if a.closing {
		return
	}
	a.closing = true
	if a.trayAdded {
		nid := a.notifyData(a.trayIcon)
		procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
		a.trayAdded = false
	}
	if a.trayIcon != 0 {
		procDestroyIcon.Call(uintptr(a.trayIcon))
		a.trayIcon = 0
	}
	procCloseHandle.Call(uintptr(a.mutex))
	procPostQuitMessage.Call(0)
}

// Panel layout values are in 96-DPI units; paintPanel maps them to the real
// DPI with a GDI mapping mode, and window sizes go through monitorApp.scale.
const (
	panelWidth        = 540
	panelHeaderHeight = 46
	panelCardGap      = 10
	panelBottomMargin = 14
	actualCardHeight  = 100
)

type panelCard struct {
	account     accountQuota
	nominal     bool
	top         int32
	height      int32
	resetButton bool
}

// panelLayout stacks one card per nominal account, then one per actual
// account. Before the first successful sync an empty nominal card is shown.
func panelLayout(quota quotaSnapshot) ([]panelCard, int32) {
	cards, end := codexCards(quota, panelHeaderHeight)
	return cards, end - panelCardGap + panelBottomMargin
}

// codexCards lays the Codex cards out from top and returns the y just below
// the last card's trailing gap.
func codexCards(quota quotaSnapshot, top int32) ([]panelCard, int32) {
	nominal := quota.Nominal
	if len(nominal) == 0 {
		nominal = []accountQuota{{}}
	}
	cards := make([]panelCard, 0, len(nominal)+len(quota.Actual))
	for i, account := range nominal {
		height := nominalCardHeight(account)
		cards = append(cards, panelCard{account: account, nominal: true, top: top, height: height, resetButton: i == 0 && quota.ResetSupported})
		top += height + panelCardGap
	}
	for _, account := range quota.Actual {
		cards = append(cards, panelCard{account: account, top: top, height: actualCardHeight})
		top += actualCardHeight + panelCardGap
	}
	return cards, top
}

type panelColors struct {
	background uint32
	card       uint32
	border     uint32
	text       uint32
	muted      uint32
	track      uint32
	button     uint32
	buttonText uint32
	warn       uint32
	danger     uint32
	dark       bool
}

func (a *monitorApp) paintPanel(hwnd syscall.Handle) {
	ps := paintStruct{}
	hdc, _, _ := procBeginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
	s := a.snapshot()
	a.drawPanel(hdc, &s)
}

// drawPanel renders the whole panel for state s into hdc.
func (a *monitorApp) drawPanel(hdc uintptr, s *appState) {
	colors := colorsForTheme(s.settings.ThemeMode)
	sections, height := sectionLayout(s)

	// Draw in 96-DPI layout units and let GDI scale coordinates, pens and font
	// sizes to the real DPI, so text is rasterized at full resolution.
	procSetMapMode.Call(hdc, mmAnisotropic)
	procSetWindowExtEx.Call(hdc, baseDPI, baseDPI, 0)
	procSetViewportExtEx.Call(hdc, uintptr(a.dpi), uintptr(a.dpi), 0)

	background := createBrush(colors.background)
	defer procDeleteObject.Call(uintptr(background))
	full := rect{Right: panelWidth + 1, Bottom: height + 1}
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&full)), uintptr(background))
	procSetBkMode.Call(hdc, 1)
	fontHeight := int32(-17)
	font, _, _ := procCreateFont.Call(
		uintptr(fontHeight), 0, 0, 0, 500,
		0, 0, 0, 1, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(utf16Ptr("Microsoft YaHei UI"))),
	)
	if font != 0 {
		defer procDeleteObject.Call(font)
		oldFont, _, _ := procSelectObject.Call(hdc, font)
		defer procSelectObject.Call(hdc, oldFont)
	}
	themeLabel := "跟随系统"
	switch normalizedThemeMode(s.settings.ThemeMode) {
	case themeLight:
		themeLabel = "亮色"
	case themeDark:
		themeLabel = "暗色"
	}
	drawText(hdc, "额度监控", rect{Left: 18, Top: 6, Right: 190, Bottom: 36}, colors.text, dtSingleLine|dtVCenter)
	if s.updateAvailable && !strings.HasPrefix(s.updateStatus, "更新失败") {
		label := "更新到 " + s.updateRelease.Tag
		if a.installingUpdate.Load() {
			label = "正在更新…"
		}
		drawPanelButton(hdc, panelUpdateButtonRect(), label, colors)
	} else {
		headerStatus := s.updateStatus
		if headerStatus == "" {
			headerStatus = s.status
		}
		drawText(hdc, headerStatus, panelUpdateButtonRect(), colors.muted, dtSingleLine|dtVCenter|dtEndEllipsis)
	}
	drawPanelButton(hdc, panelThemeButtonRect(), themeLabel, colors)
	drawPinButton(hdc, panelTopmostButtonRect(), colors, s.settings.AlwaysOnTop)
	drawPanelButton(hdc, panelMinimizeButtonRect(), "—", colors)
	drawPanelButton(hdc, panelExitButtonRect(), "×", colors)

	if len(sections) == 0 {
		drawText(hdc, "尚未启用任何 Provider。右键托盘图标，勾选 Codex / Antigravity / Claude。",
			rect{Left: 18, Top: panelHeaderHeight, Right: panelWidth - 18, Bottom: panelHeaderHeight + emptyPanelHintHeight},
			colors.muted, dtSingleLine|dtVCenter|dtEndEllipsis)
		return
	}
	for _, section := range sections {
		if section.provider != providerCodex {
			drawExtCard(hdc, colors, section)
			continue
		}
		look := cardLook{style: styleFor(providerCodex, colors.dark), connected: section.connected, id: providerCodex}
		for _, card := range section.cards {
			if card.nominal {
				drawNominalAccountCard(hdc, colors, look, card)
			} else {
				drawActualAccountCard(hdc, colors, look, card.top, card.account)
			}
		}
	}
}

func drawRoundRect(hdc uintptr, bounds rect, radius int32, fill, border uint32) {
	brush := createBrush(fill)
	pen, _, _ := procCreatePen.Call(0, 1, uintptr(border))
	oldBrush, _, _ := procSelectObject.Call(hdc, uintptr(brush))
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	procRoundRect.Call(hdc, uintptr(bounds.Left), uintptr(bounds.Top), uintptr(bounds.Right), uintptr(bounds.Bottom), uintptr(radius), uintptr(radius))
	procSelectObject.Call(hdc, oldPen)
	procSelectObject.Call(hdc, oldBrush)
	procDeleteObject.Call(pen)
	procDeleteObject.Call(uintptr(brush))
}

func drawPanelButton(hdc uintptr, bounds rect, label string, colors panelColors) {
	drawRoundRect(hdc, bounds, 6, colors.button, colors.border)
	drawText(hdc, label, bounds, colors.buttonText, dtSingleLine|dtVCenter|dtCenter)
}

func drawPinButton(hdc uintptr, bounds rect, colors panelColors, enabled bool) {
	background := colors.button
	iconColor := colors.buttonText
	if enabled {
		background = colorRef(63, 139, 214)
		iconColor = colorRef(255, 255, 255)
	}
	drawRoundRect(hdc, bounds, 6, background, colors.border)
	drawText(hdc, "📌", bounds, iconColor, dtSingleLine|dtVCenter|dtCenter)
}

// codexCardDetail is the band text, laid out like the other providers' "email · plan".
func codexCardDetail(account accountQuota) string {
	return accountEmail(account) + "  ·  Plus"
}

func drawNominalAccountCard(hdc uintptr, colors panelColors, look cardLook, card panelCard) {
	top, account := card.top, card.account
	drawCardShell(hdc, colors, look, top, card.height, codexCardDetail(account))
	drawStyledMeter(hdc, "5小时", 27, top+bandHeight+14, account.Primary, colors, look)
	drawStyledMeter(hdc, "周额度", 27, top+bandHeight+14+extRowPitch, account.Secondary, colors, look)
	drawHorizontalDivider(hdc, 27, panelWidth-27, top+94, colors.border)
	drawText(hdc, fmt.Sprintf("重置次数  ·  %d 次", account.Reset.AvailableCount),
		rect{Left: 27, Top: top + 97, Right: panelWidth - 74, Bottom: top + 120}, colors.text, dtSingleLine|dtVCenter|dtEndEllipsis)
	if card.resetButton {
		drawPanelButton(hdc, panelResetButtonRect(top, card.height), "↻", colors)
	}
	for i, line := range resetCreditLines(account.Reset, time.Now()) {
		rowTop := top + 123 + int32(i)*20
		drawText(hdc, line, rect{Left: 27, Top: rowTop, Right: panelWidth - 72, Bottom: rowTop + 19}, colors.muted, dtSingleLine|dtVCenter|dtEndEllipsis)
	}
}

func drawActualAccountCard(hdc uintptr, colors panelColors, look cardLook, top int32, account accountQuota) {
	drawCardShell(hdc, colors, look, top, actualCardHeight, codexCardDetail(account))
	drawStyledMeter(hdc, "5小时", 27, top+bandHeight+14, account.Primary, colors, look)
	drawStyledMeter(hdc, "周额度", 27, top+bandHeight+14+extRowPitch, account.Secondary, colors, look)
}

func accountEmail(account accountQuota) string {
	if account.Email == "" {
		return "未知账户"
	}
	return account.Email
}

func drawHorizontalDivider(hdc uintptr, left, right, y int32, color uint32) {
	pen, _, _ := procCreatePen.Call(0, 1, uintptr(color))
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	procMoveToEx.Call(hdc, uintptr(left), uintptr(y), 0)
	procLineTo.Call(hdc, uintptr(right), uintptr(y))
	procSelectObject.Call(hdc, oldPen)
	procDeleteObject.Call(pen)
}

func quotaColor(percent int) uint32 {
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}
	var from, to [3]float64
	var fraction float64
	if percent <= 50 {
		from = [3]float64{232, 68, 68}
		to = [3]float64{70, 145, 255}
		fraction = float64(percent) / 50
	} else {
		from = [3]float64{70, 145, 255}
		to = [3]float64{52, 190, 116}
		fraction = float64(percent-50) / 50
	}
	return colorRef(
		byte(math.Round(from[0]+(to[0]-from[0])*fraction)),
		byte(math.Round(from[1]+(to[1]-from[1])*fraction)),
		byte(math.Round(from[2]+(to[2]-from[2])*fraction)),
	)
}

func drawText(hdc uintptr, text string, bounds rect, color uint32, flags uintptr) {
	procSetTextColor.Call(hdc, uintptr(color))
	procDrawText.Call(hdc, uintptr(unsafe.Pointer(utf16Ptr(text))), ^uintptr(0), uintptr(unsafe.Pointer(&bounds)), flags)
}

// utf16Text converts s for Win32 calls. Interior NULs (which can arrive in
// server-provided text) are dropped instead of making the conversion fail,
// so the result always holds at least the terminating NUL.
func utf16Text(s string) []uint16 {
	wide, _ := syscall.UTF16FromString(strings.ReplaceAll(s, "\x00", ""))
	return wide
}

func utf16Ptr(s string) *uint16 {
	return &utf16Text(s)[0]
}

func createBrush(color uint32) syscall.Handle {
	r, _, _ := procCreateSolidBrush.Call(uintptr(color))
	return syscall.Handle(r)
}

func colorRef(r, g, b byte) uint32 {
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16
}

func normalizedThemeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case themeLight:
		return themeLight
	case themeDark:
		return themeDark
	default:
		return themeSystem
	}
}

func colorsForTheme(mode string) panelColors {
	dark := normalizedThemeMode(mode) == themeDark ||
		(normalizedThemeMode(mode) == themeSystem && !systemPrefersLightTheme())
	if dark {
		return panelColors{
			background: colorRef(23, 24, 28),
			card:       colorRef(32, 34, 42),
			border:     colorRef(46, 49, 58),
			text:       colorRef(236, 237, 241),
			muted:      colorRef(154, 160, 173),
			track:      colorRef(54, 57, 68),
			button:     colorRef(40, 43, 52),
			buttonText: colorRef(220, 222, 230),
			warn:       colorRef(242, 192, 55),
			danger:     colorRef(229, 72, 77),
			dark:       true,
		}
	}
	return panelColors{
		background: colorRef(246, 245, 242),
		card:       colorRef(255, 255, 255),
		border:     colorRef(227, 225, 218),
		text:       colorRef(31, 32, 36),
		muted:      colorRef(107, 111, 122),
		track:      colorRef(230, 228, 222),
		button:     colorRef(236, 234, 228),
		buttonText: colorRef(60, 62, 70),
		warn:       colorRef(214, 150, 0),
		danger:     colorRef(229, 72, 77),
	}
}

func systemPrefersLightTheme() bool {
	keyPath := utf16Ptr("Software\\Microsoft\\Windows\\CurrentVersion\\Themes\\Personalize")
	valueName := utf16Ptr("AppsUseLightTheme")
	var key uintptr
	r, _, _ := procRegOpenKeyEx.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(keyPath)), 0, keyQueryValue, uintptr(unsafe.Pointer(&key)))
	if r != 0 {
		return true
	}
	defer procRegCloseKey.Call(key)
	var value, valueType, size uint32
	size = uint32(unsafe.Sizeof(value))
	r, _, _ = procRegQueryValueEx.Call(key, uintptr(unsafe.Pointer(valueName)), 0,
		uintptr(unsafe.Pointer(&valueType)), uintptr(unsafe.Pointer(&value)), uintptr(unsafe.Pointer(&size)))
	if r != 0 || valueType != regDword {
		return true
	}
	return value != 0
}

func nominalCardHeight(account accountQuota) int32 {
	rows := len(resetCreditLines(account.Reset, time.Now()))
	return int32(133 + rows*20)
}

func panelHeightFor(quota quotaSnapshot) int32 {
	_, height := panelLayout(quota)
	return height
}

func resetCreditLines(info resetCreditInfo, now time.Time) []string {
	type expiry struct {
		at time.Time
	}
	valid := make([]expiry, 0, len(info.ExpiresAt))
	invalid := make([]string, 0)
	for _, raw := range info.ExpiresAt {
		value := strings.TrimSpace(raw)
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			invalid = append(invalid, "到期 "+value+" · 时间格式异常")
			continue
		}
		valid = append(valid, expiry{at: parsed})
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].at.Before(valid[j].at) })
	lines := make([]string, 0, len(valid)+len(invalid)+2)
	for _, item := range valid {
		lines = append(lines, "到期 "+item.at.Local().Format("2006-01-02 15:04")+"  ·  剩余 "+formatRemaining(item.at.Sub(now)))
	}
	lines = append(lines, invalid...)
	if info.WithoutExpiry > 0 {
		lines = append(lines, fmt.Sprintf("无固定到期时间：%d 次", info.WithoutExpiry))
	}
	if info.AvailableCount == 0 && len(lines) == 0 {
		return []string{"当前没有可用重置次数"}
	}
	if !info.ExpiryDetailsAvailable && len(valid) == 0 {
		lines = append(lines, "有效期明细暂不可用")
	} else {
		missing := info.AvailableCount - len(info.ExpiresAt) - info.WithoutExpiry
		if missing > 0 {
			lines = append(lines, fmt.Sprintf("还有 %d 次的到期时间未返回", missing))
		} else if !info.ExpiryDetailsComplete {
			lines = append(lines, "有效期明细可能不完整")
		}
	}
	if len(lines) == 0 {
		return []string{"暂无到期明细"}
	}
	return lines
}

// formatRemaining renders a countdown: minutes under an hour, hours and
// minutes under a day, days and hours beyond that.
func formatRemaining(remaining time.Duration) string {
	if remaining <= 0 {
		return "已到期"
	}
	if remaining < time.Hour {
		return fmt.Sprintf("%d分", max(int(remaining.Minutes()), 1))
	}
	hours := int(remaining.Hours())
	if hours >= 24 {
		return fmt.Sprintf("%d天%d小时", hours/24, hours%24)
	}
	return fmt.Sprintf("%d小时%d分", hours, int(remaining.Minutes())%60)
}

// createTrayIcon renders the icon at exactly size×size pixels (the shell's
// small-icon size) so the taskbar shows it 1:1 instead of resampling it.
func createTrayIcon(actual accountQuota, size int) syscall.Handle {
	dc, _, _ := procGetDC.Call(0)
	info := bitmapInfo{Header: bitmapInfoHeader{
		Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: int32(size), Height: -int32(size),
		Planes: 1, BitCount: 32, Compression: biRGB, SizeImage: uint32(size * size * 4),
	}}
	var bits unsafe.Pointer
	bitmap, _, _ := procCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&info)), dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	procReleaseDC.Call(0, dc)
	if bitmap == 0 {
		return 0
	}
	defer procDeleteObject.Call(bitmap)
	if bits == nil {
		return 0
	}
	drawTrayIconPixels(unsafe.Slice((*byte)(bits), size*size*4), size, actual)

	// Monochrome mask rows are padded to 16-bit boundaries; all zero means the
	// color bitmap's alpha channel alone decides transparency.
	maskBits := make([]byte, (size+15)/16*2*size)
	mask, _, _ := procCreateBitmap.Call(uintptr(size), uintptr(size), 1, 1, uintptr(unsafe.Pointer(&maskBits[0])))
	if mask == 0 {
		return 0
	}
	defer procDeleteObject.Call(mask)
	ii := iconInfo{Icon: 1, Mask: syscall.Handle(mask), Color: syscall.Handle(bitmap)}
	icon, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	return syscall.Handle(icon)
}

// drawTrayIconPixels lays out the two meters on the original 32px design grid
// scaled to size, rounding every edge to a whole pixel so bars stay crisp.
func drawTrayIconPixels(pixels []byte, size int, account accountQuota) {
	grid := func(v int) int { return int(math.Round(float64(v*size) / 32)) }
	trackX := max(grid(2), 1)
	trackW := size - 2*trackX
	trackH := grid(10)
	inset := max(grid(2), 1)
	for _, meter := range []struct {
		top    int
		bucket quotaBucket
	}{{grid(4), account.Primary}, {grid(18), account.Secondary}} {
		fillRoundedPixels(pixels, size, trackX, meter.top, trackW, trackH, trackH*3/10, colorRef(48, 59, 74))
		innerX, innerY := trackX+inset, meter.top+inset
		innerW, innerH := trackW-2*inset, trackH-2*inset
		fillRoundedPixels(pixels, size, innerX, innerY, innerW, innerH, innerH/3, colorRef(21, 27, 37))
		if !meter.bucket.Known {
			continue
		}
		if width := int(math.Round(float64(innerW) * float64(meter.bucket.Percent) / 100.0)); width > 0 {
			fillRoundedPixels(pixels, size, innerX, innerY, width, innerH, innerH/3, quotaColor(meter.bucket.Percent))
		}
	}
}

func fillRoundedPixels(pixels []byte, size, left, top, width, height, radius int, color uint32) {
	r := float64(radius)
	for y := top; y < top+height; y++ {
		for x := left; x < left+width; x++ {
			cx, cy := float64(x), float64(y)
			if x < left+radius {
				cx = float64(left + radius)
			} else if x >= left+width-radius {
				cx = float64(left + width - radius - 1)
			}
			if y < top+radius {
				cy = float64(top + radius)
			} else if y >= top+height-radius {
				cy = float64(top + height - radius - 1)
			}
			dx, dy := float64(x)-cx, float64(y)-cy
			if dx*dx+dy*dy > r*r || x < 0 || y < 0 || x >= size || y >= size {
				continue
			}
			i := (y*size + x) * 4
			pixels[i] = byte(color >> 16)
			pixels[i+1] = byte(color >> 8)
			pixels[i+2] = byte(color)
			pixels[i+3] = 255
		}
	}
}

type responseEnvelope struct {
	Nominal json.RawMessage "json:\"nominal_accounts\""
	Actual  json.RawMessage "json:\"actual_accounts\""
}

type directResponseEnvelope struct {
	Accounts json.RawMessage `json:"accounts"`
}

type quotaResetResponse struct {
	Success         bool                               `json:"success"`
	Partial         bool                               `json:"partial"`
	NominalAccounts map[string]quotaResetAccountResult `json:"nominal_accounts"`
	Errors          []string                           `json:"errors"`
}

type quotaDirectResetResponse struct {
	Success  bool                               `json:"success"`
	Partial  bool                               `json:"partial"`
	Accounts map[string]quotaResetAccountResult `json:"accounts"`
	Errors   []string                           `json:"errors"`
}

type quotaResetAccountResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type accountDetails struct {
	Groups       []quotaGroup    "json:\"groups\""
	ResetCredits resetCreditInfo "json:\"reset_credits\""
}

type quotaGroup struct {
	DisplayName string           "json:\"displayName\""
	Buckets     []quotaBucketRaw "json:\"buckets\""
}

type quotaBucketRaw struct {
	Window            string   "json:\"window\""
	RemainingFraction *float64 "json:\"remainingFraction\""
	ResetTime         string   "json:\"resetTime\""
	ResetTimeSnake    string   "json:\"reset_time\""
}

type quotaEndpoints struct {
	quota       string
	direct      string
	reset       string
	directReset string
}

// quotaEndpointsFor builds the plugin routes under the configured CPA base
// URL. Credentials are only ever sent over HTTPS, or plain HTTP to loopback.
func quotaEndpointsFor(serverURL string) (quotaEndpoints, error) {
	base := strings.TrimSpace(serverURL)
	if base == "" {
		base = defaultServerURL
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return quotaEndpoints{}, fmt.Errorf("设置中的 ServerURL 无效：%q", serverURL)
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return quotaEndpoints{}, fmt.Errorf("设置中的 ServerURL 必须使用 HTTPS（仅本机地址允许 HTTP）：%q", serverURL)
	}
	root := strings.TrimRight(parsed.Scheme+"://"+parsed.Host+parsed.EscapedPath(), "/") + quotaRoutePath
	return quotaEndpoints{
		quota:       root,
		direct:      root + "/direct",
		reset:       root + "/reset",
		directReset: root + "/direct/reset",
	}, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func userAgent() string {
	return "CPA-Quota-Query/" + appVersion
}

// newQuotaClient does not follow redirects, so credentials are never replayed
// to another location; a redirect surfaces as an HTTP status error instead.
func newQuotaClient(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Timeout:   12 * time.Second,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type httpStatusError struct {
	operation string
	code      int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s返回 HTTP %d", e.operation, e.code)
}

// quotaRetryError wraps the last failure after more than one attempt.
type quotaRetryError struct {
	attempts int
	err      error
}

func (e *quotaRetryError) Error() string {
	return fmt.Sprintf("额度查询连续失败（共尝试 %d 次）：%v", e.attempts, e.err)
}

func (e *quotaRetryError) Unwrap() error {
	return e.err
}

// quotaFailureCause strips the retry wrapper so short status lines show the
// actual reason rather than the attempt count.
func quotaFailureCause(err error) error {
	var retryErr *quotaRetryError
	if errors.As(err, &retryErr) {
		return retryErr.err
	}
	return err
}

// retryableQuotaError reports whether another attempt could succeed. Client
// errors such as 401/403/404 will not change on retry.
func retryableQuotaError(err error) bool {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.code == http.StatusRequestTimeout || statusErr.code == http.StatusTooManyRequests || statusErr.code >= 500
	}
	return true
}

func fetchQuota(serverURL string) (quotaSnapshot, error) {
	endpoints, err := quotaEndpointsFor(serverURL)
	if err != nil {
		return quotaSnapshot{}, err
	}
	credentials, err := readQuotaCredentials()
	if err != nil {
		return quotaSnapshot{}, err
	}
	client := newQuotaClient(nil)
	return fetchQuotaWithRetry(func() (quotaSnapshot, error) {
		return fetchQuotaUsingAuth(client, credentials, endpoints.quota, endpoints.direct)
	}, time.Sleep)
}

func fetchQuotaWithRetry(request func() (quotaSnapshot, error), sleep func(time.Duration)) (quotaSnapshot, error) {
	for attempt := 0; ; attempt++ {
		result, err := request()
		if err == nil {
			return result, nil
		}
		if attempt == quotaRequestRetries || !retryableQuotaError(err) {
			if attempt == 0 {
				return quotaSnapshot{}, err
			}
			return quotaSnapshot{}, &quotaRetryError{attempts: attempt + 1, err: err}
		}
		sleep(min(time.Second<<attempt, 8*time.Second))
	}
}

func fetchQuotaUsingAuth(client *http.Client, credentials quotaCredentials, apiKeyEndpoint, chatGPTEndpoint string) (quotaSnapshot, error) {
	switch credentials.Mode {
	case authModeAPIKey:
		return fetchQuotaAt(client, apiKeyEndpoint, credentials.APIKey)
	case authModeChatGPT:
		return fetchDirectQuotaAt(client, chatGPTEndpoint, credentials.AccessToken, credentials.AccountID)
	default:
		return quotaSnapshot{}, errors.New("auth.json 中 auth_mode 不受支持")
	}
}

func fetchQuotaAt(client *http.Client, endpoint, apiKey string) (quotaSnapshot, error) {
	var result quotaSnapshot
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("额度接口请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, &httpStatusError{operation: "额度接口", code: resp.StatusCode}
	}
	var envelope responseEnvelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxQuotaResponseBytes)).Decode(&envelope); err != nil {
		return result, fmt.Errorf("解析额度响应失败：%w", err)
	}
	return parseQuotaEnvelope(envelope), nil
}

func fetchDirectQuotaAt(client *http.Client, endpoint, accessToken, accountID string) (quotaSnapshot, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return quotaSnapshot{}, err
	}
	setDirectQuotaHeaders(req, accessToken, accountID)
	resp, err := client.Do(req)
	if err != nil {
		return quotaSnapshot{}, fmt.Errorf("直接额度接口请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return quotaSnapshot{}, &httpStatusError{operation: "直接额度接口", code: resp.StatusCode}
	}
	var envelope directResponseEnvelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxQuotaResponseBytes)).Decode(&envelope); err != nil {
		return quotaSnapshot{}, fmt.Errorf("解析直接额度响应失败：%w", err)
	}
	return parseDirectQuotaEnvelope(envelope)
}

func parseQuotaEnvelope(envelope responseEnvelope) quotaSnapshot {
	nominal := parseAccounts(envelope.Nominal)
	return quotaSnapshot{
		Nominal:        nominal,
		Actual:         parseAccounts(envelope.Actual),
		ResetSupported: len(nominal) > 0,
	}
}

func parseDirectQuotaEnvelope(envelope directResponseEnvelope) (quotaSnapshot, error) {
	accounts := parseAccounts(envelope.Accounts)
	if len(accounts) == 0 {
		return quotaSnapshot{}, errors.New("直接额度响应中没有账户数据")
	}
	return quotaSnapshot{Nominal: accounts, DirectMode: true, ResetSupported: true}, nil
}

func setDirectQuotaHeaders(req *http.Request, accessToken, accountID string) {
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("ChatGPT-Account-ID", accountID)
}

type quotaCredentials struct {
	Mode        string
	APIKey      string
	AccessToken string
	AccountID   string
}

func readQuotaCredentials() (quotaCredentials, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return quotaCredentials{}, err
	}
	return readQuotaCredentialsFromPath(filepath.Join(home, authRelPath))
}

func readQuotaCredentialsFromPath(path string) (quotaCredentials, error) {
	authBytes, err := os.ReadFile(path)
	if err != nil {
		return quotaCredentials{}, fmt.Errorf("读取 Codex 凭据失败：%w", err)
	}
	credentials, err := parseQuotaCredentials(authBytes)
	if err != nil {
		return quotaCredentials{}, fmt.Errorf("解析 Codex 凭据失败：%w", err)
	}
	return credentials, nil
}

func parseQuotaCredentials(authBytes []byte) (quotaCredentials, error) {
	var auth struct {
		AuthMode string          `json:"auth_mode"`
		APIKey   json.RawMessage `json:"OPENAI_API_KEY"`
		Tokens   struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(authBytes, &auth); err != nil {
		return quotaCredentials{}, err
	}
	mode := strings.ToLower(strings.TrimSpace(auth.AuthMode))
	switch mode {
	case "", authModeAPIKey:
		var apiKey string
		if len(auth.APIKey) == 0 || string(bytes.TrimSpace(auth.APIKey)) == "null" || json.Unmarshal(auth.APIKey, &apiKey) != nil || strings.TrimSpace(apiKey) == "" {
			return quotaCredentials{}, errors.New("auth.json 中未找到有效的 OPENAI_API_KEY")
		}
		return quotaCredentials{Mode: authModeAPIKey, APIKey: apiKey}, nil
	case authModeChatGPT:
		accessToken := strings.TrimSpace(auth.Tokens.AccessToken)
		accountID := strings.TrimSpace(auth.Tokens.AccountID)
		if accessToken == "" || accountID == "" {
			return quotaCredentials{}, errors.New("auth.json 中 ChatGPT access_token 或 account_id 缺失")
		}
		return quotaCredentials{Mode: authModeChatGPT, AccessToken: accessToken, AccountID: accountID}, nil
	default:
		return quotaCredentials{}, errors.New("auth.json 中 auth_mode 不受支持")
	}
}

func requestQuotaReset(serverURL string) (string, error) {
	endpoints, err := quotaEndpointsFor(serverURL)
	if err != nil {
		return "", err
	}
	credentials, err := readQuotaCredentials()
	if err != nil {
		return "", err
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return "", errors.New("无法创建单次重置请求")
	}
	// No connection reuse and no HTTP/2, so the transport can never silently
	// replay this side-effecting GET on a stale connection.
	transport := defaultTransport.Clone()
	transport.DisableKeepAlives = true
	transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	defer transport.CloseIdleConnections()
	return requestQuotaResetWithAuth(newQuotaClient(transport), credentials, endpoints.reset, endpoints.directReset)
}

func requestQuotaResetWithAuth(client *http.Client, credentials quotaCredentials, apiKeyEndpoint, chatGPTEndpoint string) (string, error) {
	endpoint := apiKeyEndpoint
	directMode := false
	var req *http.Request
	var err error
	switch credentials.Mode {
	case authModeAPIKey:
		req, err = http.NewRequest(http.MethodGet, endpoint, nil)
		if err == nil {
			req.Header.Set("User-Agent", userAgent())
			req.Header.Set("Authorization", "Bearer "+credentials.APIKey)
		}
	case authModeChatGPT:
		endpoint = chatGPTEndpoint
		directMode = true
		req, err = http.NewRequest(http.MethodGet, endpoint, nil)
		if err == nil {
			setDirectQuotaHeaders(req, credentials.AccessToken, credentials.AccountID)
		}
	default:
		return "", errors.New("auth.json 中 auth_mode 不受支持")
	}
	if err != nil {
		return "", err
	}
	if client == nil {
		return "", errors.New("无法创建单次重置请求")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("网络连接失败，无法判断服务端是否已消费额度")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("服务端返回 HTTP %d，无法确认重置结果", resp.StatusCode)
	}
	if directMode {
		var result quotaDirectResetResponse
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
			return "", errors.New("服务端已响应，但结果无法解析；无法确认是否已消费额度")
		}
		return formatQuotaResetResponse(quotaResetResponse{
			Success:         result.Success,
			Partial:         result.Partial,
			NominalAccounts: result.Accounts,
			Errors:          result.Errors,
		}), nil
	}
	var result quotaResetResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", errors.New("服务端已响应，但结果无法解析；无法确认是否已消费额度")
	}
	return formatQuotaResetResponse(result), nil
}

func formatQuotaResetResponse(result quotaResetResponse) string {
	lines := []string{"重置结果："}
	switch {
	case result.Success:
		lines = append(lines, "全部账户重置成功。")
	case result.Partial:
		lines = append(lines, "部分账户重置成功，其余账户未成功。")
	default:
		lines = append(lines, "服务端未报告重置成功。")
	}
	accounts := make([]string, 0, len(result.NominalAccounts))
	for email := range result.NominalAccounts {
		accounts = append(accounts, email)
	}
	sort.Strings(accounts)
	for _, email := range accounts {
		account := result.NominalAccounts[email]
		status := "失败"
		if account.Success {
			status = "成功"
		}
		line := email + "：" + status
		if detail := localizeResetMessage(account.Message); detail != "" {
			line += "（" + detail + "）"
		}
		lines = append(lines, line)
	}
	if len(result.NominalAccounts) == 0 {
		lines = append(lines, "服务端没有返回逐账户结果。")
	}
	if len(result.Errors) > 0 {
		lines = append(lines, fmt.Sprintf("另有 %d 条服务端错误信息。", len(result.Errors)))
	}
	return strings.Join(lines, "\n")
}

func localizeResetMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	switch message {
	case "Codex quota reset credit consumed":
		return "已消耗 1 次重置额度"
	case "Codex credentials are unavailable":
		return "服务端无法读取该账户凭据"
	case "unable to check Codex reset-credit availability":
		return "无法检查重置额度是否可用"
	case "invalid Codex reset-credit response":
		return "服务端收到的额度数据无效"
	case "no Codex quota reset credits are available":
		return "没有可用的重置额度"
	case "no selectable Codex reset-credit details are available":
		return "没有可供消费的重置额度明细"
	case "invalid Codex base URL":
		return "账户服务地址无效"
	case "could not create a reset request identifier":
		return "无法创建重置请求编号"
	case "Codex reset-credit consumption failed; the final account state may be uncertain":
		return "消费请求失败，最终账户状态可能不确定"
	case "Codex did not confirm reset-credit consumption":
		return "服务端未确认重置额度已消费"
	default:
		return "服务端返回了其他状态，请刷新额度确认"
	}
}

// parseAccounts reads an {"email": details, ...} object, keeping the server's
// account order. Anything other than a non-empty object yields no accounts.
func parseAccounts(raw json.RawMessage) []accountQuota {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 || value[0] != '{' {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	if _, err := decoder.Token(); err != nil {
		return nil
	}
	var accounts []accountQuota
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			break
		}
		email, _ := keyToken.(string)
		var details accountDetails
		if err := decoder.Decode(&details); err != nil {
			accounts = append(accounts, accountQuota{Email: email})
			break
		}
		accounts = append(accounts, accountFromDetails(email, details))
	}
	return accounts
}

func accountFromDetails(email string, details accountDetails) accountQuota {
	account := accountQuota{Email: email, Reset: details.ResetCredits}
	for _, group := range details.Groups {
		if group.DisplayName == "Codex" || !account.Primary.Known {
			for _, bucket := range group.Buckets {
				if bucket.RemainingFraction == nil {
					continue
				}
				percent := int(math.Round(*bucket.RemainingFraction * 100))
				if percent < 0 {
					percent = 0
				}
				if percent > 100 {
					percent = 100
				}
				resetAt := bucket.ResetTime
				if resetAt == "" {
					resetAt = bucket.ResetTimeSnake
				}
				quota := quotaBucket{Percent: percent, Known: true, ResetAt: parseQuotaResetTime(resetAt)}
				switch bucket.Window {
				case "primary":
					account.Primary = quota
				case "secondary":
					account.Secondary = quota
				}
			}
		}
	}
	return account
}

func bucketText(b quotaBucket) string {
	if !b.Known {
		return "--"
	}
	return fmt.Sprintf("%d%%", b.Percent)
}

func parseQuotaResetTime(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func quotaRemainingText(bucket quotaBucket, now time.Time) string {
	if bucket.ResetAt.IsZero() {
		return "剩余时间：--"
	}
	return "剩余时间：" + formatRemaining(bucket.ResetAt.Sub(now))
}

type quotaRecoveryNotice struct {
	title string
	text  string
}

type exhaustedWindow struct {
	email   string
	window  string
	label   string
	resetAt time.Time
}

// quotaRecoveryTracker remembers nominal-account windows seen at 0% and
// reports each one once when its reset time passes.
type quotaRecoveryTracker struct {
	exhausted map[string]exhaustedWindow
	announced map[string]time.Time
}

func newQuotaRecoveryTracker() *quotaRecoveryTracker {
	return &quotaRecoveryTracker{exhausted: map[string]exhaustedWindow{}, announced: map[string]time.Time{}}
}

// update records exhausted windows from the latest nominal accounts and
// returns notices for those whose reset time is now due. A window that comes
// back above 0% before its reset time (for example after consuming a reset
// credit) is dropped without a notice.
func (t *quotaRecoveryTracker) update(accounts []accountQuota, now time.Time) []quotaRecoveryNotice {
	for _, account := range accounts {
		for _, window := range []struct {
			key, label string
			bucket     quotaBucket
		}{{"primary", "5h", account.Primary}, {"secondary", "周", account.Secondary}} {
			key := account.Email + "\x00" + window.key
			bucket := window.bucket
			switch {
			case !bucket.Known:
			case bucket.Percent == 0 && !bucket.ResetAt.IsZero():
				if announced, ok := t.announced[key]; !ok || !announced.Equal(bucket.ResetAt) {
					t.exhausted[key] = exhaustedWindow{email: account.Email, window: window.key, label: window.label, resetAt: bucket.ResetAt}
				}
			case bucket.Percent > 0:
				if pending, ok := t.exhausted[key]; ok && pending.resetAt.After(now) {
					delete(t.exhausted, key)
				}
			}
		}
	}

	var due []exhaustedWindow
	for key, pending := range t.exhausted {
		if !pending.resetAt.After(now) {
			due = append(due, pending)
			t.announced[key] = pending.resetAt
			delete(t.exhausted, key)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].email != due[j].email {
			return due[i].email < due[j].email
		}
		return due[i].window < due[j].window
	})
	var notices []quotaRecoveryNotice
	for i := 0; i < len(due); {
		j, labels := i, []string{}
		for ; j < len(due) && due[j].email == due[i].email; j++ {
			labels = append(labels, due[j].label)
		}
		notices = append(notices, quotaRecoveryNotice{
			title: strings.Join(labels, "/") + "额度已恢复",
			text:  accountEmail(accountQuota{Email: due[i].email}) + " 已到达重置时间",
		})
		i = j
	}
	return notices
}

// nextDue returns the earliest pending reset time, or zero if none.
func (t *quotaRecoveryTracker) nextDue() time.Time {
	var next time.Time
	for _, pending := range t.exhausted {
		if next.IsZero() || pending.resetAt.Before(next) {
			next = pending.resetAt
		}
	}
	return next
}

// nextQuotaExpiry finds the earliest upcoming reset or credit expiry across the
// Codex snapshot and any extra reset times (Antigravity / Claude windows).
func nextQuotaExpiry(quota quotaSnapshot, now, handledThrough time.Time, extra ...time.Time) (time.Time, bool) {
	var next time.Time
	expired := false
	consider := func(at time.Time) {
		if at.IsZero() || (!handledThrough.IsZero() && !at.After(handledThrough)) {
			return
		}
		if !at.After(now) {
			expired = true
			return
		}
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	for _, accounts := range [][]accountQuota{quota.Nominal, quota.Actual} {
		for _, account := range accounts {
			consider(account.Primary.ResetAt)
			consider(account.Secondary.ResetAt)
			for _, raw := range account.Reset.ExpiresAt {
				consider(parseQuotaResetTime(raw))
			}
		}
	}
	for _, at := range extra {
		consider(at)
	}
	return next, expired
}

func shortError(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) > 46 {
		return string(runes[:43]) + "…"
	}
	return s
}

func configPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "CodexQuotaTrayMonitor", "settings.json")
}

func loadSettings() appSettings {
	var settings appSettings
	data, err := os.ReadFile(configPath())
	if err == nil {
		_ = json.Unmarshal(data, &settings)
	}
	if !validRefreshInterval(settings.RefreshMinutes) {
		settings.RefreshMinutes = 10
	}
	settings.ThemeMode = normalizedThemeMode(settings.ThemeMode)
	settings.ServerURL = strings.TrimSpace(settings.ServerURL)
	return settings
}

func validRefreshInterval(minutes int) bool {
	return minutes == 5 || minutes == 10 || minutes == 15 || minutes == 20 || minutes == 30
}

func refreshIntervalLabel(minutes int) string {
	return fmt.Sprintf("%d分钟", minutes)
}

func saveSettings(settings appSettings) {
	path := configPath()
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func startupEnabled() bool {
	keyPath := utf16Ptr("Software\\Microsoft\\Windows\\CurrentVersion\\Run")
	valueName := utf16Ptr("CodexQuotaTrayMonitor")
	var key uintptr
	r, _, _ := procRegOpenKeyEx.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(keyPath)), 0, keyQueryValue, uintptr(unsafe.Pointer(&key)))
	if r != 0 {
		return false
	}
	defer procRegCloseKey.Call(key)
	var size uint32
	r, _, _ = procRegQueryValueEx.Call(key, uintptr(unsafe.Pointer(valueName)), 0, 0, 0, uintptr(unsafe.Pointer(&size)))
	return r == 0
}

func setStartup(enabled bool) error {
	keyPath := utf16Ptr("Software\\Microsoft\\Windows\\CurrentVersion\\Run")
	valueName := utf16Ptr("CodexQuotaTrayMonitor")
	var key uintptr
	r, _, _ := procRegCreateKeyEx.Call(
		hkeyCurrentUser, uintptr(unsafe.Pointer(keyPath)), 0, 0, 0,
		keySetValue, 0, uintptr(unsafe.Pointer(&key)), 0,
	)
	if r != 0 {
		return fmt.Errorf("打开启动注册表项失败（代码 %d）", r)
	}
	defer procRegCloseKey.Call(key)
	if !enabled {
		r, _, _ = procRegDeleteValue.Call(key, uintptr(unsafe.Pointer(valueName)))
		if r != 0 && r != errorFileNotFound {
			return fmt.Errorf("删除启动项失败（代码 %d）", r)
		}
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := utf16Text("\"" + executable + "\"")
	r, _, _ = procRegSetValueEx.Call(
		key, uintptr(unsafe.Pointer(valueName)), 0, regSZ,
		uintptr(unsafe.Pointer(&command[0])), uintptr(uint32(len(command)*2)),
	)
	if r != 0 {
		return fmt.Errorf("写入启动项失败（代码 %d）", r)
	}
	return nil
}

func winError(operation string) error {
	err := syscall.GetLastError()
	if err == nil {
		return fmt.Errorf("%s 失败", operation)
	}
	return fmt.Errorf("%s 失败：%w", operation, err)
}

func showError(message string) {
	showMessageBox(0, message, mbIconError)
}

func showMessageBox(owner syscall.Handle, message string, flags uintptr) int32 {
	result, _, _ := procMessageBox.Call(uintptr(owner), uintptr(unsafe.Pointer(utf16Ptr(message))), uintptr(unsafe.Pointer(utf16Ptr(windowName))), flags)
	return int32(result)
}
