package skills

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGitHubURL(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		rawURL     string
		wantSource GitHubSource
		wantErr    error
	}{
		{
			name:   "repo root",
			rawURL: "https://github.com/example/weather-skill",
			wantSource: GitHubSource{
				OriginalURL: "https://github.com/example/weather-skill",
				Owner:       "example",
				Repo:        "weather-skill",
			},
		},
		{
			name:   "tree subpath",
			rawURL: "https://github.com/example/weather-skill/tree/main/skills/current",
			wantSource: GitHubSource{
				OriginalURL: "https://github.com/example/weather-skill/tree/main/skills/current",
				Owner:       "example",
				Repo:        "weather-skill",
				Ref:         "main",
				Subpath:     "skills/current",
			},
		},
		{
			name:    "invalid host",
			rawURL:  "https://example.com/weather-skill",
			wantErr: ErrInvalidGitHubURL,
		},
		{
			name:    "invalid scheme",
			rawURL:  "http://github.com/example/weather-skill",
			wantErr: ErrInvalidGitHubURL,
		},
		{
			name:    "missing repo path",
			rawURL:  "https://github.com/example",
			wantErr: ErrInvalidGitHubURL,
		},
		{
			name:    "query string unsupported",
			rawURL:  "https://github.com/example/weather-skill?ref=main",
			wantErr: ErrInvalidGitHubURL,
		},
		{
			name:    "tree requires ref",
			rawURL:  "https://github.com/example/weather-skill/tree",
			wantErr: ErrInvalidGitHubURL,
		},
		{
			name:    "unsupported format",
			rawURL:  "https://github.com/example/weather-skill/releases/tag/v1",
			wantErr: ErrInvalidGitHubURL,
		},
		{
			name:    "blob requires path",
			rawURL:  "https://github.com/example/weather-skill/blob/main",
			wantErr: ErrInvalidGitHubURL,
		},
		{
			name:   "git suffix",
			rawURL: "https://github.com/example/weather-skill.git",
			wantSource: GitHubSource{
				OriginalURL: "https://github.com/example/weather-skill.git",
				Owner:       "example",
				Repo:        "weather-skill",
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			source, err := ParseGitHubURL(testCase.rawURL)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("ParseGitHubURL() error = %v, want %v", err, testCase.wantErr)
			}
			if testCase.wantErr != nil {
				return
			}

			if source != testCase.wantSource {
				t.Fatalf("ParseGitHubURL() = %#v, want %#v", source, testCase.wantSource)
			}
		})
	}
}

func TestCandidateGitHubSourcesIncludesSlashRefCandidate(t *testing.T) {
	t.Parallel()

	sources, err := candidateGitHubSources("https://github.com/example/weather-skill/tree/feature/foo/skills/current")
	if err != nil {
		t.Fatalf("candidateGitHubSources() error = %v", err)
	}

	var found bool
	for _, source := range sources {
		if source.Ref == "feature/foo" && source.Subpath == "skills/current" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("candidateGitHubSources() = %#v, want feature/foo + skills/current candidate", sources)
	}
}

