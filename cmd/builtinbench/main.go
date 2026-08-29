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
		Name          string  `json:"name"`
		FileType      string  `json:"file_type"`
		Error         string  `json:"error,omitempty"`
		Output        string  `json:"output,omitempty"`
		Coverage      float64 `json:"coverage,omitempty"`
		Similarity    float64 `json:"similarity,omitempty"`
		Structure     float64 `json:"structure_fidelity,omitempty"`
	}

	results := make([]result, 0, len(probes))
	for _, p := range probes {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		resp, err := c.Read(ctx, &proto.ReadRequest{
			FileContent: []byte(p.content),
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
