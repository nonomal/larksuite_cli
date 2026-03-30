// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package minutes

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/larksuite/cli/internal/output"
	"github.com/larksuite/cli/internal/validate"
	"github.com/larksuite/cli/shortcuts/common"
)

const defaultMediaDownloadTimeout = 300 * time.Second

var MinutesDownload = common.Shortcut{
	Service:     "minutes",
	Command:     "+download",
	Description: "Download audio/video media file of a minute",
	Risk:        "read",
	Scopes:      []string{"minutes:minutes.media:export"},
	AuthTypes:   []string{"user", "bot"},
	Flags: []common.Flag{
		{Name: "minute-token", Desc: "minute token (from the minutes URL)", Required: true},
		{Name: "output", Desc: "local save path (defaults to <minute-token>.media)"},
		{Name: "overwrite", Type: "bool", Desc: "overwrite existing output file"},
		{Name: "url-only", Type: "bool", Desc: "only print the download URL without downloading the file"},
	},
	DryRun: func(ctx context.Context, runtime *common.RuntimeContext) *common.DryRunAPI {
		minuteToken := runtime.Str("minute-token")
		outputPath := runtime.Str("output")
		if outputPath == "" {
			outputPath = minuteToken + ".media"
		}
		return common.NewDryRunAPI().
			GET("/open-apis/minutes/v1/minutes/:minute_token/media").
			Set("minute_token", minuteToken).Set("output", outputPath)
	},
	Execute: func(ctx context.Context, runtime *common.RuntimeContext) error {
		minuteToken := runtime.Str("minute-token")
		outputPath := runtime.Str("output")
		overwrite := runtime.Bool("overwrite")
		urlOnly := runtime.Bool("url-only")

		if err := validate.ResourceName(minuteToken, "--minute-token"); err != nil {
			return output.ErrValidation("%s", err)
		}

		// 第一步：调用 API 获取下载链接
		data, err := runtime.DoAPIJSON(http.MethodGet,
			fmt.Sprintf("/open-apis/minutes/v1/minutes/%s/media", validate.EncodePathSegment(minuteToken)),
			nil, nil)
		if err != nil {
			return err
		}

		downloadURL := common.GetString(data, "download_url")
		if downloadURL == "" {
			return output.Errorf(output.ExitAPI, "api_error", "API returned empty download_url")
		}

		// --url-only 模式：仅输出下载链接
		if urlOnly {
			runtime.Out(map[string]interface{}{
				"download_url": downloadURL,
			}, nil)
			return nil
		}

		// 第二步：从下载链接下载文件
		if outputPath == "" {
			outputPath = minuteToken + ".media"
		}
		safePath, err := validate.SafeOutputPath(outputPath)
		if err != nil {
			return output.ErrValidation("unsafe output path: %s", err)
		}
		if err := common.EnsureWritableFile(safePath, overwrite); err != nil {
			return err
		}

		fmt.Fprintf(runtime.IO().ErrOut, "Downloading media: %s\n", common.MaskToken(minuteToken))

		sizeBytes, err := downloadMediaFile(ctx, runtime, downloadURL, safePath)
		if err != nil {
			return err
		}

		runtime.Out(map[string]interface{}{
			"saved_path": safePath,
			"size_bytes": sizeBytes,
		}, nil)
		return nil
	},
}

// downloadMediaFile 从 pre-signed URL 流式下载媒体文件到本地
func downloadMediaFile(ctx context.Context, runtime *common.RuntimeContext, downloadURL, safePath string) (int64, error) {
	httpClient, err := runtime.Factory.HttpClient()
	if err != nil {
		return 0, output.ErrNetwork("failed to get HTTP client: %s", err)
	}

	// 复制 client 并覆盖超时，避免默认 30s 超时导致大文件下载失败
	downloadClient := *httpClient
	downloadClient.Timeout = defaultMediaDownloadTimeout

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return 0, output.ErrNetwork("invalid download URL: %s", err)
	}
	// 不发送 Authorization header，download_url 是 pre-signed URL

	resp, err := downloadClient.Do(req)
	if err != nil {
		return 0, output.ErrNetwork("download failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if len(body) > 0 {
			return 0, output.ErrNetwork("download failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return 0, output.ErrNetwork("download failed: HTTP %d", resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(safePath), 0755); err != nil {
		return 0, output.Errorf(output.ExitInternal, "api_error", "cannot create parent directory: %s", err)
	}

	sizeBytes, err := validate.AtomicWriteFromReader(safePath, resp.Body, 0644)
	if err != nil {
		return 0, output.Errorf(output.ExitInternal, "api_error", "cannot create file: %s", err)
	}
	return sizeBytes, nil
}
