package liqmap

import (
	"strings"
	"testing"
)

func TestWebDataSourceNoDataMessageExplainsInterruptedRun(t *testing.T) {
	got := webDataSourceNoDataMessage("30d", "run interrupted before completion")

	for _, want := range []string{"30天页面数据源暂无可用快照", "上次抓取在服务停止或重启前未完成", "/webdatasource"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q to contain %q", got, want)
		}
	}
}

func TestWebDataSourceNoDataMessageIncludesRecentError(t *testing.T) {
	got := webDataSourceNoDataMessage("7d", "chrome not found")

	for _, want := range []string{"7天页面数据源暂无可用快照", "最近错误：chrome not found"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q to contain %q", got, want)
		}
	}
}
