package gitstore

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"goxidized/internal/pipeline"
	"goxidized/pkg/goxidized"
)

type Store struct {
	BasePath      string
	ShardStrategy goxidized.ShardStrategy
	ShardCount    int
	AuthorName    string
	AuthorEmail   string

	mu sync.Mutex
}

func New(basePath string, strategy goxidized.ShardStrategy, shardCount int, authorName, authorEmail string) *Store {
	return &Store{
		BasePath: basePath, ShardStrategy: strategy, ShardCount: shardCount,
		AuthorName: authorName, AuthorEmail: authorEmail,
	}
}

func (s *Store) Save(ctx context.Context, t goxidized.Target, cfg goxidized.RedactedConfig, meta goxidized.CommitMeta) (goxidized.Revision, error) {
	select {
	case <-ctx.Done():
		return goxidized.Revision{}, ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	repoPath, shard := s.repoPath(t)
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		return goxidized.Revision{}, err
	}
	repo, err := openOrInit(repoPath)
	if err != nil {
		return goxidized.Revision{}, err
	}
	rel, abs, err := s.configPath(repoPath, t)
	if err != nil {
		return goxidized.Revision{}, err
	}
	oldContent, _ := os.ReadFile(abs)
	contentSHA := pipeline.SHA256Hex(cfg.Content)
	parent := headHash(repo)
	previousFileCommit := latestCommitForFile(repo, filepath.ToSlash(rel))
	if string(oldContent) == string(cfg.Content) && previousFileCommit != "" {
		return goxidized.Revision{
			ID: previousFileCommit, TargetID: t.ID, Shard: shard, Path: filepath.ToSlash(rel), ContentSHA256: contentSHA,
			CommitSHA: previousFileCommit, ParentCommit: previousFileCommit, CreatedAt: time.Now().UTC(), Changed: false,
			CommitTrailers: trailers(t, meta, contentSHA),
		}, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return goxidized.Revision{}, err
	}
	if err := os.WriteFile(abs, cfg.Content, 0o600); err != nil {
		return goxidized.Revision{}, err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return goxidized.Revision{}, err
	}
	if _, err := wt.Add(filepath.ToSlash(rel)); err != nil {
		return goxidized.Revision{}, err
	}
	commitHash, err := wt.Commit(commitMessage(t, meta, contentSHA), &git.CommitOptions{
		Author: &object.Signature{Name: s.authorName(), Email: s.authorEmail(), When: time.Now().UTC()},
	})
	if err != nil {
		if errors.Is(err, git.ErrEmptyCommit) {
			head := headHash(repo)
			return goxidized.Revision{ID: head, TargetID: t.ID, Shard: shard, Path: filepath.ToSlash(rel), ContentSHA256: contentSHA, CommitSHA: head, ParentCommit: parent, CreatedAt: time.Now().UTC(), Changed: false}, nil
		}
		return goxidized.Revision{}, err
	}
	return goxidized.Revision{
		ID: commitHash.String(), TargetID: t.ID, Shard: shard, Path: filepath.ToSlash(rel),
		ContentSHA256: contentSHA, CommitSHA: commitHash.String(), ParentCommit: previousFileCommit,
		CreatedAt: time.Now().UTC(), Changed: true, CommitTrailers: trailers(t, meta, contentSHA),
	}, nil
}

func (s *Store) Latest(ctx context.Context, targetID string) (goxidized.RedactedConfig, goxidized.Revision, error) {
	path, repoPath, err := s.locateTargetFile(targetID)
	if err != nil {
		return goxidized.RedactedConfig{}, goxidized.Revision{}, err
	}
	content, err := os.ReadFile(filepath.Join(repoPath, path))
	if err != nil {
		return goxidized.RedactedConfig{}, goxidized.Revision{}, err
	}
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return goxidized.RedactedConfig{}, goxidized.Revision{}, err
	}
	head := latestCommitForFile(repo, filepath.ToSlash(path))
	if head == "" {
		head = headHash(repo)
	}
	return goxidized.RedactedConfig{TargetID: targetID, Content: content}, goxidized.Revision{
		ID: head, TargetID: targetID, Path: filepath.ToSlash(path), CommitSHA: head,
		ContentSHA256: pipeline.SHA256Hex(content), CreatedAt: time.Now().UTC(),
	}, nil
}

