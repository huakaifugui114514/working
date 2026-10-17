package migrations

import (
	"regexp"
	"strconv"
	"testing"
)

// migrate 的文件名规则（与 migrate/source.Parse 的正则一致）：
//
//	^([0-9]+)_(.*)\.(down|up)\.(.*)$
//
// 为什么要测它：不匹配的文件会被 iofs 源【静默跳过】（源码里 err != nil 就 continue），
// 结果是 "migrate up 成功但什么都没建表"。这种失败没有任何报错，只能靠测试拦住。
var filenameRE = regexp.MustCompile(`^([0-9]+)_(.+)\.(up|down)\.sql$`)

func TestMigrationFilenames(t *testing.T) {
	entries, err := FS.ReadDir(".")
	if err != nil {
		t.Fatalf("读取内嵌迁移目录失败: %v", err)
	}

	ups := map[uint]bool{}
	downs := map[uint]bool{}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()

		m := filenameRE.FindStringSubmatch(name)
		if m == nil {
			t.Errorf("迁移文件 %q 不符合 {version}_{title}.{up|down}.sql —— 会被 migrate 静默跳过", name)
			continue
		}

		version, err := strconv.ParseUint(m[1], 10, 32)
		if err != nil {
			t.Errorf("迁移文件 %q 的版本号无法解析: %v", name, err)
			continue
		}

		switch m[3] {
		case "up":
			if ups[uint(version)] {
				t.Errorf("版本 %d 有多个 up 文件（migrate 会报重复迁移）", version)
			}
			ups[uint(version)] = true
		case "down":
			downs[uint(version)] = true
		}
	}

	if len(ups) == 0 {
		t.Fatal("没有任何 up 迁移文件")
	}
	// 有 down 必须有对应的 up：否则 down 是无主的，回滚语义说不清
	for v := range downs {
		if !ups[v] {
			t.Errorf("版本 %d 有 down 但没有 up", v)
		}
	}
}
