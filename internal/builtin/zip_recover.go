package builtin

import (
	stdzip "archive/zip"
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cryptozip "github.com/yeka/zip"
)

const (
	zipLocalHeaderSignature   = 0x04034b50
	zipCentralHeaderSignature = 0x02014b50
	zipEndSignature           = 0x06054b50

	// ZipCracker ?s uses Python string.punctuation.
	zipMaskDigits  = "0123456789"
	zipMaskLower   = "abcdefghijklmnopqrstuvwxyz"
	zipMaskUpper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	zipMaskSymbols = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"
)

type zipEntrySummary struct {
	Name       string
	Encrypted  bool
	Encryption string
	Size       uint64
	CRC32      uint32
}

type zipFlagOffsets struct {
	central int64
	local   int64
	clear   uint16
}

// ZipPasswordRecover implements a bounded, controller-local equivalent of the
// public ZipCracker capability surface. It is an independent Go implementation
// and does not copy the unlicensed upstream Python source.
func ZipPasswordRecover(rt Runtime, args map[string]string) (string, error) {
	if rt.IsRemote || rt.Exec != nil && rt.Exec.IsRemote() {
		return "", fmt.Errorf("ZIP 恢复只允许在控制端执行；请先用 file_download 将归档下载到本地")
	}
	action := strings.ToLower(strings.TrimSpace(args["action"]))
	if action == "" {
		action = "auto"
	}
	source := strings.TrimSpace(firstZipArg(args, "source", "path", "file"))
	if source == "" {
		return "", fmt.Errorf("source 必填；ZipCracker 兼容流程请传本地 ZIP 路径，例如 {\"action\":\"auto\",\"source\":\"/tmp/test01.zip\"}")
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", fmt.Errorf("读取 ZIP 失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("source 必须是控制端普通文件")
	}
	switch action {
	case "inspect":
		return inspectEncryptedZIP(source)
	case "repair":
		dest := strings.TrimSpace(args["dest"])
		if dest == "" {
			dest = source + ".repaired.zip"
		}
		return repairPseudoEncryptedZIP(source, dest)
	case "crc32":
		out, _, err := recoverZIPCRC32(source, args)
		return out, err
	case "recover":
		return recoverZIPPassword(source, args)
	case "auto":
		return autoZIPRecover(source, args)
	default:
		return "", fmt.Errorf("不支持的 action %q；可用 auto|inspect|repair|crc32|recover", action)
	}
}

func firstZipArg(args map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(args[key]); value != "" {
			return value
		}
	}
	return ""
}

