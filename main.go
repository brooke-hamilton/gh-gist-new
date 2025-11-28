package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

const usageText = `Usage: gh gist-new [name] [flags]

Create a new gist from all regular, non-dotfiles inside [name]. When [name] is '.',
the current directory is used. The directory must not contain subdirectories or
directory symlinks.

Flags:
  --public          Create the gist as public (defaults to secret)
  -d, --description Description to attach to the gist (must not be empty)
  --verbose         Show detailed per-file logs and timing information
  -h, --help        Show this message
`

var errHelpRequested = errors.New("help requested")

type options struct {
	name        string
	public      bool
	description string
	verbose     bool
}

type stringFlag struct {
	value string
	set   bool
}

func (s *stringFlag) String() string {
	return s.value
}

func (s *stringFlag) Set(v string) error {
	s.value = v
	s.set = true
	return nil
}

type filePayload struct {
	Name    string
	Path    string
	Content []byte
}

type gistCreateRequest struct {
	Description string              `json:"description,omitempty"`
	Public      bool                `json:"public"`
	Files       map[string]gistFile `json:"files"`
}

type gistFile struct {
	Content string `json:"content"`
}

type gistCreateResponse struct {
	ID      string `json:"id"`
	HTMLURL string `json:"html_url"`
}

type logger struct {
	verbose bool
}

func (l logger) Info(format string, args ...interface{}) {
	fmt.Fprintf(os.Stdout, format+"\n", args...)
}

func (l logger) Verbose(format string, args ...interface{}) {
	if l.verbose {
		fmt.Fprintf(os.Stdout, format+"\n", args...)
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, errHelpRequested) {
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	opts, err := parseArgs(args)
	if err != nil {
		return err
	}
	log := logger{verbose: opts.verbose}

	log.Info("Resolving target directory…")
	targetDir, displayName, err := resolveTargetDirectory(opts.name)
	if err != nil {
		return err
	}
	log.Verbose("Target directory: %s", targetDir)

	log.Info("Collecting files for gist…")
	startScan := time.Now()
	files, err := gatherFiles(targetDir, displayName, log)
	if err != nil {
		return err
	}
	log.Info("Collected %d file(s)", len(files))
	log.Verbose("File collection completed in %s", time.Since(startScan).Round(time.Millisecond))

	log.Info("Creating gist via GitHub API…")
	startCreate := time.Now()
	gistURL, gistID, err := createGist(files, opts, log)
	if err != nil {
		return err
	}
	log.Verbose("Gist creation completed in %s", time.Since(startCreate).Round(time.Millisecond))

	log.Info("Cloning gist metadata into target directory…")
	startClone := time.Now()
	if err := cloneGistMetadata(gistID, targetDir, log); err != nil {
		return err
	}
	log.Verbose("Metadata cloning completed in %s", time.Since(startClone).Round(time.Millisecond))

	log.Info("Done! Gist ready at %s", gistURL)
	return nil
}

func parseArgs(args []string) (options, error) {
	var opts options
	fs := flag.NewFlagSet("gh gist-new", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.public, "public", false, "create a public gist")
	fs.BoolVar(&opts.verbose, "verbose", false, "enable verbose logging")
	var desc stringFlag
	fs.Var(&desc, "description", "description for the gist")
	fs.Var(&desc, "d", "description for the gist")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, usageText)
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.Usage()
			return opts, errHelpRequested
		}
		fs.Usage()
		return opts, fmt.Errorf("failed to parse flags: %w", err)
	}

	remaining := fs.Args()
	if len(remaining) == 0 {
		fs.Usage()
		return opts, errors.New("missing required [name] argument")
	}
	if len(remaining) > 1 {
		fs.Usage()
		return opts, errors.New("only one [name] argument is supported")
	}
	name := strings.TrimSpace(remaining[0])
	if err := validateName(name); err != nil {
		fs.Usage()
		return opts, err
	}
	opts.name = name
	if desc.set {
		opts.description = strings.TrimSpace(desc.value)
		if opts.description == "" {
			return opts, errors.New("description cannot be empty when provided")
		}
	}
	return opts, nil
}

func validateName(name string) error {
	if name == "" {
		return errors.New("name cannot be empty")
	}
	if name == "." {
		return nil
	}
	if name == ".." {
		return errors.New("'..' is not a supported directory name")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("name %q may not contain path separators", name)
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("name %q may not start with '-'", name)
	}
	return nil
}

func resolveTargetDirectory(name string) (string, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("determine working directory: %w", err)
	}

	if name == "." {
		abs, err := filepath.Abs(cwd)
		if err != nil {
			return "", "", fmt.Errorf("resolve current directory: %w", err)
		}
		display := filepath.Base(abs)
		if display == "." || display == string(filepath.Separator) || display == "" {
			display = "gist"
		}
		if err := ensureWritable(abs); err != nil {
			return "", "", err
		}
		if err := ensureNotGitRepo(abs); err != nil {
			return "", "", err
		}
		return abs, display, nil
	}

	target := filepath.Join(cwd, name)
	if err := ensureDirectoryExists(target); err != nil {
		return "", "", err
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", "", fmt.Errorf("resolve directory path: %w", err)
	}
	if err := ensureWritable(abs); err != nil {
		return "", "", err
	}
	if err := ensureNotGitRepo(abs); err != nil {
		return "", "", err
	}
	return abs, name, nil
}

func ensureDirectoryExists(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", path, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect directory %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists but is not a directory", path)
	}
	return nil
}

func ensureWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".gh-gist-new-*")
	if err != nil {
		return fmt.Errorf("directory %s must be writable: %w", dir, err)
	}
	probePath := probe.Name()
	probe.Close()
	_ = os.Remove(probePath)
	return nil
}

func ensureNotGitRepo(dir string) error {
	gitPath := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitPath); err == nil {
		return fmt.Errorf("%s already contains git metadata; pick a clean folder", dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check git metadata in %s: %w", dir, err)
	}
	return nil
}

func gatherFiles(dir, displayName string, log logger) ([]filePayload, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", dir, err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	var files []filePayload
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", entry.Name(), err)
		}
		if entry.Type()&os.ModeSymlink != 0 && info.IsDir() {
			return nil, fmt.Errorf("symlink %s targets a directory; gists cannot include directories", entry.Name())
		}
		if info.IsDir() {
			return nil, fmt.Errorf("subdirectory %s detected; gists only support flat file sets", entry.Name())
		}
		if strings.HasPrefix(entry.Name(), ".") {
			log.Verbose("Skipping dotfile %s", entry.Name())
			continue
		}
		if !info.Mode().IsRegular() {
			log.Verbose("Skipping non-regular file %s", entry.Name())
			continue
		}
		path := filepath.Join(dir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		files = append(files, filePayload{Name: entry.Name(), Path: path, Content: content})
		log.Verbose("Queued %s (%d bytes)", entry.Name(), len(content))
	}
	if len(files) == 0 {
		name := defaultFileName(displayName)
		content := []byte(fmt.Sprintf("# %s\n", displayName))
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return nil, fmt.Errorf("bootstrap default file %s: %w", name, err)
		}
		files = append(files, filePayload{Name: name, Path: path, Content: content})
		log.Info("Directory was empty; created %s", name)
	}
	return files, nil
}

func defaultFileName(name string) string {
	return fmt.Sprintf("%s.md", name)
}

func createGist(files []filePayload, opts options, log logger) (string, string, error) {
	req := gistCreateRequest{
		Public: opts.public,
		Files:  make(map[string]gistFile, len(files)),
	}
	if opts.description != "" {
		req.Description = opts.description
	}
	for _, f := range files {
		req.Files[f.Name] = gistFile{Content: string(f.Content)}
	}
	client, err := api.DefaultRESTClient()
	if err != nil {
		return "", "", fmt.Errorf("init GitHub client: %w", err)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return "", "", fmt.Errorf("encode gist payload: %w", err)
	}
	var resp gistCreateResponse
	if err := client.Post("gists", bytes.NewReader(payload), &resp); err != nil {
		return "", "", fmt.Errorf("create gist via GitHub API: %w", err)
	}
	if resp.ID == "" || resp.HTMLURL == "" {
		return "", "", errors.New("GitHub API returned an incomplete gist response")
	}
	visibility := "secret"
	if opts.public {
		visibility = "public"
	}
	log.Info("Created %s gist: %s", visibility, resp.HTMLURL)
	return resp.HTMLURL, resp.ID, nil
}

func cloneGistMetadata(gistID, targetDir string, log logger) error {
	tempParent, err := os.MkdirTemp("", "gh-gist-new-")
	if err != nil {
		return fmt.Errorf("create temporary directory for cloning: %w", err)
	}
	defer os.RemoveAll(tempParent)

	cloneDir := filepath.Join(tempParent, "clone")
	cmd := exec.Command("gh", "gist", "clone", gistID, cloneDir)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf(
			"failed to clone gist metadata (retry manually: 'gh gist clone %s <tempdir>' then move .git into %s): %v\n%s",
			gistID,
			targetDir,
			err,
			strings.TrimSpace(output.String()),
		)
	}
	trimmed := strings.TrimSpace(output.String())
	if trimmed != "" {
		log.Verbose("gh gist clone output:\n%s", trimmed)
	}
	if err := moveGitMetadata(cloneDir, targetDir); err != nil {
		return fmt.Errorf(
			"failed to move git metadata (run 'gh gist clone %s <tempdir>' and move .git into %s manually): %w",
			gistID,
			targetDir,
			err,
		)
	}
	return nil
}

func moveGitMetadata(from, to string) error {
	entries, err := os.ReadDir(from)
	if err != nil {
		return fmt.Errorf("inspect cloned gist: %w", err)
	}
	movedGitDir := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".git") {
			continue
		}
		if name == ".gitignore" {
			continue
		}
		src := filepath.Join(from, name)
		dst := filepath.Join(to, name)
		if err := os.RemoveAll(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("prepare destination %s: %w", dst, err)
		}
		if err := moveFileOrDir(src, dst); err != nil {
			return fmt.Errorf("move %s into target directory: %w", name, err)
		}
		if name == ".git" {
			movedGitDir = true
		}
	}
	if !movedGitDir {
		return errors.New("cloned gist did not include a .git directory")
	}
	return nil
}

// moveFileOrDir moves a file or directory from src to dst.
// It first attempts os.Rename, and falls back to a copy-then-delete
// approach if the rename fails due to a cross-device link error.
func moveFileOrDir(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	// Check for cross-device link error (EXDEV)
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		return err
	}
	// Fall back to copy + delete for cross-device moves
	if err := copyDir(src, dst); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("remove source after copy: %w", err)
	}
	return nil
}

// copyDir recursively copies a directory tree from src to dst.
func copyDir(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyFile copies a single file from src to dst, preserving permissions.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	return dstFile.Close()
}
