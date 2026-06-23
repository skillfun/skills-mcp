package skills

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
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

func TestListResourcesSkipsHiddenEntries(t *testing.T) {
	service := newTestService(t)
	writeSkillFile(t, service.root, "weather-current", "prompt.md", "# prompt")
	writeSkillFile(t, service.root, "weather-current", ".secret", "hidden")
	writeSkillFile(t, service.root, "weather-current", "docs/readme.md", "docs")

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