// zipAllocateExtractDest keeps user-specified dest exclusive, but auto-picks
// source.extracted, source.extracted-2, ... when dest was omitted and the
// default path already exists from a previous run.
func zipAllocateExtractDest(source, dest string) (string, string, error) {
	specified := strings.TrimSpace(dest)
	if specified != "" {
		if _, err := os.Stat(specified); err == nil {
			return "", "", fmt.Errorf("dest 已存在，拒绝覆盖: %s", specified)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
		return specified, "", nil
	}
	base := source + ".extracted"
	for i := 1; i <= 99; i++ {
		candidate := base
		if i > 1 {
			candidate = base + "-" + strconv.Itoa(i)
		}
		_, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			note := ""
			if i > 1 {
				note = fmt.Sprintf("默认解压目录已存在，改用: %s", candidate)
			}
			return candidate, note, nil
		}
		if err != nil {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("无法分配解压目录：%s 及其 -2..-99 均已存在", base)
}

func inspectEncryptedZIP(source string) (string, error) {
	r, err := cryptozip.OpenReader(source)
	if err != nil {
		return "", fmt.Errorf("解析 ZIP 失败: %w", err)
	}
	defer r.Close()
	entries := make([]zipEntrySummary, 0, len(r.File))
	encrypted := 0
	hasAES := false
	templateHint := false
	for _, file := range r.File {
		method := "none"
		if file.IsEncrypted() {
			encrypted++
			method = "ZipCrypto"
			if file.Method == 99 {
				method = "WinZip AES"
				hasAES = true
			}
		}
		if zipLooksLikeKPATemplate(file.Name) {
			templateHint = true
		}
		entries = append(entries, zipEntrySummary{Name: file.Name, Encrypted: file.IsEncrypted(), Encryption: method, Size: file.UncompressedSize64, CRC32: file.CRC32})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ZIP 检查完成：%s\n条目=%d，加密条目=%d\n", source, len(entries), encrypted)
	short := 0
	for i, entry := range entries {
		if i >= 80 {
			fmt.Fprintf(&b, "… 另有 %d 个条目未显示\n", len(entries)-i)
			break
		}
		fmt.Fprintf(&b, "- %s · %d bytes · %s · CRC32=%08x\n", entry.Name, entry.Size, entry.Encryption, entry.CRC32)
		if entry.Size >= 1 && entry.Size <= 6 {
			short++
			fmt.Fprintf(&b, "  短明文候选：1～6 字节，CRC32 枚举恢复的是文件内容，不是口令。下一步 {\"action\":\"crc32\",\"source\":%q}\n", source)
		}
	}
	b.WriteString("\nZipCracker 解题顺序（必须始终带 source）：\n")
	fmt.Fprintf(&b, "1. 一次走完全流程：{\"action\":\"auto\",\"source\":%q,\"extract\":\"true\"}\n", source)
	if encrypted > 0 {
		fmt.Fprintf(&b, "2. 伪加密（用户说无密码/伪加密时优先）：{\"action\":\"repair\",\"source\":%q}\n", source)
	}
	if short > 0 {
		fmt.Fprintf(&b, "3. 短明文 CRC32：{\"action\":\"crc32\",\"source\":%q,\"extract\":\"true\"}\n", source)
	}
	fmt.Fprintf(&b, "4. 内置 6000 字典 + 1-6 位数字：{\"action\":\"recover\",\"source\":%q}\n", source)
	if encrypted > 0 {
		b.WriteString("提示：加密标志存在时不要先盲爆破。ZipCracker 会先尝试伪加密修复，再对短文件做 CRC32，最后才用内置字典。\n")
	} else {
		b.WriteString("结论：未发现加密条目，无需恢复口令。\n")
	}
	if hasAES {
		b.WriteString("WinZip AES：可用内置字典或掩码恢复口令；不支持 CRC32、已知明文或 bkcrack。\n")
	}
	if templateHint {
		b.WriteString("条目名像 png/zip/exe/pcapng 时，上游 ZipCracker 会走模板 KPA/bkcrack。DeepSentry 原生引擎未复刻该路径，请继续 auto/字典/掩码。\n")
	}
	return b.String(), nil
}

func zipLooksLikeKPATemplate(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png", ".exe", ".dll", ".pcap", ".pcapng", ".zip":
		return true
	default:
		return false
	}
}

func autoZIPRecover(source string, args map[string]string) (string, error) {
	var b strings.Builder
	if strings.TrimSpace(args["mask"]) != "" {
		b.WriteString("ZipCracker 兼容自动流程：伪加密修复 → 短明文 CRC32 → 用户提供的 mask → 必要时再走内置字典\n")
	} else {
		b.WriteString("ZipCracker 兼容自动流程：伪加密修复 → 短明文 CRC32 → 内置 6000 字典/数字掩码\n")
	}
	inspectOut, err := inspectEncryptedZIP(source)
	if err != nil {
		return "", err
	}
	b.WriteString(inspectOut)
	b.WriteString("\n")

	tmp, err := os.CreateTemp(filepath.Dir(source), filepath.Base(source)+".repaired-*.zip")
	if err != nil {
		return "", err
	}
	repairedPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(repairedPath)
	if repaired, repairErr := repairPseudoEncryptedZIP(source, repairedPath); repairErr == nil {
		b.WriteString(repaired + "\n")
		if zipBoolDefault(args["extract"], true) {
			dest, destNote, err := zipAllocateExtractDest(source, args["dest"])
			if err != nil {
				return "", fmt.Errorf("%s\n伪加密已修复，但解压失败: %w", strings.TrimSpace(b.String()), err)
			}
			if destNote != "" {
				b.WriteString(destNote + "\n")
			}
			if err := extractEncryptedZIP(repairedPath, dest, ""); err != nil {
				return "", fmt.Errorf("%s\n伪加密已修复，但解压失败: %w", strings.TrimSpace(b.String()), err)
			}
			fmt.Fprintf(&b, "安全解压目录: %s\n", dest)
		}
		return b.String(), nil
	} else {
		fmt.Fprintf(&b, "伪加密修复未成功，判定为真加密: %s\n", repairErr)
	}

	crcArgs := cloneZIPArgs(args)
	if strings.TrimSpace(crcArgs["extract"]) == "" {
		crcArgs["extract"] = "true"
	}
	if crcOut, all, crcErr := recoverZIPCRC32(source, crcArgs); crcErr == nil {
		b.WriteString(crcOut)
		if all {
			b.WriteString("全部短明文条目已恢复，跳过字典爆破。\n")
			return b.String(), nil
		}
	} else if !strings.Contains(crcErr.Error(), "没有 1～6 字节") {
		fmt.Fprintf(&b, "短明文 CRC32：%s\n", crcErr)
	}

	recoverArgs := cloneZIPArgs(args)
	if strings.TrimSpace(recoverArgs["extract"]) == "" {
		recoverArgs["extract"] = "true"
	}
	if mask := strings.TrimSpace(recoverArgs["mask"]); mask != "" && strings.TrimSpace(recoverArgs["dictionary"]) == "" {
		b.WriteString("使用用户提供的 mask 恢复口令: " + mask + "\n")
	}
	out, err := recoverZIPPassword(source, recoverArgs)
	if err != nil {
		return "", fmt.Errorf("%s\n%s", strings.TrimSpace(b.String()), err)
	}
	b.WriteString(out)
	return b.String(), nil
}

func cloneZIPArgs(args map[string]string) map[string]string {
	out := make(map[string]string, len(args))
	for key, value := range args {
		out[key] = value
	}
	return out
}

func recoverZIPPassword(source string, args map[string]string) (string, error) {
	file, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	probe, err := cryptozip.NewReader(file, info.Size())
	if err != nil {
		return "", fmt.Errorf("解析 ZIP 失败: %w", err)
	}
	targetIndex := -1
	var targetSize uint64
	for index, entry := range probe.File {
		if !entry.IsEncrypted() || entry.FileInfo().IsDir() {
			continue
		}
		if targetIndex < 0 || entry.UncompressedSize64 < targetSize {
			targetIndex, targetSize = index, entry.UncompressedSize64
		}
	}
	if targetIndex < 0 {
		return "", fmt.Errorf("归档没有可验证的加密普通文件；可用 action=inspect 查看详情")
	}

	workers := zipPositiveInt(args["workers"], runtime.NumCPU(), 64)
	maxAttempts := zipPositiveInt64(args["max_attempts"], 1_200_000, 100_000_000)
	timeout := time.Duration(zipPositiveInt(args["timeout_sec"], 300, 3600)) * time.Second
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	stop := make(chan struct{})
	jobs := make(chan string, workers*2)
	found := make(chan string, 1)
	errCh := make(chan error, 1)
	var once sync.Once
	var attempts atomic.Int64
	stopAll := func() { once.Do(func() { close(stop) }) }

	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reader, readerErr := cryptozip.NewReader(file, info.Size())
			if readerErr != nil {
				select {
				case errCh <- readerErr:
				default:
				}
				stopAll()
				return
			}
			entry := reader.File[targetIndex]
			for {
				select {
				case <-stop:
					return
				case candidate, ok := <-jobs:
					if !ok {
						return
					}
					attempts.Add(1)
					entry.SetPassword(candidate)
					rc, openErr := entry.Open()
					if openErr != nil {
						continue
					}
					_, readErr := io.Copy(io.Discard, rc)
					closeErr := rc.Close()
					if readErr == nil && closeErr == nil {
						select {
						case found <- candidate:
						default:
						}
						stopAll()
						return
					}
				}
			}
		}()
	}
	producerDone := make(chan error, 1)
	go func() {
		defer close(jobs)
		producerDone <- produceZIPCandidates(args, maxAttempts, jobs, stop)
	}()
	workersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(workersDone)
	}()

	var password string
	var producerErr error
	select {
	case password = <-found:
		stopAll()
	case producerErr = <-producerDone:
		<-workersDone
		select {
		case password = <-found:
		default:
		}
	case workerErr := <-errCh:
		stopAll()
		<-workersDone
		return "", fmt.Errorf("初始化 ZIP 恢复 worker 失败: %w", workerErr)
	case <-deadline.C:
		stopAll()
		<-workersDone
		return "", fmt.Errorf("ZIP 口令恢复超时：已尝试 %d 次（上限 %d，超时 %s）", attempts.Load(), maxAttempts, timeout)
	}
	stopAll()
	<-workersDone
	if producerErr != nil {
		return "", producerErr
	}
	if password == "" {
		return "", fmt.Errorf("在 %d 次候选内未恢复口令；请提供更匹配的 dictionary 或 mask", attempts.Load())
	}

	var b strings.Builder
	fmt.Fprintf(&b, "ZIP 口令恢复成功\n归档: %s\n验证条目: %s\n尝试次数: %d\n恢复口令: %q\n", source, probe.File[targetIndex].Name, attempts.Load(), password)
	if zipBool(args["extract"]) {
		dest, destNote, err := zipAllocateExtractDest(source, args["dest"])
		if err != nil {
			return "", fmt.Errorf("口令已恢复，但安全解压失败: %w", err)
		}
		if destNote != "" {
			b.WriteString(destNote + "\n")
		}
		if err := extractEncryptedZIP(source, dest, password); err != nil {
			return "", fmt.Errorf("口令已恢复，但安全解压失败: %w", err)
		}
		fmt.Fprintf(&b, "安全解压目录: %s\n", dest)
	}
	return b.String(), nil
}