func TestCandidateGitHubSourcesFallsBackToParsedSource(t *testing.T) {
	t.Parallel()

	sources, err := candidateGitHubSources("https://github.com/example/weather-skill")
	if err != nil {
		t.Fatalf("candidateGitHubSources() error = %v", err)
	}
	if len(sources) != 1 || sources[0].Repo != "weather-skill" || sources[0].Ref != "" {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestCandidateGitHubSourcesWithTreeRefOnlyReturnsParsedSource(t *testing.T) {
	t.Parallel()

	sources, err := candidateGitHubSources("https://github.com/example/weather-skill/tree/main")
	if err != nil {
		t.Fatalf("candidateGitHubSources() error = %v", err)
	}
	if len(sources) != 1 || sources[0].Ref != "main" || sources[0].Subpath != "" {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestCandidateGitHubSourcesForBlobSlashRef(t *testing.T) {
	t.Parallel()

	sources, err := candidateGitHubSources("https://github.com/example/weather-skill/blob/feature/foo/skills/current/prompt.md")
	if err != nil {
		t.Fatalf("candidateGitHubSources() error = %v", err)
	}
	if len(sources) < 2 {
		t.Fatalf("sources = %#v", sources)
	}
	if sources[0].Ref != "feature/foo/skills/current" || sources[2].Ref != "feature/foo" {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestListResourcesSkipsHiddenEntries(t *testing.T) {
	service := newTestService(t)
	writeSkillFile(t, service.root, "weather-current", "prompt.md", "# prompt")
	writeSkillFile(t, service.root, "weather-current", ".secret", "hidden")
	writeSkillFile(t, service.root, "weather-current", "docs/readme.md", "docs")
	writeSkillFile(t, service.root, "weather-current", ".hidden/readme.md", "skip")

	resources, err := service.ListResources("current", "weather-current")
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}

	if len(resources) != 2 {
		t.Fatalf("len(resources) = %d, want 2", len(resources))
	}
	if resources[0].URI != BuildResourceURI("current", "docs/readme.md") {
		t.Fatalf("resources[0].URI = %q", resources[0].URI)
	}
	if resources[1].URI != BuildResourceURI("current", "prompt.md") {
		t.Fatalf("resources[1].URI = %q", resources[1].URI)
	}
}

func TestListResourcesRejectsEscapingSymlink(t *testing.T) {
	service := newTestService(t)
	outsideFile := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	skillRoot := filepath.Join(service.root, "weather-current")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(skillRoot, "escape.txt")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	_, err := service.ListResources("current", "weather-current")
	if !errors.Is(err, ErrPathEscape) {
		t.Fatalf("ListResources() error = %v, want %v", err, ErrPathEscape)
	}
}

func TestListResourcesFollowsPublishedSymlink(t *testing.T) {
	service := newTestService(t)
	tempDir := filepath.Join(t.TempDir(), "content")
	if err := os.MkdirAll(filepath.Join(tempDir, "docs"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "docs", "readme.md"), []byte("docs"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "prompt.md"), []byte("# prompt"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := atomicReplaceDirectory(tempDir, filepath.Join(service.root, "weather-current")); err != nil {
		t.Fatalf("atomicReplaceDirectory() error = %v", err)
	}

	resources, err := service.ListResources("current", "weather-current")
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("len(resources) = %d, want 2", len(resources))
	}
}

func TestReadResourceRejectsEscapingSymlink(t *testing.T) {
	service := newTestService(t)
	outsideFile := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	skillRoot := filepath.Join(service.root, "weather-current")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(skillRoot, "escape.txt")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	_, err := service.ReadResource("current", "weather-current", BuildResourceURI("current", "escape.txt"))
	if !errors.Is(err, ErrPathEscape) {
		t.Fatalf("ReadResource() error = %v, want %v", err, ErrPathEscape)
	}
}

func TestReadResourceReturnsBlobForBinaryFile(t *testing.T) {
	service := newTestService(t)
	writeSkillBinaryFile(t, service.root, "weather-current", "icon.png", []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x01})

	content, err := service.ReadResource("current", "weather-current", BuildResourceURI("current", "icon.png"))
	if err != nil {
		t.Fatalf("ReadResource() error = %v", err)
	}
	if content.Text != "" {
		t.Fatalf("content.Text = %q, want empty", content.Text)
	}
	if content.Blob != base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x01}) {
		t.Fatalf("content.Blob = %q", content.Blob)
	}
}

func TestReadResourceReturnsTextForTextFile(t *testing.T) {
	service := newTestService(t)
	writeSkillFile(t, service.root, "weather-current", "prompt.md", "# prompt")

	content, err := service.ReadResource("current", "weather-current", BuildResourceURI("current", "prompt.md"))
	if err != nil {
		t.Fatalf("ReadResource() error = %v", err)
	}
	if content.Text != "# prompt" || content.Blob != "" {
		t.Fatalf("content = %#v", content)
	}
}

func TestAtomicReplaceDirectoryUsesVersionedSymlink(t *testing.T) {
	root := t.TempDir()
	firstTempDir := filepath.Join(root, "tmp-first")
	if err := os.MkdirAll(firstTempDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(firstTempDir, "prompt.md"), []byte("first"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	finalDir := filepath.Join(root, "weather-current")
	if err := atomicReplaceDirectory(firstTempDir, finalDir); err != nil {
		t.Fatalf("atomicReplaceDirectory() error = %v", err)
	}

	info, err := os.Lstat(finalDir)
	if err != nil {
		t.Fatalf("Lstat() error = %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("finalDir is not a symlink: mode=%v", info.Mode())
	}

	firstTarget, err := linkedDirectoryTarget(finalDir)
	if err != nil {
		t.Fatalf("linkedDirectoryTarget() error = %v", err)
	}

	secondTempDir := filepath.Join(root, "tmp-second")
	if err := os.MkdirAll(secondTempDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(secondTempDir, "prompt.md"), []byte("second"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := atomicReplaceDirectory(secondTempDir, finalDir); err != nil {
		t.Fatalf("atomicReplaceDirectory() second error = %v", err)
	}

	secondTarget, err := linkedDirectoryTarget(finalDir)
	if err != nil {
		t.Fatalf("linkedDirectoryTarget() second error = %v", err)
	}
	if secondTarget == firstTarget {
		t.Fatalf("symlink target did not change: %q", secondTarget)
	}
	if _, err := os.Stat(firstTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old snapshot still exists, err=%v", err)
	}
}

func TestNewServiceFromEnv(t *testing.T) {
	root := t.TempDir()
	t.Setenv(StorageRootEnv, root)

	service, err := NewServiceFromEnv()
	if err != nil {
		t.Fatalf("NewServiceFromEnv() error = %v", err)
	}
	if service.root != root {
		t.Fatalf("service.root = %q, want %q", service.root, root)
	}
}

func TestNewServiceRejectsEmptyRoot(t *testing.T) {
	_, err := NewService(" ")
	if err == nil {
		t.Fatal("NewService() error = nil, want error")
	}
}

func TestSyncDownloadsArchiveSubpath(t *testing.T) {
	service := newTestService(t)
	service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "api.github.com" {
			t.Fatalf("request host = %q, want api.github.com", request.URL.Host)
		}
		if request.URL.Path != "/repos/example/weather-skill/tarball/main" {
			t.Fatalf("request path = %q", request.URL.Path)
		}

		body := buildGitHubArchive(t, map[string]string{
			"weather-skill-main/skills-current/prompt.md":      "# current",
			"weather-skill-main/skills-current/docs/readme.md": "docs",
			"weather-skill-main/skills-other/prompt.md":        "# other",
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(body)),
		}, nil
	})}

	if err := service.Sync(context.Background(), "https://github.com/example/weather-skill/tree/main/skills-current", "weather-current"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	payload, err := os.ReadFile(filepath.Join(service.root, "weather-current", "prompt.md"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(payload) != "# current" {
		t.Fatalf("prompt.md = %q", string(payload))
	}
	if _, err := os.Stat(filepath.Join(service.root, "weather-current", "docs", "readme.md")); err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(service.root, "weather-current", "..", "other", "prompt.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected other prompt visibility, err = %v", err)
	}
}

func TestSyncTriesSlashRefCandidates(t *testing.T) {
	service := newTestService(t)
	requests := 0
	service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		switch request.URL.Path {
		case "/repos/example/weather-skill/tarball/feature/foo/skills":
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(nil)),
			}, nil
		case "/repos/example/weather-skill/tarball/feature/foo":
			body := buildGitHubArchive(t, map[string]string{
				"weather-skill-feature-foo/skills/current/prompt.md": "# current",
			})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		default:
			t.Fatalf("unexpected request path: %s", request.URL.Path)
			return nil, nil
		}
	})}

	err := service.Sync(context.Background(), "https://github.com/example/weather-skill/tree/feature/foo/skills/current", "weather-current")
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestSyncReturnsLastArchiveErrorAfterTryingCandidates(t *testing.T) {
	service := newTestService(t)
	service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(nil)),
		}, nil
	})}

	err := service.Sync(context.Background(), "https://github.com/example/weather-skill/tree/feature/foo/skills/current", "weather-current")
	if err == nil || !strings.Contains(err.Error(), "unexpected status 404") {
		t.Fatalf("Sync() error = %v", err)
	}
}

func TestSyncRejectsNilServiceAndInvalidDir(t *testing.T) {
	var service *Service
	if err := service.Sync(context.Background(), "https://github.com/example/weather-skill", "weather-current"); err == nil {
		t.Fatal("nil service Sync() error = nil, want error")
	}

	service = newTestService(t)
	if err := service.Sync(context.Background(), "https://github.com/example/weather-skill", "../bad"); !errors.Is(err, ErrInvalidSkillDir) {
		t.Fatalf("Sync() error = %v, want %v", err, ErrInvalidSkillDir)
	}
}

func TestDownloadArchiveRejectsMissingSubpath(t *testing.T) {
	service := newTestService(t)
	service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := buildGitHubArchive(t, map[string]string{
			"weather-skill-main/skills/other/prompt.md": "# other",
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(body)),
		}, nil
	})}

	err := service.downloadArchive(context.Background(), GitHubSource{
		Owner:   "example",
		Repo:    "weather-skill",
		Ref:     "main",
		Subpath: "skills/current",
	}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "does not contain the requested skill path") {
		t.Fatalf("downloadArchive() error = %v", err)
	}
}

func TestDownloadArchiveRejectsUnexpectedStatus(t *testing.T) {
	service := newTestService(t)
	service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(nil)),
		}, nil
	})}

	err := service.downloadArchive(context.Background(), GitHubSource{Owner: "example", Repo: "weather-skill"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unexpected status 404") {
		t.Fatalf("downloadArchive() error = %v", err)
	}
}

