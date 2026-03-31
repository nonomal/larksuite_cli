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

// mimeToExt maps Content-Type to file extension.
var mimeToExt = map[string]string{
	"video/mp4":        ".mp4",
	"video/webm":       ".webm",
	"audio/mpeg":       ".mp3",
	"audio/mp4":        ".m4a",
	"audio/ogg":        ".ogg",
	"audio/wav":        ".wav",
	"application/pdf":  ".pdf",
	"application/zip":  ".zip",
	"application/gzip": ".gz",
}

var MinutesDownload = common.Shortcut{
	Service:     "minutes",
	Command:     "+download",
	Description: "Download audio/video media file of a minute",
	Risk:        "read",
	Scopes:      []string{"minutes:minutes.media:export"},
	AuthTypes:   []string{"user", "bot"},
	Flags: []common.Flag{
		{Name: "minute-token", Desc: "minute token (from the minutes URL)", Required: true},
		{Name: "output", Desc: "local save path (defaults to <title>.<ext> based on minute title and content type)"},
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

		// Step 2: resolve output path (use minute title when --output is not specified)
		if outputPath == "" {
			outputPath = resolveDefaultOutputPath(ctx, runtime, minuteToken)
		}
		safePath, err := validate.SafeOutputPath(outputPath)
		if err != nil {
			return output.ErrValidation("unsafe output path: %s", err)
		}
		if err := common.EnsureWritableFile(safePath, overwrite); err != nil {
			return err
		}

		fmt.Fprintf(runtime.IO().ErrOut, "Downloading media: %s\n", common.MaskToken(minuteToken))

		sizeBytes, contentType, err := downloadMediaFile(ctx, runtime, downloadURL, safePath)
		if err != nil {
			return err
		}

		// Auto-detect extension from Content-Type (only when --output is not specified)
		finalPath := safePath
		if runtime.Str("output") == "" {
			if ext := extFromContentType(contentType); ext != "" && filepath.Ext(safePath) != ext {
				newPath := strings.TrimSuffix(safePath, filepath.Ext(safePath)) + ext
				if renameErr := os.Rename(safePath, newPath); renameErr == nil {
					finalPath = newPath
				}
			}
		}

		runtime.Out(map[string]interface{}{
			"saved_path": finalPath,
			"size_bytes": sizeBytes,
		}, nil)
		return nil
	},
}

// resolveDefaultOutputPath fetches the minute title and uses it as the default file name.
// Falls back to <token>.media if the title cannot be retrieved.
func resolveDefaultOutputPath(ctx context.Context, runtime *common.RuntimeContext, minuteToken string) string {
	infoData, err := runtime.DoAPIJSON(http.MethodGet,
		fmt.Sprintf("/open-apis/minutes/v1/minutes/%s", validate.EncodePathSegment(minuteToken)),
		nil, nil)
	if err == nil {
		if minute, ok := infoData["minute"].(map[string]interface{}); ok {
			if title := common.GetString(minute, "title"); title != "" {
				safe := sanitizeFileName(title)
				if safe != "" {
					return safe + ".media"
				}
			}
		}
	}
	// fall back to token-based name
	return minuteToken + ".media"
}

// sanitizeFileName removes unsafe characters from a file name.
func sanitizeFileName(name string) string {
	const maxLen = 200
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_",
		"\n", "_", "\r", "_", "\t", "_", "\x00", "_",
	)
	safe := replacer.Replace(strings.TrimSpace(name))
	safe = strings.Trim(safe, ".")
	if len(safe) > maxLen {
		safe = safe[:maxLen]
	}
	return safe
}

// extFromContentType returns a file extension for the given Content-Type, or "" if unknown.
func extFromContentType(contentType string) string {
	mimeType := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	if ext, ok := mimeToExt[mimeType]; ok {
		return ext
	}
	return ""
}

// downloadMediaFile streams a media file from a pre-signed URL to disk. Returns size and Content-Type.
func downloadMediaFile(ctx context.Context, runtime *common.RuntimeContext, downloadURL, safePath string) (int64, string, error) {
	httpClient, err := runtime.Factory.HttpClient()
	if err != nil {
		return 0, "", output.ErrNetwork("failed to get HTTP client: %s", err)
	}

	// clone the client with a longer timeout for large media files
	downloadClient := *httpClient
	downloadClient.Timeout = defaultMediaDownloadTimeout

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return 0, "", output.ErrNetwork("invalid download URL: %s", err)
	}
	// no Authorization header — download_url is a pre-signed URL

	resp, err := downloadClient.Do(req)
	if err != nil {
		return 0, "", output.ErrNetwork("download failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if len(body) > 0 {
			return 0, "", output.ErrNetwork("download failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return 0, "", output.ErrNetwork("download failed: HTTP %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")

	if err := os.MkdirAll(filepath.Dir(safePath), 0755); err != nil {
		return 0, "", output.Errorf(output.ExitInternal, "api_error", "cannot create parent directory: %s", err)
	}

	sizeBytes, err := validate.AtomicWriteFromReader(safePath, resp.Body, 0644)
	if err != nil {
		return 0, "", output.Errorf(output.ExitInternal, "api_error", "cannot create file: %s", err)
	}
	return sizeBytes, contentType, nil
}
