package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	securejoin "github.com/cyphar/filepath-securejoin"
	"github.com/fluxcd/pkg/http/fetch"
	"github.com/fluxcd/pkg/tar"
	"github.com/go-logr/logr"
	templatesv1 "github.com/weaveworks/gitopssets-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/yaml"
)

type archiveFetcher interface {
	Fetch(archiveURL, checksum, dir string) error
}

// retries is the number of retries to make when fetching artifacts.
const retries = 9

// RepositoryParser fetches archives from a GitRepository and parses the
// resources from them.
type RepositoryParser struct {
	fetcher archiveFetcher
	logr.Logger
}

// NewRepositoryParser creates and returns a RepositoryParser.
func NewRepositoryParser(logger logr.Logger) *RepositoryParser {
	return &RepositoryParser{fetcher: fetch.NewArchiveFetcher(retries, tar.UnlimitedUntarSize, tar.UnlimitedUntarSize, ""), Logger: logger}
}

// GenerateFromFiles extracts the archive and processes the files.
func (p *RepositoryParser) GenerateFromFiles(ctx context.Context, archiveURL, checksum string, files []templatesv1.GitRepositoryGeneratorFileItem) ([]map[string]any, error) {
	tempDir, err := os.MkdirTemp("", "parsing")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary directory when parsing artifacts: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			p.Logger.Error(err, "failed to remove temporary archive directory")
		}
	}()

	if err := p.fetcher.Fetch(archiveURL, checksum, tempDir); err != nil {
		return nil, fmt.Errorf("failed to get archive URL %s: %w", archiveURL, err)
	}

	result := []map[string]any{}
	for _, file := range files {
		fullPath, err := securejoin.SecureJoin(tempDir, file.Path)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read from archive file %q: %w", file.Path, err)
		}

		r := map[string]any{}
		if err := yaml.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("failed to parse archive file %q: %w", file.Path, err)
		}

		result = append(result, r)
	}

	return result, nil
}

// GenerateFromDirectories extracts the archive and processes the directories.
func (p *RepositoryParser) GenerateFromDirectories(ctx context.Context, archiveURL, checksum string, dirs []templatesv1.GitRepositoryGeneratorDirectoryItem) ([]map[string]any, error) {
	tempDir, err := os.MkdirTemp("", "parsing")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary directory when parsing artifacts: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			p.Logger.Error(err, "failed to remove temporary archive directory")
		}
	}()

	if err := p.fetcher.Fetch(archiveURL, checksum, tempDir); err != nil {
		return nil, fmt.Errorf("failed to get archive URL %s: %w", archiveURL, err)
	}

	exclusions := sets.NewString()
	paths := []string{}
	for _, dir := range dirs {
		if dir.Exclude == true {
			exclusions.Insert(dir.Path)
			continue
		}

		fullPath, err := securejoin.SecureJoin(tempDir, dir.Path)
		if err != nil {
			return nil, err
		}
		matches, err := filepath.Glob(fullPath)
		if err != nil {
			return nil, err
		}

		for _, v := range matches {
			relPath, err := filepath.Rel(tempDir, v)
			// This error should never happen?!
			if err != nil {
				return nil, err
			}
			paths = append(paths, relPath)
		}
	}

	unexcluded := []map[string]any{}
	for _, r := range paths {
		if exclusions.Has(r) {
			continue
		}
		unexcluded = append(unexcluded, map[string]any{"Directory": "./" + r, "Base": filepath.Base(r)})
	}

	return unexcluded, nil
}
