package liqmap

import (
	"fmt"
	"strings"
)

func webDataSourceNoDataMessage(window, lastError string) string {
	label := webDataSourceWindowLabel(window)
	reason := strings.TrimSpace(lastError)
	if reason == "" {
		return fmt.Sprintf("%s页面数据源暂无可用快照，请先打开 /webdatasource 执行抓取，成功后再发送", label)
	}
	if strings.EqualFold(reason, "run interrupted before completion") {
		return fmt.Sprintf("%s页面数据源暂无可用快照；上次抓取在服务停止或重启前未完成。请打开 /webdatasource 执行抓取，成功后再发送", label)
	}
	return fmt.Sprintf("%s页面数据源暂无可用快照；最近错误：%s", label, reason)
}

func webDataSourceWindowLabel(window string) string {
	switch strings.TrimSpace(strings.ToLower(window)) {
	case "1d":
		return "1天"
	case "7d":
		return "7天"
	case "30d":
		return "30天"
	default:
		return "30天"
	}
}
