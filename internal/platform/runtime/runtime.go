package runtime

import (
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
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
	if m.debug && r.URL.Path != "/api/version" {
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
