package skills

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"skillfun-mcp/internal/mcp"
)

const StorageRootEnv = "SKILL_STORAGE_ROOT"

var (
	ErrInvalidGitHubURL   = errors.New("invalid github url")
	ErrInvalidSkillDir    = errors.New("invalid skill directory name")
	ErrInvalidResourceURI = errors.New("invalid resource uri")
	ErrResourceNotFound   = errors.New("resource not found")
	ErrPathEscape         = errors.New("resource path escapes skill root")
)

var (
	resolveAbsPath        = filepath.Abs
	parseRawURL           = url.Parse
	createRequest         = http.NewRequestWithContext
	createDirAll          = os.MkdirAll
	createTempDir         = os.MkdirTemp
	removePathAll         = os.RemoveAll
	evalSymlinksPath      = filepath.EvalSymlinks
	statPath              = os.Stat
	relPath               = filepath.Rel
	readPathFile          = os.ReadFile
	openPathFile          = os.OpenFile
	closePathFile         = func(file *os.File) error { return file.Close() }
	renamePath            = os.Rename
	lstatPath             = os.Lstat
	createSymlink         = os.Symlink
	resolveLinkedDir      = linkedDirectoryTarget
	joinUnderRootPath     = safeJoinUnderRoot
	unescapePathSegment   = url.PathUnescape
	publishDirectory      = atomicReplaceDirectory
)

// Storage 定义 skill 文件同步与资源读取能力。
type Storage interface {
	Sync(ctx context.Context, githubURL string, skillDirName string) error
	ListResources(skillName string, skillDirName string) ([]mcp.MCPResource, error)
	ReadResource(skillName string, skillDirName string, resourceURI string) (mcp.MCPResourceContent, error)
}

// GitHubSource 表示从 public GitHub URL 解析出的代码来源。
type GitHubSource struct {
	OriginalURL string
	Owner       string
	Repo        string
	Ref         string
	Subpath     string
}

// Service 负责同步 GitHub skill 内容并提供文件资源读取。
type Service struct {
	root   string
	client *http.Client
}

// NewService 创建一个基于本地 skill 根目录的存储服务。
func NewService(root string) (*Service, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("%s is required", StorageRootEnv)
	}

	absRoot, err := resolveAbsPath(root)
	if err != nil {
		return nil, fmt.Errorf("resolve skill storage root: %w", err)
	}

	return &Service{
		root: absRoot,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}, nil
}

// NewServiceFromEnv 从环境变量创建存储服务。
func NewServiceFromEnv() (*Service, error) {
	return NewService(os.Getenv(StorageRootEnv))
}

// ParseGitHubURL 解析支持的 public GitHub URL。
func ParseGitHubURL(raw string) (GitHubSource, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return GitHubSource{}, fmt.Errorf("%w: githubUrl is required", ErrInvalidGitHubURL)
	}

	parsed, err := parseRawURL(raw)
	if err != nil {
		return GitHubSource{}, fmt.Errorf("%w: parse githubUrl: %v", ErrInvalidGitHubURL, err)
	}

	if parsed.Scheme != "https" {
		return GitHubSource{}, fmt.Errorf("%w: only https github urls are supported", ErrInvalidGitHubURL)
	}
	if host := strings.ToLower(parsed.Hostname()); host != "github.com" && host != "www.github.com" {
		return GitHubSource{}, fmt.Errorf("%w: host must be github.com", ErrInvalidGitHubURL)
	}
	if parsed.RawQuery != "" {
		return GitHubSource{}, fmt.Errorf("%w: query parameters are not supported", ErrInvalidGitHubURL)
	}

	segments := splitPathSegments(parsed.Path)
	if len(segments) < 2 {
		return GitHubSource{}, fmt.Errorf("%w: repository path is incomplete", ErrInvalidGitHubURL)
	}

	repo := strings.TrimSuffix(segments[1], ".git")
	source := GitHubSource{
		OriginalURL: raw,
		Owner:       segments[0],
		Repo:        repo,
	}

	if source.Owner == "" || source.Repo == "" {
		return GitHubSource{}, fmt.Errorf("%w: repository path is incomplete", ErrInvalidGitHubURL)
	}

	if len(segments) == 2 {
		return source, nil
	}

	switch segments[2] {
	case "tree", "blob":
		if len(segments) < 4 {
			return GitHubSource{}, fmt.Errorf("%w: ref is required for tree/blob github urls", ErrInvalidGitHubURL)
		}

		source.Ref = segments[3]
		source.Subpath = normalizeGitHubSubpath(segments[4:])
		if segments[2] == "blob" && source.Subpath == "" {
			return GitHubSource{}, fmt.Errorf("%w: blob github url must include a file path", ErrInvalidGitHubURL)
		}
	default:
		return GitHubSource{}, fmt.Errorf("%w: unsupported github url format", ErrInvalidGitHubURL)
	}

	return source, nil
}

