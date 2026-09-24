package builtin

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func SQLiteInspect(rt Runtime, path string) (string, error) {
	data, err := readTargetLimited(path, 8<<20)
	if err != nil {
		return "", err
	}
	if len(data) < 100 || string(data[:16]) != "SQLite format 3\x00" {
		return "", fmt.Errorf("不是 SQLite3 数据库文件: %s", path)
	}
	pageSize := int(binary.BigEndian.Uint16(data[16:18]))
	pageCount := int(binary.BigEndian.Uint32(data[28:32]))
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s SQLite Inspect\n路径: %s\n", rt.tag(), path))
	b.WriteString(fmt.Sprintf("page_size=%d page_count=%d approx_size=%d bytes\n", pageSize, pageCount, pageSize*pageCount))
	schema := extractSQLiteSchemaStrings(data)
	if len(schema) == 0 {
		b.WriteString("\n未从文件字符串中提取到 sqlite_schema。可能需要完整 SQL 解析。\n")
		return b.String(), nil
	}
	b.WriteString("\n可见 schema 片段:\n")
	for i, s := range schema {
		if i >= 30 {
			b.WriteString("...(仅显示前 30 条)...\n")
			break
		}
		b.WriteString("  - " + truncateOneLine(s, 240) + "\n")
	}
	return b.String(), nil
}

func AppConfigDiscover(rt Runtime, roots, query string, limit int) (string, error) {
	if roots == "" {
		roots = "/etc,/opt,/var/www,/srv,/app"
	}
	if limit <= 0 {
		limit = 80
	}
	patterns := []string{".env", ".conf", ".ini", ".yaml", ".yml", ".properties", ".json", ".toml", ".xml"}
	var hits []string
	for _, root := range strings.Split(roots, ",") {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		walkTarget(root, 0, 3, limit, func(path string, isDir bool) bool {
			if isDir || !looksConfigFile(path, patterns) {
				return true
			}
			data, err := readTargetLimited(path, 512*1024)
			if err != nil {
				return true
			}
			text := string(data)
			if query != "" && !strings.Contains(strings.ToLower(text), strings.ToLower(query)) && !strings.Contains(strings.ToLower(path), strings.ToLower(query)) {
				return true
			}
			if containsConfigSignal(text) {
				hits = append(hits, fmt.Sprintf("%s\n%s", path, redactSecrets(extractConfigSignals(text, 8))))
			}
			return len(hits) < limit
		})
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 应用配置发现\n", rt.tag()))
	if len(hits) == 0 {
		b.WriteString("(未发现明显连接串/凭据配置)\n")
		return b.String(), nil
	}
	for _, h := range hits {
		b.WriteString("--- " + h + "\n")
	}
	return truncate(b.String(), 30000), nil
}

func DBConfigAudit(rt Runtime, dbType, paths string) (string, error) {
	candidates := dbConfigCandidates(dbType, paths)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 数据库配置审计 type=%s\n", rt.tag(), emptyDefault(dbType, "auto")))
	found := 0
	for _, path := range candidates {
		data, err := readTargetLimited(path, 1024*1024)
		if err != nil {
			continue
		}
		found++
		text := string(data)
		b.WriteString("\n--- " + path + " ---\n")
		b.WriteString(redactSecrets(extractConfigSignals(text, 20)))
		b.WriteString(configRiskHints(path, text))
	}
	if found == 0 {
		b.WriteString("未读取到常见数据库配置文件。可传 paths=/path/a,/path/b 指定。\n")
	}
	return b.String(), nil
}

func DBLogRead(rt Runtime, dbType, path, pattern string, lines int) (string, error) {
	if path == "" {
		path = defaultDBLogPath(dbType)
	}
	if pattern == "" {
		pattern = defaultDBLogPattern(dbType)
	}
	return ReadLog(rt, path, lines, pattern)
}

func SecretScan(rt Runtime, root, pattern string, limit int) (string, error) {
	if root == "" {
		root = "."
	}
	if limit <= 0 {
		limit = 80
	}
	rules := secretRules()
	if pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return "", err
		}
		rules = map[string]*regexp.Regexp{"custom": re}
	}
	var hits []string
	walkTarget(root, 0, 3, limit, func(path string, isDir bool) bool {
		if isDir || isLikelyBinaryName(path) {
			return true
		}
		data, err := readTargetLimited(path, 512*1024)
		if err != nil {
			return true
		}
		for name, re := range rules {
			for _, m := range re.FindAllString(string(data), 5) {
				hits = append(hits, fmt.Sprintf("%s [%s] %s", path, name, redactSecrets(m)))
				if len(hits) >= limit {
					return false
				}
			}
		}
		return len(hits) < limit
	})
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s Secret Scan root=%s\n", rt.tag(), root))
	if len(hits) == 0 {
		b.WriteString("(未发现匹配 secret)\n")
		return b.String(), nil
	}
	for _, h := range hits {
		b.WriteString("  - " + h + "\n")
	}
	return b.String(), nil
}