func TestDownloadArchiveRejectsInvalidGzip(t *testing.T) {
	service := newTestService(t)
	service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("not gzip")),
		}, nil
	})}

	err := service.downloadArchive(context.Background(), GitHubSource{Owner: "example", Repo: "weather-skill"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "open github archive") {
		t.Fatalf("downloadArchive() error = %v", err)
	}
}

func TestDownloadArchiveRejectsUnsupportedEntryType(t *testing.T) {
	service := newTestService(t)
	service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var buffer bytes.Buffer
		gzipWriter := gzip.NewWriter(&buffer)
		tarWriter := tar.NewWriter(gzipWriter)
		if err := tarWriter.WriteHeader(&tar.Header{
			Name:     "weather-skill-main/skills/current/link",
			Typeflag: tar.TypeSymlink,
			Linkname: "prompt.md",
			Mode:     0o644,
		}); err != nil {
			t.Fatalf("WriteHeader() error = %v", err)
		}
		if err := tarWriter.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if err := gzipWriter.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(buffer.Bytes())),
		}, nil
	})}

	err := service.downloadArchive(context.Background(), GitHubSource{
		Owner:   "example",
		Repo:    "weather-skill",
		Ref:     "main",
		Subpath: "skills/current",
	}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unsupported github archive entry type") {
		t.Fatalf("downloadArchive() error = %v", err)
	}
}

func TestDownloadArchiveSupportsDirAndRegAEntries(t *testing.T) {
	service := newTestService(t)
	service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var buffer bytes.Buffer
		gzipWriter := gzip.NewWriter(&buffer)
		tarWriter := tar.NewWriter(gzipWriter)
		if err := tarWriter.WriteHeader(&tar.Header{
			Name:     "weather-skill-main/skills/current/docs",
			Typeflag: tar.TypeDir,
			Mode:     0o755,
		}); err != nil {
			t.Fatalf("WriteHeader() dir error = %v", err)
		}
		payload := []byte("# prompt")
		if err := tarWriter.WriteHeader(&tar.Header{
			Name:     "weather-skill-main/skills/current/docs/prompt.md",
			Typeflag: tar.TypeRegA,
			Mode:     0o644,
			Size:     int64(len(payload)),
		}); err != nil {
			t.Fatalf("WriteHeader() file error = %v", err)
		}
		if _, err := tarWriter.Write(payload); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		if err := tarWriter.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if err := gzipWriter.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(buffer.Bytes())),
		}, nil
	})}

	targetDir := filepath.Join(t.TempDir(), "skill")
	err := service.downloadArchive(context.Background(), GitHubSource{
		Owner:   "example",
		Repo:    "weather-skill",
		Ref:     "main",
		Subpath: "skills/current",
	}, targetDir)
	if err != nil {
		t.Fatalf("downloadArchive() error = %v", err)
	}
	if payload, err := os.ReadFile(filepath.Join(targetDir, "docs", "prompt.md")); err != nil || string(payload) != "# prompt" {
		t.Fatalf("archive output = %q err=%v", string(payload), err)
	}
}

func TestTrimArchiveRootAndMatchArchiveSubpath(t *testing.T) {
	t.Parallel()

	if got := trimArchiveRoot("weather-skill-main/skills/current/prompt.md"); got != "skills/current/prompt.md" {
		t.Fatalf("trimArchiveRoot() = %q", got)
	}

	relativePath, ok := matchArchiveSubpath("skills/current/docs/readme.md", "skills/current")
	if !ok || relativePath != "docs/readme.md" {
		t.Fatalf("matchArchiveSubpath() = (%q, %v)", relativePath, ok)
	}

	relativePath, ok = matchArchiveSubpath("prompt.md", "")
	if !ok || relativePath != "prompt.md" {
		t.Fatalf("matchArchiveSubpath() root = (%q, %v)", relativePath, ok)
	}

	relativePath, ok = matchArchiveSubpath("skills/other/prompt.md", "skills/current")
	if ok || relativePath != "" {
		t.Fatalf("matchArchiveSubpath() mismatch = (%q, %v)", relativePath, ok)
	}

	relativePath, ok = matchArchiveSubpath("skills/current", "skills/current")
	if !ok || relativePath != "current" {
		t.Fatalf("matchArchiveSubpath() exact = (%q, %v)", relativePath, ok)
	}
}

func TestAtomicReplaceDirectoryUpgradesExistingDirectory(t *testing.T) {
	root := t.TempDir()
	finalDir := filepath.Join(root, "weather-current")
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(finalDir, "old.md"), []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tempDir := filepath.Join(root, "tmp-new")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "new.md"), []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := atomicReplaceDirectory(tempDir, finalDir); err != nil {
		t.Fatalf("atomicReplaceDirectory() error = %v", err)
	}

	info, err := os.Lstat(finalDir)
	if err != nil {
		t.Fatalf("Lstat() error = %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("finalDir is not a symlink: mode=%v", info.Mode())
	}
	if _, err := os.Stat(filepath.Join(finalDir, "new.md")); err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if _, err := os.Stat(finalDir + ".backup"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup still exists, err=%v", err)
	}
}

func TestListResourcesRejectsInvalidSkillDir(t *testing.T) {
	service := newTestService(t)
	_, err := service.ListResources("current", "../bad")
	if !errors.Is(err, ErrInvalidSkillDir) {
		t.Fatalf("ListResources() error = %v, want %v", err, ErrInvalidSkillDir)
	}
}

func TestReadResourceValidationErrors(t *testing.T) {
	service := newTestService(t)
	writeSkillFile(t, service.root, "weather-current", "prompt.md", "# prompt")

	_, err := service.ReadResource("current", "weather-current", "bad-uri")
	if !errors.Is(err, ErrInvalidResourceURI) {
		t.Fatalf("ReadResource() invalid uri error = %v", err)
	}

	_, err = service.ReadResource("forecast", "weather-current", BuildResourceURI("current", "prompt.md"))
	if !errors.Is(err, ErrInvalidResourceURI) {
		t.Fatalf("ReadResource() mismatch error = %v", err)
	}

	_, err = service.ReadResource("current", "weather-current", BuildResourceURI("current", "missing.md"))
	if !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("ReadResource() missing error = %v", err)
	}
}

func TestBuildAndParseResourceURI(t *testing.T) {
	resourceURI := BuildResourceURI("current weather", "docs/read me.md")
	skillName, relativePath, err := ParseResourceURI(resourceURI)
	if err != nil {
		t.Fatalf("ParseResourceURI() error = %v", err)
	}
	if skillName != "current weather" || relativePath != "docs/read me.md" {
		t.Fatalf("ParseResourceURI() = (%q, %q)", skillName, relativePath)
	}

	resourceURI = BuildResourceURI(" current ", "/docs//read me.md/")
	if resourceURI != "skillfun://skills/current/files/docs/read%20me.md" {
		t.Fatalf("BuildResourceURI() = %q", resourceURI)
	}
}