// Sync 将指定 githubUrl 的最终快照同步到 skill 目录。
func (s *Service) Sync(ctx context.Context, githubURL string, skillDirName string) error {
	if s == nil {
		return fmt.Errorf("skill storage service is nil")
	}
	if err := ensureSkillDirName(skillDirName); err != nil {
		return err
	}

	sources, err := candidateGitHubSources(githubURL)
	if err != nil {
		return err
	}

	if err := createDirAll(s.root, 0o755); err != nil {
		return fmt.Errorf("create skill storage root: %w", err)
	}

	tempParentDir, err := createTempDir(s.root, skillDirName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp skill directory: %w", err)
	}
	defer removePathAll(tempParentDir)

	tempDir := filepath.Join(tempParentDir, "content")
	var lastErr error
	for _, source := range sources {
		if err := removePathAll(tempDir); err != nil {
			return fmt.Errorf("reset temp skill directory: %w", err)
		}
		if err := createDirAll(tempDir, 0o755); err != nil {
			return fmt.Errorf("create temp skill directory: %w", err)
		}
		if err := s.downloadArchive(ctx, source, tempDir); err != nil {
			lastErr = err
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return lastErr
	}

	finalDir := filepath.Join(s.root, skillDirName)
	if err := publishDirectory(tempDir, finalDir); err != nil {
		return fmt.Errorf("publish skill directory: %w", err)
	}

	return nil
}

func candidateGitHubSources(raw string) ([]GitHubSource, error) {
	source, err := ParseGitHubURL(raw)
	if err != nil {
		return nil, err
	}

	parsedURL, err := parseRawURL(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: parse githubUrl: %v", ErrInvalidGitHubURL, err)
	}

	segments := splitPathSegments(parsedURL.Path)
	if len(segments) < 3 || (segments[2] != "tree" && segments[2] != "blob") {
		return []GitHubSource{source}, nil
	}

	tail := segments[3:]
	if len(tail) == 1 && segments[2] == "tree" {
		return []GitHubSource{source}, nil
	}

	candidates := make([]GitHubSource, 0, len(tail))
	for split := len(tail) - 1; split >= 1; split-- {
		candidate := GitHubSource{
			OriginalURL: source.OriginalURL,
			Owner:       source.Owner,
			Repo:        source.Repo,
			Ref:         strings.Join(tail[:split], "/"),
			Subpath:     normalizeGitHubSubpath(tail[split:]),
		}
		candidates = append(candidates, candidate)
	}

	return candidates, nil
}

// ListResources 列出 skill 目录下可暴露的所有文件资源。
func (s *Service) ListResources(skillName string, skillDirName string) ([]mcp.MCPResource, error) {
	skillRoot, err := s.skillRoot(skillDirName)
	if err != nil {
		return nil, err
	}
	walkRoot := skillRoot
	if resolvedRoot, err := evalSymlinksPath(skillRoot); err == nil {
		walkRoot = resolvedRoot
	}

	var resources []mcp.MCPResource
	err = filepath.WalkDir(walkRoot, func(currentPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if currentPath == walkRoot {
			return nil
		}
		if shouldIgnoreEntry(entry.Name()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if entry.IsDir() {
			return nil
		}

		resolvedPath, err := resolveResourcePath(skillRoot, currentPath)
		if err != nil {
			return err
		}

		info, err := statPath(resolvedPath)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relativePath, err := relPath(walkRoot, currentPath)
		if err != nil {
			return fmt.Errorf("resolve resource path: %w", err)
		}

		relativePath = filepath.ToSlash(relativePath)
		resources = append(resources, mcp.MCPResource{
			URI:         BuildResourceURI(skillName, relativePath),
			Name:        skillName + "/" + relativePath,
			Title:       skillName + ": " + path.Base(relativePath),
			MimeType:    detectMimeType(currentPath, nil),
			Description: "File resource in skill " + skillName,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list skill resources: %w", err)
	}

	sort.Slice(resources, func(i, j int) bool {
		return resources[i].URI < resources[j].URI
	})

	return resources, nil
}

// ReadResource 读取 skill 目录中的单个资源文件。
func (s *Service) ReadResource(skillName string, skillDirName string, resourceURI string) (mcp.MCPResourceContent, error) {
	skillRoot, err := s.skillRoot(skillDirName)
	if err != nil {
		return mcp.MCPResourceContent{}, err
	}

	parsedSkillName, relativePath, err := ParseResourceURI(resourceURI)
	if err != nil {
		return mcp.MCPResourceContent{}, err
	}
	if parsedSkillName != skillName {
		return mcp.MCPResourceContent{}, fmt.Errorf("%w: skill name mismatch", ErrInvalidResourceURI)
	}

	targetPath, err := joinUnderRootPath(skillRoot, filepath.FromSlash(relativePath))
	if err != nil {
		return mcp.MCPResourceContent{}, err
	}

	resolvedPath, err := resolveResourcePath(skillRoot, targetPath)
	if err != nil {
		return mcp.MCPResourceContent{}, err
	}

	payload, err := readPathFile(resolvedPath)
	if errors.Is(err, os.ErrNotExist) {
		return mcp.MCPResourceContent{}, ErrResourceNotFound
	}
	if err != nil {
		return mcp.MCPResourceContent{}, fmt.Errorf("read skill resource: %w", err)
	}

	mimeType := detectMimeType(resolvedPath, payload)
	content := mcp.MCPResourceContent{
		URI:      resourceURI,
		MimeType: mimeType,
	}
	if isTextResource(mimeType, payload) {
		content.Text = string(payload)
		return content, nil
	}

	content.Blob = base64.StdEncoding.EncodeToString(payload)
	return content, nil
}

// BuildResourceURI 构建 skill 资源对应的 MCP URI。
func BuildResourceURI(skillName string, relativePath string) string {
	segments := []string{
		url.PathEscape(strings.TrimSpace(skillName)),
		"files",
	}
	for _, segment := range strings.Split(filepath.ToSlash(strings.TrimSpace(relativePath)), "/") {
		if segment == "" {
			continue
		}
		segments = append(segments, url.PathEscape(segment))
	}

	return "skillfun://skills/" + strings.Join(segments, "/")
}

// ParseResourceURI 解析 MCP 资源 URI。
func ParseResourceURI(resourceURI string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(resourceURI))
	if err != nil {
		return "", "", fmt.Errorf("%w: parse resource uri: %v", ErrInvalidResourceURI, err)
	}

	if parsed.Scheme != "skillfun" || parsed.Host != "skills" {
		return "", "", fmt.Errorf("%w: unsupported scheme or host", ErrInvalidResourceURI)
	}

	segments := splitPathSegments(parsed.EscapedPath())
	if len(segments) < 3 || segments[1] != "files" {
		return "", "", fmt.Errorf("%w: unsupported resource path", ErrInvalidResourceURI)
	}

	skillName, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(skillName) == "" {
		return "", "", fmt.Errorf("%w: invalid skill name", ErrInvalidResourceURI)
	}

	var relativeSegments []string
	for _, segment := range segments[2:] {
		unescaped, err := unescapePathSegment(segment)
		if err != nil || unescaped == "" {
			return "", "", fmt.Errorf("%w: invalid resource path", ErrInvalidResourceURI)
		}
		relativeSegments = append(relativeSegments, unescaped)
	}
	relativePath := path.Clean(strings.Join(relativeSegments, "/"))
	if relativePath == "." || strings.HasPrefix(relativePath, "../") || strings.HasPrefix(relativePath, "/") {
		return "", "", fmt.Errorf("%w: invalid resource path", ErrInvalidResourceURI)
	}

	return strings.TrimSpace(skillName), relativePath, nil
}

func (s *Service) downloadArchive(ctx context.Context, source GitHubSource, destinationRoot string) error {
	downloadURL := "https://api.github.com/repos/" + source.Owner + "/" + source.Repo + "/tarball"
	if source.Ref != "" {
		downloadURL += "/" + url.PathEscape(source.Ref)
	}

	request, err := createRequest(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("create github archive request: %w", err)
	}
	request.Header.Set("User-Agent", "skillfun-mcp-gateway")
	request.Header.Set("Accept", "application/vnd.github+json")

	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("download github archive: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download github archive: unexpected status %d", response.StatusCode)
	}

	gzipReader, err := gzip.NewReader(response.Body)
	if err != nil {
		return fmt.Errorf("open github archive: %w", err)
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	var extracted bool
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read github archive: %w", err)
		}

		archivePath := trimArchiveRoot(header.Name)
		if archivePath == "" {
			continue
		}

		targetRelativePath, ok := matchArchiveSubpath(archivePath, source.Subpath)
		if !ok || targetRelativePath == "" {
			continue
		}

		targetPath, err := joinUnderRootPath(destinationRoot, filepath.FromSlash(targetRelativePath))
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := createDirAll(targetPath, 0o755); err != nil {
				return fmt.Errorf("create extracted directory: %w", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := createDirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return fmt.Errorf("create extracted parent directory: %w", err)
			}

			file, err := openPathFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return fmt.Errorf("create extracted file: %w", err)
			}

			if _, err := io.Copy(file, tarReader); err != nil {
				_ = closePathFile(file)
				return fmt.Errorf("copy extracted file: %w", err)
			}
			if err := closePathFile(file); err != nil {
				return fmt.Errorf("close extracted file: %w", err)
			}
			extracted = true
		default:
			return fmt.Errorf("unsupported github archive entry type for %s", archivePath)
		}
	}

	if !extracted {
		return fmt.Errorf("github archive does not contain the requested skill path")
	}

	return nil
}

func (s *Service) skillRoot(skillDirName string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("skill storage service is nil")
	}
	if err := ensureSkillDirName(skillDirName); err != nil {
		return "", err
	}

	return joinUnderRootPath(s.root, skillDirName)
}