func produceZIPCandidates(args map[string]string, limit int64, out chan<- string, stop <-chan struct{}) error {
	var emitted int64
	emit := func(candidate string) bool {
		if emitted >= limit {
			return false
		}
		emitted++
		select {
		case <-stop:
			return false
		case out <- candidate:
			return true
		}
	}
	hasStrategy := false
	if raw := args["passwords"]; strings.TrimSpace(raw) != "" {
		hasStrategy = true
		for _, candidate := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
			if !emit(strings.TrimSpace(candidate)) {
				return nil
			}
		}
	}
	if dictionary := strings.TrimSpace(args["dictionary"]); dictionary != "" {
		hasStrategy = true
		useFile := dictionary
		useBuiltin := isBuiltinZIPDictionary(dictionary)
		if useBuiltin {
			if _, err := os.Stat(dictionary); err == nil {
				useBuiltin = false
			} else {
				useFile = ""
			}
		}
		if useFile != "" {
			info, statErr := os.Stat(useFile)
			if statErr != nil {
				return fmt.Errorf("打开字典失败: %w", statErr)
			}
			var complete bool
			var err error
			if info.IsDir() {
				complete, err = emitZIPDictionaryDir(useFile, emit)
			} else {
				complete, err = emitZIPDictionaryFile(useFile, emit)
			}
			if err != nil {
				return err
			}
			if !complete {
				return nil
			}
		} else if !emitBuiltinZIPDictionary(emit) {
			return nil
		}
	}
	if mask := strings.TrimSpace(args["mask"]); mask != "" {
		hasStrategy = true
		sets, err := parseZIPMask(mask)
		if err != nil {
			return err
		}
		if !generateZIPMask(sets, emit) {
			return nil
		}
	}
	if !hasStrategy {
		if !emitBuiltinZIPDictionary(emit) {
			return nil
		}
		for length := 1; length <= 6; length++ {
			sets := make([][]rune, length)
			for i := range sets {
				sets[i] = []rune("0123456789")
			}
			if !generateZIPMask(sets, emit) {
				return nil
			}
		}
	}
	return nil
}

