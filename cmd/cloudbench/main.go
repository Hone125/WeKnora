// Command cloudbench measures the two cloud parser engines — mineru_cloud and
// paddleocr_vl_cloud — against golden Markdown, extending M5's cross-engine
// baseline beyond the two locally-measured engines (simple, builtin).
//
// The two engines are HTTP forwarders to hosted APIs. This tool talks to those
// APIs directly over net/http and scores the returned Markdown with
// internal/parsequality, so it needs no cgo and no internal/types (which drag in
// the cgo-heavy gojieba + pg_query deps that break a CGO_ENABLED=0 build on a
// machine without a C toolchain). Credentials come from environment variables,
// never committed:
//
//	MINERU_API_KEY            MinerU Cloud API key (mineru.net → API 管理 → Token)
//	PADDLEOCR_VL_CLOUD_TOKEN  PaddleOCR-VL AI Studio access token
//
// The same documents (and, where possible, the same golden) are used across all
// bench tools, so the resulting scores sit side-by-side in the baseline:
//   - issue_2634_vertical_merge.docx is the same fixture builtinbench scores.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/parsequality"
)

const (
	mineruBaseURL = "https://mineru.net/api/v4"
	paddleBaseURL = "https://paddleocr.aistudio-app.com/api/v2/ocr/jobs"
	paddleModel   = "PaddleOCR-VL-1.6"
	pollInterval  = 3 * time.Second
	engineTimeout = 180 * time.Second
	httpTimeout   = 60 * time.Second
)

// probe is one input document fed to each cloud engine. `golden` is the
// Markdown a correct parser should produce; when empty the tool only prints the
// raw engine output (used to first observe behaviour, then pin the golden).
type probe struct {
	name     string
	fileType string
	fileName string
	filePath string
	golden   string
}

