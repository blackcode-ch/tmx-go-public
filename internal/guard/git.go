package guard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var sharedBranch = regexp.MustCompile(`^(main|master|trunk|develop|development|dev|staging|stage|production|prod|stable|release|releases|v\d+)$|^release[/-]`)

// gitHead reads which branch is checked out in dir (walking up to the repository) and the
// remote's default branch. It only reads .git/HEAD and refs/remotes/origin/HEAD.
func gitHead(dir string) (branch, remoteDefault string) {
	gitDir := ""
	for d, i := filepath.FromSlash(dir), 0; d != "" && i < 64; i++ {
		p := filepath.Join(d, ".git")
		if st, err := os.Stat(p); err == nil {
			if st.IsDir() {
				gitDir = p
			} else if b, err := os.ReadFile(p); err == nil {
				if g, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:"); ok {
					g = strings.TrimSpace(g)
					if !filepath.IsAbs(g) {
						g = filepath.Join(d, g)
					}
					gitDir = g
				}
			}
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if gitDir == "" {
		return "", ""
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", ""
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/")
	if !ok {
		return "", "" // detached
	}
	common := gitDir
	if b, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		c := strings.TrimSpace(string(b))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitDir, c)
		}
		common = c
	}
	return ref, originHead(common)
}

func originHead(common string) string {
	b, err := os.ReadFile(filepath.Join(common, "refs", "remotes", "origin", "HEAD"))
	if err != nil {
		return ""
	}
	d, _ := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: refs/remotes/origin/")
	return d
}

func isShared(branch, remoteDefault string) bool {
	return branch != "" && (sharedBranch.MatchString(branch) || branch == remoteDefault)
}

func (a *analyzer) gitInfo(dir string) (branch, remoteDefault string) {
	if a.ctx.Git != nil {
		return a.ctx.Git(dir)
	}
	return gitHead(dir)
}

// hGit holds force pushes and hard resets of shared branches.
func hGit(a *analyzer, c *call) {
	dir := a.cwd
	i := 1
	for i < len(c.args) {
		s := c.args[i].s
		switch {
		case s == "-C" && i+1 < len(c.args):
			if t := a.target(c.args[i+1]); !t.unknown {
				dir = t.p
			}
			i += 2
			continue
		case (s == "-c" || s == "--git-dir" || s == "--work-tree" || s == "--namespace") && i+1 < len(c.args):
			i += 2
			continue
		case strings.HasPrefix(s, "-"):
			i++
			continue
		}
		break
	}
	if i >= len(c.args) {
		return
	}
	sub, rest := c.args[i].s, c.args[i+1:]
	switch sub {
	case "push":
		gitPush(a, c, dir, rest)
	case "reset":
		for _, x := range rest {
			if x.s == "--hard" {
				if b, def := a.gitInfo(dir); isShared(b, def) {
					a.add(RuleGitRewrite, "git reset --hard on the shared branch "+b+" discards uncommitted work", c.raw)
				}
				return
			}
		}
	}
}

func gitPush(a *analyzer, c *call, dir string, rest []arg) {
	force, del, everything, mirror := false, false, false, false
	var pos []string
	for i := 0; i < len(rest); i++ {
		s := rest[i].s
		switch {
		case s == "-n" || s == "--dry-run":
			return
		case s == "-f" || s == "--force" || strings.HasPrefix(s, "--force-with-lease") || s == "--force-if-includes":
			force = true
		case s == "--mirror":
			mirror = true
		case s == "--all" || s == "--branches":
			everything = true
		case s == "-d" || s == "--delete":
			del = true
		case s == "-o" || s == "--push-option" || s == "--repo" || s == "--receive-pack" || s == "--exec":
			i++
		case strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "--") && len(s) > 1:
			if strings.ContainsRune(s[1:], 'f') {
				force = true
			}
			if strings.ContainsRune(s[1:], 'd') {
				del = true
			}
			if strings.ContainsRune(s[1:], 'n') {
				return
			}
		case strings.HasPrefix(s, "-"):
		default:
			pos = append(pos, s)
		}
	}
	current, def := a.gitInfo(dir)
	if mirror {
		a.add(RuleGitRewrite, "git push --mirror overwrites every branch on the remote", c.raw)
		return
	}
	if !force && !del && !anyPrefix(pos, "+", ":") {
		return
	}
	if everything && (force || del) {
		a.add(RuleGitRewrite, "git push --force --all rewrites every branch on the remote", c.raw)
		return
	}
	var targets []string
	if len(pos) > 1 {
		for _, spec := range pos[1:] {
			f := strings.HasPrefix(spec, "+")
			spec = strings.TrimPrefix(spec, "+")
			src, dst, hasColon := strings.Cut(spec, ":")
			d := del || hasColon && src == ""
			if !hasColon {
				dst = src
			}
			dst = strings.TrimPrefix(dst, "refs/heads/")
			if dst == "HEAD" || dst == "" {
				dst = current
			}
			if f || force || d {
				targets = append(targets, dst)
			}
		}
	} else {
		targets = []string{current}
	}
	verb := "force-pushes"
	if del {
		verb = "deletes"
	}
	for _, t := range targets {
		if isShared(t, def) {
			a.add(RuleGitRewrite, "git push "+verb+" the shared branch "+t, c.raw)
			return
		}
		if t == "" {
			a.add(RuleGitRewrite, "git push "+verb+" a branch tmx cannot identify", c.raw)
			return
		}
	}
}

func anyPrefix(list []string, prefixes ...string) bool {
	for i, s := range list {
		if i == 0 {
			continue // the remote
		}
		for _, p := range prefixes {
			if strings.HasPrefix(s, p) {
				return true
			}
		}
	}
	return false
}
