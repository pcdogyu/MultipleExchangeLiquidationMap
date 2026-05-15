package runtime

import (
	"bufio"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	"multipleexchangeliquidationmap/internal/platform/httpx"
)

var (
	versionBranch     = ""
	versionCommitID   = ""
	versionCommitTime = ""
)

type Manager struct {
	debug bool
	run   func(name string, args ...string) ([]byte, error)
	async func(fn func())
}

func New(debug bool) *Manager {
	return &Manager{
		debug: debug,
		run: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		},
		async: func(fn func()) {
			go fn()
		},
	}
}

func (m *Manager) logRequest(r *http.Request) {
	if m.debug && r.URL.Path != "/api/version" && r.URL.Path != "/api/logs" {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
}

func (m *Manager) HandleVersion(w http.ResponseWriter, r *http.Request) {
	m.logRequest(r)
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	branch, commitID, commitTime := m.buildVersionInfo()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"branch":      branch,
		"commit_id":   commitID,
		"commit_time": commitTime,
	})
}

func (m *Manager) buildVersionInfo() (string, string, string) {
	branch := firstNonEmpty(versionBranch, os.Getenv("VERSION_BRANCH"), os.Getenv("GIT_BRANCH"))
	commitID := firstNonEmpty(versionCommitID, os.Getenv("VERSION_COMMIT"), os.Getenv("GIT_COMMIT"))
	commitTime := firstNonEmpty(versionCommitTime, os.Getenv("VERSION_COMMIT_TIME"), os.Getenv("GIT_COMMIT_TIME"))
	if commitID == "" || commitTime == "" {
		buildCommit, buildTime := buildInfoVCS()
		if commitID == "" {
			commitID = shortCommit(buildCommit)
		}
		if commitTime == "" {
			commitTime = buildTime
		}
	}
	if branch == "" {
		branch = m.gitOutput("rev-parse", "--abbrev-ref", "HEAD")
	}
	if commitID == "" {
		commitID = m.gitOutput("rev-parse", "--short", "HEAD")
	}
	if commitTime == "" {
		commitTime = m.gitOutput("show", "-s", "--format=%ci", "HEAD")
	}
	return branch, commitID, commitTime
}

func (m *Manager) gitOutput(args ...string) string {
	out, err := m.run("git", args...)
	if err == nil {
		if text := strings.TrimSpace(string(out)); text != "" {
			return text
		}
	}
	for _, dir := range versionGitDirs() {
		dirArgs := append([]string{"-C", dir}, args...)
		out, err := m.run("git", dirArgs...)
		if err == nil {
			if text := strings.TrimSpace(string(out)); text != "" {
				return text
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func buildInfoVCS() (string, string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", ""
	}
	var revision, commitTime string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = strings.TrimSpace(setting.Value)
		case "vcs.time":
			commitTime = strings.TrimSpace(setting.Value)
		}
	}
	return revision, commitTime
}

func shortCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

func versionGitDirs() []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		abs, err := filepath.Abs(dir)
		if err == nil {
			dir = abs
		}
		key := strings.ToLower(filepath.Clean(dir))
		if seen[key] {
			return
		}
		seen[key] = true
		dirs = append(dirs, dir)
	}
	if wd, err := os.Getwd(); err == nil {
		add(wd)
	}
	if exe, err := os.Executable(); err == nil {
		add(filepath.Dir(exe))
	}
	if repo := strings.TrimSpace(os.Getenv("VERSION_GIT_DIR")); repo != "" {
		add(repo)
	}
	return dirs
}

