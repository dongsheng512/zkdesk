//go:build windows

package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/text/encoding/simplifiedchinese"
)

type rpcRequest struct {
	ID     string         `json:"id"`
	Action string         `json:"action"`
	Params map[string]any `json:"params"`
}

type rpcResponse struct {
	ID      string `json:"id"`
	Event   string `json:"event,omitempty"`
	Current int    `json:"current,omitempty"`
	Total   int    `json:"total,omitempty"`
	Message string `json:"message,omitempty"`
	OK      bool   `json:"ok"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}

func emitProgress(reqID string, current, total int, message string) {
	if reqID == "" {
		return
	}
	writeJSONLine(rpcResponse{
		ID:      reqID,
		Event:   "progress",
		Current: current,
		Total:   total,
		Message: message,
	})
}

type dispParams struct {
	rgvarg            uintptr
	rgdispidNamedArgs uintptr
	cArgs             uint32
	cNamedArgs        uint32
}

// ==================== COM / VARIANT Helpers ====================

func vI4(v int32) ole.VARIANT {
	return ole.NewVariant(ole.VT_I4, int64(v))
}

func vBool(v bool) ole.VARIANT {
	if v {
		return ole.NewVariant(ole.VT_BOOL, -1)
	}
	return ole.NewVariant(ole.VT_BOOL, 0)
}

func vBSTR(s string) (ole.VARIANT, *int16) {
	p := ole.SysAllocStringLen(s)
	return ole.NewVariant(ole.VT_BSTR, int64(uintptr(unsafe.Pointer(p)))), p
}

func vBSTRRef(p **int16) ole.VARIANT {
	return ole.NewVariant(ole.VT_BSTR|ole.VT_BYREF, int64(uintptr(unsafe.Pointer(p))))
}

func vI4Ref(p *int32) ole.VARIANT {
	return ole.NewVariant(ole.VT_I4|ole.VT_BYREF, int64(uintptr(unsafe.Pointer(p))))
}

func vBoolRef(p *int16) ole.VARIANT {
	return ole.NewVariant(ole.VT_BOOL|ole.VT_BYREF, int64(uintptr(unsafe.Pointer(p))))
}

func readAndFreeBSTR(p **int16) string {
	if p == nil || *p == nil {
		return ""
	}
	s := ole.BstrToString((*uint16)(unsafe.Pointer(*p)))
	_ = ole.SysFreeString(*p)
	*p = nil
	return s
}

func boolVal(v ole.VARIANT) bool {
	if v.VT == ole.VT_BOOL {
		return v.Val != 0
	}
	if b, ok := v.Value().(bool); ok {
		return b
	}
	return v.Val != 0
}

func invokeExact(disp *ole.IDispatch, method string, args ...ole.VARIANT) (ole.VARIANT, error) {
	dispid, err := disp.GetIDsOfName([]string{method})
	if err != nil {
		return ole.VARIANT{}, fmt.Errorf("GetIDsOfName(%s): %w", method, err)
	}
	var dp dispParams
	n := len(args)
	var rev []ole.VARIANT
	if n > 0 {
		rev = make([]ole.VARIANT, n)
		for i := 0; i < n; i++ {
			rev[n-1-i] = args[i]
		}
		dp.rgvarg = uintptr(unsafe.Pointer(&rev[0]))
		dp.cArgs = uint32(n)
	}

	var result ole.VARIANT
	var excep ole.EXCEPINFO
	var argErr uint32
	ole.VariantInit(&result)

	hr, _, _ := syscall.SyscallN(
		disp.VTable().Invoke,
		uintptr(unsafe.Pointer(disp)),
		uintptr(dispid[0]),
		uintptr(unsafe.Pointer(ole.IID_NULL)),
		uintptr(0),
		uintptr(ole.DISPATCH_METHOD),
		uintptr(unsafe.Pointer(&dp)),
		uintptr(unsafe.Pointer(&result)),
		uintptr(unsafe.Pointer(&excep)),
		uintptr(unsafe.Pointer(&argErr)),
	)
	if hr != 0 {
		return result, fmt.Errorf("Invoke(%s) hr=0x%x argErr=%d excep=%s", method, hr, argErr, excep.String())
	}
	return result, nil
}

// ==================== Encoding & Param Helpers ====================

// Windows-1252 printable mappings for bytes 0x80..0x9F.
// Under Wine's default CP1252 ANSI codepage, zkemkeeper.dll passes raw GBK bytes
// through MultiByteToWideChar(CP_ACP=1252), which maps 0x80..0x9F to code points > 0xFF
// (e.g. 0x88 -> U+02C6 'ˆ' in "翀", 0x86 -> U+2020 '†' in "喆").
var cp1252ByteToRune = [32]rune{
	0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, // 0x80..0x87
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F, // 0x88..0x8F
	0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, // 0x90..0x97
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178, // 0x98..0x9F
}

var cp1252RuneToByte = func() map[rune]byte {
	m := make(map[rune]byte, 32)
	for i, r := range cp1252ByteToRune {
		if r > 0xFF {
			m[r] = byte(0x80 + i)
		}
	}
	return m
}()

func decodeZKStr(s string) string {
	if idx := strings.IndexByte(s, 0); idx >= 0 {
		s = s[:idx]
	}
	if s == "" {
		return ""
	}
	raw := make([]byte, 0, len(s))
	hasHigh := false
	for _, r := range s {
		if r <= 0xFF {
			if r >= 0x80 {
				hasHigh = true
			}
			raw = append(raw, byte(r))
		} else if b, ok := cp1252RuneToByte[r]; ok {
			hasHigh = true
			raw = append(raw, b)
		} else {
			return s
		}
	}
	if !hasHigh {
		return s
	}
	out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err != nil {
		return s
	}
	return string(out)
}

func encodeZKStr(s string) string {
	if s == "" {
		return ""
	}
	gbkBytes, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(s))
	if err != nil {
		return s
	}
	runes := make([]rune, len(gbkBytes))
	for i, b := range gbkBytes {
		if b >= 0x80 && b <= 0x9F {
			runes[i] = cp1252ByteToRune[b-0x80]
		} else {
			runes[i] = rune(b)
		}
	}
	return string(runes)
}

func unixToWinePath(p string) string {
	if p == "" {
		return ""
	}
	if len(p) >= 2 && p[1] == ':' {
		return p
	}
	return "Z:" + strings.ReplaceAll(p, "/", `\`)
}

func paramStr(m map[string]any, k, def string) string {
	if m == nil {
		return def
	}
	v, ok := m[k]
	if !ok || v == nil {
		return def
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatInt(int64(x), 10)
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

func paramInt(m map[string]any, k string, def int) int {
	if m == nil {
		return def
	}
	v, ok := m[k]
	if !ok || v == nil {
		return def
	}
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n
		}
	}
	return def
}

func paramBool(m map[string]any, k string, def bool) bool {
	if m == nil {
		return def
	}
	v, ok := m[k]
	if !ok || v == nil {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func paramStrSlice(m map[string]any, k string) []string {
	if m == nil {
		return nil
	}
	raw, ok := m[k].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, it := range raw {
		s := strings.TrimSpace(fmt.Sprint(it))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ==================== ZK Session Wrapper ====================

type zkHandle struct {
	unk  *ole.IUnknown
	disp *ole.IDispatch
}

func newZKHandle() (*zkHandle, error) {
	unk, err := oleutil.CreateObject("zkemkeeper.ZKEM.1")
	if err != nil {
		return nil, fmt.Errorf("CreateObject(zkemkeeper.ZKEM.1): %w", err)
	}
	disp, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		unk.Release()
		return nil, fmt.Errorf("QueryInterface(IDispatch): %w", err)
	}
	return &zkHandle{unk: unk, disp: disp}, nil
}

func (h *zkHandle) close() {
	if h == nil {
		return
	}
	if h.disp != nil {
		_, _ = invokeExact(h.disp, "Disconnect")
		h.disp.Release()
		h.disp = nil
	}
	if h.unk != nil {
		h.unk.Release()
		h.unk = nil
	}
}

func (h *zkHandle) getLastError() int32 {
	var code int32
	r, err := invokeExact(h.disp, "GetLastError", vI4Ref(&code))
	if err == nil {
		_ = r.Clear()
	}
	return code
}

func isSafeFileToken(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." || strings.Contains(s, "..") || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (h *zkHandle) connect(ip string, port, password int) error {
	rPwd, err := invokeExact(h.disp, "SetCommPassword", vI4(int32(password)))
	if err != nil {
		return err
	}
	_ = rPwd.Clear()
	vIP, pIP := vBSTR(ip)
	r, err := invokeExact(h.disp, "Connect_Net", vIP, vI4(int32(port)))
	_ = ole.SysFreeString(pIP)
	if err != nil {
		return err
	}
	ok := boolVal(r)
	_ = r.Clear()
	if !ok {
		return fmt.Errorf("连接设备 %s:%d 失败 (SDK错误码=%d)", ip, port, h.getLastError())
	}
	return nil
}

func (h *zkHandle) enableDevice(mn int32, enabled bool) {
	r, err := invokeExact(h.disp, "EnableDevice", vI4(mn), vBool(enabled))
	if err == nil {
		_ = r.Clear()
	}
}

func (h *zkHandle) refreshData(mn int32) {
	r, err := invokeExact(h.disp, "RefreshData", vI4(mn))
	if err == nil {
		_ = r.Clear()
	}
}

func (h *zkHandle) getSysOption(mn int32, key string) string {
	vK, pK := vBSTR(key)
	var outPtr *int16
	r, err := invokeExact(h.disp, "GetSysOption", vI4(mn), vK, vBSTRRef(&outPtr))
	_ = ole.SysFreeString(pK)
	if err != nil {
		_ = readAndFreeBSTR(&outPtr)
		return ""
	}
	ok := boolVal(r)
	_ = r.Clear()
	val := strings.TrimSpace(decodeZKStr(readAndFreeBSTR(&outPtr)))
	if !ok {
		return ""
	}
	return val
}

func (h *zkHandle) getStatus(mn int32, code int32) int {
	var val int32
	r, err := invokeExact(h.disp, "GetDeviceStatus", vI4(mn), vI4(code), vI4Ref(&val))
	if err != nil {
		return 0
	}
	ok := boolVal(r)
	_ = r.Clear()
	if !ok {
		return 0
	}
	return int(val)
}

func (h *zkHandle) getDeviceTime(mn int32) (string, bool) {
	var y, m, d, hr, mi, sec int32
	r, err := invokeExact(h.disp, "GetDeviceTime",
		vI4(mn), vI4Ref(&y), vI4Ref(&m), vI4Ref(&d), vI4Ref(&hr), vI4Ref(&mi), vI4Ref(&sec))
	if err != nil {
		return "", false
	}
	ok := boolVal(r)
	_ = r.Clear()
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d", y, m, d, hr, mi, sec), true
}

func (h *zkHandle) getSerialNumber(mn int32) string {
	var ptr *int16
	r, err := invokeExact(h.disp, "GetSerialNumber", vI4(mn), vBSTRRef(&ptr))
	if err != nil {
		_ = readAndFreeBSTR(&ptr)
		return ""
	}
	ok := boolVal(r)
	_ = r.Clear()
	val := strings.TrimSpace(decodeZKStr(readAndFreeBSTR(&ptr)))
	if !ok {
		return ""
	}
	return val
}

func (h *zkHandle) getFirmwareVersion(mn int32) string {
	var ptr *int16
	r, err := invokeExact(h.disp, "GetFirmwareVersion", vI4(mn), vBSTRRef(&ptr))
	if err != nil {
		_ = readAndFreeBSTR(&ptr)
		return ""
	}
	ok := boolVal(r)
	_ = r.Clear()
	val := strings.TrimSpace(decodeZKStr(readAndFreeBSTR(&ptr)))
	if !ok {
		return ""
	}
	return val
}

func (h *zkHandle) getStrCardNumber() string {
	var ptr *int16
	r, err := invokeExact(h.disp, "GetStrCardNumber", vBSTRRef(&ptr))
	if err != nil {
		_ = readAndFreeBSTR(&ptr)
		return ""
	}
	ok := boolVal(r)
	_ = r.Clear()
	val := strings.TrimSpace(readAndFreeBSTR(&ptr))
	if !ok || val == "0" {
		return ""
	}
	return val
}

func (h *zkHandle) ssrGetUserInfo(mn int32, pin string) (name, pwd string, priv int32, enabled bool, card string, ok bool) {
	vPin, pPin := vBSTR(pin)
	var namePtr, pwdPtr *int16
	var pr int32
	var en int16
	r, err := invokeExact(h.disp, "SSR_GetUserInfo",
		vI4(mn), vPin, vBSTRRef(&namePtr), vBSTRRef(&pwdPtr), vI4Ref(&pr), vBoolRef(&en))
	_ = ole.SysFreeString(pPin)
	if err != nil {
		_ = readAndFreeBSTR(&namePtr)
		_ = readAndFreeBSTR(&pwdPtr)
		return "", "", 0, false, "", false
	}
	ok = boolVal(r)
	_ = r.Clear()
	name = strings.TrimSpace(decodeZKStr(readAndFreeBSTR(&namePtr)))
	pwd = strings.TrimSpace(decodeZKStr(readAndFreeBSTR(&pwdPtr)))
	priv = pr
	enabled = en != 0
	if ok {
		card = h.getStrCardNumber()
	}
	return
}

// normalizeCardForSet converts an empty card string into "0" so SetStrCardNumber
// always overwrites zkemkeeper.dll's internal card buffer before SSR_SetUserInfo.
func normalizeCardForSet(card string) string {
	c := strings.TrimSpace(card)
	if c == "" {
		return "0"
	}
	return c
}

func (h *zkHandle) setStrCardNumber(card string) {
	vC, pC := vBSTR(normalizeCardForSet(card))
	r, err := invokeExact(h.disp, "SetStrCardNumber", vC)
	_ = ole.SysFreeString(pC)
	if err == nil {
		_ = r.Clear()
	}
}

func (h *zkHandle) listFacePhotoPINs(mn int32) map[string]bool {
	pins := map[string]bool{}
	var ptr *int16
	r, err := invokeExact(h.disp, "GetUserFacePhotoNames", vI4(mn), vBSTRRef(&ptr))
	if err != nil {
		_ = readAndFreeBSTR(&ptr)
		return pins
	}
	ok := boolVal(r)
	_ = r.Clear()
	raw := decodeZKStr(readAndFreeBSTR(&ptr))
	if !ok || raw == "" {
		return pins
	}
	raw = strings.NewReplacer("\r", "\t", "\n", "\t").Replace(raw)
	for _, item := range strings.Split(raw, "\t") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.HasSuffix(strings.ToLower(item), ".jpg") {
			item = item[:len(item)-4]
		}
		if item != "" {
			pins[item] = true
		}
	}
	return pins
}

func (h *zkHandle) getFacePhotoBase64(mn int32, pin string) (string, int, error) {
	vName, pName := vBSTR(pin + ".jpg")
	var dataPtr *int16
	var length int32
	r, err := invokeExact(h.disp, "GetUserFacePhotoByNameEx", vI4(mn), vName, vBSTRRef(&dataPtr), vI4Ref(&length))
	_ = ole.SysFreeString(pName)
	if err != nil {
		_ = readAndFreeBSTR(&dataPtr)
		return "", 0, err
	}
	ok := boolVal(r)
	_ = r.Clear()
	b64 := strings.TrimSpace(readAndFreeBSTR(&dataPtr))
	if !ok || b64 == "" {
		return "", 0, fmt.Errorf("设备上未找到工号 %s 的人脸照片", pin)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", 0, fmt.Errorf("人脸 Base64 解码失败: %w", err)
	}
	return b64, len(raw), nil
}

func (h *zkHandle) getUserTmpExStr(mn int32, pin string, fid int32) (int32, string, bool) {
	vPin, pPin := vBSTR(pin)
	var flag, tmpLen int32
	var tmpPtr *int16
	r, err := invokeExact(h.disp, "GetUserTmpExStr",
		vI4(mn), vPin, vI4(fid), vI4Ref(&flag), vBSTRRef(&tmpPtr), vI4Ref(&tmpLen))
	_ = ole.SysFreeString(pPin)
	if err != nil {
		_ = readAndFreeBSTR(&tmpPtr)
		return 0, "", false
	}
	ok := boolVal(r)
	_ = r.Clear()
	tpl := readAndFreeBSTR(&tmpPtr)
	if !ok || tpl == "" {
		return 0, "", false
	}
	return flag, tpl, true
}

func (h *zkHandle) readDeviceInfo(mn int32) map[string]any {
	sn := h.getSerialNumber(mn)
	fw := h.getFirmwareVersion(mn)
	devTime, _ := h.getDeviceTime(mn)
	platform := h.getSysOption(mn, "~Platform")
	faceVer := h.getSysOption(mn, "ZKFaceVersion")
	bioVer := h.getSysOption(mn, "BiometricVersion")

	userCnt := h.getStatus(mn, 2)
	fpCnt := h.getStatus(mn, 3)
	logCnt := h.getStatus(mn, 6)
	faceCnt := h.getStatus(mn, 21)

	var photoCnt int32
	if r, err := invokeExact(h.disp, "GetUserFacePhotoCount", vI4(mn), vI4Ref(&photoCnt)); err == nil {
		if boolVal(r) && int(photoCnt) > faceCnt {
			faceCnt = int(photoCnt)
		}
		_ = r.Clear()
	}

	mode := "standard"
	if detectMaskMode(fw, platform) {
		mode = "with_mask"
	}

	return map[string]any{
		"sn":           sn,
		"firmware":     fw,
		"platform":     platform,
		"device_time":  devTime,
		"face_version": faceVer,
		"bio_version":  bioVer,
		"user_count":   userCnt,
		"fp_count":     fpCnt,
		"face_count":   faceCnt,
		"log_count":    logCnt,
		"log_mode":     mode,
	}
}

func detectMaskMode(firmware, platform string) bool {
	for _, yr := range []string{"2023", "2024", "2025", "2026", "2027", "2028"} {
		if strings.Contains(firmware, yr) {
			return true
		}
	}
	return strings.Contains(platform, "ZAM230")
}

// ==================== Worker Core ====================

type sessionEntry struct {
	handle   *zkHandle
	ip       string
	port     int
	password int
}

type Worker struct {
	sessions map[string]*sessionEntry
}

func newWorker() (*Worker, error) {
	// Verify COM registration immediately
	probe, err := newZKHandle()
	if err != nil {
		return nil, err
	}
	probe.close()
	return &Worker{sessions: map[string]*sessionEntry{}}, nil
}

func sessionKey(ip string, port int) string {
	return fmt.Sprintf("%s:%d", strings.TrimSpace(ip), port)
}

func (w *Worker) acquire(ip string, port, password int, mn int32) (*zkHandle, bool, error) {
	if mn <= 0 {
		mn = 1
	}
	key := sessionKey(ip, port)
	sess, ok := w.sessions[key]
	if !ok {
		return nil, false, fmt.Errorf("设备 %s:%d 连接已断开，请先在「设备总览」中重新连接", ip, port)
	}
	if _, alive := sess.handle.getDeviceTime(mn); alive {
		return sess.handle, true, nil
	}
	sess.handle.close()
	h, err := newZKHandle()
	if err != nil {
		delete(w.sessions, key)
		return nil, false, err
	}
	if err := h.connect(ip, port, password); err != nil {
		h.close()
		delete(w.sessions, key)
		return nil, false, err
	}
	sess.handle = h
	return h, true, nil
}

func (w *Worker) release(h *zkHandle, persistent bool) {
	if persistent {
		return
	}
	h.close()
}

func (w *Worker) closeSession(key string) bool {
	sess, ok := w.sessions[key]
	if !ok {
		return false
	}
	delete(w.sessions, key)
	sess.handle.close()
	return true
}

func (w *Worker) closeAllSessions() int {
	n := len(w.sessions)
	for k, s := range w.sessions {
		delete(w.sessions, k)
		s.handle.close()
	}
	return n
}

func (w *Worker) connParams(p map[string]any) (string, int, int, int32) {
	return paramStr(p, "ip", ""), paramInt(p, "port", 4370), paramInt(p, "password", 0), int32(paramInt(p, "machine_number", 1))
}

// ==================== Action Handlers ====================

func (w *Worker) handlePing(_ map[string]any) (any, error) {
	keys := make([]string, 0, len(w.sessions))
	for k := range w.sessions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return map[string]any{
		"status":         "ready",
		"sdk":            "zkemkeeper.ZKEM.1 (Go x64)",
		"connected_keys": keys,
	}, nil
}

func (w *Worker) handleConnectDevice(p map[string]any) (any, error) {
	ip, port, pwd, mn := w.connParams(p)
	key := sessionKey(ip, port)

	var h *zkHandle
	var err error
	if _, exists := w.sessions[key]; exists {
		h, _, err = w.acquire(ip, port, pwd, mn)
		if err != nil {
			return nil, err
		}
	} else {
		h, err = newZKHandle()
		if err != nil {
			return nil, err
		}
		if err := h.connect(ip, port, pwd); err != nil {
			h.close()
			return nil, err
		}
		w.sessions[key] = &sessionEntry{handle: h, ip: ip, port: port, password: pwd}
	}

	info := h.readDeviceInfo(mn)
	info["connected"] = true
	return info, nil
}

func (w *Worker) handleDisconnectDevice(p map[string]any) (any, error) {
	ip, port, _, _ := w.connParams(p)
	was := w.closeSession(sessionKey(ip, port))
	return map[string]any{"connected": false, "was_connected": was}, nil
}

func (w *Worker) handleDisconnectAll(_ map[string]any) (any, error) {
	closed := w.closeAllSessions()
	return map[string]any{"closed": closed}, nil
}

func (w *Worker) handleDeviceInfo(p map[string]any) (any, error) {
	ip, port, pwd, mn := w.connParams(p)
	h, persist, err := w.acquire(ip, port, pwd, mn)
	if err != nil {
		return nil, err
	}
	defer w.release(h, persist)

	info := h.readDeviceInfo(mn)
	info["connected"] = persist
	return info, nil
}

func (w *Worker) handleListUsers(p map[string]any) (any, error) {
	ip, port, pwd, mn := w.connParams(p)
	includeFP := paramBool(p, "include_fp", true)

	h, persist, err := w.acquire(ip, port, pwd, mn)
	if err != nil {
		return nil, err
	}
	defer w.release(h, persist)

	facePINs := h.listFacePhotoPINs(mn)
	if r, err := invokeExact(h.disp, "ReadAllUserID", vI4(mn)); err == nil {
		_ = r.Clear()
	}
	if includeFP {
		if r, err := invokeExact(h.disp, "ReadAllTemplate", vI4(mn)); err == nil {
			_ = r.Clear()
		}
	}

	users := make([]map[string]any, 0)
	for {
		var pinPtr, namePtr, pwdPtr *int16
		var priv int32
		var enabled int16
		r, err := invokeExact(h.disp, "SSR_GetAllUserInfo",
			vI4(mn), vBSTRRef(&pinPtr), vBSTRRef(&namePtr), vBSTRRef(&pwdPtr), vI4Ref(&priv), vBoolRef(&enabled))
		if err != nil {
			_ = readAndFreeBSTR(&pinPtr)
			_ = readAndFreeBSTR(&namePtr)
			_ = readAndFreeBSTR(&pwdPtr)
			break
		}
		more := boolVal(r)
		_ = r.Clear()
		pin := strings.TrimSpace(readAndFreeBSTR(&pinPtr))
		name := strings.TrimSpace(decodeZKStr(readAndFreeBSTR(&namePtr)))
		upwd := strings.TrimSpace(decodeZKStr(readAndFreeBSTR(&pwdPtr)))
		if !more {
			break
		}
		if pin == "" {
			continue
		}
		card := h.getStrCardNumber()
		fpIndices := make([]int, 0)
		if includeFP {
			for fid := int32(0); fid < 10; fid++ {
				if _, _, ok := h.getUserTmpExStr(mn, pin, fid); ok {
					fpIndices = append(fpIndices, int(fid))
				}
			}
		}
		users = append(users, map[string]any{
			"pin":        pin,
			"name":       name,
			"password":   upwd,
			"privilege":  int(priv),
			"enabled":    enabled != 0,
			"card":       card,
			"has_face":   facePINs[pin],
			"fp_indices": fpIndices,
			"fp_count":   len(fpIndices),
		})
	}

	faceList := make([]string, 0, len(facePINs))
	for pin := range facePINs {
		faceList = append(faceList, pin)
	}
	sort.Strings(faceList)
	return map[string]any{
		"users":           users,
		"face_photo_pins": faceList,
		"total":           len(users),
	}, nil
}

func (w *Worker) handleGetFacePhoto(p map[string]any) (any, error) {
	ip, port, pwd, mn := w.connParams(p)
	pin := paramStr(p, "pin", "")
	h, persist, err := w.acquire(ip, port, pwd, mn)
	if err != nil {
		return nil, err
	}
	defer w.release(h, persist)

	b64, size, err := h.getFacePhotoBase64(mn, pin)
	if err != nil {
		return nil, err
	}
	return map[string]any{"pin": pin, "base64": b64, "size": size}, nil
}

func (w *Worker) handleDownloadFaces(reqID string, p map[string]any) (any, error) {
	ip, port, pwd, mn := w.connParams(p)
	outDir := paramStr(p, "out_dir", "")
	pins := paramStrSlice(p, "pins")

	winOutDir := unixToWinePath(outDir)
	if err := os.MkdirAll(winOutDir, 0o755); err != nil {
		return nil, err
	}

	h, persist, err := w.acquire(ip, port, pwd, mn)
	if err != nil {
		return nil, err
	}
	defer w.release(h, persist)

	if len(pins) == 0 {
		for pin := range h.listFacePhotoPINs(mn) {
			pins = append(pins, pin)
		}
		sort.Strings(pins)
	}

	downloaded := make([]map[string]any, 0)
	failed := make([]map[string]any, 0)
	total := len(pins)
	for i, pin := range pins {
		emitProgress(reqID, i+1, total, fmt.Sprintf("正在下载人脸 %s.jpg (%d/%d)...", pin, i+1, total))
		if !isSafeFileToken(pin) {
			failed = append(failed, map[string]any{"pin": pin, "error": "非法工号，跳过写入"})
			continue
		}
		b64, _, err := h.getFacePhotoBase64(mn, pin)
		if err != nil {
			failed = append(failed, map[string]any{"pin": pin, "error": err.Error()})
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			failed = append(failed, map[string]any{"pin": pin, "error": err.Error()})
			continue
		}
		destWin := filepath.Join(winOutDir, pin+".jpg")
		if err := os.WriteFile(destWin, raw, 0o644); err != nil {
			failed = append(failed, map[string]any{"pin": pin, "error": err.Error()})
			continue
		}
		downloaded = append(downloaded, map[string]any{
			"pin":  pin,
			"path": outDir + "/" + pin + ".jpg",
			"size": len(raw),
		})
	}
	return map[string]any{"downloaded": downloaded, "failed": failed}, nil
}

func (w *Worker) handleUpsertUser(p map[string]any) (any, error) {
	ip, port, pwd, mn := w.connParams(p)
	pin := paramStr(p, "pin", "")
	name := paramStr(p, "name", "")
	upwd := paramStr(p, "user_password", "")
	priv := int32(paramInt(p, "privilege", 0))
	enabled := paramBool(p, "enabled", true)
	card := paramStr(p, "card", "")
	preserveCard := paramBool(p, "preserve_card_if_empty", true)
	preservePwd := paramBool(p, "preserve_pwd_if_empty", true)

	h, persist, err := w.acquire(ip, port, pwd, mn)
	if err != nil {
		return nil, err
	}
	defer w.release(h, persist)

	// Preserve existing card / password on device when left blank so renaming never wipes them
	if (preserveCard && card == "") || (preservePwd && upwd == "") {
		if _, oldPwd, _, _, oldCard, found := h.ssrGetUserInfo(mn, pin); found {
			if preserveCard && card == "" && oldCard != "" {
				card = oldCard
			}
			if preservePwd && upwd == "" && oldPwd != "" {
				upwd = oldPwd
			}
		}
	}

	h.enableDevice(mn, false)
	defer h.enableDevice(mn, true)

	// Always set card buffer explicitly ("0" when empty) so a previous user's card
	// cached in zkemkeeper.dll can never bleed into this user.
	h.setStrCardNumber(card)
	vPin, pPin := vBSTR(pin)
	vName, pName := vBSTR(encodeZKStr(name))
	vPwd, pPwd := vBSTR(encodeZKStr(upwd))
	r, err := invokeExact(h.disp, "SSR_SetUserInfo", vI4(mn), vPin, vName, vPwd, vI4(priv), vBool(enabled))
	_ = ole.SysFreeString(pPin)
	_ = ole.SysFreeString(pName)
	_ = ole.SysFreeString(pPwd)
	if err != nil {
		return nil, err
	}
	ok := boolVal(r)
	_ = r.Clear()
	if !ok {
		return nil, fmt.Errorf("写入员工 %s 失败 (SDK错误码=%d)", pin, h.getLastError())
	}
	h.refreshData(mn)
	return map[string]any{"pin": pin, "ok": true}, nil
}

func (w *Worker) handleClearUserBiometric(p map[string]any) (any, error) {
	ip, port, pwd, mn := w.connParams(p)
	pin := paramStr(p, "pin", "")
	target := paramStr(p, "target", "")

	h, persist, err := w.acquire(ip, port, pwd, mn)
	if err != nil {
		return nil, err
	}
	defer w.release(h, persist)

	h.enableDevice(mn, false)
	defer h.enableDevice(mn, true)

	switch target {
	case "fingerprints":
		cleared := 0
		for fid := int32(0); fid < 10; fid++ {
			vPin, pPin := vBSTR(pin)
			if r, err := invokeExact(h.disp, "SSR_DeleteEnrollData", vI4(mn), vPin, vI4(fid)); err == nil {
				if boolVal(r) {
					cleared++
				}
				_ = r.Clear()
			}
			_ = ole.SysFreeString(pPin)
		}
		h.refreshData(mn)
		return map[string]any{"pin": pin, "target": target, "cleared": cleared}, nil
	case "password":
		oldName, _, oldPriv, oldEn, oldCard, found := h.ssrGetUserInfo(mn, pin)
		if !found {
			return nil, fmt.Errorf("设备上未找到工号 %s", pin)
		}
		h.setStrCardNumber(oldCard)
		vPin, pPin := vBSTR(pin)
		vName, pName := vBSTR(encodeZKStr(oldName))
		vPwd, pPwd := vBSTR("")
		r, err := invokeExact(h.disp, "SSR_SetUserInfo", vI4(mn), vPin, vName, vPwd, vI4(oldPriv), vBool(oldEn))
		_ = ole.SysFreeString(pPin)
		_ = ole.SysFreeString(pName)
		_ = ole.SysFreeString(pPwd)
		if err != nil {
			return nil, err
		}
		ok := boolVal(r)
		_ = r.Clear()
		if !ok {
			return nil, fmt.Errorf("清空密码失败 (SDK错误码=%d)", h.getLastError())
		}
		h.refreshData(mn)
		return map[string]any{"pin": pin, "target": target, "ok": true}, nil
	case "face":
		vPhoto, pPhoto := vBSTR(pin + ".jpg")
		if r, err := invokeExact(h.disp, "DeleteUserPhoto", vI4(mn), vPhoto); err == nil {
			_ = r.Clear()
		}
		_ = ole.SysFreeString(pPhoto)
		vPin, pPin := vBSTR(pin)
		if r, err := invokeExact(h.disp, "DelUserFace", vI4(mn), vPin, vI4(50)); err == nil {
			_ = r.Clear()
		}
		_ = ole.SysFreeString(pPin)
		h.refreshData(mn)
		return map[string]any{"pin": pin, "target": target, "ok": true}, nil
	default:
		return nil, fmt.Errorf("不支持的清理类型: %s", target)
	}
}

func (w *Worker) handleDeleteUser(p map[string]any) (any, error) {
	ip, port, pwd, mn := w.connParams(p)
	pin := paramStr(p, "pin", "")

	h, persist, err := w.acquire(ip, port, pwd, mn)
	if err != nil {
		return nil, err
	}
	defer w.release(h, persist)

	h.enableDevice(mn, false)
	defer h.enableDevice(mn, true)

	vPin, pPin := vBSTR(pin)
	r, err := invokeExact(h.disp, "SSR_DeleteEnrollData", vI4(mn), vPin, vI4(12))
	_ = ole.SysFreeString(pPin)
	if err != nil {
		return nil, err
	}
	ok := boolVal(r)
	_ = r.Clear()
	if !ok {
		return nil, fmt.Errorf("删除员工 %s 失败 (SDK错误码=%d)", pin, h.getLastError())
	}
	vPhoto, pPhoto := vBSTR(pin + ".jpg")
	if r2, err2 := invokeExact(h.disp, "DeleteUserPhoto", vI4(mn), vPhoto); err2 == nil {
		_ = r2.Clear()
	}
	_ = ole.SysFreeString(pPhoto)
	h.refreshData(mn)
	return map[string]any{"pin": pin, "ok": true}, nil
}

func (w *Worker) handleUploadFacePhoto(p map[string]any) (any, error) {
	ip, port, pwd, mn := w.connParams(p)
	pin := strings.TrimSpace(paramStr(p, "pin", ""))
	if !isSafeFileToken(pin) {
		return nil, fmt.Errorf("工号格式非法: %q", pin)
	}
	srcUnix := paramStr(p, "file_path", "")
	srcWin := unixToWinePath(srcUnix)

	raw, err := os.ReadFile(srcWin)
	if err != nil {
		return nil, fmt.Errorf("读取照片文件失败: %w", err)
	}

	uploadPath := srcWin
	var tempDir string
	if !strings.EqualFold(filepath.Base(srcWin), pin+".jpg") {
		tempDir, err = os.MkdirTemp("", "zk_face_")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tempDir)
		uploadPath = filepath.Join(tempDir, pin+".jpg")
		if err := os.WriteFile(uploadPath, raw, 0o644); err != nil {
			return nil, err
		}
	}

	h, persist, err := w.acquire(ip, port, pwd, mn)
	if err != nil {
		return nil, err
	}
	defer w.release(h, persist)

	h.enableDevice(mn, false)
	defer h.enableDevice(mn, true)

	vPath, pPath := vBSTR(uploadPath)
	r, err := invokeExact(h.disp, "SendUserFacePhoto", vI4(mn), vPath)
	_ = ole.SysFreeString(pPath)
	if err != nil {
		return nil, err
	}
	ok := boolVal(r)
	_ = r.Clear()
	if !ok {
		return nil, fmt.Errorf("上传人脸照片 %s.jpg 失败 (SDK错误码=%d，请确认照片为清晰正面人像且工号已存在)", pin, h.getLastError())
	}
	h.refreshData(mn)
	return map[string]any{"pin": pin, "ok": true}, nil
}

func (w *Worker) handleDeleteFacePhoto(p map[string]any) (any, error) {
	ip, port, pwd, mn := w.connParams(p)
	pin := paramStr(p, "pin", "")

	h, persist, err := w.acquire(ip, port, pwd, mn)
	if err != nil {
		return nil, err
	}
	defer w.release(h, persist)

	h.enableDevice(mn, false)
	defer h.enableDevice(mn, true)

	okPhoto := false
	vPhoto, pPhoto := vBSTR(pin + ".jpg")
	if r, err := invokeExact(h.disp, "DeleteUserPhoto", vI4(mn), vPhoto); err == nil {
		okPhoto = boolVal(r)
		_ = r.Clear()
	}
	_ = ole.SysFreeString(pPhoto)

	okFace := false
	vPin, pPin := vBSTR(pin)
	if r, err := invokeExact(h.disp, "DelUserFace", vI4(mn), vPin, vI4(50)); err == nil {
		okFace = boolVal(r)
		_ = r.Clear()
	}
	_ = ole.SysFreeString(pPin)

	h.refreshData(mn)
	return map[string]any{"pin": pin, "deleted_photo": okPhoto, "deleted_face": okFace}, nil
}

func (w *Worker) handleReadUserBundle(reqID string, p map[string]any) (any, error) {
	ip, port, pwd, mn := w.connParams(p)
	pins := paramStrSlice(p, "pins")
	wantFP := paramBool(p, "include_fp", true)
	wantFace := paramBool(p, "include_face", true)

	pinSet := map[string]bool{}
	for _, x := range pins {
		pinSet[x] = true
	}

	h, persist, err := w.acquire(ip, port, pwd, mn)
	if err != nil {
		return nil, err
	}
	defer w.release(h, persist)

	facePINs := map[string]bool{}
	if wantFace {
		facePINs = h.listFacePhotoPINs(mn)
	}
	if r, err := invokeExact(h.disp, "ReadAllUserID", vI4(mn)); err == nil {
		_ = r.Clear()
	}
	if wantFP {
		if r, err := invokeExact(h.disp, "ReadAllTemplate", vI4(mn)); err == nil {
			_ = r.Clear()
		}
	}

	bundles := make([]map[string]any, 0)
	matched := 0
	totalExpected := len(pinSet)
	for {
		var pinPtr, namePtr, pwdPtr *int16
		var priv int32
		var enabled int16
		r, err := invokeExact(h.disp, "SSR_GetAllUserInfo",
			vI4(mn), vBSTRRef(&pinPtr), vBSTRRef(&namePtr), vBSTRRef(&pwdPtr), vI4Ref(&priv), vBoolRef(&enabled))
		if err != nil {
			_ = readAndFreeBSTR(&pinPtr)
			_ = readAndFreeBSTR(&namePtr)
			_ = readAndFreeBSTR(&pwdPtr)
			break
		}
		more := boolVal(r)
		_ = r.Clear()
		pin := strings.TrimSpace(readAndFreeBSTR(&pinPtr))
		name := strings.TrimSpace(decodeZKStr(readAndFreeBSTR(&namePtr)))
		upwd := strings.TrimSpace(decodeZKStr(readAndFreeBSTR(&pwdPtr)))
		if !more {
			break
		}
		if len(pinSet) > 0 && !pinSet[pin] {
			continue
		}
		matched++
		if totalExpected > 0 {
			emitProgress(reqID, matched, totalExpected, fmt.Sprintf("正在读取源设备数据：%s (%s) %d/%d", name, pin, matched, totalExpected))
		}
		card := h.getStrCardNumber()

		fps := make([]map[string]any, 0)
		if wantFP {
			for fid := int32(0); fid < 10; fid++ {
				if flag, tpl, ok := h.getUserTmpExStr(mn, pin, fid); ok {
					fps = append(fps, map[string]any{
						"finger_id": int(fid),
						"flag":      int(flag),
						"template":  tpl,
					})
				}
			}
		}

		faceB64 := ""
		if wantFace && facePINs[pin] {
			if b64, _, err := h.getFacePhotoBase64(mn, pin); err == nil {
				faceB64 = b64
			}
		}

		bundles = append(bundles, map[string]any{
			"pin":          pin,
			"name":         name,
			"password":     upwd,
			"privilege":    int(priv),
			"enabled":      enabled != 0,
			"card":         card,
			"fingerprints": fps,
			"face_base64":  faceB64,
		})
	}
	return map[string]any{"bundles": bundles}, nil
}

func (w *Worker) handleWriteUserBundle(reqID string, p map[string]any) (any, error) {
	ip, port, pwd, mn := w.connParams(p)
	syncInfo := paramBool(p, "sync_info", true)
	syncFP := paramBool(p, "sync_fp", true)
	syncFace := paramBool(p, "sync_face", true)
	preserveCard := paramBool(p, "preserve_card_if_empty", true)
	preservePwd := paramBool(p, "preserve_pwd_if_empty", true)

	rawBundles, _ := p["bundles"].([]any)
	tempDir, err := os.MkdirTemp("", "zk_sync_")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)

	h, persist, err := w.acquire(ip, port, pwd, mn)
	if err != nil {
		return nil, err
	}
	defer w.release(h, persist)

	h.enableDevice(mn, false)
	defer h.enableDevice(mn, true)

	results := make([]map[string]any, 0, len(rawBundles))
	total := len(rawBundles)
	for idx, rb := range rawBundles {
		b, ok := rb.(map[string]any)
		if !ok {
			continue
		}
		pin := paramStr(b, "pin", "")
		uname := paramStr(b, "name", "")
		emitProgress(reqID, idx+1, total, fmt.Sprintf("正在同步写入 %s (%s) %d/%d...", uname, pin, idx+1, total))
		infoOK := false
		fpSynced := 0
		faceOK := false
		errs := make([]string, 0)

		if syncInfo {
			upwd := paramStr(b, "password", "")
			card := paramStr(b, "card", "")
			if (preserveCard && card == "") || (preservePwd && upwd == "") {
				if _, existPwd, _, _, existCard, ok := h.ssrGetUserInfo(mn, pin); ok {
					if preserveCard && card == "" {
						card = existCard
					}
					if preservePwd && upwd == "" {
						upwd = existPwd
					}
				}
			}
			h.setStrCardNumber(card)
			vPin, pPin := vBSTR(pin)
			vName, pName := vBSTR(encodeZKStr(paramStr(b, "name", "")))
			vPwd, pPwd := vBSTR(encodeZKStr(upwd))
			r, err := invokeExact(h.disp, "SSR_SetUserInfo",
				vI4(mn), vPin, vName, vPwd, vI4(int32(paramInt(b, "privilege", 0))), vBool(paramBool(b, "enabled", true)))
			_ = ole.SysFreeString(pPin)
			_ = ole.SysFreeString(pName)
			_ = ole.SysFreeString(pPwd)
			if err != nil {
				errs = append(errs, "基础信息异常: "+err.Error())
			} else {
				infoOK = boolVal(r)
				_ = r.Clear()
				if !infoOK {
					errs = append(errs, fmt.Sprintf("基础信息写入失败(err=%d)", h.getLastError()))
				}
			}
		}

		if syncFP {
			if rawFPs, ok := b["fingerprints"].([]any); ok {
				for _, rfp := range rawFPs {
					fp, ok := rfp.(map[string]any)
					if !ok {
						continue
					}
					fid := int32(paramInt(fp, "finger_id", 0))
					flag := int32(paramInt(fp, "flag", 1))
					tpl := paramStr(fp, "template", "")
					if tpl == "" {
						continue
					}
					vPin, pPin := vBSTR(pin)
					vTpl, pTpl := vBSTR(tpl)
					okFP := false
					if r, err := invokeExact(h.disp, "SetUserTmpExStr", vI4(mn), vPin, vI4(fid), vI4(flag), vTpl); err == nil {
						okFP = boolVal(r)
						_ = r.Clear()
					}
					if !okFP {
						if r, err := invokeExact(h.disp, "SSR_SetUserTmpStr", vI4(mn), vPin, vI4(fid), vTpl); err == nil {
							okFP = boolVal(r)
							_ = r.Clear()
						}
					}
					_ = ole.SysFreeString(pPin)
					_ = ole.SysFreeString(pTpl)
					if okFP {
						fpSynced++
					} else {
						errs = append(errs, fmt.Sprintf("指纹#%d写入失败(err=%d)", fid, h.getLastError()))
					}
				}
			}
		}

		if syncFace {
			if b64 := paramStr(b, "face_base64", ""); b64 != "" {
				if !isSafeFileToken(pin) {
					errs = append(errs, "人脸跳过: 非法工号")
				} else if raw, err := base64.StdEncoding.DecodeString(b64); err != nil {
					errs = append(errs, "人脸解码失败: "+err.Error())
				} else {
					fp := filepath.Join(tempDir, pin+".jpg")
					if err := os.WriteFile(fp, raw, 0o600); err != nil {
						errs = append(errs, "临时写人脸失败: "+err.Error())
					} else {
						vPath, pPath := vBSTR(fp)
						r, err := invokeExact(h.disp, "SendUserFacePhoto", vI4(mn), vPath)
						_ = ole.SysFreeString(pPath)
						_ = os.Remove(fp)
						if err != nil {
							errs = append(errs, "人脸上传异常: "+err.Error())
						} else {
							faceOK = boolVal(r)
							_ = r.Clear()
							if !faceOK {
								errs = append(errs, fmt.Sprintf("人脸上传失败(err=%d)", h.getLastError()))
							}
						}
					}
				}
			}
		}

		results = append(results, map[string]any{
			"pin":       pin,
			"info_ok":   infoOK,
			"fp_synced": fpSynced,
			"face_ok":   faceOK,
			"errors":    errs,
		})
	}

	h.refreshData(mn)
	return map[string]any{"results": results}, nil
}

func (h *zkHandle) readLogsMethod(mn int32, useMask bool) ([]map[string]any, int) {
	logs := make([]map[string]any, 0)
	invalid := 0
	for {
		var pinPtr, tempPtr *int16
		var vm, st, yy, mm, dd, hr, mi, sec, wc, mask int32
		var r ole.VARIANT
		var err error
		if useMask {
			r, err = invokeExact(h.disp, "SSR_GetGeneralLogDataWithMask",
				vI4(mn), vBSTRRef(&pinPtr), vI4Ref(&vm), vI4Ref(&st),
				vI4Ref(&yy), vI4Ref(&mm), vI4Ref(&dd), vI4Ref(&hr), vI4Ref(&mi), vI4Ref(&sec),
				vI4Ref(&wc), vI4Ref(&mask), vBSTRRef(&tempPtr))
		} else {
			r, err = invokeExact(h.disp, "SSR_GetGeneralLogData",
				vI4(mn), vBSTRRef(&pinPtr), vI4Ref(&vm), vI4Ref(&st),
				vI4Ref(&yy), vI4Ref(&mm), vI4Ref(&dd), vI4Ref(&hr), vI4Ref(&mi), vI4Ref(&sec),
				vI4Ref(&wc))
		}
		if err != nil {
			_ = readAndFreeBSTR(&pinPtr)
			_ = readAndFreeBSTR(&tempPtr)
			break
		}
		more := boolVal(r)
		_ = r.Clear()
		pin := strings.TrimSpace(readAndFreeBSTR(&pinPtr))
		_ = readAndFreeBSTR(&tempPtr)
		if !more {
			break
		}
		if pin != "" && yy >= 2005 && yy <= 2035 && mm >= 1 && mm <= 12 && dd >= 1 && dd <= 31 && hr >= 0 && hr <= 23 && mi >= 0 && mi <= 59 && sec >= 0 && sec <= 59 {
			logs = append(logs, map[string]any{
				"pin":         pin,
				"timestamp":   fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d", yy, mm, dd, hr, mi, sec),
				"verify_mode": int(vm),
				"in_out_mode": int(st),
				"work_code":   int(wc),
			})
		} else {
			invalid++
		}
	}
	return logs, invalid
}

func (w *Worker) handlePullLogs(p map[string]any) (any, error) {
	ip, port, pwd, mn := w.connParams(p)
	startTime := paramStr(p, "start_time", "")
	endTime := paramStr(p, "end_time", "")
	forcedMode := paramStr(p, "log_mode", "auto")

	h, persist, err := w.acquire(ip, port, pwd, mn)
	if err != nil {
		return nil, err
	}
	defer w.release(h, persist)

	fw := h.getFirmwareVersion(mn)
	platform := h.getSysOption(mn, "~Platform")

	// Read user names for direct enrichment
	userNames := map[string]string{}
	if r, err := invokeExact(h.disp, "ReadAllUserID", vI4(mn)); err == nil {
		_ = r.Clear()
		for {
			var pinPtr, namePtr, pwdPtr *int16
			var priv int32
			var enabled int16
			ur, err := invokeExact(h.disp, "SSR_GetAllUserInfo",
				vI4(mn), vBSTRRef(&pinPtr), vBSTRRef(&namePtr), vBSTRRef(&pwdPtr), vI4Ref(&priv), vBoolRef(&enabled))
			if err != nil {
				_ = readAndFreeBSTR(&pinPtr)
				_ = readAndFreeBSTR(&namePtr)
				_ = readAndFreeBSTR(&pwdPtr)
				break
			}
			more := boolVal(ur)
			_ = ur.Clear()
			pin := strings.TrimSpace(readAndFreeBSTR(&pinPtr))
			name := strings.TrimSpace(decodeZKStr(readAndFreeBSTR(&namePtr)))
			_ = readAndFreeBSTR(&pwdPtr)
			if !more {
				break
			}
			if pin != "" {
				userNames[pin] = name
			}
		}
	}

	primaryMask := detectMaskMode(fw, platform)
	if forcedMode == "with_mask" {
		primaryMask = true
	} else if forcedMode == "standard" {
		primaryMask = false
	}

	h.enableDevice(mn, false)
	defer h.enableDevice(mn, true)

	readTime := func() (bool, error) {
		vS, pS := vBSTR(startTime)
		vE, pE := vBSTR(endTime)
		r, err := invokeExact(h.disp, "ReadTimeGLogData", vI4(mn), vS, vE)
		_ = ole.SysFreeString(pS)
		_ = ole.SysFreeString(pE)
		if err != nil {
			return false, err
		}
		ok := boolVal(r)
		_ = r.Clear()
		return ok, nil
	}

	ok, err := readTime()
	if err != nil {
		return nil, err
	}
	modeUsed := "standard"
	if primaryMask {
		modeUsed = "with_mask"
	}
	if !ok {
		code := h.getLastError()
		if code == 0 || code == -1 {
			return map[string]any{"logs": []any{}, "mode_used": modeUsed, "invalid_dropped": 0}, nil
		}
		return nil, fmt.Errorf("读取时间段考勤失败 (SDK错误码=%d)", code)
	}

	logs, invalid := h.readLogsMethod(mn, primaryMask)
	if invalid > 0 && invalid >= len(logs) {
		if okRetry, _ := readTime(); okRetry {
			altLogs, altInvalid := h.readLogsMethod(mn, !primaryMask)
			if len(altLogs) > len(logs) || altInvalid < invalid {
				logs, invalid = altLogs, altInvalid
				if !primaryMask {
					modeUsed = "with_mask"
				} else {
					modeUsed = "standard"
				}
			}
		}
	}

	filtered := make([]map[string]any, 0, len(logs))
	for _, it := range logs {
		ts, _ := it["timestamp"].(string)
		if ts >= startTime && ts <= endTime {
			pin, _ := it["pin"].(string)
			it["name"] = userNames[pin]
			filtered = append(filtered, it)
		}
	}

	return map[string]any{
		"logs":            filtered,
		"mode_used":       modeUsed,
		"invalid_dropped": invalid,
	}, nil
}

func (w *Worker) handleSyncTime(p map[string]any) (any, error) {
	ip, port, pwd, mn := w.connParams(p)
	year := int32(paramInt(p, "year", 0))
	month := int32(paramInt(p, "month", 0))
	day := int32(paramInt(p, "day", 0))
	hour := int32(paramInt(p, "hour", 0))
	minute := int32(paramInt(p, "minute", 0))
	second := int32(paramInt(p, "second", 0))

	h, persist, err := w.acquire(ip, port, pwd, mn)
	if err != nil {
		return nil, err
	}
	defer w.release(h, persist)

	ok := false
	if year >= 2020 {
		if r, err := invokeExact(h.disp, "SetDeviceTime2",
			vI4(mn), vI4(year), vI4(month), vI4(day), vI4(hour), vI4(minute), vI4(second)); err == nil {
			ok = boolVal(r)
			_ = r.Clear()
		}
	}
	if !ok {
		r, err := invokeExact(h.disp, "SetDeviceTime", vI4(mn))
		if err != nil {
			return nil, fmt.Errorf("校时调用失败: %w", err)
		}
		ok = boolVal(r)
		_ = r.Clear()
	}
	if !ok {
		return nil, fmt.Errorf("同步设备时间失败 (SDK错误码=%d)", h.getLastError())
	}

	devTime, _ := h.getDeviceTime(mn)
	return map[string]any{
		"ok":          true,
		"device_time": devTime,
		"connected":   persist,
	}, nil
}

func (w *Worker) dispatch(req rpcRequest) (any, error) {
	switch req.Action {
	case "ping":
		return w.handlePing(req.Params)
	case "connect_device":
		return w.handleConnectDevice(req.Params)
	case "disconnect_device":
		return w.handleDisconnectDevice(req.Params)
	case "disconnect_all":
		return w.handleDisconnectAll(req.Params)
	case "device_info":
		return w.handleDeviceInfo(req.Params)
	case "list_users":
		return w.handleListUsers(req.Params)
	case "get_face_photo":
		return w.handleGetFacePhoto(req.Params)
	case "download_faces":
		return w.handleDownloadFaces(req.ID, req.Params)
	case "upsert_user":
		return w.handleUpsertUser(req.Params)
	case "clear_user_biometric":
		return w.handleClearUserBiometric(req.Params)
	case "delete_user":
		return w.handleDeleteUser(req.Params)
	case "upload_face_photo":
		return w.handleUploadFacePhoto(req.Params)
	case "delete_face_photo":
		return w.handleDeleteFacePhoto(req.Params)
	case "read_user_bundle":
		return w.handleReadUserBundle(req.ID, req.Params)
	case "write_user_bundle":
		return w.handleWriteUserBundle(req.ID, req.Params)
	case "pull_logs":
		return w.handlePullLogs(req.Params)
	case "sync_time":
		return w.handleSyncTime(req.Params)
	default:
		return nil, fmt.Errorf("未知操作: %s", req.Action)
	}
}

func writeJSONLine(v any) {
	b, _ := json.Marshal(v)
	_, _ = os.Stdout.Write(append(b, '\n'))
}

func main() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		writeJSONLine(rpcResponse{ID: "__init__", OK: false, Error: "CoInitializeEx: " + err.Error()})
		os.Exit(1)
	}
	defer ole.CoUninitialize()

	w, err := newWorker()
	if err != nil {
		writeJSONLine(rpcResponse{ID: "__init__", OK: false, Error: err.Error()})
		os.Exit(1)
	}
	defer w.closeAllSessions()

	writeJSONLine(rpcResponse{ID: "__ready__", OK: true, Data: map[string]string{"status": "ready"}})

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 256*1024), 64*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			writeJSONLine(rpcResponse{ID: "", OK: false, Error: "JSON 解析失败: " + err.Error()})
			continue
		}
		if req.Action == "shutdown" {
			w.closeAllSessions()
			writeJSONLine(rpcResponse{ID: req.ID, OK: true, Data: map[string]any{}})
			break
		}
		data, err := w.dispatch(req)
		if err != nil {
			writeJSONLine(rpcResponse{ID: req.ID, OK: false, Error: err.Error()})
		} else {
			writeJSONLine(rpcResponse{ID: req.ID, OK: true, Data: data})
		}
	}
}
