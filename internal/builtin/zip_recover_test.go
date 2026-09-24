package builtin

import (
	stdzip "archive/zip"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cryptozip "github.com/yeka/zip"
)

func TestZIPPasswordRecoverSupportsZipCryptoAndAES(t *testing.T) {
	methods := []struct {
		name   string
		method cryptozip.EncryptionMethod
	}{
		{"zipcrypto", cryptozip.StandardEncryption},
		{"aes256", cryptozip.AES256Encryption},
	}
	for _, test := range methods {
		t.Run(test.name, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), test.name+".zip")
			writeEncryptedZIPFixture(t, source, "hawk2026", test.method)
			out, err := ZipPasswordRecover(Runtime{}, map[string]string{
				"action": "recover", "source": source, "passwords": "wrong\nhawk2026", "workers": "2", "max_attempts": "10", "timeout_sec": "5",
			})
			if err != nil {
				t.Fatalf("recover %s: %v", test.name, err)
			}
			if !strings.Contains(out, `恢复口令: "hawk2026"`) {
				t.Fatalf("recovered output missing password: %s", out)
			}
		})
	}
}

func TestZIPPasswordRecoverMaskAndSafeExtract(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "mask.zip")
	dest := filepath.Join(root, "out")
	writeEncryptedZIPFixture(t, source, "team07", cryptozip.StandardEncryption)
	out, err := ZipPasswordRecover(Runtime{}, map[string]string{
		"action": "recover", "source": source, "mask": "team?d?d", "workers": "3", "extract": "true", "dest": dest, "timeout_sec": "5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `恢复口令: "team07"`) {
		t.Fatalf("unexpected recovery output: %s", out)
	}
	data, err := os.ReadFile(filepath.Join(dest, "secret.txt"))
	if err != nil || string(data) != "classified evidence" {
		t.Fatalf("safe extract data=%q err=%v", data, err)
	}
}

func TestZIPRepairPseudoEncryptionWritesNewValidatedArchive(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "pseudo.zip")
	dest := filepath.Join(root, "repaired.zip")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	writer := cryptozip.NewWriter(file)
	entry, err := writer.Create("evidence.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("plain but flagged")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	markZIPFixturePseudoEncrypted(t, source)

	out, err := ZipPasswordRecover(Runtime{}, map[string]string{"action": "repair", "source": source, "dest": dest})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "源文件未修改") {
		t.Fatalf("unexpected repair output: %s", out)
	}
	if err := validatePlainZIP(dest); err != nil {
		t.Fatalf("repaired ZIP did not validate: %v", err)
	}
	sourceData, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	local := bytes.Index(sourceData, []byte{'P', 'K', 3, 4})
	if local < 0 || binary.LittleEndian.Uint16(sourceData[local+6:local+8])&1 == 0 {
		t.Fatal("source ZIP was unexpectedly modified")
	}
}

func TestZIPMaskValidation(t *testing.T) {
	sets, err := parseZIPMask("A?d?l??")
	if err != nil || len(sets) != 4 || string(sets[3]) != "?" {
		t.Fatalf("mask parse sets=%v err=%v", sets, err)
	}
	if _, err := parseZIPMask("abc?x"); err == nil {
		t.Fatal("unknown mask token should fail")
	}
	if _, err := parseZIPMask(strings.Repeat("a", 33)); err == nil {
		t.Fatal("literal-only mask longer than 32 positions should fail")
	}
	sets, err = parseZIPMask("?uali?s?d?d?d")
	if err != nil || len(sets) != 8 || string(sets[1]) != "a" || string(sets[2]) != "l" || string(sets[3]) != "i" {
		t.Fatalf("ZipCracker example mask parse sets=%v err=%v", sets, err)
	}
	if string(sets[4]) != zipMaskSymbols || !strings.ContainsRune(string(sets[4]), '~') || !strings.ContainsRune(string(sets[4]), '\'') {
		t.Fatalf("?s should match Python string.punctuation: %q", string(sets[4]))
	}
}