func TestParseResourceURIRejectsInvalidPaths(t *testing.T) {
	testCases := []string{
		"skillfun://skills/current",
		"skillfun://skills/current/files",
		"skillfun://skills/current/files/%2Fbad",
		"skillfun://other/current/files/prompt.md",
		"skillfun://skills//files/prompt.md",
		"skillfun://skills/current/files/%2E%2E/secret.md",
		"skillfun://skills/current/files/%ZZ",
		"skillfun://skills/%ZZ/files/prompt.md",
		"https://skills/current/files/prompt.md",
	}
	for _, resourceURI := range testCases {
		if _, _, err := ParseResourceURI(resourceURI); !errors.Is(err, ErrInvalidResourceURI) {
			t.Fatalf("ParseResourceURI(%q) error = %v, want %v", resourceURI, err, ErrInvalidResourceURI)
		}
	}
}

func TestSkillRootAndPathHelpers(t *testing.T) {
	service := newTestService(t)
	if _, err := (*Service)(nil).skillRoot("weather-current"); err == nil {
		t.Fatal("skillRoot() nil service error = nil")
	}
	if _, err := service.skillRoot("../bad"); !errors.Is(err, ErrInvalidSkillDir) {
		t.Fatalf("skillRoot() error = %v, want %v", err, ErrInvalidSkillDir)
	}
	if root, err := service.skillRoot("weather-current"); err != nil || filepath.Base(root) != "weather-current" {
		t.Fatalf("skillRoot() = (%q, %v)", root, err)
	}

	targetPath, err := safeJoinUnderRoot(service.root, "weather-current/prompt.md")
	if err != nil {
		t.Fatalf("safeJoinUnderRoot() error = %v", err)
	}
	if _, err := safeJoinUnderRoot(service.root, "../escape"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("safeJoinUnderRoot() escape error = %v", err)
	}
	if !isWithinRoot(service.root, service.root) || !isWithinRoot(service.root, targetPath) || isWithinRoot(service.root, filepath.Dir(service.root)) {
		t.Fatalf("isWithinRoot() returned unexpected result")
	}
}

func TestResolveResourcePathAndHelpers(t *testing.T) {
	service := newTestService(t)
	writeSkillFile(t, service.root, "weather-current", "prompt.md", "# prompt")

	targetPath := filepath.Join(service.root, "weather-current", "prompt.md")
	resolvedPath, err := resolveResourcePath(filepath.Join(service.root, "weather-current"), targetPath)
	if err != nil {
		t.Fatalf("resolveResourcePath() error = %v", err)
	}
	if filepath.Base(resolvedPath) != "prompt.md" || !strings.Contains(resolvedPath, "weather-current") {
		t.Fatalf("resolveResourcePath() = %q", resolvedPath)
	}

	_, err = resolveResourcePath(filepath.Join(service.root, "weather-current"), filepath.Join(service.root, "weather-current", "missing.md"))
	if !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("resolveResourcePath() missing error = %v", err)
	}
}

func TestNormalizeGitHubSubpathAndSplitSegments(t *testing.T) {
	if got := normalizeGitHubSubpath([]string{"skills", "current", "..", "forecast"}); got != "skills/forecast" {
		t.Fatalf("normalizeGitHubSubpath() = %q", got)
	}
	if got := normalizeGitHubSubpath([]string{"skills", ".."}); got != "" {
		t.Fatalf("normalizeGitHubSubpath() invalid = %q", got)
	}
	segments := splitPathSegments("/skills//current/")
	if strings.Join(segments, ",") != "skills,current" {
		t.Fatalf("splitPathSegments() = %#v", segments)
	}
	if got := normalizeGitHubSubpath(nil); got != "" {
		t.Fatalf("normalizeGitHubSubpath(nil) = %q", got)
	}
}

func TestDetectMimeTypeAndTextDetection(t *testing.T) {
	if mimeType := detectMimeType("prompt.txt", nil); !strings.HasPrefix(mimeType, "text/plain") {
		t.Fatalf("detectMimeType() = %q", mimeType)
	}
	if mimeType := detectMimeType("payload.bin", []byte(`{"ok":true}`)); mimeType == "" {
		t.Fatalf("detectMimeType() fallback = %q", mimeType)
	}
	if mimeType := detectMimeType("payload.bin", nil); mimeType != "application/octet-stream" {
		t.Fatalf("detectMimeType() empty = %q", mimeType)
	}
	if !isTextResource("application/json", []byte(`{"ok":true}`)) {
		t.Fatal("expected json to be text")
	}
	if !isTextResource("application/atom+xml", []byte("<feed/>")) {
		t.Fatal("expected +xml payload to be text")
	}
	if !isTextResource("application/problem+json", []byte(`{"ok":true}`)) {
		t.Fatal("expected +json payload to be text")
	}
	if !isTextResource("application/octet-stream", []byte("plain utf8")) {
		t.Fatal("expected utf8 payload to be text")
	}
	if isTextResource("application/octet-stream", []byte("bad\x00utf8")) {
		t.Fatal("expected nul payload to be non-text")
	}
	if isTextResource("application/octet-stream", []byte{0xff, 0x00}) {
		t.Fatal("expected binary payload to be non-text")
	}
}

func TestLinkedDirectoryTargetAcceptsAbsoluteTarget(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "snapshot")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	linkPath := filepath.Join(root, "current")
	if err := os.Symlink(targetDir, linkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	resolvedTarget, err := linkedDirectoryTarget(linkPath)
	if err != nil {
		t.Fatalf("linkedDirectoryTarget() error = %v", err)
	}
	if resolvedTarget != targetDir {
		t.Fatalf("linkedDirectoryTarget() = %q, want %q", resolvedTarget, targetDir)
	}
}

func TestLinkedDirectoryTargetResolvesRelativeTarget(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "snapshots", "current")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	linkPath := filepath.Join(root, "links", "current")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.Symlink("../snapshots/current", linkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	resolvedTarget, err := linkedDirectoryTarget(linkPath)
	if err != nil {
		t.Fatalf("linkedDirectoryTarget() error = %v", err)
	}
	if resolvedTarget != targetDir {
		t.Fatalf("linkedDirectoryTarget() = %q, want %q", resolvedTarget, targetDir)
	}
}

func TestLinkedDirectoryTargetRejectsNonLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-link")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := linkedDirectoryTarget(path); err == nil || !strings.Contains(err.Error(), "read skill directory link") {
		t.Fatalf("linkedDirectoryTarget() error = %v", err)
	}
}

func TestEnsureSkillDirNameAndTrimArchiveRoot(t *testing.T) {
	testCases := []string{"", ".", "..", "nested/dir"}
	for _, skillDirName := range testCases {
		if err := ensureSkillDirName(skillDirName); !errors.Is(err, ErrInvalidSkillDir) {
			t.Fatalf("ensureSkillDirName(%q) error = %v", skillDirName, err)
		}
	}
	if err := ensureSkillDirName("weather-current"); err != nil {
		t.Fatalf("ensureSkillDirName() error = %v", err)
	}
	if got := trimArchiveRoot("weather-skill-main"); got != "" {
		t.Fatalf("trimArchiveRoot() = %q", got)
	}
}