func (s *Store) History(ctx context.Context, targetID string, limit int) ([]goxidized.Revision, error) {
	path, repoPath, err := s.locateTargetFile(targetID)
	if err != nil {
		return nil, err
	}
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}
	iter, err := repo.Log(&git.LogOptions{})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	if limit <= 0 {
		limit = 100
	}
	var revs []goxidized.Revision
	err = iter.ForEach(func(c *object.Commit) error {
		if len(revs) >= limit {
			return stopeach{}
		}
		if touchedFile(c, filepath.ToSlash(path)) {
			revs = append(revs, goxidized.Revision{
				ID: c.Hash.String(), TargetID: targetID, Path: filepath.ToSlash(path), CommitSHA: c.Hash.String(),
				CreatedAt: c.Author.When,
			})
		}
		return nil
	})
	var stop stopeach
	if err != nil && !errors.As(err, &stop) {
		return nil, err
	}
	return revs, nil
}

func (s *Store) Diff(ctx context.Context, targetID, fromRev, toRev string) (string, error) {
	path, repoPath, err := s.locateTargetFile(targetID)
	if err != nil {
		return "", err
	}
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", err
	}
	oldContent, err := fileAtCommit(repo, fromRev, filepath.ToSlash(path))
	if err != nil {
		return "", err
	}
	newContent, err := fileAtCommit(repo, toRev, filepath.ToSlash(path))
	if err != nil {
		return "", err
	}
	diff, err := pipeline.UnifiedDiff(ctx, targetID, fromRev, toRev, oldContent, newContent)
	if err != nil {
		return "", err
	}
	return diff.UnifiedDiff, nil
}

func openOrInit(path string) (*git.Repository, error) {
	repo, err := git.PlainOpen(path)
	if err == nil {
		return repo, nil
	}
	if !errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, err
	}
	return git.PlainInit(path, false)
}

func (s *Store) repoPath(t goxidized.Target) (string, string) {
	shard := shardKey(t, s.ShardStrategy, s.ShardCount)
	return filepath.Join(s.BasePath, sanitize(string(s.ShardStrategy)), sanitize(shard)), sanitize(shard)
}

func (s *Store) configPath(repoPath string, t goxidized.Target) (string, string, error) {
	rel := filepath.Join(sanitize(t.Vendor), sanitize(t.ID)+".cfg")
	abs := filepath.Join(repoPath, rel)
	cleanRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return "", "", err
	}
	cleanAbs, err := filepath.Abs(abs)
	if err != nil {
		return "", "", err
	}
	if cleanAbs != cleanRepo && !strings.HasPrefix(cleanAbs, cleanRepo+string(os.PathSeparator)) {
		return "", "", fmt.Errorf("resolved path escapes repository root")
	}
	return rel, abs, nil
}

func (s *Store) locateTargetFile(targetID string) (rel string, repoPath string, err error) {
	pattern := filepath.Join(s.BasePath, "*", "*", "*", sanitize(targetID)+".cfg")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", "", err
	}
	if len(matches) == 0 {
		return "", "", os.ErrNotExist
	}
	abs, err := filepath.Abs(matches[0])
	if err != nil {
		return "", "", err
	}
	repoPath = filepath.Dir(filepath.Dir(abs))
	rel, err = filepath.Rel(repoPath, abs)
	return rel, repoPath, err
}