func (m *Manager) HandleUpgradePull(w http.ResponseWriter, r *http.Request) {
	m.logRequest(r)
	if r.Method != http.MethodPost {
		httpx.MethodNotAllowed(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"output": "upgrade queued; will restart liqmap.service",
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	m.async(func() {
		_, _ = m.run("bash", "-lc", "rm -f /tmp/liqmap-upgrade.exit /tmp/liqmap-upgrade.pid /tmp/liqmap-upgrade.unit; : >/tmp/liqmap-upgrade.log; unit=liqmap-upgrade.scope; echo \"$unit\" > /tmp/liqmap-upgrade.unit; systemd-run --scope --collect --no-block --unit=liqmap-upgrade /bin/bash -lc 'cd /opt/MultipleExchangeLiquidationMap && echo [git fetch] && echo \"root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# git fetch --all --prune\" && git fetch --all --prune && echo [git reset] && echo \"root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# git reset --hard origin/golangv2\" && git reset --hard origin/golangv2 && echo [go build] && echo \"root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# go build -o multipleexchangeliquidationmap.exe ./cmd/server\" && go build -o multipleexchangeliquidationmap.exe ./cmd/server && echo [restart service] && echo \"root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# systemctl restart liqmap.service\" && systemctl restart liqmap.service && echo [service status] && echo \"root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# systemctl status liqmap.service --no-pager\" && systemctl status liqmap.service --no-pager; ec=$?; echo $ec >/tmp/liqmap-upgrade.exit' >>/tmp/liqmap-upgrade.log 2>&1")
	})
}

func (m *Manager) HandleUpgradeProgress(w http.ResponseWriter, r *http.Request) {
	m.logRequest(r)
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	logOut, _ := m.run("bash", "-lc", "tail -n 260 /tmp/liqmap-upgrade.log 2>/dev/null || true")
	runningOut, _ := m.run("bash", "-lc", "unit=$(cat /tmp/liqmap-upgrade.unit 2>/dev/null || true); if [ -n \"$unit\" ] && systemctl is-active --quiet \"$unit\"; then echo 1; else echo 0; fi")
	exitOut, _ := m.run("bash", "-lc", "cat /tmp/liqmap-upgrade.exit 2>/dev/null || true")
	running := strings.TrimSpace(string(runningOut)) == "1"
	exitCode := strings.TrimSpace(string(exitOut))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"running":   running,
		"done":      !running && exitCode != "",
		"exit_code": exitCode,
		"log":       string(logOut),
	})
}

type runtimeLogRow struct {
	Line    int    `json:"line"`
	TS      string `json:"ts"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Raw     string `json:"raw"`
}

func (m *Manager) HandleLogs(w http.ResponseWriter, r *http.Request) {
	m.logRequest(r)
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	httpx.NoStore(w)

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	level := normalizeLogLevel(r.URL.Query().Get("level"))
	page := positiveIntQuery(r, "page", 1)
	limit := positiveIntQuery(r, "limit", 20)
	if limit > 100 {
		limit = 100
	}

	path := runtimeLogPath()
	rows, total, err := readRuntimeLogs(path, q, level, page, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + limit - 1) / limit
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"path":        path,
		"page":        page,
		"page_size":   limit,
		"total":       total,
		"total_pages": totalPages,
		"level":       level,
		"q":           q,
		"rows":        rows,
	})
}

func positiveIntQuery(r *http.Request, key string, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func runtimeLogPath() string {
	if raw := strings.TrimSpace(os.Getenv("DEBUG_LOG")); raw != "" {
		return raw
	}
	return "log/server.log"
}

func readRuntimeLogs(path, q, level string, page, limit int) ([]runtimeLogRow, int, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []runtimeLogRow{}, 0, nil
		}
		return nil, 0, err
	}
	defer file.Close()

	q = strings.ToLower(strings.TrimSpace(q))
	level = normalizeLogLevel(level)
	var matched []runtimeLogRow
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := strings.TrimRight(scanner.Text(), "\r")
		row := parseRuntimeLogLine(lineNo, raw)
		if level != "all" && row.Level != level {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(raw), q) {
			continue
		}
		matched = append(matched, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}

	for i, j := 0, len(matched)-1; i < j; i, j = i+1, j-1 {
		matched[i], matched[j] = matched[j], matched[i]
	}
	total := len(matched)
	start := (page - 1) * limit
	if start >= total {
		return []runtimeLogRow{}, total, nil
	}
	end := start + limit
	if end > total {
		end = total
	}
	return matched[start:end], total, nil
}

func parseRuntimeLogLine(lineNo int, raw string) runtimeLogRow {
	ts := ""
	message := strings.TrimSpace(raw)
	if len(raw) >= 20 && raw[4] == '/' && raw[7] == '/' && raw[10] == ' ' && raw[13] == ':' && raw[16] == ':' {
		ts = raw[:19]
		message = strings.TrimSpace(raw[20:])
	}
	return runtimeLogRow{
		Line:    lineNo,
		TS:      ts,
		Level:   classifyLogLevel(raw),
		Message: message,
		Raw:     raw,
	}
}

func normalizeLogLevel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "info":
		return "info"
	case "warn", "warning":
		return "warning"
	case "err", "error":
		return "error"
	default:
		return "all"
	}
}

func classifyLogLevel(raw string) string {
	lower := strings.ToLower(raw)
	for _, marker := range []string{
		"error", " err", "failed", "fail:", "panic", "fatal", "deadline exceeded",
		"no such host", "connection refused", "timeout", "bad request", "错误", "失败", "超时",
	} {
		if strings.Contains(lower, marker) {
			return "error"
		}
	}
	for _, marker := range []string{
		"warn", "warning", "retry", "paused", "skip ", "skipped", "unknown",
		"not found", "empty", "unavailable", "警告", "跳过", "暂无",
	} {
		if strings.Contains(lower, marker) {
			return "warning"
		}
	}
	return "info"
}
