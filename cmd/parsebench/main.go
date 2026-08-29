// Command parsebench produces the M5 cross-engine parsing quality baseline.
//
// It reports two things:
//  1. engines — the capability matrix of the 8 locally registered parser
//     engines (name, description, supported file types, availability), the
//     "horizontal" skeleton of the baseline.
//  2. simple_engine_quality — measured quality of the Go-native "simple"
//     engine on simple formats (md/txt/csv) against golden Markdown, scored by
//     the internal/parsequality metrics (coverage / similarity / structure).
//
// Most engines need an external service (DocReader, MinerU, PaddleOCR-VL) or a
// Rust-linked build (anydoc), so only the dependency-free "simple" engine can
// be measured in this environment. The pipeline is engine-agnostic: plugging
// another engine's output into the same metrics yields a direct comparison.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/parsequality"
	"github.com/Tencent/WeKnora/internal/types"
)

// benchCase is one simple-format parse benchmark: input bytes and the golden
// Markdown a perfect parser should produce.
type benchCase struct {
	name     string
	fileType string
	fileName string
	content  string
	golden   string
}

// benchResult is the measured quality of one case.
type benchResult struct {
	FileType          string  `json:"file_type"`
	Coverage          float64 `json:"coverage"`
	Similarity        float64 `json:"similarity"`
	StructureFidelity float64 `json:"structure_fidelity"`
}

var benchCases = []benchCase{
	{
		name:     "markdown passthrough",
		fileType: "md",
		fileName: "doc.md",
		content:  "# Title\n\nSome **bold** text and a [link](https://example.com).\n\n- item one\n- item two\n",
		golden:   "# Title\n\nSome **bold** text and a [link](https://example.com).\n\n- item one\n- item two\n",
	},
	{
		name:     "plain text passthrough",
		fileType: "txt",
		fileName: "note.txt",
		content:  "Hello, world!\nThis is plain text with no markup.\n",
		golden:   "Hello, world!\nThis is plain text with no markup.\n",
	},
	{
		name:     "csv to markdown table",
		fileType: "csv",
		fileName: "people.csv",
		content:  "name,age\nAlice,30\nBob,25\n",
		golden:   "| name | age |\n| --- | --- |\n| Alice | 30 |\n| Bob | 25 |\n",
	},
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "parsebench:", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. 8 引擎能力矩阵（docreader 未连接，简单引擎可用，其余依赖外部服务）。
	engines := docparser.ListAllEngines(false, nil, nil)

	// 2. simple 引擎实测解析质量。
	reader := &docparser.SimpleFormatReader{}
	results := make([]benchResult, 0, len(benchCases))
	for _, c := range benchCases {
		out, err := reader.Read(context.Background(), &types.ReadRequest{
			FileName:    c.fileName,
			FileType:    c.fileType,
			FileContent: []byte(c.content),
		})
		if err != nil {
			return fmt.Errorf("parse %s: %w", c.name, err)
		}
		results = append(results, benchResult{
			FileType:          c.fileType,
			Coverage:          parsequality.TextCoverage(c.golden, out.MarkdownContent),
			Similarity:        parsequality.TextSimilarity(c.golden, out.MarkdownContent),
			StructureFidelity: parsequality.StructureFidelity(c.golden, out.MarkdownContent),
		})
	}

	report := map[string]interface{}{
		"engines":               engines,
		"simple_engine_quality": results,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