func TestZIPOfficialTest04MaskIfPresent(t *testing.T) {
	src := filepath.Join("..", "..", "build", "test04.zip")
	if _, err := os.Stat(src); err != nil {
		t.Skip("ZipCracker sample test04.zip is not in build/")
	}
	work := t.TempDir()
	copied := filepath.Join(work, "test04.zip")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copied, data, 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(work, "out")
	out, err := ZipPasswordRecover(Runtime{}, map[string]string{
		"action": "recover", "source": copied, "mask": "?uali?s?d?d?d", "extract": "true", "dest": dest, "timeout_sec": "60", "workers": "4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "恢复口令:") {
		t.Fatalf("test04 mask recover failed: %s", out)
	}
}

func TestZIPBuiltinDictionaryContainsZipCrackerDefaults(t *testing.T) {
	lines := 0
	found := false
	for _, line := range strings.Split(zipCrackerPasswordList, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines++
		if strings.TrimSpace(line) == "123456" {
			found = true
		}
	}
	if lines < 6000 || !found {
		t.Fatalf("embedded ZipCracker dictionary lines=%d found123456=%v", lines, found)
	}
	if !isBuiltinZIPDictionary("6000.txt") || !isBuiltinZIPDictionary("builtin") {
		t.Fatal("6000.txt / builtin should resolve to the embedded dictionary")
	}
}

func TestZIPCRC32RecoversShortPlaintextContent(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "key.zip")
	writeStoredZIPFixture(t, source, "key.txt", "G00d")
	dest := filepath.Join(root, "out")
	out, err := ZipPasswordRecover(Runtime{}, map[string]string{
		"action": "crc32", "source": source, "extract": "true", "dest": dest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `内容: "G00d"`) || strings.Contains(out, "恢复口令") {
		t.Fatalf("CRC32 should recover file content, not a password: %s", out)
	}
	data, err := os.ReadFile(filepath.Join(dest, "key.txt"))
	if err != nil || string(data) != "G00d" {
		t.Fatalf("extracted CRC32 content=%q err=%v", data, err)
	}
}

func TestZIPInspectPrintsExactNextStepWithSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "flag.zip")
	writeStoredZIPFixture(t, source, "flag.txt", "hi")
	out, err := ZipPasswordRecover(Runtime{}, map[string]string{"action": "inspect", "source": source})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"action":"auto"`) || !strings.Contains(out, source) {
		t.Fatalf("inspect should spell the next auto call with source: %s", out)
	}
	if !strings.Contains(out, `"action":"crc32"`) {
		t.Fatalf("short file should recommend crc32: %s", out)
	}
}

func TestZIPRecoverReadsDictionaryDirectory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "dir.zip")
	dest := filepath.Join(root, "out")
	writeEncryptedZIPFixture(t, source, "from-dir", cryptozip.StandardEncryption)
	dictDir := filepath.Join(root, "wordlists")
	if err := os.MkdirAll(dictDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dictDir, "a.txt"), []byte("wrong\nfrom-dir\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := ZipPasswordRecover(Runtime{}, map[string]string{
		"action": "recover", "source": source, "dictionary": dictDir, "extract": "true", "dest": dest, "timeout_sec": "5", "workers": "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `恢复口令: "from-dir"`) {
		t.Fatalf("directory dictionary should be used: %s", out)
	}
}

func TestZIPAutoRepairsPseudoEncryptionBeforeBruteForce(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "pseudo.zip")
	dest := filepath.Join(root, "out")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	writer := cryptozip.NewWriter(file)
	entry, err := writer.Create("evidence.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("plain but flagged")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	markZIPFixturePseudoEncrypted(t, source)

	out, err := ZipPasswordRecover(Runtime{}, map[string]string{"action": "auto", "source": source, "dest": dest})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "伪加密修复成功") {
		t.Fatalf("auto should repair fake encryption first: %s", out)
	}
	data, err := os.ReadFile(filepath.Join(dest, "evidence.txt"))
	if err != nil || string(data) != "plain but flagged" {
		t.Fatalf("auto extract data=%q err=%v", data, err)
	}
}

func TestZIPAutoExtractUsesUniqueDestWhenDefaultExists(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "pseudo.zip")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	writer := cryptozip.NewWriter(file)
	entry, err := writer.Create("evidence.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("plain but flagged")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	markZIPFixturePseudoEncrypted(t, source)
	if err := os.Mkdir(source+".extracted", 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := ZipPasswordRecover(Runtime{}, map[string]string{"action": "auto", "source": source})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "改用:") || !strings.Contains(out, source+".extracted-2") {
		t.Fatalf("should allocate unique extract dest: %s", out)
	}
	data, err := os.ReadFile(filepath.Join(source+".extracted-2", "evidence.txt"))
	if err != nil || string(data) != "plain but flagged" {
		t.Fatalf("unique dest data=%q err=%v", data, err)
	}
}

func TestZIPExplicitDestStillRefusesOverwrite(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "pseudo.zip")
	dest := filepath.Join(root, "out")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	writer := cryptozip.NewWriter(file)
	entry, err := writer.Create("evidence.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("plain but flagged")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	markZIPFixturePseudoEncrypted(t, source)
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = ZipPasswordRecover(Runtime{}, map[string]string{"action": "auto", "source": source, "dest": dest})
	if err == nil || !strings.Contains(err.Error(), "dest 已存在，拒绝覆盖") {
		t.Fatalf("explicit dest should refuse overwrite, err=%v", err)
	}
}

func TestZIPAutoUsesBuiltinDictionary(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "dict.zip")
	dest := filepath.Join(root, "out")
	writeEncryptedZIPFixture(t, source, "123456", cryptozip.StandardEncryption)
	out, err := ZipPasswordRecover(Runtime{}, map[string]string{
		"action": "auto", "source": source, "dest": dest, "workers": "2", "timeout_sec": "20",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `恢复口令: "123456"`) {
		t.Fatalf("auto should hit the embedded ZipCracker dictionary: %s", out)
	}
}

func TestZIPCrackerOfficialFixturesIfPresent(t *testing.T) {
	root := filepath.Join("..", "..", "build")
	cases := []struct {
		name   string
		want   string
		action string
	}{
		{"test01.zip", "伪加密修复成功", "auto"},
		{"test02.zip", `恢复口令: "123456"`, "auto"},
		{"test03.zip", `内容: "G00d"`, "crc32"},
	}
	for _, test := range cases {
		src := filepath.Join(root, test.name)
		t.Run(test.name, func(t *testing.T) {
			if _, err := os.Stat(src); err != nil {
				t.Skip("ZipCracker sample zip is not in build/")
			}
			work := t.TempDir()
			copied := filepath.Join(work, test.name)
			data, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(copied, data, 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := ZipPasswordRecover(Runtime{}, map[string]string{
				"action": test.action, "source": copied, "dest": filepath.Join(work, "out"), "timeout_sec": "30", "workers": "2",
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, test.want) {
				t.Fatalf("got %s", out)
			}
		})
	}
}

func writeStoredZIPFixture(t *testing.T, path, name, content string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := stdzip.NewWriter(file)
	header := &stdzip.FileHeader{Name: name, Method: stdzip.Store}
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeEncryptedZIPFixture(t *testing.T, path, password string, method cryptozip.EncryptionMethod) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := cryptozip.NewWriter(file)
	entry, err := writer.Encrypt("secret.txt", password, method)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("classified evidence")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func markZIPFixturePseudoEncrypted(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	local := bytes.Index(data, []byte{'P', 'K', 3, 4})
	central := bytes.Index(data, []byte{'P', 'K', 1, 2})
	if local < 0 || central < 0 {
		t.Fatal("fixture ZIP headers not found")
	}
	localFlags := binary.LittleEndian.Uint16(data[local+6:local+8]) | 1
	centralFlags := binary.LittleEndian.Uint16(data[central+8:central+10]) | 1
	binary.LittleEndian.PutUint16(data[local+6:local+8], localFlags)
	binary.LittleEndian.PutUint16(data[central+8:central+10], centralFlags)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
