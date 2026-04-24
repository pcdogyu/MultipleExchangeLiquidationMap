package runtime

import (
	"log"
	"net/http"
	"os/exec"
	"strings"

	"multipleexchangeliquidationmap/internal/platform/httpx"
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
	if m.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
}

func (m *Manager) HandleVersion(w http.ResponseWriter, r *http.Request) {
	m.logRequest(r)
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	branchOut, _ := m.run("bash", "-lc", "git rev-parse --abbrev-ref HEAD 2>/dev/null || true")
	commitOut, _ := m.run("bash", "-lc", "git rev-parse --short HEAD 2>/dev/null || true")
	timeOut, _ := m.run("bash", "-lc", "git show -s --format=%ci HEAD 2>/dev/null || true")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"branch":      strings.TrimSpace(string(branchOut)),
		"commit_id":   strings.TrimSpace(string(commitOut)),
		"commit_time": strings.TrimSpace(string(timeOut)),
	})
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
		_, _ = m.run("bash", "-lc", "rm -f /tmp/liqmap-upgrade.exit /tmp/liqmap-upgrade.pid /tmp/liqmap-upgrade.unit; : >/tmp/liqmap-upgrade.log; unit=liqmap-upgrade.scope; echo \"$unit\" > /tmp/liqmap-upgrade.unit; systemd-run --scope --collect --no-block --unit=liqmap-upgrade /bin/bash -lc 'cd /opt/MultipleExchangeLiquidationMap && echo [git fetch] && echo \"root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# git fetch --all --prune\" && git fetch --all --prune && echo [git reset] && echo \"root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# git reset --hard origin/golang\" && git reset --hard origin/golang && echo [go build] && echo \"root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# go build -o multipleexchangeliquidationmap.exe ./cmd/server\" && go build -o multipleexchangeliquidationmap.exe ./cmd/server && echo [restart service] && echo \"root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# systemctl restart liqmap.service\" && systemctl restart liqmap.service && echo [service status] && echo \"root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# systemctl status liqmap.service --no-pager\" && systemctl status liqmap.service --no-pager; ec=$?; echo $ec >/tmp/liqmap-upgrade.exit' >>/tmp/liqmap-upgrade.log 2>&1")
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
