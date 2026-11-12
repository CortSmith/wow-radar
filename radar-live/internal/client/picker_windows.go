//go:build windows

package client

import (
	"context"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Flags and sizes of the Win32 open-file dialog (commdlg.h).
const (
	ofnHideReadOnly   = 0x00000004
	ofnNoChangeDir    = 0x00000008
	ofnPathMustExist  = 0x00000800
	ofnFileMustExist  = 0x00001000
	ofnExplorer       = 0x00080000
	pathBufferChars   = 32768 // the longest path Windows allows
	defaultPickerName = "Choose the WoW combat log"
)

var (
	comdlg32           = windows.NewLazySystemDLL("comdlg32.dll")
	procGetOpenFile    = comdlg32.NewProc("GetOpenFileNameW")
	procDialogErrorNum = comdlg32.NewProc("CommDlgExtendedError")
)

// openFileName mirrors OPENFILENAMEW; Go's struct layout rules produce the same padding as C on amd64.
type openFileName struct {
	structSize       uint32
	owner            windows.HWND
	instance         windows.Handle
	filter           *uint16
	customFilter     *uint16
	maxCustomFilter  uint32
	filterIndex      uint32
	file             *uint16
	maxFile          uint32
	fileTitle        *uint16
	maxFileTitle     uint32
	initialDir       *uint16
	title            *uint16
	flags            uint32
	fileOffset       uint16
	fileExtension    uint16
	defaultExtension *uint16
	customData       uintptr
	hook             uintptr
	templateName     *uint16
	reserved         unsafe.Pointer
	reserved2        uint32
	flagsEx          uint32
}

// windowsPicker shows the standard "Open" dialog (comdlg32 GetOpenFileNameW); no cgo, no COM.
type windowsPicker struct{}

// NewNativePicker returns the Windows "Open file" dialog.
func NewNativePicker() FilePicker { return windowsPicker{} }

// Pick shows the dialog and blocks until the user answers. The dialog is modal and cannot be
// interrupted by ctx; ctx is only checked before it opens.
func (windowsPicker) Pick(ctx context.Context, opts PickOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	runtime.LockOSThread() // a dialog's message loop must stay on one thread
	defer runtime.UnlockOSThread()

	buffer := make([]uint16, pathBufferChars)
	filter := buildFilter([]filterPair{{"Combat logs (WoWCombatLog*.txt)", "WoWCombatLog*.txt"}, {"Text files (*.txt)", "*.txt"}, {"All files (*.*)", "*.*"}})
	title := opts.Title
	if title == "" {
		title = defaultPickerName
	}
	ofn := openFileName{
		owner:       windows.GetForegroundWindow(),
		filter:      &filter[0],
		filterIndex: 1,
		file:        &buffer[0],
		maxFile:     uint32(len(buffer)),
		flags:       ofnExplorer | ofnFileMustExist | ofnPathMustExist | ofnHideReadOnly | ofnNoChangeDir,
	}
	ofn.structSize = uint32(unsafe.Sizeof(ofn))
	var err error
	if ofn.title, err = windows.UTF16PtrFromString(title); err != nil {
		return "", fmt.Errorf("dialog title: %w", err)
	}
	if dir := initialDir(opts.InitialPath); dir != "" {
		if ofn.initialDir, err = windows.UTF16PtrFromString(dir); err != nil {
			return "", fmt.Errorf("dialog folder: %w", err)
		}
	}
	return runDialog(&ofn, buffer)
}

// runDialog calls GetOpenFileNameW and converts its answer.
func runDialog(ofn *openFileName, buffer []uint16) (string, error) {
	ok, _, _ := procGetOpenFile.Call(uintptr(unsafe.Pointer(ofn)))
	if ok != 0 {
		return windows.UTF16ToString(buffer), nil
	}
	code, _, _ := procDialogErrorNum.Call()
	if code == 0 {
		return "", ErrPickCancelled
	}
	return "", fmt.Errorf("file dialog failed (CommDlgExtendedError 0x%X)", code)
}
