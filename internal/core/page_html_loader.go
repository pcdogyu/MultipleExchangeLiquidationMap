package liqmap

import (
	"fmt"
	"os"
)

func loadPageHTML(path, fallback string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	return string(body)
}

func missingPageHTML(title, path string) string {
	return fmt.Sprintf(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>%s</title></head><body style="font-family:Segoe UI,Microsoft YaHei,sans-serif;padding:24px"><h2>%s 页面文件缺失</h2><p>请确认 <code>%s</code> 存在，然后刷新页面。</p></body></html>`, title, title, path)
}