func TestSkillsAdditionalCoveragePaths(t *testing.T) {
	t.Run("parse github url additional cases", func(t *testing.T) {
		previousParseRawURL := parseRawURL
		parseRawURL = func(raw string) (*url.URL, error) {
			return nil, errors.New("bad url")
		}
		if _, err := ParseGitHubURL("https://github.com/example/weather-skill"); !errors.Is(err, ErrInvalidGitHubURL) {
			t.Fatalf("ParseGitHubURL() parse error = %v", err)
		}
		parseRawURL = previousParseRawURL

		if _, err := ParseGitHubURL(""); !errors.Is(err, ErrInvalidGitHubURL) {
			t.Fatalf("ParseGitHubURL() empty error = %v", err)
		}
		if _, err := ParseGitHubURL("https://github.com/example/.git"); !errors.Is(err, ErrInvalidGitHubURL) {
			t.Fatalf("ParseGitHubURL() empty repo error = %v", err)
		}
		source, err := ParseGitHubURL("https://www.github.com/example/weather-skill/blob/main/skills/current/prompt.md")
		if err != nil {
			t.Fatalf("ParseGitHubURL() www error = %v", err)
		}
		if source.Ref != "main" || source.Subpath != "skills/current/prompt.md" {
			t.Fatalf("ParseGitHubURL() = %#v", source)
		}
		if _, err := ParseGitHubURL("https://%zz"); !errors.Is(err, ErrInvalidGitHubURL) {
			t.Fatalf("ParseGitHubURL() parse error = %v", err)
		}
	})

	t.Run("new service abs path error", func(t *testing.T) {
		previousResolveAbsPath := resolveAbsPath
		resolveAbsPath = func(string) (string, error) {
			return "", errors.New("abs failed")
		}
		defer func() {
			resolveAbsPath = previousResolveAbsPath
		}()
		if _, err := NewService("root"); err == nil || !strings.Contains(err.Error(), "resolve skill storage root") {
			t.Fatalf("NewService() error = %v", err)
		}
	})

	t.Run("candidate github sources invalid url", func(t *testing.T) {
		if _, err := candidateGitHubSources(""); !errors.Is(err, ErrInvalidGitHubURL) {
			t.Fatalf("candidateGitHubSources() error = %v", err)
		}
		if err := newTestService(t).Sync(context.Background(), "", "weather-current"); !errors.Is(err, ErrInvalidGitHubURL) {
			t.Fatalf("Sync() invalid url error = %v", err)
		}

		previousParseRawURL := parseRawURL
		callCount := 0
		parseRawURL = func(raw string) (*url.URL, error) {
			callCount++
			if callCount == 2 {
				return nil, errors.New("parse failed")
			}
			return previousParseRawURL(raw)
		}
		defer func() {
			parseRawURL = previousParseRawURL
		}()
		if _, err := candidateGitHubSources("https://github.com/example/weather-skill/tree/feature/foo/skills/current"); !errors.Is(err, ErrInvalidGitHubURL) {
			t.Fatalf("candidateGitHubSources() second parse error = %v", err)
		}
	})

	t.Run("list resources additional branches", func(t *testing.T) {
		var nilService *Service
		if _, err := nilService.ListResources("current", "weather-current"); err == nil {
			t.Fatal("ListResources() nil service error = nil")
		}

		service := newTestService(t)
		if _, err := service.ListResources("current", "weather-current"); err == nil || !strings.Contains(err.Error(), "list skill resources") {
			t.Fatalf("ListResources() missing root error = %v", err)
		}

		skillRoot := filepath.Join(service.root, "weather-current")
		if err := os.MkdirAll(filepath.Join(skillRoot, "docs"), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		writeSkillFile(t, service.root, "weather-current", "docs/readme.md", "docs")
		if err := os.Symlink(filepath.Join(skillRoot, "docs"), filepath.Join(skillRoot, "docs-link")); err != nil {
			t.Fatalf("Symlink() error = %v", err)
		}
		if err := os.Symlink(filepath.Join(skillRoot, "missing.md"), filepath.Join(skillRoot, "missing-link.md")); err != nil {
			t.Fatalf("Symlink() error = %v", err)
		}
		if _, err := service.ListResources("current", "weather-current"); !errors.Is(err, ErrResourceNotFound) {
			t.Fatalf("ListResources() broken link error = %v", err)
		}

		if err := os.Remove(filepath.Join(skillRoot, "missing-link.md")); err != nil {
			t.Fatalf("Remove() error = %v", err)
		}
		resources, err := service.ListResources("current", "weather-current")
		if err != nil {
			t.Fatalf("ListResources() error = %v", err)
		}
		for _, resource := range resources {
			if strings.Contains(resource.URI, "docs-link") {
				t.Fatalf("unexpected directory symlink resource: %#v", resources)
			}
		}

		previousStatPath := statPath
		statPath = func(name string) (os.FileInfo, error) {
			if strings.HasSuffix(name, "readme.md") {
				return nil, errors.New("stat failed")
			}
			return previousStatPath(name)
		}
		if _, err := service.ListResources("current", "weather-current"); err == nil || !strings.Contains(err.Error(), "list skill resources") {
			t.Fatalf("ListResources() stat error = %v", err)
		}
		statPath = previousStatPath

		previousRelPath := relPath
		relPath = func(basePath string, targetPath string) (string, error) {
			return "", errors.New("rel failed")
		}
		defer func() {
			relPath = previousRelPath
		}()
		if _, err := service.ListResources("current", "weather-current"); err == nil || !strings.Contains(err.Error(), "resolve resource path") {
			t.Fatalf("ListResources() rel error = %v", err)
		}
	})

	t.Run("read resource additional branches", func(t *testing.T) {
		var nilService *Service
		if _, err := nilService.ReadResource("current", "weather-current", BuildResourceURI("current", "prompt.md")); err == nil {
			t.Fatal("ReadResource() nil service error = nil")
		}

		service := newTestService(t)
		if err := os.MkdirAll(filepath.Join(service.root, "weather-current", "docs"), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if _, err := service.ReadResource("current", "weather-current", BuildResourceURI("current", "docs")); err == nil || !strings.Contains(err.Error(), "read skill resource") {
			t.Fatalf("ReadResource() directory error = %v", err)
		}

		previousJoinUnderRootPath := joinUnderRootPath
		joinUnderRootPath = func(root string, relativePath string) (string, error) {
			if root != service.root {
				return "", ErrPathEscape
			}
			return previousJoinUnderRootPath(root, relativePath)
		}
		if _, err := service.ReadResource("current", "weather-current", BuildResourceURI("current", "prompt.md")); !errors.Is(err, ErrPathEscape) {
			t.Fatalf("ReadResource() join error = %v", err)
		}
		joinUnderRootPath = previousJoinUnderRootPath

		writeSkillFile(t, service.root, "weather-current", "prompt.md", "# prompt")
		previousReadPathFile := readPathFile
		readPathFile = func(name string) ([]byte, error) {
			return nil, os.ErrNotExist
		}
		defer func() {
			readPathFile = previousReadPathFile
		}()
		if _, err := service.ReadResource("current", "weather-current", BuildResourceURI("current", "prompt.md")); !errors.Is(err, ErrResourceNotFound) {
			t.Fatalf("ReadResource() read not found error = %v", err)
		}
	})

	t.Run("parse resource uri additional cases", func(t *testing.T) {
		if _, _, err := ParseResourceURI("skillfun://skills/%20/files/prompt.md"); !errors.Is(err, ErrInvalidResourceURI) {
			t.Fatalf("ParseResourceURI() invalid skill name error = %v", err)
		}
		if _, _, err := ParseResourceURI("skillfun://skills/current/files/%2F"); !errors.Is(err, ErrInvalidResourceURI) {
			t.Fatalf("ParseResourceURI() invalid resource path error = %v", err)
		}
		if _, _, err := ParseResourceURI("skillfun://skills/current/files/docs/%ZZ"); !errors.Is(err, ErrInvalidResourceURI) {
			t.Fatalf("ParseResourceURI() invalid segment error = %v", err)
		}

		previousUnescapePathSegment := unescapePathSegment
		unescapePathSegment = func(segment string) (string, error) {
			if segment == "docs" {
				return "", errors.New("bad segment")
			}
			return previousUnescapePathSegment(segment)
		}
		defer func() {
			unescapePathSegment = previousUnescapePathSegment
		}()
		if _, _, err := ParseResourceURI("skillfun://skills/current/files/docs/readme.md"); !errors.Is(err, ErrInvalidResourceURI) {
			t.Fatalf("ParseResourceURI() unescape error = %v", err)
		}
	})

	t.Run("download archive additional errors", func(t *testing.T) {
		previousCreateRequest := createRequest
		createRequest = func(context.Context, string, string, io.Reader) (*http.Request, error) {
			return nil, errors.New("request failed")
		}
		service := newTestService(t)
		if err := service.downloadArchive(context.Background(), GitHubSource{Owner: "example", Repo: "weather-skill"}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "create github archive request") {
			t.Fatalf("downloadArchive() request error = %v", err)
		}
		createRequest = previousCreateRequest

		service = newTestService(t)
		service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, errors.New("network down")
		})}
		if err := service.downloadArchive(context.Background(), GitHubSource{Owner: "example", Repo: "weather-skill"}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "download github archive") {
			t.Fatalf("downloadArchive() client error = %v", err)
		}

		service = newTestService(t)
		service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			var buffer bytes.Buffer
			gzipWriter := gzip.NewWriter(&buffer)
			if _, err := gzipWriter.Write([]byte("not a tar archive")); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			if err := gzipWriter.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(buffer.Bytes())),
			}, nil
		})}
		if err := service.downloadArchive(context.Background(), GitHubSource{Owner: "example", Repo: "weather-skill"}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "read github archive") {
			t.Fatalf("downloadArchive() tar read error = %v", err)
		}

		service = newTestService(t)
		service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			var buffer bytes.Buffer
			gzipWriter := gzip.NewWriter(&buffer)
			tarWriter := tar.NewWriter(gzipWriter)
			if err := tarWriter.WriteHeader(&tar.Header{
				Name:     "weather-skill-main",
				Typeflag: tar.TypeDir,
				Mode:     0o755,
			}); err != nil {
				t.Fatalf("WriteHeader() error = %v", err)
			}
			if err := tarWriter.WriteHeader(&tar.Header{
				Name:     "weather-skill-main/skills/current/docs",
				Typeflag: tar.TypeDir,
				Mode:     0o755,
			}); err != nil {
				t.Fatalf("WriteHeader() error = %v", err)
			}
			if err := tarWriter.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if err := gzipWriter.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(buffer.Bytes())),
			}, nil
		})}
		targetDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(targetDir, "docs"), []byte("file"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if err := service.downloadArchive(context.Background(), GitHubSource{
			Owner:   "example",
			Repo:    "weather-skill",
			Ref:     "main",
			Subpath: "skills/current",
		}, targetDir); err == nil || !strings.Contains(err.Error(), "create extracted directory") {
			t.Fatalf("downloadArchive() create dir error = %v", err)
		}

		previousJoinUnderRootPath := joinUnderRootPath
		joinUnderRootPath = func(root string, relativePath string) (string, error) {
			return "", ErrPathEscape
		}
		service = newTestService(t)
		service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body := buildGitHubArchive(t, map[string]string{
				"weather-skill-main/skills/current/prompt.md": "# prompt",
			})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		})}
		if err := service.downloadArchive(context.Background(), GitHubSource{
			Owner:   "example",
			Repo:    "weather-skill",
			Ref:     "main",
			Subpath: "skills/current",
		}, t.TempDir()); !errors.Is(err, ErrPathEscape) {
			t.Fatalf("downloadArchive() join error = %v", err)
		}
		joinUnderRootPath = previousJoinUnderRootPath

		service = newTestService(t)
		service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body := buildGitHubArchive(t, map[string]string{
				"weather-skill-main/skills/current/docs/prompt.md": "# prompt",
			})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		})}
		targetDir = t.TempDir()
		if err := os.WriteFile(filepath.Join(targetDir, "docs"), []byte("file"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if err := service.downloadArchive(context.Background(), GitHubSource{
			Owner:   "example",
			Repo:    "weather-skill",
			Ref:     "main",
			Subpath: "skills/current",
		}, targetDir); err == nil || !strings.Contains(err.Error(), "create extracted parent directory") {
			t.Fatalf("downloadArchive() create parent error = %v", err)
		}

		service = newTestService(t)
		service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body := buildGitHubArchive(t, map[string]string{
				"weather-skill-main/skills/current/prompt.md": "# prompt",
			})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		})}
		targetDir = t.TempDir()
		if err := os.MkdirAll(filepath.Join(targetDir, "prompt.md"), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := service.downloadArchive(context.Background(), GitHubSource{
			Owner:   "example",
			Repo:    "weather-skill",
			Ref:     "main",
			Subpath: "skills/current",
		}, targetDir); err == nil || !strings.Contains(err.Error(), "create extracted file") {
			t.Fatalf("downloadArchive() create file error = %v", err)
		}

		previousClosePathFile := closePathFile
		closePathFile = func(file *os.File) error {
			return errors.New("close failed")
		}
		service = newTestService(t)
		service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body := buildGitHubArchive(t, map[string]string{
				"weather-skill-main/skills/current/prompt.md": "# prompt",
			})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		})}
		if err := service.downloadArchive(context.Background(), GitHubSource{
			Owner:   "example",
			Repo:    "weather-skill",
			Ref:     "main",
			Subpath: "skills/current",
		}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "close extracted file") {
			t.Fatalf("downloadArchive() close error = %v", err)
		}
		closePathFile = previousClosePathFile

		service = newTestService(t)
		service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			var buffer bytes.Buffer
			gzipWriter := gzip.NewWriter(&buffer)
			tarWriter := tar.NewWriter(gzipWriter)
			if err := tarWriter.WriteHeader(&tar.Header{
				Name:     "weather-skill-main/skills/current/prompt.md",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
				Size:     16,
			}); err != nil {
				t.Fatalf("WriteHeader() error = %v", err)
			}
			if _, err := tarWriter.Write([]byte("short")); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			if err := tarWriter.Close(); err == nil {
				t.Fatal("Close() error = nil, want error for truncated tar")
			}
			if err := gzipWriter.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(buffer.Bytes())),
			}, nil
		})}
		if err := service.downloadArchive(context.Background(), GitHubSource{
			Owner:   "example",
			Repo:    "weather-skill",
			Ref:     "main",
			Subpath: "skills/current",
		}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "copy extracted file") {
			t.Fatalf("downloadArchive() copy error = %v", err)
		}
	})

	t.Run("path helper additional branches", func(t *testing.T) {
		service := newTestService(t)
		previousResolveAbsPath := resolveAbsPath
		resolveAbsPath = func(string) (string, error) {
			return "", errors.New("abs failed")
		}
		if _, err := safeJoinUnderRoot(service.root, "weather-current/prompt.md"); err == nil || !strings.Contains(err.Error(), "resolve root path") {
			t.Fatalf("safeJoinUnderRoot() abs error = %v", err)
		}
		resolveAbsPath = func(string) (string, error) {
			return "", nil
		}
		if _, err := safeJoinUnderRoot(service.root, "weather-current/prompt.md"); !errors.Is(err, ErrPathEscape) {
			t.Fatalf("safeJoinUnderRoot() within-root error = %v", err)
		}
		resolveAbsPath = previousResolveAbsPath

		if _, err := safeJoinUnderRoot(service.root, "/absolute/path"); !errors.Is(err, ErrPathEscape) {
			t.Fatalf("safeJoinUnderRoot() absolute error = %v", err)
		}

		skillRoot := filepath.Join(service.root, "weather-current")
		if err := os.MkdirAll(skillRoot, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		loopPath := filepath.Join(skillRoot, "loop")
		if err := os.Symlink("loop", loopPath); err != nil {
			t.Fatalf("Symlink() error = %v", err)
		}
		if _, err := resolveResourcePath(skillRoot, loopPath); err == nil || strings.Contains(err.Error(), ErrResourceNotFound.Error()) {
			t.Fatalf("resolveResourcePath() loop error = %v", err)
		}
	})

	t.Run("sync additional branches", func(t *testing.T) {
		service := newTestService(t)
		service.root = filepath.Join(t.TempDir(), "storage-root")
		if err := os.WriteFile(service.root, []byte("file"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if err := service.Sync(context.Background(), "https://github.com/example/weather-skill", "weather-current"); err == nil || !strings.Contains(err.Error(), "create skill storage root") {
			t.Fatalf("Sync() root mkdir error = %v", err)
		}

		service = newTestService(t)
		previousCreateTempDir := createTempDir
		createTempDir = func(string, string) (string, error) {
			return "", errors.New("temp failed")
		}
		if err := service.Sync(context.Background(), "https://github.com/example/weather-skill", "weather-current"); err == nil || !strings.Contains(err.Error(), "create temp skill directory") {
			t.Fatalf("Sync() temp dir error = %v", err)
		}
		createTempDir = previousCreateTempDir

		service = newTestService(t)
		previousRemovePathAll := removePathAll
		removePathAll = func(path string) error {
			if strings.HasSuffix(path, string(filepath.Separator)+"content") {
				return errors.New("remove failed")
			}
			return previousRemovePathAll(path)
		}
		if err := service.Sync(context.Background(), "https://github.com/example/weather-skill", "weather-current"); err == nil || !strings.Contains(err.Error(), "reset temp skill directory") {
			t.Fatalf("Sync() temp reset error = %v", err)
		}
		removePathAll = previousRemovePathAll

		service = newTestService(t)
		previousCreateDirAll := createDirAll
		createDirAll = func(path string, perm os.FileMode) error {
			if strings.HasSuffix(path, string(filepath.Separator)+"content") {
				return errors.New("mkdir content failed")
			}
			return previousCreateDirAll(path, perm)
		}
		if err := service.Sync(context.Background(), "https://github.com/example/weather-skill", "weather-current"); err == nil || !strings.Contains(err.Error(), "create temp skill directory") {
			t.Fatalf("Sync() content mkdir error = %v", err)
		}
		createDirAll = previousCreateDirAll

		service = newTestService(t)
		service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body := buildGitHubArchive(t, map[string]string{
				"weather-skill-main/prompt.md": "# prompt",
			})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		})}
		previousPublishDirectory := publishDirectory
		publishDirectory = func(string, string) error {
			return errors.New("publish failed")
		}
		defer func() {
			publishDirectory = previousPublishDirectory
		}()
		if err := service.Sync(context.Background(), "https://github.com/example/weather-skill", "weather-current"); err == nil || !strings.Contains(err.Error(), "publish skill directory") {
			t.Fatalf("Sync() publish error = %v", err)
		}
	})

	t.Run("atomic replace directory additional branches", func(t *testing.T) {
		root := t.TempDir()
		if err := atomicReplaceDirectory(filepath.Join(root, "missing"), filepath.Join(root, "weather-current")); err == nil {
			t.Fatal("atomicReplaceDirectory() missing temp error = nil")
		}

		tempDir := filepath.Join(root, "tmp-first")
		if err := os.MkdirAll(tempDir, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(tempDir, "prompt.md"), []byte("first"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		finalDir := filepath.Join(root, "weather-current")
		if err := atomicReplaceDirectory(tempDir, finalDir); err != nil {
			t.Fatalf("atomicReplaceDirectory() setup error = %v", err)
		}

		secondTempDir := filepath.Join(root, "tmp-second")
		if err := os.MkdirAll(secondTempDir, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(secondTempDir, "prompt.md"), []byte("second"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		previousCreateSymlink := createSymlink
		createSymlink = func(oldPath string, newPath string) error {
			if newPath == finalDir+".next" {
				return errors.New("next link failed")
			}
			return previousCreateSymlink(oldPath, newPath)
		}
		if err := atomicReplaceDirectory(secondTempDir, finalDir); err == nil {
			t.Fatal("atomicReplaceDirectory() next link error = nil")
		}
		createSymlink = previousCreateSymlink

		t.Run("lstat error", func(t *testing.T) {
			previousLstatPath := lstatPath
			lstatPath = func(string) (os.FileInfo, error) {
				return nil, errors.New("lstat failed")
			}
			defer func() {
				lstatPath = previousLstatPath
			}()
			tempDir := filepath.Join(root, "tmp-lstat")
			if err := os.MkdirAll(tempDir, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := atomicReplaceDirectory(tempDir, filepath.Join(root, "lstat-final")); err == nil || !strings.Contains(err.Error(), "lstat failed") {
				t.Fatalf("atomicReplaceDirectory() lstat error = %v", err)
			}
		})

		t.Run("linked target error", func(t *testing.T) {
			linkRoot := t.TempDir()
			tempDir := filepath.Join(linkRoot, "tmp-one")
			if err := os.MkdirAll(tempDir, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			finalDir := filepath.Join(linkRoot, "current")
			if err := atomicReplaceDirectory(tempDir, finalDir); err != nil {
				t.Fatalf("atomicReplaceDirectory() setup error = %v", err)
			}
			previousResolveLinkedDir := resolveLinkedDir
			resolveLinkedDir = func(string) (string, error) {
				return "", errors.New("link failed")
			}
			defer func() {
				resolveLinkedDir = previousResolveLinkedDir
			}()
			tempDir = filepath.Join(linkRoot, "tmp-two")
			if err := os.MkdirAll(tempDir, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := atomicReplaceDirectory(tempDir, finalDir); err == nil || !strings.Contains(err.Error(), "link failed") {
				t.Fatalf("atomicReplaceDirectory() link error = %v", err)
			}
		})

		t.Run("rename current symlink error", func(t *testing.T) {
			linkRoot := t.TempDir()
			tempDir := filepath.Join(linkRoot, "tmp-one")
			if err := os.MkdirAll(tempDir, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			finalDir := filepath.Join(linkRoot, "current")
			if err := atomicReplaceDirectory(tempDir, finalDir); err != nil {
				t.Fatalf("atomicReplaceDirectory() setup error = %v", err)
			}
			previousRenamePath := renamePath
			renamePath = func(oldPath string, newPath string) error {
				if strings.HasSuffix(oldPath, ".next") && newPath == finalDir {
					return errors.New("rename failed")
				}
				return previousRenamePath(oldPath, newPath)
			}
			defer func() {
				renamePath = previousRenamePath
			}()
			tempDir = filepath.Join(linkRoot, "tmp-two")
			if err := os.MkdirAll(tempDir, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := atomicReplaceDirectory(tempDir, finalDir); err == nil || !strings.Contains(err.Error(), "rename failed") {
				t.Fatalf("atomicReplaceDirectory() rename current error = %v", err)
			}
		})

		t.Run("remove previous snapshot error", func(t *testing.T) {
			linkRoot := t.TempDir()
			tempDir := filepath.Join(linkRoot, "tmp-one")
			if err := os.MkdirAll(tempDir, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			finalDir := filepath.Join(linkRoot, "current")
			if err := atomicReplaceDirectory(tempDir, finalDir); err != nil {
				t.Fatalf("atomicReplaceDirectory() setup error = %v", err)
			}
			previousVersionDir, err := linkedDirectoryTarget(finalDir)
			if err != nil {
				t.Fatalf("linkedDirectoryTarget() error = %v", err)
			}
			previousRemovePathAll := removePathAll
			removePathAll = func(path string) error {
				if path == previousVersionDir {
					return errors.New("remove failed")
				}
				return previousRemovePathAll(path)
			}
			defer func() {
				removePathAll = previousRemovePathAll
			}()
			tempDir = filepath.Join(linkRoot, "tmp-two")
			if err := os.MkdirAll(tempDir, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := atomicReplaceDirectory(tempDir, finalDir); err == nil || !strings.Contains(err.Error(), "remove failed") {
				t.Fatalf("atomicReplaceDirectory() remove snapshot error = %v", err)
			}
		})

		t.Run("rename backup error", func(t *testing.T) {
			dirRoot := t.TempDir()
			finalDir := filepath.Join(dirRoot, "current")
			if err := os.MkdirAll(finalDir, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			previousRenamePath := renamePath
			renamePath = func(oldPath string, newPath string) error {
				if newPath == finalDir+".backup" {
					return errors.New("backup rename failed")
				}
				return previousRenamePath(oldPath, newPath)
			}
			defer func() {
				renamePath = previousRenamePath
			}()
			tempDir := filepath.Join(dirRoot, "tmp")
			if err := os.MkdirAll(tempDir, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := atomicReplaceDirectory(tempDir, finalDir); err == nil || !strings.Contains(err.Error(), "backup rename failed") {
				t.Fatalf("atomicReplaceDirectory() backup rename error = %v", err)
			}
		})

		t.Run("symlink final dir error", func(t *testing.T) {
			dirRoot := t.TempDir()
			finalDir := filepath.Join(dirRoot, "current")
			if err := os.MkdirAll(finalDir, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			previousCreateSymlink := createSymlink
			createSymlink = func(oldPath string, newPath string) error {
				if newPath == finalDir {
					return errors.New("symlink failed")
				}
				return previousCreateSymlink(oldPath, newPath)
			}
			defer func() {
				createSymlink = previousCreateSymlink
			}()
			tempDir := filepath.Join(dirRoot, "tmp")
			if err := os.MkdirAll(tempDir, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := atomicReplaceDirectory(tempDir, finalDir); err == nil || !strings.Contains(err.Error(), "symlink failed") {
				t.Fatalf("atomicReplaceDirectory() symlink error = %v", err)
			}
		})

		t.Run("remove backup error", func(t *testing.T) {
			dirRoot := t.TempDir()
			finalDir := filepath.Join(dirRoot, "current")
			if err := os.MkdirAll(finalDir, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			previousRemovePathAll := removePathAll
			removeCalls := 0
			removePathAll = func(path string) error {
				if path == finalDir+".backup" {
					removeCalls++
					if removeCalls == 2 {
						return errors.New("remove backup failed")
					}
				}
				return previousRemovePathAll(path)
			}
			defer func() {
				removePathAll = previousRemovePathAll
			}()
			tempDir := filepath.Join(dirRoot, "tmp")
			if err := os.MkdirAll(tempDir, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := atomicReplaceDirectory(tempDir, finalDir); err == nil || !strings.Contains(err.Error(), "remove backup failed") {
				t.Fatalf("atomicReplaceDirectory() remove backup error = %v", err)
			}
		})
	})
}

func newTestService(t *testing.T) *Service {
	t.Helper()

	service, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	return service
}

func writeSkillFile(t *testing.T, root string, skillDirName string, relativePath string, content string) {
	t.Helper()
	writeSkillBinaryFile(t, root, skillDirName, relativePath, []byte(content))
}

func writeSkillBinaryFile(t *testing.T, root string, skillDirName string, relativePath string, content []byte) {
	t.Helper()

	targetPath := filepath.Join(root, skillDirName, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(targetPath, content, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func buildGitHubArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range files {
		if err := tarWriter.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}); err != nil {
			t.Fatalf("WriteHeader() error = %v", err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	return buffer.Bytes()
}
