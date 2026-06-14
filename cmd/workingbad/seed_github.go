package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/Leon180/workingbad/internal/config"
	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/repository"
)

// actionSeedGitHub fetches every issue + PR from the configured GitHub
// repo and inserts them into the local SQLite as truth-source entries:
//
//	issue → goal entry (status: open|done from issue state)
//	PR    → activity entry
//	"Closes/Fixes/Resolves #N" in a PR body → part_of edge from the PR
//	  activity into the matching goal
//
// This is a Phase-1 dogfood seeder, not the real GitHub Source connector
// (Phase 2). The mapping mirrors what the eventual connector should
// produce so the UI demo and the real sync share semantics. Read-only —
// nothing is pushed back to GitHub.
func actionSeedGitHub(ctx context.Context, c *cli.Command) error {
	cfg, err := config.LoadFile(c.String("config"))
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	repoSlug := c.String("repo")
	if repoSlug == "" {
		repoSlug, err = discoverRepoSlug(ctx)
		if err != nil {
			return fmt.Errorf("discover repo: %w (try --repo owner/name)", err)
		}
	}
	if !ghSlugRe.MatchString(repoSlug) {
		return fmt.Errorf("invalid repo %q: expected owner/name (letters, digits, . _ -)", repoSlug)
	}

	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token, err = ghAuthToken(ctx)
		if err != nil {
			return fmt.Errorf("auth token: %w (set GH_TOKEN env, or run `gh auth login`)", err)
		}
	}

	if c.Bool("wipe") {
		if err := os.Remove(cfg.DB.Path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("wipe db: %w", err)
		}
		fmt.Printf("wiped %s\n", cfg.DB.Path)
	}

	db, err := repository.Open(cfg.DB.Path)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = db.Close() }()
	svc := repository.NewService(db)

	fmt.Printf("fetching issues + PRs from %s …\n", repoSlug)
	items, err := fetchAllIssues(ctx, token, repoSlug)
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	fmt.Printf("fetched %d items\n", len(items))

	// Pass 1: insert issues as goals. We need every goal in the DB
	// before any PR-attach round-trip so "Closes #N" lookups can resolve.
	numberToGoalID := make(map[int]string, len(items))
	var goals int
	for _, it := range items {
		if it.PullRequest != nil {
			continue
		}
		e, err := svc.InsertEntry(ctx, domain.Entry{
			Type:      domain.EntryTypeGoal,
			Title:     truncate(it.Title, 500),
			Body:      it.Body,
			Source:    domain.SourceGitHub,
			SourceRef: fmt.Sprintf("issue-%d", it.Number),
			Origin:    domain.OriginFetched,
			Status:    goalStatusFromIssue(it.State),
		})
		if err != nil {
			return fmt.Errorf("insert issue #%d: %w", it.Number, err)
		}
		numberToGoalID[it.Number] = e.ID
		goals++
	}

	// Pass 2: insert PRs as activities + part_of edges from "Closes #N".
	// PR bodies frequently reference issues that don't exist locally
	// (closed cross-repo refs, edited-after-close, etc.); those silently
	// skip.
	var prs, edges int
	for _, it := range items {
		if it.PullRequest == nil {
			continue
		}
		e, err := svc.InsertEntry(ctx, domain.Entry{
			Type:      domain.EntryTypeActivity,
			Title:     truncate(it.Title, 500),
			Body:      it.Body,
			Source:    domain.SourceGitHub,
			SourceRef: fmt.Sprintf("pr-%d", it.Number),
			Origin:    domain.OriginFetched,
		})
		if err != nil {
			return fmt.Errorf("insert PR #%d: %w", it.Number, err)
		}
		prs++
		for _, n := range parseClosesRefs(it.Body) {
			goalID, ok := numberToGoalID[n]
			if !ok {
				continue
			}
			if _, err := svc.AttachToGoal(ctx, e.ID, goalID); err != nil {
				// Idempotent; if it fails it's a real error.
				return fmt.Errorf("attach pr-%d → issue-%d: %w", it.Number, n, err)
			}
			edges++
		}
	}

	fmt.Printf("\nseeded %s into %s:\n  %d goals (issues)\n  %d activities (PRs)\n  %d part_of edges (Closes #N)\n",
		repoSlug, cfg.DB.Path, goals, prs, edges)
	fmt.Printf("\nstart the UI:\n  workingbad --config %s serve\n", c.String("config"))
	return nil
}

