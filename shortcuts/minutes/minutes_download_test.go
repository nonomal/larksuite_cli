// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package minutes

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/larksuite/cli/internal/cmdutil"
	"github.com/larksuite/cli/internal/core"
	"github.com/larksuite/cli/internal/httpmock"
	"github.com/larksuite/cli/shortcuts/common"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

var warmOnce sync.Once

func warmTokenCache(t *testing.T) {
	t.Helper()
	warmOnce.Do(func() {
		f, _, _, reg := cmdutil.TestFactory(t, defaultConfig())
		reg.Register(&httpmock.Stub{
			URL: "/open-apis/auth/v3/tenant_access_token/internal",
			Body: map[string]interface{}{
				"code": 0, "msg": "ok",
				"tenant_access_token": "t-test-token", "expire": 7200,
			},
		})
		reg.Register(&httpmock.Stub{
			URL:  "/open-apis/test/v1/warm",
			Body: map[string]interface{}{"code": 0, "msg": "ok", "data": map[string]interface{}{}},
		})
		s := common.Shortcut{
			Service:   "test",
			Command:   "+warm",
			AuthTypes: []string{"bot"},
			Execute: func(_ context.Context, rctx *common.RuntimeContext) error {
				_, err := rctx.CallAPI("GET", "/open-apis/test/v1/warm", nil, nil)
				return err
			},
		}
		parent := &cobra.Command{Use: "test"}
		s.Mount(parent, f)
		parent.SetArgs([]string{"+warm"})
		parent.SilenceErrors = true
		parent.SilenceUsage = true
		parent.Execute()
	})
}

func mountAndRun(t *testing.T, s common.Shortcut, args []string, f *cmdutil.Factory, stdout *bytes.Buffer) error {
	t.Helper()
	warmTokenCache(t)
	parent := &cobra.Command{Use: "minutes"}
	s.Mount(parent, f)
	parent.SetArgs(args)
	parent.SilenceErrors = true
	parent.SilenceUsage = true
	if stdout != nil {
		stdout.Reset()
	}
	return parent.Execute()
}

func defaultConfig() *core.CliConfig {
	return &core.CliConfig{
		AppID: "test-app", AppSecret: "test-secret", Brand: core.BrandFeishu,
		UserOpenId: "ou_testuser",
	}
}

func mediaStub(token, downloadURL string) *httpmock.Stub {
	return &httpmock.Stub{
		Method: "GET",
		URL:    "/open-apis/minutes/v1/minutes/" + token + "/media",
		Body: map[string]interface{}{
			"code": 0, "msg": "ok",
			"data": map[string]interface{}{"download_url": downloadURL},
		},
	}
}

func downloadStub(url string, body []byte, contentType string) *httpmock.Stub {
	return &httpmock.Stub{
		URL:     url,
		RawBody: body,
		Headers: http.Header{"Content-Type": []string{contentType}},
	}
}

// chdir 切换工作目录并在测试结束后恢复
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir to %s: %v", dir, err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
}

// ---------------------------------------------------------------------------
// Unit tests: resolveOutputFromResponse
// ---------------------------------------------------------------------------

func TestResolveOutputFromResponse_ContentDisposition(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{
			"Content-Disposition": []string{`attachment; filename="meeting_recording.mp4"`},
			"Content-Type":        []string{"video/mp4"},
		},
	}
	got := resolveOutputFromResponse(resp, "tok001")
	if got != "meeting_recording.mp4" {
		t.Errorf("expected Content-Disposition filename, got %q", got)
	}
}

func TestResolveOutputFromResponse_ContentType(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{
			"Content-Type": []string{"video/mp4"},
		},
	}
	got := resolveOutputFromResponse(resp, "tok001")
	if !strings.HasPrefix(got, "tok001") {
		t.Errorf("expected token prefix, got %q", got)
	}
	if filepath := got[len("tok001"):]; filepath == "" {
		t.Errorf("expected extension after token, got %q", got)
	}
}

func TestResolveOutputFromResponse_Fallback(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	got := resolveOutputFromResponse(resp, "tok001")
	if got != "tok001.media" {
		t.Errorf("expected fallback %q, got %q", "tok001.media", got)
	}
}

