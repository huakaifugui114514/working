// Package migrations 把 SQL 迁移文件编译进二进制。
//
// 为什么用 embed 而不是运行时读目录：部署时只有一个二进制，不需要额外拷贝 migrations/；
// 也就不会出现"二进制是新版、SQL 文件是旧版"这种最难查的错配。
//
// ⚠️ 文件名必须是 `{version}_{title}.{up|down}.sql`：
// golang-migrate 的解析正则是 `^([0-9]+)_(.*)\.(down|up)\.(.*)$`，
// 不匹配的文件会被 iofs 源【静默跳过】—— 表现为 up 什么都没做却不报错。
// 这条规则由 migrations_test.go 里的 TestMigrationFilenames 兜住。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