var errZIPDictStop = errors.New("zip dictionary enumeration stopped")

func emitZIPDictionaryDir(root string, emit func(string) bool) (bool, error) {
	complete := true
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".txt", ".dic", ".lst", ".dict":
		default:
			return nil
		}
		ok, fileErr := emitZIPDictionaryFile(path, emit)
		if fileErr != nil {
			return fileErr
		}
		if !ok {
			complete = false
			return errZIPDictStop
		}
		return nil
	})
	if errors.Is(err, errZIPDictStop) {
		return false, nil
	}
	return complete, err
}

func emitZIPDictionaryFile(path string, emit func(string) bool) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("打开字典失败: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		candidate := strings.TrimSuffix(scanner.Text(), "\r")
		if candidate != "" && !emit(candidate) {
			return false, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("读取字典失败: %w", err)
	}
	return true, nil
}

func parseZIPMask(mask string) ([][]rune, error) {
	sets := make([][]rune, 0, len(mask))
	runes := []rune(mask)
	for index := 0; index < len(runes); index++ {
		if runes[index] != '?' {
			sets = append(sets, []rune{runes[index]})
			continue
		}
		if index+1 >= len(runes) {
			return nil, fmt.Errorf("mask 末尾的 ? 缺少类型；字面问号请写 ??")
		}
		index++
		switch runes[index] {
		case 'd':
			sets = append(sets, []rune(zipMaskDigits))
		case 'l':
			sets = append(sets, []rune(zipMaskLower))
		case 'u':
			sets = append(sets, []rune(zipMaskUpper))
		case 's':
			sets = append(sets, []rune(zipMaskSymbols))
		case '?':
			sets = append(sets, []rune{'?'})
		default:
			return nil, fmt.Errorf("不支持的 mask 类型 ?%c；可用 ?d/?l/?u/?s/??", runes[index])
		}
	}
	if len(sets) == 0 {
		return nil, fmt.Errorf("mask 不能为空")
	}
	if len(sets) > 32 {
		return nil, fmt.Errorf("mask 最多 32 个字符位")
	}
	return sets, nil
}

