// Command builtinbench measures the "builtin" (DocReader) parser engine's
// output quality against golden Markdown, complementing parsebench's
// "simple" engine measurement so M5's cross-engine baseline covers two of the
// eight engines with real measurements.
//
// It talks to the DocReader gRPC service directly through the pure-Go
// docreader/client + docreader/proto packages (no internal/types, no cgo), so
// it compiles and runs with CGO_ENABLED=0 — the same reason embedbench exists.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/Tencent/WeKnora/docreader/client"
	"github.com/Tencent/WeKnora/docreader/proto"
	"github.com/Tencent/WeKnora/internal/parsequality"
)

// probe is one input document fed to the builtin engine. `golden` is the
// Markdown a correct parser should produce; when empty the program only
// prints the raw engine output (used to first observe behaviour, then pin the
// golden).
type probe struct {
	name     string
	fileType string
	fileName string
	content  string
	filePath string // 可选：从磁盘读取二进制文件内容（docx/pdf/xlsx 等），优先于 content
	golden   string
}

var probes = []probe{
	{
		name:     "markdown passthrough",
		fileType: "md",
		fileName: "doc.md",
		content:  "# Title\n\nSome **bold** text and a [link](https://example.com).\n\n- item one\n- item two\n",
		golden:   "# Title\n\nSome **bold** text and a [link](https://example.com).\n\n- item one\n- item two\n",
	},
	{
		name:     "markdown table normalization",
		fileType: "md",
		fileName: "table.md",
		content:  "# 人员表\n\n|姓名|年龄|城市|\n|:---|---:|:---:|\n|张三|25|北京|\n|李四|30|上海|\n",
		golden:   "# 人员表\n\n| 姓名 | 年龄 | 城市 |\n| :--- | ---: | :---: |\n| 张三 | 25 | 北京 |\n| 李四 | 30 | 上海 |\n",
	},
	{
		name:     "html to markdown",
		fileType: "html",
		fileName: "page.html",
		content:  "<html><body><h1>标题</h1><p>这是<strong>一段</strong>文字。</p><ul><li>项目一</li><li>项目二</li></ul><p>访问<a href=\"https://example.com\">示例链接</a>。</p><table><tr><th>姓名</th><th>年龄</th></tr><tr><td>张三</td><td>25</td></tr><tr><td>李四</td><td>30</td></tr></table></body></html>",
		golden:   "# 标题\n\n这是**一段**文字。\n\n* 项目一\n* 项目二\n\n访问[示例链接](https://example.com)。\n\n| 姓名 | 年龄 |\n| --- | --- |\n| 张三 | 25 |\n| 李四 | 30 |",
	},
	{
		name:     "docx vertical-merge table",
		fileType: "docx",
		fileName: "issue_2634_vertical_merge.docx",
		filePath: "docreader/tests/fixtures/issue_2634_vertical_merge.docx",
		// golden 为「期望的正确输出」：纵向合并的「检测方法」单元格应展开，每条检测项目都保留该方法
		// （docx 自身说明文字即此回归场景的验收标准）。builtin 引擎实际输出会留空，故 similarity < 1。
		golden: `# 遗传病检测项目表

回归场景：检测方法纵向合并，但每条检测项目在转换后都必须保留该方法。

| 项目编号 | 检测项目 | 相关基因 | 周期 | 检测方法 |
| --- | --- | --- | --- | --- |
| Q0101 | 遗传性乳腺癌 | BRCA1 | 15个工作日 | 检测方法：提取外周血基因组 DNA，采用高通量测序，并对候选位点进行Sanger 测序验证；检测结果需结合临床表现综合判断。 |
| Q0102 | 遗传性卵巢癌 | BRCA2 | 15个工作日 | 检测方法：提取外周血基因组 DNA，采用高通量测序，并对候选位点进行Sanger 测序验证；检测结果需结合临床表现综合判断。 |
| Q0103 | 林奇综合征 | MLH1 | 15个工作日 | 检测方法：提取外周血基因组 DNA，采用高通量测序，并对候选位点进行Sanger 测序验证；检测结果需结合临床表现综合判断。 |
| Q0104 | 家族性腺瘤性息肉病 | APC | 15个工作日 | 检测方法：提取外周血基因组 DNA，采用高通量测序，并对候选位点进行Sanger 测序验证；检测结果需结合临床表现综合判断。 |

非合并对照表：相邻单元格内容允许相同，但不应被当作合并。

| 列A | 列B | 列C |
| --- | --- | --- |
| 相同值 | 相同值 | 独立值 |`,
	},
}

func main() {
	addr := "localhost:50051"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	if err := run(addr); err != nil {
		fmt.Fprintln(os.Stderr, "builtinbench:", err)
		os.Exit(1)
	}
}

func run(addr string) error {
	c, err := client.NewClient(addr)
	if err != nil {
		return fmt.Errorf("connect docreader %s: %w", addr, err)
	}
	defer c.Close()

	type result struct {
		Name       string  `json:"name"`
		FileType   string  `json:"file_type"`
		Error      string  `json:"error,omitempty"`
		Output     string  `json:"output,omitempty"`
		Coverage   float64 `json:"coverage,omitempty"`
		Similarity float64 `json:"similarity,omitempty"`
		Structure  float64 `json:"structure_fidelity,omitempty"`
	}

	results := make([]result, 0, len(probes))
	for _, p := range probes {
		var content []byte
		if p.filePath != "" {
			b, err := os.ReadFile(p.filePath)
			if err != nil {
				results = append(results, result{Name: p.name, FileType: p.fileType, Error: "read file: " + err.Error()})
				continue
			}
			content = b
		} else {
			content = []byte(p.content)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		resp, err := c.Read(ctx, &proto.ReadRequest{
			FileContent: content,
			FileName:    p.fileName,
			FileType:    p.fileType,
			Config:      &proto.ReadConfig{ParserEngine: "builtin"},
		})
		cancel()
		if err != nil {
			results = append(results, result{Name: p.name, FileType: p.fileType, Error: err.Error()})
			continue
		}
		if resp.GetError() != "" {
			results = append(results, result{Name: p.name, FileType: p.fileType, Error: resp.GetError()})
			continue
		}
		out := resp.GetMarkdownContent()

		r := result{Name: p.name, FileType: p.fileType, Output: out}
		if p.golden != "" {
			r.Coverage = parsequality.TextCoverage(p.golden, out)
			r.Similarity = parsequality.TextSimilarity(p.golden, out)
			r.Structure = parsequality.StructureFidelity(p.golden, out)
		}
		results = append(results, r)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(results)
}