func ensureSkillDirName(skillDirName string) error {
	skillDirName = strings.TrimSpace(skillDirName)
	if skillDirName == "" || skillDirName == "." || skillDirName == ".." {
		return fmt.Errorf("%w: empty skill directory name", ErrInvalidSkillDir)
	}
	if skillDirName != filepath.Base(skillDirName) || strings.Contains(skillDirName, string(filepath.Separator)) {
		return fmt.Errorf("%w: path separators are not allowed", ErrInvalidSkillDir)
	}

	return nil
}

func normalizeGitHubSubpath(segments []string) string {
	if len(segments) == 0 {
		return ""
	}

	cleaned := path.Clean(strings.Join(segments, "/"))
	if cleaned == "." || cleaned == "/" || strings.HasPrefix(cleaned, "../") {
		return ""
	}

	return strings.TrimPrefix(cleaned, "/")
}

func splitPathSegments(rawPath string) []string {
	parts := strings.Split(strings.Trim(rawPath, "/"), "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		segments = append(segments, part)
	}

	return segments
}

func trimArchiveRoot(archivePath string) string {
	archivePath = strings.TrimPrefix(path.Clean(archivePath), "./")
	segments := splitPathSegments(archivePath)
	if len(segments) <= 1 {
		return ""
	}

	return strings.Join(segments[1:], "/")
}