func generateZIPMask(sets [][]rune, emit func(string) bool) bool {
	indices := make([]int, len(sets))
	buf := make([]rune, len(sets))
	for {
		for i := range sets {
			buf[i] = sets[i][indices[i]]
		}
		if !emit(string(buf)) {
			return false
		}
		position := len(indices) - 1
		for position >= 0 {
			indices[position]++
			if indices[position] < len(sets[position]) {
				break
			}
			indices[position] = 0
			position--
		}
		if position < 0 {
			return true
		}
	}
}

func extractEncryptedZIP(source, dest, password string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("dest 已存在，拒绝覆盖: %s", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	r, err := cryptozip.OpenReader(source)
	if err != nil {
		return err
	}
	defer r.Close()
	root, err := openArchiveRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	budget := newArchiveBudget()
	for _, entry := range r.File {
		name, err := safeArchiveName(entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if _, err := budget.allow(name, 0); err != nil {
				return err
			}
			if err := root.MkdirAll(name, 0o750); err != nil {
				return err
			}
			continue
		}
		if !entry.Mode().IsRegular() {
			return fmt.Errorf("拒绝解压非普通 ZIP 条目: %s", entry.Name)
		}
		if entry.UncompressedSize64 > uint64(^uint64(0)>>1) {
			return fmt.Errorf("ZIP 条目尺寸超出 int64: %s", entry.Name)
		}
		allowed, err := budget.allow(name, int64(entry.UncompressedSize64))
		if err != nil {
			return err
		}
		if err := root.MkdirAll(filepath.Dir(name), 0o750); err != nil {
			return err
		}
		if entry.IsEncrypted() {
			entry.SetPassword(password)
		}
		in, err := entry.Open()
		if err != nil {
			return err
		}
		out, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = in.Close()
			return err
		}
		n, copyErr := io.Copy(out, io.LimitReader(in, allowed+1))
		closeInErr := in.Close()
		closeOutErr := out.Close()
		if copyErr == nil {
			copyErr = closeInErr
		}
		if copyErr == nil {
			copyErr = closeOutErr
		}
		if copyErr == nil {
			copyErr = budget.addExtracted(name, n, allowed)
		}
		if copyErr != nil {
			_ = root.Remove(name)
			return copyErr
		}
	}
	return nil
}