func goalStatusFromIssue(state string) domain.Status {
	if state == "closed" {
		return domain.StatusDone
	}
	return domain.StatusOpen
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// closesRefRegexp matches the GitHub "Closes/Fixes/Resolves #N" syntax
// case-insensitively. We deliberately stay narrow: cross-repo refs
// (owner/repo#N) and the singular "close" without ing/ed are out of
// scope — they trigger more false positives than they recover edges.
var closesRefRegexp = regexp.MustCompile(`(?i)\b(?:closes|fixes|resolves)\s+#(\d+)`)

func parseClosesRefs(body string) []int {
	matches := closesRefRegexp.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]int, 0, len(matches))
	seen := make(map[int]bool, len(matches))
	for _, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err != nil || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// discoverRepoSlug parses `git remote get-url origin` to "owner/repo".
// Supports both ssh and https forms. Returns "" if the remote isn't on
// github.com.
func discoverRepoSlug(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(string(out))
	trimmed := strings.TrimSuffix(raw, ".git")
	idx := strings.Index(trimmed, "github.com")
	if idx < 0 {
		return "", fmt.Errorf("origin %q is not a github.com remote", raw)
	}
	slug := strings.TrimLeft(trimmed[idx+len("github.com"):], ":/")
	if !strings.Contains(slug, "/") {
		return "", fmt.Errorf("could not parse owner/repo from %q", raw)
	}
	return slug, nil
}

// ghAuthToken shells out to `gh auth token` to get a token using whatever
// the user's gh CLI is logged in as. Avoids requiring GH_TOKEN env.
func ghAuthToken(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("gh auth token returned empty")
	}
	return tok, nil
}

// ghIssue is the subset of the GitHub Issues API payload we use. PRs
// arrive on the same endpoint with a non-nil PullRequest field.
type ghIssue struct {
	Number      int              `json:"number"`
	Title       string           `json:"title"`
	Body        string           `json:"body"`
	State       string           `json:"state"`
	HTMLURL     string           `json:"html_url"`
	PullRequest *json.RawMessage `json:"pull_request,omitempty"`
}

// fetchAllIssues paginates the GitHub Issues API until it gets a short
// page. state=all includes closed; PRs come through the same endpoint
// (filtered apart by the PullRequest field at the call site).
// ghSlugRe constrains repoSlug to owner/name so it cannot inject query
// parameters or redirect the request to another host (e.g. an "@host" userinfo
// trick) when interpolated into the GitHub API URL.
var ghSlugRe = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// ghHTTPClient bounds GitHub fetches; http.DefaultClient has no timeout, so a
// hung connection would block the seed command forever.
var ghHTTPClient = &http.Client{Timeout: 30 * time.Second}

func fetchAllIssues(ctx context.Context, token, repoSlug string) ([]ghIssue, error) {
	var all []ghIssue
	const perPage = 100
	for page := 1; ; page++ {
		url := fmt.Sprintf(
			"https://api.github.com/repos/%s/issues?state=all&per_page=%d&page=%d&sort=created&direction=asc",
			repoSlug, perPage, page,
		)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "workingbad-seed/1")
		resp, err := ghHTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("github HTTP %d on page %d: %s", resp.StatusCode, page, string(body))
		}
		var batch []ghIssue
		if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("decode page %d: %w", page, err)
		}
		_ = resp.Body.Close()
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if len(batch) < perPage {
			break
		}
	}
	return all, nil
}
