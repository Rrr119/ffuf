package integration

import (
	"testing"

	"github.com/ffuf/ffuf/v2/pkg/ffuf"
	"github.com/ffuf/ffuf/v2/pkg/testtarget"
)

// TestRecursivePathDiscoverySites checks recursion against three independent,
// hand-verified directory layouts. The expected sets below are the exact paths a
// person gets by walking each tree and applying that subtest's matcher/filter and
// depth limit; they are deliberately not derived from ffuf's own output.
func TestRecursivePathDiscoverySites(t *testing.T) {
	t.Run("greedy tree within depth", func(t *testing.T) {
		site := testtarget.NewRecursiveSite(testtarget.RecursiveGreedySite)
		defer site.Close()

		// Hand-verified tree:
		//   /root -> /root/child -> /root/child/leaf
		//        \-> /root/sibling
		//   /noise
		//   /slash/ -> /slash/leaf
		paths := runScanCapture(t, site.URL+"/FUZZ",
			[]string{"root", "noise", "slash/", "child", "sibling", "leaf", "missing"},
			func(o *ffuf.ConfigOptions) {
				o.HTTP.Recursion = true
				o.HTTP.RecursionStrategy = "greedy"
				o.HTTP.RecursionDepth = 2
			},
			func(mm ffuf.MatcherManager) { mustMatch(t, mm, "status", "200") },
		).paths()

		assertSet(t, paths, []string{
			"/root",
			"/noise",
			"/slash/",
			"/root/child",
			"/root/sibling",
			"/slash/leaf",
			"/root/child/leaf",
		})
	})

	t.Run("greedy depth limit", func(t *testing.T) {
		site := testtarget.NewRecursiveSite(testtarget.RecursiveGreedySite)
		defer site.Close()

		// Same hand-verified tree as above, but depth 1 may only descend once from
		// the root, so /root/child/leaf must not be discovered.
		paths := runScanCapture(t, site.URL+"/FUZZ",
			[]string{"root", "noise", "slash/", "child", "sibling", "leaf", "missing"},
			func(o *ffuf.ConfigOptions) {
				o.HTTP.Recursion = true
				o.HTTP.RecursionStrategy = "greedy"
				o.HTTP.RecursionDepth = 1
			},
			func(mm ffuf.MatcherManager) { mustMatch(t, mm, "status", "200") },
		).paths()

		assertSet(t, paths, []string{
			"/root",
			"/noise",
			"/slash/",
			"/root/child",
			"/root/sibling",
			"/slash/leaf",
		})
	})

	t.Run("greedy honors matcher and filter", func(t *testing.T) {
		site := testtarget.NewRecursiveSite(testtarget.RecursiveFilteredSite)
		defer site.Close()

		// Hand-verified tree:
		//   /api -> /api/v1
		//   /soft404 -> /soft404/hidden
		// /soft404 has a junk body and is filtered, so recursion must not descend
		// into it even though /soft404/hidden would otherwise match.
		paths := runScanCapture(t, site.URL+"/FUZZ",
			[]string{"api", "v1", "soft404", "hidden", "missing"},
			func(o *ffuf.ConfigOptions) {
				o.HTTP.Recursion = true
				o.HTTP.RecursionStrategy = "greedy"
				o.HTTP.RecursionDepth = 2
			},
			func(mm ffuf.MatcherManager) {
				mustMatch(t, mm, "status", "200")
				mustFilter(t, mm, "regexp", "junk")
			},
		).paths()

		assertSet(t, paths, []string{"/api", "/api/v1"})
		for _, req := range site.Requests() {
			if req.Path == "/soft404/hidden" {
				t.Errorf("filtered directory was recursed into: requested %s", req.Path)
			}
		}
	})

	t.Run("default redirect tree", func(t *testing.T) {
		site := testtarget.NewRecursiveSite(testtarget.RecursiveRedirectSite)
		defer site.Close()

		// Hand-verified tree:
		//   /docs (301) -> /docs/ (200)
		//   /docs/guide (301) -> /docs/guide/ (200)
		//   /docs/guide/intro (200)
		// The 301 responses are part of the reported path set; the directory
		// indexes are not wordlist entries and therefore are not reported.
		paths := runScanCapture(t, site.URL+"/FUZZ",
			[]string{"docs", "guide", "intro", "missing"},
			func(o *ffuf.ConfigOptions) {
				o.HTTP.Recursion = true
				o.HTTP.RecursionStrategy = "default"
				o.HTTP.RecursionDepth = 2
			},
			func(mm ffuf.MatcherManager) { mustMatch(t, mm, "status", "200,301") },
		).paths()

		assertSet(t, paths, []string{"/docs", "/docs/guide", "/docs/guide/intro"})
	})
}