func repairPseudoEncryptedZIP(source, dest string) (string, error) {
	sourceAbs, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return "", err
	}
	if sourceAbs == destAbs {
		return "", fmt.Errorf("repair 必须写入新文件，dest 不能与 source 相同")
	}
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("dest 已存在，拒绝覆盖: %s", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return "", err
	}
	offsets, err := zipEncryptionFlagOffsets(in, info.Size())
	if err != nil {
		return "", err
	}
	if len(offsets) == 0 {
		return "", fmt.Errorf("未发现加密标志，无需伪加密修复")
	}
	if err := os.MkdirAll(filepath.Dir(destAbs), 0o700); err != nil {
		return "", err
	}
	out, err := os.OpenFile(destAbs, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	removeOnError := true
	defer func() {
		_ = out.Close()
		if removeOnError {
			_ = os.Remove(destAbs)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return "", err
	}
	for _, offset := range offsets {
		for _, flagOffset := range []int64{offset.central, offset.local} {
			var raw [2]byte
			if _, err := out.ReadAt(raw[:], flagOffset); err != nil {
				return "", err
			}
			flags := binary.LittleEndian.Uint16(raw[:]) &^ offset.clear
			binary.LittleEndian.PutUint16(raw[:], flags)
			if _, err := out.WriteAt(raw[:], flagOffset); err != nil {
				return "", err
			}
		}
	}
	if err := out.Sync(); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := validatePlainZIP(destAbs); err != nil {
		return "", fmt.Errorf("清除标志后完整校验失败，这不是可安全修复的伪加密 ZIP: %w", err)
	}
	removeOnError = false
	return fmt.Sprintf("ZIP 伪加密修复成功\n源文件: %s\n新文件: %s\n修复条目: %d\n已完整读取全部条目并通过 CRC 校验；源文件未修改。", source, destAbs, len(offsets)), nil
}

func zipEncryptionFlagOffsets(reader io.ReaderAt, size int64) ([]zipFlagOffsets, error) {
	if size < 22 {
		return nil, fmt.Errorf("ZIP 文件过小")
	}
	tailSize := int64(22 + 65535)
	if tailSize > size {
		tailSize = size
	}
	tail := make([]byte, tailSize)
	if _, err := reader.ReadAt(tail, size-tailSize); err != nil {
		return nil, err
	}
	eocd := -1
	for index := len(tail) - 22; index >= 0; index-- {
		if binary.LittleEndian.Uint32(tail[index:index+4]) == zipEndSignature {
			commentLen := int(binary.LittleEndian.Uint16(tail[index+20 : index+22]))
			if index+22+commentLen == len(tail) {
				eocd = index
				break
			}
		}
	}
	if eocd < 0 {
		return nil, fmt.Errorf("未找到 ZIP EOCD")
	}
	record := tail[eocd:]
	if binary.LittleEndian.Uint16(record[4:6]) != 0 || binary.LittleEndian.Uint16(record[6:8]) != 0 {
		return nil, fmt.Errorf("不支持分卷 ZIP 伪加密修复")
	}
	records := binary.LittleEndian.Uint16(record[10:12])
	centralSize := binary.LittleEndian.Uint32(record[12:16])
	centralOffset := binary.LittleEndian.Uint32(record[16:20])
	if records == 0xffff || centralSize == 0xffffffff || centralOffset == 0xffffffff {
		return nil, fmt.Errorf("暂不支持 ZIP64 伪加密修复")
	}
	position := int64(centralOffset)
	centralEnd := position + int64(centralSize)
	offsets := make([]zipFlagOffsets, 0)
	for index := uint16(0); index < records; index++ {
		var header [46]byte
		if _, err := reader.ReadAt(header[:], position); err != nil {
			return nil, fmt.Errorf("读取中央目录失败: %w", err)
		}
		if binary.LittleEndian.Uint32(header[0:4]) != zipCentralHeaderSignature {
			return nil, fmt.Errorf("中央目录第 %d 项签名无效", index+1)
		}
		nameLen := int64(binary.LittleEndian.Uint16(header[28:30]))
		extraLen := int64(binary.LittleEndian.Uint16(header[30:32]))
		commentLen := int64(binary.LittleEndian.Uint16(header[32:34]))
		localOffset := int64(binary.LittleEndian.Uint32(header[42:46]))
		if localOffset+30 > size {
			return nil, fmt.Errorf("本地文件头偏移越界")
		}
		var local [30]byte
		if _, err := reader.ReadAt(local[:], localOffset); err != nil {
			return nil, err
		}
		if binary.LittleEndian.Uint32(local[0:4]) != zipLocalHeaderSignature {
			return nil, fmt.Errorf("本地文件头签名无效")
		}
		centralFlags := binary.LittleEndian.Uint16(header[8:10])
		localFlags := binary.LittleEndian.Uint16(local[6:8])
		if centralFlags&1 != 0 || localFlags&1 != 0 {
			clear := uint16(1)
			// ZipCracker's rewrite produces a normal ZIP. If CRC/sizes already
			// live in the local header, also drop the data-descriptor bit so
			// Go's archive/zip can validate; leaving 0x8 set yields checksum
			// errors even when the payload is plaintext deflate.
			localCRC := binary.LittleEndian.Uint32(local[14:18])
			localComp := binary.LittleEndian.Uint32(local[18:22])
			localUncomp := binary.LittleEndian.Uint32(local[22:26])
			if localCRC != 0 || localComp != 0 || localUncomp != 0 {
				clear |= 0x8
			}
			offsets = append(offsets, zipFlagOffsets{central: position + 8, local: localOffset + 6, clear: clear})
		}
		position += 46 + nameLen + extraLen + commentLen
		if position > centralEnd || position > size {
			return nil, fmt.Errorf("中央目录长度越界")
		}
	}
	return offsets, nil
}

func validatePlainZIP(path string) error {
	r, err := stdzip.OpenReader(path)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, entry := range r.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return fmt.Errorf("%s: %w", entry.Name, err)
		}
		_, readErr := io.Copy(io.Discard, rc)
		closeErr := rc.Close()
		if readErr != nil {
			return fmt.Errorf("%s: %w", entry.Name, readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("%s: %w", entry.Name, closeErr)
		}
	}
	return nil
}

func zipPositiveInt(raw string, fallback, maximum int) int {
	value := argInt(map[string]string{"value": raw}, "value", fallback, maximum)
	if value < 1 {
		return fallback
	}
	return value
}

func zipPositiveInt64(raw string, fallback, maximum int64) int64 {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	var value int64
	if _, err := fmt.Sscan(strings.TrimSpace(raw), &value); err != nil || value <= 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}

func zipBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
