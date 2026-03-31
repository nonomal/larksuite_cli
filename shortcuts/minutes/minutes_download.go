// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package minutes

import (
	"context"
	"fmt"
	"io"
	"mime"
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
		{Name: "output", Desc: "local save path (defaults to original filename from server response)"},
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

		// Step 1: get the download URL from the media API
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

		// --url-only mode: print download URL only
		if urlOnly {
			runtime.Out(map[string]interface{}{
				"download_url": downloadURL,
			}, nil)
			return nil
		}

		// Step 2: download the file and resolve the output path
		fmt.Fprintf(runtime.IO().ErrOut, "Downloading media: %s\n", common.MaskToken(minuteToken))

		result, err := downloadMediaFile(ctx, runtime, downloadURL, outputPath, minuteToken, overwrite)
		if err != nil {
			return err
		}

		runtime.Out(map[string]interface{}{
			"saved_path": result.savedPath,
			"size_bytes": result.sizeBytes,
		}, nil)
		return nil
	},
}

type downloadResult struct {
	savedPath string
	sizeBytes int64
}

// downloadMediaFile streams a media file from a pre-signed URL to disk.
// When outputPath is empty, it resolves the filename from the Content-Disposition
// header, falling back to Content-Type extension detection, then <token>.media.
func downloadMediaFile(ctx context.Context, runtime *common.RuntimeContext, downloadURL, outputPath, minuteToken string, overwrite bool) (*downloadResult, error) {
	httpClient, err := runtime.Factory.HttpClient()
	if err != nil {
		return nil, output.ErrNetwork("failed to get HTTP client: %s", err)
	}

	// clone the client with a longer timeout for large media files
	downloadClient := *httpClient
	downloadClient.Timeout = defaultMediaDownloadTimeout

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, output.ErrNetwork("invalid download URL: %s", err)
	}
	// no Authorization header — download_url is a pre-signed URL

	resp, err := downloadClient.Do(req)
	if err != nil {
		return nil, output.ErrNetwork("download failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if len(body) > 0 {
			return nil, output.ErrNetwork("download failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, output.ErrNetwork("download failed: HTTP %d", resp.StatusCode)
	}

	// resolve output path from response headers when --output is not specified
	if outputPath == "" {
		outputPath = resolveOutputFromResponse(resp, minuteToken)
	}

	safePath, err := validate.SafeOutputPath(outputPath)
	if err != nil {
		return nil, output.ErrValidation("unsafe output path: %s", err)
	}
	if err := common.EnsureWritableFile(safePath, overwrite); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(safePath), 0755); err != nil {
		return nil, output.Errorf(output.ExitInternal, "api_error", "cannot create parent directory: %s", err)
	}

	sizeBytes, err := validate.AtomicWriteFromReader(safePath, resp.Body, 0644)
	if err != nil {
		return nil, output.Errorf(output.ExitInternal, "api_error", "cannot create file: %s", err)
	}
	return &downloadResult{savedPath: safePath, sizeBytes: sizeBytes}, nil
}

// resolveOutputFromResponse derives the output filename from HTTP response headers.
// Priority: Content-Disposition filename > Content-Type extension > fallback to <token>.media.
func resolveOutputFromResponse(resp *http.Response, minuteToken string) string {
	// try Content-Disposition header for the original filename
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if filename := params["filename"]; filename != "" {
				return filename
			}
		}
	}

	// fall back to Content-Type extension detection
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		if mediaType, _, err := mime.ParseMediaType(ct); err == nil {
			if exts, err := mime.ExtensionsByType(mediaType); err == nil && len(exts) > 0 {
				return minuteToken + exts[0]
			}
		}
	}

	return minuteToken + ".media"
}
