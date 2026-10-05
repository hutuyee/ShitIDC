// zjmfimport —— 魔方财务 server 插件离线转换工具。
//
// 把魔方的经典服务器模块（PHP）转换成 ShitIDC 的声明式上游规格 JSON：
//
//	zjmfimport -dir  路径/到/bthosts          # 已解包目录
//	zjmfimport -zip  路径/到/插件.zip         # zip 包
//	zjmfimport -zip  插件.zip -o spec.json    # 输出到文件
//
// 产物可以直接在「管理后台 → 供应商 → 导入魔方插件」里上传应用，或者
// 由 -apply 配合 API 导入（见 docs/zjmf-plugin-import.md）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/hutuyee/ShitIDC/internal/zjmfimport"
)

func main() {
	dir := flag.String("dir", "", "已解包的插件目录")
	zipPath := flag.String("zip", "", "插件 zip 包")
	out := flag.String("o", "", "输出文件（缺省打印到标准输出）")
	flag.Parse()

	var res *zjmfimport.ParseResult
	var err error
	switch {
	case *zipPath != "":
		f, ferr := os.Open(*zipPath)
		if ferr != nil {
			fail("打开 zip 失败: %v", ferr)
		}
		defer f.Close()
		res, err = zjmfimport.FromZipReader(f)
	case *dir != "":
		res, err = zjmfimport.FromDir(*dir)
	default:
		fail("请用 -zip 或 -dir 指定魔方插件包")
	}
	if err != nil {
		fail("转换失败: %v", err)
	}

	b, merr := json.MarshalIndent(res.Spec, "", "  ")
	if merr != nil {
		fail("序列化失败: %v", merr)
	}
	if *out != "" {
		if werr := os.WriteFile(*out, b, 0o644); werr != nil {
			fail("写文件失败: %v", werr)
		}
	} else {
		fmt.Println(string(b))
	}
	if len(res.Spec.Warnings) > 0 {
		fmt.Fprintf(os.Stderr, "\n转换警告（导入后可在规格编辑器里补充）：\n")
		for _, w := range res.Spec.Warnings {
			fmt.Fprintf(os.Stderr, "  - %s\n", w)
		}
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "zjmfimport: "+strings.TrimSuffix(format, "\n")+"\n", args...)
	os.Exit(1)
}