func matchArchiveSubpath(archivePath string, subpath string) (string, bool) {
	archivePath = strings.TrimPrefix(path.Clean(archivePath), "/")
	subpath = strings.TrimPrefix(path.Clean(subpath), "/")
	if subpath == "" || subpath == "." {
		return archivePath, true
	}
	if archivePath == subpath {
		return path.Base(archivePath), true
	}
	if strings.HasPrefix(archivePath, subpath+"/") {
		return strings.TrimPrefix(archivePath, subpath+"/"), true
	}

	return "", false
}

func safeJoinUnderRoot(root string, relativePath string) (string, error) {
	cleanRoot, err := resolveAbsPath(root)
	if err != nil {
		return "", fmt.Errorf("resolve root path: %w", err)
	}

	cleanRelativePath := filepath.Clean(relativePath)
	if filepath.IsAbs(cleanRelativePath) || cleanRelativePath == ".." || strings.HasPrefix(cleanRelativePath, ".."+string(filepath.Separator)) {
		return "", ErrPathEscape
	}

	targetPath := filepath.Join(cleanRoot, cleanRelativePath)
	if !isWithinRoot(cleanRoot, targetPath) {
		return "", ErrPathEscape
	}

	return targetPath, nil
}

func resolveResourcePath(skillRoot string, currentPath string) (string, error) {
	resolvedPath, err := evalSymlinksPath(currentPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrResourceNotFound
		}
		return "", fmt.Errorf("resolve resource path: %w", err)
	}
	canonicalRoot := filepath.Clean(skillRoot)
	if resolvedRoot, err := evalSymlinksPath(skillRoot); err == nil {
		canonicalRoot = filepath.Clean(resolvedRoot)
	}
	if !isWithinRoot(canonicalRoot, resolvedPath) {
		return "", ErrPathEscape
	}

	return resolvedPath, nil
}