func TestResolveOutputFromResponse_InvalidContentDisposition(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{
			"Content-Disposition": []string{"invalid;;;"},
			"Content-Type":        []string{"audio/mpeg"},
		},
	}
	got := resolveOutputFromResponse(resp, "tok001")
	// Content-Disposition 解析失败，应 fall back 到 Content-Type
	if !strings.HasPrefix(got, "tok001") {
		t.Errorf("expected token prefix from Content-Type fallback, got %q", got)
	}
}

func TestResolveOutputFromResponse_EmptyDispositionFilename(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{
			"Content-Disposition": []string{"attachment"},
			"Content-Type":        []string{"video/mp4"},
		},
	}
	got := resolveOutputFromResponse(resp, "tok001")
	if got == "" {
		t.Error("expected non-empty filename")
	}
	// Content-Disposition 没有 filename，应 fall back 到 Content-Type
	if !strings.HasPrefix(got, "tok001") {
		t.Errorf("expected token prefix, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Integration tests: +download with mocked HTTP
// ---------------------------------------------------------------------------

func TestDownload_DryRun(t *testing.T) {
	f, stdout, _, _ := cmdutil.TestFactory(t, defaultConfig())
	err := mountAndRun(t, MinutesDownload, []string{
		"+download", "--minute-token", "tok001", "--dry-run", "--as", "user",
	}, f, stdout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "media") {
		t.Errorf("dry-run should show media API path, got: %s", out)
	}
	if !strings.Contains(out, "tok001") {
		t.Errorf("dry-run should show minute_token, got: %s", out)
	}
}

func TestDownload_UrlOnly(t *testing.T) {
	f, stdout, _, reg := cmdutil.TestFactory(t, defaultConfig())
	reg.Register(mediaStub("tok001", "https://example.com/presigned/download"))

	err := mountAndRun(t, MinutesDownload, []string{
		"+download", "--minute-token", "tok001", "--url-only", "--as", "bot",
	}, f, stdout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), "https://example.com/presigned/download") {
		t.Errorf("url-only should output download URL, got: %s", stdout.String())
	}
}

func TestDownload_FullDownload(t *testing.T) {
	chdir(t, t.TempDir())

	f, stdout, _, reg := cmdutil.TestFactory(t, defaultConfig())
	reg.Register(mediaStub("tok001", "https://example.com/presigned/download"))
	reg.Register(downloadStub("example.com/presigned/download", []byte("fake-video-content"), "video/mp4"))

	err := mountAndRun(t, MinutesDownload, []string{
		"+download", "--minute-token", "tok001", "--output", "output.mp4", "--as", "bot",
	}, f, stdout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile("output.mp4")
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	if string(data) != "fake-video-content" {
		t.Errorf("file content = %q, want %q", string(data), "fake-video-content")
	}
	if !strings.Contains(stdout.String(), "saved_path") {
		t.Errorf("output should contain saved_path, got: %s", stdout.String())
	}
}

func TestDownload_OverwriteProtection(t *testing.T) {
	chdir(t, t.TempDir())
	os.WriteFile("existing.mp4", []byte("old"), 0644)

	f, _, _, reg := cmdutil.TestFactory(t, defaultConfig())
	reg.Register(mediaStub("tok001", "https://example.com/presigned/download"))
	reg.Register(downloadStub("example.com/presigned/download", []byte("new-content"), "video/mp4"))

	err := mountAndRun(t, MinutesDownload, []string{
		"+download", "--minute-token", "tok001", "--output", "existing.mp4", "--as", "bot",
	}, f, nil)
	if err == nil {
		t.Fatal("expected error for existing file without --overwrite")
	}
	if !strings.Contains(err.Error(), "exists") {
		t.Errorf("error should mention file exists, got: %v", err)
	}

	// 原文件不应被覆盖
	data, _ := os.ReadFile("existing.mp4")
	if string(data) != "old" {
		t.Errorf("original file should be preserved, got %q", string(data))
	}
}

func TestDownload_HttpError(t *testing.T) {
	chdir(t, t.TempDir())

	f, _, _, reg := cmdutil.TestFactory(t, defaultConfig())
	reg.Register(mediaStub("tok001", "https://example.com/presigned/download"))
	reg.Register(&httpmock.Stub{
		URL:     "example.com/presigned/download",
		Status:  403,
		RawBody: []byte("Forbidden"),
	})

	err := mountAndRun(t, MinutesDownload, []string{
		"+download", "--minute-token", "tok001", "--output", "output.mp4", "--as", "bot",
	}, f, nil)
	if err == nil {
		t.Fatal("expected error for HTTP 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should contain status code, got: %v", err)
	}
}