func ServiceUnitAudit(rt Runtime, query string, limit int) (string, error) {
	if limit <= 0 {
		limit = 80
	}
	roots := []string{"/etc/systemd/system", "/lib/systemd/system", "/usr/lib/systemd/system", "/etc/init.d", "/etc/cron.d"}
	var hits []string
	for _, root := range roots {
		if !walkTarget(root, 0, 2, limit, func(path string, isDir bool) bool {
			if isDir {
				return true
			}
			data, err := readTargetLimited(path, 512*1024)
			if err != nil {
				return true
			}
			text := string(data)
			if query != "" && !strings.Contains(strings.ToLower(path+text), strings.ToLower(query)) {
				return true
			}
			signals := extractUnitSignals(text)
			if signals != "" {
				hits = append(hits, path+"\n"+redactSecrets(signals))
			}
			return len(hits) < limit
		}) {
			break
		}
		if len(hits) >= limit {
			break
		}
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 服务/自启动审计\n", rt.tag()))
	if len(hits) == 0 {
		b.WriteString("(未发现匹配 unit/init/cron 信号)\n")
		return b.String(), nil
	}
	for _, h := range hits {
		b.WriteString("--- " + h + "\n")
	}
	return truncate(b.String(), 30000), nil
}

func ContainerInventory(rt Runtime) (string, error) {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 容器环境识别\n", rt.tag()))
	paths := []string{"/proc/1/cgroup", "/proc/self/mountinfo", "/.dockerenv", "/run/.containerenv"}
	found := false
	for _, path := range paths {
		data, err := readTargetLimited(path, 256*1024)
		if err != nil {
			continue
		}
		found = true
		b.WriteString("\n--- " + path + " ---\n")
		b.WriteString(truncate(redactSecrets(string(data)), 4000) + "\n")
	}
	if !found {
		b.WriteString("未发现明显容器标记文件。\n")
	}
	return b.String(), nil
}

func extractSQLiteSchemaStrings(data []byte) []string {
	strs := extractStrings(data, 8)
	var out []string
	seen := map[string]bool{}
	for _, s := range strs {
		lower := strings.ToLower(s)
		if strings.Contains(lower, "create table") || strings.Contains(lower, "create index") || strings.Contains(lower, "sqlite_sequence") {
			s = strings.TrimSpace(s)
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

func walkTarget(root string, depth, maxDepth, maxHits int, visit func(path string, isDir bool) bool) bool {
	if depth > maxDepth {
		return true
	}
	entries, err := listTarget(root)
	if err != nil {
		return true
	}
	for _, name := range entries {
		if strings.HasPrefix(name, ".") && name != ".env" {
			continue
		}
		full := filepath.Join(root, name)
		isDir := isTargetDir(full)
		if !visit(full, isDir) {
			return false
		}
		if isDir && depth < maxDepth && !skipWalkDir(name) {
			if !walkTarget(full, depth+1, maxDepth, maxHits, visit) {
				return false
			}
		}
	}
	return true
}

func skipWalkDir(name string) bool {
	switch name {
	case "proc", "sys", "dev", "run", "tmp", "node_modules", ".git":
		return true
	default:
		return false
	}
}

func looksConfigFile(path string, suffixes []string) bool {
	lower := strings.ToLower(path)
	for _, s := range suffixes {
		if strings.HasSuffix(lower, s) || strings.Contains(lower, s+".") {
			return true
		}
	}
	return false
}

func containsConfigSignal(text string) bool {
	lower := strings.ToLower(text)
	signals := []string{"password", "passwd", "secret", "token", "redis://", "mysql://", "postgres://", "jdbc:", "dsn", "database_url", "db_host"}
	for _, s := range signals {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

func extractConfigSignals(text string, limit int) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		lower := strings.ToLower(line)
		if containsConfigSignal(lower) || strings.Contains(lower, "bind") || strings.Contains(lower, "listen") || strings.Contains(lower, "port") {
			out = append(out, strings.TrimSpace(line))
			if len(out) >= limit {
				break
			}
		}
	}
	if len(out) == 0 {
		return "(无明显配置项)\n"
	}
	return strings.Join(out, "\n") + "\n"
}

func dbConfigCandidates(dbType, paths string) []string {
	if paths != "" {
		return splitCSV(paths)
	}
	switch strings.ToLower(dbType) {
	case "redis":
		return []string{"/etc/redis/redis.conf", "/etc/redis.conf", "/usr/local/etc/redis.conf"}
	case "mysql", "mariadb":
		return []string{"/etc/my.cnf", "/etc/mysql/my.cnf", "/etc/mysql/mysql.conf.d/mysqld.cnf", "/etc/my.cnf.d/server.cnf"}
	case "postgres", "postgresql", "pg":
		return []string{"/etc/postgresql/postgresql.conf", "/var/lib/pgsql/data/postgresql.conf", "/var/lib/postgresql/data/postgresql.conf"}
	case "oracle":
		return []string{"/etc/oratab", "/opt/oracle/product/network/admin/listener.ora", "/opt/oracle/product/network/admin/tnsnames.ora"}
	default:
		return []string{"/etc/redis/redis.conf", "/etc/my.cnf", "/etc/mysql/my.cnf", "/etc/postgresql/postgresql.conf", "/etc/oratab"}
	}
}

func configRiskHints(path, text string) string {
	lower := strings.ToLower(text)
	var hints []string
	if strings.Contains(lower, "bind 0.0.0.0") || strings.Contains(lower, "listen_addresses = '*'") {
		hints = append(hints, "风险: 监听所有地址")
	}
	if strings.Contains(lower, "protected-mode no") {
		hints = append(hints, "风险: Redis protected-mode 关闭")
	}
	if strings.Contains(lower, "requirepass") && strings.Contains(lower, "# requirepass") {
		hints = append(hints, "风险: Redis 密码可能未启用")
	}
	if strings.Contains(lower, "skip-grant-tables") {
		hints = append(hints, "高危: MySQL skip-grant-tables")
	}
	if len(hints) == 0 {
		return ""
	}
	return "提示(" + path + "): " + strings.Join(hints, "; ") + "\n"
}

func defaultDBLogPath(dbType string) string {
	switch strings.ToLower(dbType) {
	case "redis":
		return "/var/log/redis/redis-server.log"
	case "mysql", "mariadb":
		return "/var/log/mysql/error.log"
	case "postgres", "postgresql", "pg":
		return "/var/log/postgresql/postgresql.log"
	default:
		return "/var/log/syslog"
	}
}

func defaultDBLogPattern(dbType string) string {
	switch strings.ToLower(dbType) {
	case "redis":
		return "(?i)(warning|error|auth|denied|accepted|ready)"
	case "mysql", "mariadb":
		return "(?i)(error|warning|denied|access|aborted|ready)"
	case "postgres", "postgresql", "pg":
		return "(?i)(fatal|error|password|authentication|connection|listening)"
	default:
		return "(?i)(error|failed|denied|warning)"
	}
}

func secretRules() map[string]*regexp.Regexp {
	return map[string]*regexp.Regexp{
		"password": regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[:=]\s*['"]?[^'"\s]{6,}`),
		"token":    regexp.MustCompile(`(?i)(token|secret|api[_-]?key)\s*[:=]\s*['"]?[^'"\s]{12,}`),
		"private":  regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		"dsn":      regexp.MustCompile(`(?i)(mysql|postgres|redis)://[^ \n\r\t]+`),
	}
}

func redactSecrets(s string) string {
	replacements := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(password|passwd|pwd|token|secret|api[_-]?key)(\s*[:=]\s*)['"]?[^'"\s]+`),
		regexp.MustCompile(`(?i)(mysql|postgres|redis)://([^:\s]+):([^@\s]+)@`),
	}
	s = replacements[0].ReplaceAllString(s, `${1}${2}***`)
	s = replacements[1].ReplaceAllString(s, `${1}://${2}:***@`)
	return s
}

func extractUnitSignals(text string) string {
	keys := []string{"ExecStart", "ExecStartPre", "Environment", "User", "Group", "WorkingDirectory", "Restart", "OnCalendar"}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		for _, k := range keys {
			if strings.HasPrefix(line, k+"=") || strings.Contains(line, " "+k+"=") {
				out = append(out, line)
				break
			}
		}
		if len(out) >= 20 {
			break
		}
	}
	return strings.Join(out, "\n") + "\n"
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func emptyDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func isLikelyBinaryName(path string) bool {
	lower := strings.ToLower(path)
	for _, ext := range []string{".jpg", ".png", ".gif", ".pdf", ".zip", ".gz", ".tar", ".so", ".bin", ".exe"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}
