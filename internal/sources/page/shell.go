package page

import (
	"html"
	"strings"

	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
)

const (
	shellMinVisibleBytes     = 120
	shellPlaceholderMaxBytes = 300
	shellChromePercent       = 85
	shellPlaceholderCap      = 512
)

var loadingPlaceholders = []string{
	"loading...",
	"loading…",
	"loading jobs",
	"loading positions",
	"loading roles",
	"loading careers",
	"loading openings",
	"loading opportunities",
	"loading page",
	"loading content",
	"loading data",
	"loading results",
	"fetching jobs",
	"fetching roles",
	"please wait",
	"enable javascript",
	"javascript is required",
	"requires javascript",
	"javascript must be enabled",
	"turn on javascript",
	"without javascript",
	"just a moment",
	"checking your browser",
	"initializing...",
	"initializing…",
}

// looksLikeShell reports whether doc resembles an unhydrated JS/SPA shell or a
// nav-only chrome page where the main content has not rendered yet. Work is
// linear in len(doc) and safe against truncated or hostile HTML.
func looksLikeShell(doc string) bool {
	var (
		inHead           bool
		navDepth         int
		headerDepth      int
		footerDepth      int
		mainDepth        int
		hasLink          bool
		totalBytes       int
		chromeBytes      int
		mainArticleBytes int
		nonChromeBytes   int
		nonChrome        strings.Builder
	)

	addVisible := func(chunk string) {
		if inHead || len(strings.TrimSpace(chunk)) == 0 {
			return
		}
		if strings.IndexByte(chunk, '&') >= 0 {
			chunk = html.UnescapeString(chunk)
		}
		inMain := mainDepth > 0
		inChrome := !inMain && (navDepth > 0 || headerDepth > 0 || footerDepth > 0)
		for _, field := range strings.Fields(chunk) {
			n := len(field)
			if totalBytes > 0 {
				totalBytes++
			}
			totalBytes += n
			if inMain {
				if mainArticleBytes > 0 {
					mainArticleBytes++
				}
				mainArticleBytes += n
			}
			if inChrome {
				if chromeBytes > 0 {
					chromeBytes++
				}
				chromeBytes += n
			} else {
				if nonChromeBytes > 0 {
					nonChromeBytes++
				}
				nonChromeBytes += n
				if nonChrome.Len() < shellPlaceholderCap {
					if nonChrome.Len() > 0 {
						nonChrome.WriteByte(' ')
					}
					room := shellPlaceholderCap - nonChrome.Len()
					if n <= room {
						nonChrome.WriteString(field)
					} else {
						nonChrome.WriteString(field[:room])
					}
				}
			}
		}
	}

	for i := 0; i < len(doc); {
		j := strings.IndexByte(doc[i:], '<')
		if j < 0 {
			addVisible(doc[i:])
			break
		}
		if j > 0 {
			addVisible(doc[i : i+j])
		}
		i += j
		if strings.HasPrefix(doc[i:], "<!--") {
			next, ok := commentEnd(doc, i+4)
			if !ok {
				break
			}
			i = next
			continue
		}
		if i+1 >= len(doc) {
			break
		}
		if doc[i+1] == '/' {
			end := strings.IndexByte(doc[i:], '>')
			if end < 0 {
				break
			}
			name := endTagName(doc, i+2)
			switch name {
			case "head":
				inHead = false
			case "nav":
				if navDepth > 0 {
					navDepth--
				}
			case "header":
				if headerDepth > 0 {
					headerDepth--
				}
			case "footer":
				if footerDepth > 0 {
					footerDepth--
				}
			case "main", "article":
				if mainDepth > 0 {
					mainDepth--
				}
			}
			i += end + 1
			continue
		}
		if !isLetter(doc[i+1]) {
			if doc[i+1] == '!' || doc[i+1] == '?' {
				if end := strings.IndexByte(doc[i:], '>'); end >= 0 {
					i += end + 1
					continue
				}
				break
			}
			addVisible("<")
			i++
			continue
		}

		k := i + 1
		for k < len(doc) && isNameByte(doc[k]) {
			k++
		}
		name := htmltext.LowerASCII(doc[i+1 : k])
		attrs, end, ok := parseAttrs(doc, k, name == "a")
		if !ok {
			break
		}
		sc := selfClosed(doc, k, end)
		switch name {
		case "script", "style", "noscript", "template", "title", "svg", "math":
			if !sc {
				closeAt := htmltext.IndexFold(doc[end:], "</"+name)
				if closeAt < 0 {
					return isShellState(hasLink, totalBytes, chromeBytes, mainArticleBytes, nonChromeBytes, nonChrome.String())
				}
				if closeEnd := strings.IndexByte(doc[end+closeAt:], '>'); closeEnd >= 0 {
					i = end + closeAt + closeEnd + 1
					continue
				}
				return isShellState(hasLink, totalBytes, chromeBytes, mainArticleBytes, nonChromeBytes, nonChrome.String())
			}
		case "head":
			if !sc {
				inHead = true
			}
		case "body":
			inHead = false
		case "nav":
			if !inHead && !sc {
				navDepth++
			}
		case "header":
			if !inHead && !sc && mainDepth == 0 {
				headerDepth++
			}
		case "footer":
			if !inHead && !sc {
				footerDepth++
			}
		case "main", "article":
			if !inHead && !sc {
				mainDepth++
			}
		case "a":
			if !inHead {
				if href := strings.TrimSpace(attrs["href"]); href != "" &&
					!strings.HasPrefix(href, "#") &&
					!strings.HasPrefix(htmltext.LowerASCII(href), "javascript:") {
					hasLink = true
				}
			}
		}
		i = end
	}

	return isShellState(hasLink, totalBytes, chromeBytes, mainArticleBytes, nonChromeBytes, nonChrome.String())
}

func isShellState(hasLink bool, totalBytes, chromeBytes, mainArticleBytes, nonChromeBytes int, nonChromeText string) bool {
	if !hasLink && totalBytes < shellMinVisibleBytes {
		return true
	}
	if nonChromeBytes > 0 && nonChromeBytes < shellPlaceholderMaxBytes && hasLoadingPlaceholder(nonChromeText) {
		return true
	}
	if totalBytes > 0 && mainArticleBytes == 0 && chromeBytes*100 > totalBytes*shellChromePercent {
		return true
	}
	return false
}

func hasLoadingPlaceholder(s string) bool {
	lc := strings.ToLower(strings.TrimSpace(s))
	if lc == "loading" || lc == "loading." || lc == "loading.." {
		return true
	}
	for _, pat := range loadingPlaceholders {
		if strings.Contains(lc, pat) {
			return true
		}
	}
	return false
}