func isWithinRoot(root string, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if root == target {
		return true
	}

	return strings.HasPrefix(target, root+string(filepath.Separator))
}

func shouldIgnoreEntry(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~")
}

func detectMimeType(filePath string, payload []byte) string {
	if mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath))); mimeType != "" {
		return mimeType
	}
	if len(payload) > 0 {
		return http.DetectContentType(payload)
	}

	return "application/octet-stream"
}

func isTextResource(mimeType string, payload []byte) bool {
	switch {
	case strings.HasPrefix(mimeType, "text/"):
		return true
	case mimeType == "application/json", mimeType == "application/xml", mimeType == "application/yaml", mimeType == "application/x-yaml", mimeType == "application/javascript":
		return true
	case strings.HasSuffix(mimeType, "+json"), strings.HasSuffix(mimeType, "+xml"):
		return true
	}

	if !utf8.Valid(payload) {
		return false
	}
	for _, b := range payload {
		if b == 0 {
			return false
		}
	}

	return true
}

func atomicReplaceDirectory(tempDir string, finalDir string) error {
	versionDir := finalDir + ".snapshot-" + time.Now().UTC().Format("20060102150405.000000000")
	if err := renamePath(tempDir, versionDir); err != nil {
		return err
	}

	info, err := lstatPath(finalDir)
	if errors.Is(err, os.ErrNotExist) {
		return createSymlink(filepath.Base(versionDir), finalDir)
	}
	if err != nil {
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		previousVersionDir, err := resolveLinkedDir(finalDir)
		if err != nil {
			return err
		}

		nextLink := finalDir + ".next"
		_ = removePathAll(nextLink)
		if err := createSymlink(filepath.Base(versionDir), nextLink); err != nil {
			return err
		}
		if err := renamePath(nextLink, finalDir); err != nil {
			return err
		}
		if previousVersionDir != "" {
			if err := removePathAll(previousVersionDir); err != nil {
				return err
			}
		}
		return nil
	}

	backupDir := finalDir + ".backup"
	_ = removePathAll(backupDir)
	if err := renamePath(finalDir, backupDir); err != nil {
		return err
	}
	if err := createSymlink(filepath.Base(versionDir), finalDir); err != nil {
		_ = renamePath(backupDir, finalDir)
		return err
	}
	if err := removePathAll(backupDir); err != nil {
		return err
	}

	return nil
}

func linkedDirectoryTarget(linkPath string) (string, error) {
	target, err := os.Readlink(linkPath)
	if err != nil {
		return "", fmt.Errorf("read skill directory link: %w", err)
	}

	if filepath.IsAbs(target) {
		return target, nil
	}

	return filepath.Join(filepath.Dir(linkPath), target), nil
}