func shardKey(t goxidized.Target, strategy goxidized.ShardStrategy, count int) string {
	switch strategy {
	case goxidized.ShardByRegion:
		if t.Metadata != nil && t.Metadata["region"] != "" {
			return t.Metadata["region"]
		}
		return valueOrDefault(t.Site, "default")
	case goxidized.ShardBySite:
		return valueOrDefault(t.Site, "default")
	case goxidized.ShardByVendor:
		return valueOrDefault(t.Vendor, "default")
	case goxidized.ShardByRole:
		return valueOrDefault(t.Role, "default")
	case goxidized.ShardByHash:
		if count <= 0 {
			count = 1
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(t.ID))
		return "shard-" + strconv.Itoa(int(h.Sum32()%uint32(count)))
	default:
		return "default"
	}
}

func valueOrDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

var sanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func sanitize(v string) string {
	v = strings.TrimSpace(v)
	v = sanitizeRe.ReplaceAllString(v, "_")
	v = strings.Trim(v, "._-")
	if v == "" {
		return "default"
	}
	return v
}

func commitMessage(t goxidized.Target, meta goxidized.CommitMeta, contentSHA string) string {
	lines := []string{
		"backup " + t.ID,
		"",
		"Target-ID: " + t.ID,
		"Vendor: " + t.Vendor,
		"Trigger: " + meta.Trigger,
		"Actor: " + meta.Actor,
		"Job-ID: " + meta.JobID,
		"Ruleset-Version: " + meta.RulesetVersion,
		"Content-SHA256: " + contentSHA,
	}
	return strings.Join(lines, "\n") + "\n"
}

func trailers(t goxidized.Target, meta goxidized.CommitMeta, contentSHA string) map[string]string {
	return map[string]string{
		"Target-ID": t.ID, "Vendor": t.Vendor, "Trigger": meta.Trigger, "Actor": meta.Actor,
		"Job-ID": meta.JobID, "Ruleset-Version": meta.RulesetVersion, "Content-SHA256": contentSHA,
	}
}

func (s *Store) authorName() string {
	if s.AuthorName == "" {
		return "GoXidized"
	}
	return s.AuthorName
}

func (s *Store) authorEmail() string {
	if s.AuthorEmail == "" {
		return "goxidized@example.invalid"
	}
	return s.AuthorEmail
}

func headHash(repo *git.Repository) string {
	ref, err := repo.Head()
	if err != nil {
		return ""
	}
	return ref.Hash().String()
}

func latestCommitForFile(repo *git.Repository, path string) string {
	iter, err := repo.Log(&git.LogOptions{})
	if err != nil {
		return ""
	}
	defer iter.Close()
	var found string
	_ = iter.ForEach(func(c *object.Commit) error {
		if touchedFile(c, path) {
			found = c.Hash.String()
			return stopeach{}
		}
		return nil
	})
	return found
}

func touchedFile(c *object.Commit, path string) bool {
	if c.NumParents() == 0 {
		_, err := fileAtCommitFromObject(c, path)
		return err == nil
	}
	parent, err := c.Parent(0)
	if err != nil {
		return false
	}
	patch, err := parent.Patch(c)
	if err != nil {
		return false
	}
	for _, fp := range patch.FilePatches() {
		from, to := fp.Files()
		if from != nil && from.Path() == path {
			return true
		}
		if to != nil && to.Path() == path {
			return true
		}
	}
	return false
}

func fileAtCommit(repo *git.Repository, rev string, path string) ([]byte, error) {
	hash := plumbing.NewHash(rev)
	c, err := repo.CommitObject(hash)
	if err != nil {
		return nil, err
	}
	return fileAtCommitFromObject(c, path)
}

func fileAtCommitFromObject(c *object.Commit, path string) ([]byte, error) {
	tree, err := c.Tree()
	if err != nil {
		return nil, err
	}
	file, err := tree.File(path)
	if err != nil {
		return nil, err
	}
	content, err := file.Contents()
	if err != nil {
		return nil, err
	}
	return []byte(content), nil
}

type stopeach struct{}

func (stopeach) Error() string { return "stop" }