var probes = []probe{
	{
		name:     "office docx with merged table",
		fileType: "docx",
		fileName: "issue_2634_vertical_merge.docx",
		filePath: "docreader/tests/fixtures/issue_2634_vertical_merge.docx",
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

// engineResult is the measured quality of one engine on one probe.
type engineResult struct {
	Engine            string  `json:"engine"`
	File              string  `json:"file"`
	FileType          string  `json:"file_type"`
	Error             string  `json:"error,omitempty"`
	Output            string  `json:"output,omitempty"`
	Coverage          float64 `json:"coverage,omitempty"`
	Similarity        float64 `json:"similarity,omitempty"`
	StructureFidelity float64 `json:"structure_fidelity,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "cloudbench:", err)
		os.Exit(1)
	}
}

func run() error {
	mineruKey := os.Getenv("MINERU_API_KEY")
	paddleToken := os.Getenv("PADDLEOCR_VL_CLOUD_TOKEN")

	engines := []struct {
		name  string
		parse func(context.Context, []byte, string) (string, error)
	}{
		{name: "mineru_cloud", parse: func(ctx context.Context, c []byte, f string) (string, error) {
			return mineruParse(ctx, mineruKey, c, f)
		}},
		{name: "paddleocr_vl_cloud", parse: func(ctx context.Context, c []byte, f string) (string, error) {
			return paddleParse(ctx, paddleToken, c, f)
		}},
	}

	results := make([]engineResult, 0, len(probes)*len(engines))
	for _, p := range probes {
		content, err := os.ReadFile(p.filePath)
		if err != nil {
			for _, e := range engines {
				results = append(results, engineResult{Engine: e.name, File: p.fileName, FileType: p.fileType, Error: "read file: " + err.Error()})
			}
			continue
		}

		for _, e := range engines {
			ctx, cancel := context.WithTimeout(context.Background(), engineTimeout)
			markdown, err := e.parse(ctx, content, p.fileName)
			cancel()

			r := engineResult{Engine: e.name, File: p.fileName, FileType: p.fileType}
			if err != nil {
				r.Error = err.Error()
				results = append(results, r)
				continue
			}
			r.Output = markdown
			if p.golden != "" {
				r.Coverage = parsequality.TextCoverage(p.golden, markdown)
				r.Similarity = parsequality.TextSimilarity(p.golden, markdown)
				r.StructureFidelity = parsequality.StructureFidelity(p.golden, markdown)
			}
			results = append(results, r)
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(results)
}

// ---------------------------------------------------------------------------
// MinerU Cloud — POST /file-urls/batch → PUT file → poll /extract-results/batch
// ---------------------------------------------------------------------------

type mineruApplyResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		BatchID  string   `json:"batch_id"`
		FileURLs []string `json:"file_urls"`
	} `json:"data"`
}

type mineruExtractItem struct {
	State      string `json:"state"`
	Markdown   string `json:"markdown"`
	Content    string `json:"content"`
	Text       string `json:"text"`
	ErrMsg     string `json:"err_msg"`
	FullZipURL string `json:"full_zip_url"`
}

type mineruPollResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ExtractResult json.RawMessage `json:"extract_result"`
	} `json:"data"`
}

func mineruParse(ctx context.Context, apiKey string, content []byte, fileName string) (string, error) {
	if apiKey == "" {
		return "", fmt.Errorf("MINERU_API_KEY not set")
	}

	batchID, uploadURL, err := mineruApply(ctx, apiKey, fileName)
	if err != nil {
		return "", fmt.Errorf("apply upload: %w", err)
	}
	if err := mineruUpload(ctx, uploadURL, content); err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	return mineruPoll(ctx, apiKey, batchID)
}

func mineruApply(ctx context.Context, apiKey, fileName string) (string, string, error) {
	payload := map[string]interface{}{
		"files":          []map[string]string{{"name": fileName, "data_id": randomHex(16)}},
		"model_version":  "pipeline",
		"is_ocr":         true,
		"enable_formula": true,
		"enable_table":   true,
		"language":       "ch",
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mineruBaseURL+"/file-urls/batch", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	respBody, err := do(req)
	if err != nil {
		return "", "", err
	}
	var result mineruApplyResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", "", fmt.Errorf("decode apply response: %w", err)
	}
	if result.Code != 0 {
		return "", "", fmt.Errorf("apply error code=%d msg=%s", result.Code, result.Msg)
	}
	if len(result.Data.FileURLs) == 0 {
		return "", "", fmt.Errorf("apply returned no file_urls")
	}
	return result.Data.BatchID, result.Data.FileURLs[0], nil
}

func mineruUpload(ctx context.Context, uploadURL string, content []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(content))
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: httpTimeout}).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("PUT upload status %d", resp.StatusCode)
	}
	return nil
}

func mineruPoll(ctx context.Context, apiKey, batchID string) (string, error) {
	deadline := time.Now().Add(engineTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("%s/extract-results/batch/%s", mineruBaseURL, batchID), nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)

		respBody, err := do(req)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}
		var poll mineruPollResponse
		if err := json.Unmarshal(respBody, &poll); err != nil {
			time.Sleep(pollInterval)
			continue
		}
		if poll.Code != 0 {
			return "", fmt.Errorf("poll error code=%d msg=%s", poll.Code, poll.Msg)
		}

		// extract_result may be an object or an array.
		var item mineruExtractItem
		if len(poll.Data.ExtractResult) == 0 {
			time.Sleep(pollInterval)
			continue
		}
		if poll.Data.ExtractResult[0] == '[' {
			var items []mineruExtractItem
			if err := json.Unmarshal(poll.Data.ExtractResult, &items); err != nil {
				time.Sleep(pollInterval)
				continue
			}
			if len(items) == 0 {
				time.Sleep(pollInterval)
				continue
			}
			item = items[0]
		} else if err := json.Unmarshal(poll.Data.ExtractResult, &item); err != nil {
			time.Sleep(pollInterval)
			continue
		}

		switch strings.ToLower(item.State) {
		case "done":
			if md := firstNonEmpty(item.Markdown, item.Content, item.Text); md != "" {
				return md, nil
			}
			if item.FullZipURL != "" {
				md, err := mineruDownloadZip(ctx, item.FullZipURL)
				if err != nil {
					return "", fmt.Errorf("download zip: %w", err)
				}
				return md, nil
			}
			return "", fmt.Errorf("state=done but no markdown/content/zip; raw extract_result=%s", truncate(string(poll.Data.ExtractResult), 1500))
		case "failed":
			return "", fmt.Errorf("MinerU Cloud task failed: %s", item.ErrMsg)
		}
		time.Sleep(pollInterval)
	}
	return "", fmt.Errorf("MinerU Cloud task timed out")
}

// mineruDownloadZip downloads the full_zip_url result and returns the primary
// .md file inside it (MinerU returns the markdown inline for small documents but
// a zip bundle for larger ones).
func mineruDownloadZip(ctx context.Context, zipURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zipURL, nil)
	if err != nil {
		return "", err
	}
	respBody, err := do(req)
	if err != nil {
		return "", err
	}

	zr, err := zip.NewReader(bytes.NewReader(respBody), int64(len(respBody)))
	if err != nil {
		return "", fmt.Errorf("open zip: %w", err)
	}
	var mdFiles []string
	entries := make(map[string]*zip.File)
	for _, f := range zr.File {
		entries[f.Name] = f
		if strings.HasSuffix(f.Name, ".md") {
			mdFiles = append(mdFiles, f.Name)
		}
	}
	if len(mdFiles) == 0 {
		return "", fmt.Errorf("no .md file found in zip")
	}
	sort.Slice(mdFiles, func(i, j int) bool {
		di, dj := strings.Count(mdFiles[i], "/"), strings.Count(mdFiles[j], "/")
		if di != dj {
			return di < dj
		}
		return mdFiles[i] < mdFiles[j]
	})

	rc, err := entries[mdFiles[0]].Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	md, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	return string(md), nil
}

// ---------------------------------------------------------------------------
// PaddleOCR-VL Cloud — POST /jobs (multipart) → poll /jobs/{id} → download JSONL
// ---------------------------------------------------------------------------

type paddleSubmitResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		JobID string `json:"jobId"`
	} `json:"data"`
}

type paddlePollResponse struct {
	Code int `json:"code"`
	Data struct {
		State     string `json:"state"`
		ErrorMsg  string `json:"errorMsg"`
		ResultURL struct {
			JSONURL string `json:"jsonUrl"`
		} `json:"resultUrl"`
	} `json:"data"`
}

type paddleResultLine struct {
	Result struct {
		LayoutParsingResults []struct {
			Markdown struct {
				Text string `json:"text"`
			} `json:"markdown"`
		} `json:"layoutParsingResults"`
	} `json:"result"`
}

func paddleParse(ctx context.Context, token string, content []byte, fileName string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("PADDLEOCR_VL_CLOUD_TOKEN not set")
	}

	jobID, err := paddleSubmit(ctx, token, content, fileName)
	if err != nil {
		return "", fmt.Errorf("submit: %w", err)
	}
	jsonURL, err := paddlePoll(ctx, token, jobID)
	if err != nil {
		return "", fmt.Errorf("poll: %w", err)
	}
	return paddleDownload(ctx, jsonURL)
}

func paddleSubmit(ctx context.Context, token string, content []byte, fileName string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", paddleModel)
	_ = writer.WriteField("optionalPayload", `{"useSealRecognition":true,"useChartRecognition":false}`)
	part, err := writer.CreateFormFile("file", filepath.Base(fileName))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(content); err != nil {
		return "", err
	}
	writer.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, paddleBaseURL, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	respBody, err := do(req)
	if err != nil {
		return "", err
	}
	var result paddleSubmitResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode submit response: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("submit error code=%d msg=%s", result.Code, result.Msg)
	}
	if result.Data.JobID == "" {
		return "", fmt.Errorf("submit returned no jobId: %s", string(respBody))
	}
	return result.Data.JobID, nil
}

func paddlePoll(ctx context.Context, token, jobID string) (string, error) {
	deadline := time.Now().Add(engineTimeout)
	url := paddleBaseURL + "/" + jobID
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "bearer "+token)

		respBody, err := do(req)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}
		var poll paddlePollResponse
		if err := json.Unmarshal(respBody, &poll); err != nil {
			time.Sleep(pollInterval)
			continue
		}
		if poll.Code != 0 {
			return "", fmt.Errorf("poll error code=%d", poll.Code)
		}

		switch strings.ToLower(poll.Data.State) {
		case "done":
			if poll.Data.ResultURL.JSONURL == "" {
				return "", fmt.Errorf("state=done but no jsonUrl")
			}
			return poll.Data.ResultURL.JSONURL, nil
		case "failed":
			return "", fmt.Errorf("task failed: %s", poll.Data.ErrorMsg)
		}
		time.Sleep(pollInterval)
	}
	return "", fmt.Errorf("PaddleOCR-VL task timed out")
}

func paddleDownload(ctx context.Context, jsonURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jsonURL, nil)
	if err != nil {
		return "", err
	}
	respBody, err := do(req)
	if err != nil {
		return "", err
	}
	var line paddleResultLine
	if err := json.Unmarshal(respBody, &line); err != nil {
		return "", fmt.Errorf("decode result JSON: %w", err)
	}
	if len(line.Result.LayoutParsingResults) == 0 {
		return "", fmt.Errorf("result has no layoutParsingResults")
	}
	return line.Result.LayoutParsingResults[0].Markdown.Text, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func do(req *http.Request) ([]byte, error) {
	resp, err := (&http.Client{Timeout: httpTimeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %d: %s", resp.StatusCode, truncate(string(body), 500))
	}
	return body, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
