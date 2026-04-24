package runtime

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
)

type Manager struct {
	core *liqmap.App
}

func New(core *liqmap.App) *Manager {
	return &Manager{core: core}
}

func (m *Manager) HandleVersion(w http.ResponseWriter, r *http.Request) {
	m.core.HandleVersion(w, r)
}

func (m *Manager) HandleUpgradePull(w http.ResponseWriter, r *http.Request) {
	m.core.HandleUpgradePull(w, r)
}

func (m *Manager) HandleUpgradeProgress(w http.ResponseWriter, r *http.Request) {
	m.core.HandleUpgradeProgress(w, r)
}
