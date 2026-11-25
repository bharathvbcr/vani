package htmltext

import (
	"html"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// minScanCap ensures large pages with heavy <head>/<nav>/<svg> prefixes
	// can still reach <main> without permitting unbounded work.
	minScanCap = 4 << 20

	// mainScopeMinBytes is the minimum furniture-stripped byte length required
	// before Pass 2 scopes extraction to <main>, [role="main"], or <article>.
	mainScopeMinBytes = 120
)

// MainText extracts the main job-posting plain text from untrusted HTML,
// capped at max bytes on a valid UTF-8 rune boundary.
//
// Pass 1 strips comments, raw non-content elements, semantic page chrome,
// hidden elements, landmark roles, and boilerplate id/class containers while
// preserving standalone heading lines (h1-h6) and list/block line breaks
// without markdown prefixes.
//
// Pass 2 scopes to an outermost <main>, [role="main"], or single substantial
// <article> (>= 120 bytes) when present, prunes link-dense boilerplate blocks
// (>= 70% anchor bytes and < 12 non-anchor words, never dropping headings),
// and drops standalone boilerplate lines.
//
// Safety fallbacks guard against over-pruning: if Pass 2 drops > 65% of Pass 1
// text (when len(pass1) >= 200), it falls back to Pass 1; if the best result
// drops > 85% compared to raw text extraction (when len(raw) >= 120, e.g. an
// entire posting wrapped in <form>), it falls back to raw text extraction.
// A max <= 0 yields "".
func MainText(s string, max int) string {
	if max <= 0 {
		return ""
	}
	scanCap := computeScanCap(max)
	if len(s) > scanCap {
		s = s[:scanCap]
	}

	blocks, mainSizes, articleSizes := pass1Scan(s)
	pass1 := renderBlocks(blocks, max)

	scoped := scopeBlocks(blocks, mainSizes, articleSizes)
	p2Blocks := prunePass2(scoped)
	pass2 := renderBlocks(p2Blocks, max)

	best := pass2
	if len(pass1) >= 200 && len(best)*100 < len(pass1)*35 {
		best = pass1
	}

	raw := safeRawText(s, max)
	if len(raw) >= 120 && len(best)*100 < len(raw)*15 {
		return raw
	}
	return best
}

func computeScanCap(max int) int {
	if max > math.MaxInt/16 {
		return math.MaxInt
	}
	if scaled := 16 * max; scaled > minScanCap {
		return scaled
	}
	return minScanCap
}

type p1Block struct {
	text           string
	anchorBytes    int
	nonAnchorWords int
	isHeading      bool
	mainID         int
	articleID      int
}

type stackEntry struct {
	tag          string
	dropped      bool
	opensMain    bool
	opensArticle bool
	isHead       bool
	isAnchor     bool
	isHeading    bool
	isForeign    bool
}

type tagAttrs struct {
	hasHref    bool
	hasHidden  bool
	ariaHidden string
	style      string
	role       string
	id         string
	class      string
}

type p1Builder struct {
	blocks []p1Block

	// Per-region furniture-stripped byte counters (1-indexed; capped once above threshold).
	mainSizes    []int
	articleSizes []int

	// Open element stack and O(1) open counters.
	stack           []stackEntry
	openCount       map[string]int
	droppedTagCount map[string]int
	mainTagCount    map[string]int

	dropDepth          int
	mainDepth          int
	articleDepth       int
	mainOrArticleDepth int
	headDepth          int
	anchorDepth        int
	headingDepth       int
	foreignDepth       int

	activeMainID    int
	activeArticleID int
	nextMainID      int
	nextArticleID   int

	// Current block accumulator.
	lines          []string
	rawBuf         strings.Builder
	anchorBuf      strings.Builder
	nonAnchorBuf   strings.Builder
	space          bool
	anchorSpace    bool
	nonAnchorSpace bool
	curAnchorBytes int
	curIsHeading   bool
	curMainID      int
	curArticleID   int
	hasBlockMeta   bool
}

func newP1Builder() *p1Builder {
	return &p1Builder{
		openCount:       make(map[string]int, 16),
		droppedTagCount: make(map[string]int, 8),
		mainTagCount:    make(map[string]int, 4),
	}
}

func pass1Scan(s string) (blocks []p1Block, mainSizes, articleSizes []int) {
	b := newP1Builder()
	for i := 0; i < len(s); {
		c := s[i]
		if c == '<' {
			if i+1 >= len(s) {
				break
			}
			if isTagStart(s[i+1]) {
				next, ok := b.handleMarkup(s, i)
				if !ok {
					// Unterminated comment/tag/quote: stop cleanly so markup never leaks.
					break
				}
				i = next
				continue
			}
		}
		if b.dropDepth == 0 {
			b.appendByte(c)
		}
		i++
	}
	b.flushBlock()
	return b.blocks, b.mainSizes, b.articleSizes
}

func (b *p1Builder) handleMarkup(s string, start int) (int, bool) {
	if strings.HasPrefix(s[start:], "<!--") {
		next, ok := skipCommentEnd(s, start+4)
		return next, ok
	}
	c1 := s[start+1]
	if c1 == '!' || c1 == '?' {
		end := strings.IndexByte(s[start+2:], '>')
		if end < 0 {
			return len(s), false
		}
		return start + 2 + end + 1, true
	}
	if c1 == '/' {
		if start+2 >= len(s) {
			return len(s), false
		}
		if !isAlphaASCII(s[start+2]) {
			// Bogus end tag (e.g. "</1>" or "</>"): HTML5 consumes to ">".
			end := strings.IndexByte(s[start+2:], '>')
			if end < 0 {
				return len(s), false
			}
			return start + 2 + end + 1, true
		}
		k := start + 2
		for k < len(s) && isHTMLNameByte(s[k]) {
			k++
		}
		end := strings.IndexByte(s[k:], '>')
		if end < 0 {
			return len(s), false
		}
		name := LowerASCII(s[start+2 : k])
		b.handleCloseTag(name)
		return k + end + 1, true
	}

	k := start + 1
	for k < len(s) && isHTMLNameByte(s[k]) {
		k++
	}
	name := LowerASCII(s[start+1 : k])
	wantAttrs := name == "a" || (!isVoidTag(name) && name != "script" && name != "style")
	attrs, end, ok := parseTagAttrs(s, k, wantAttrs)
	if !ok {
		return len(s), false
	}

	// Inside <svg>/<math> foreign content, <script/> and <style/> self-close.
	selfClosed := isSelfClosed(s, k, end)
	if (name == "script" || name == "style") && !(b.foreignDepth > 0 && selfClosed) {
		closeAt := findRawCloseTag(s, end, name)
		if closeAt < 0 {
			return len(s), false
		}
		return closeAt, true
	}

	b.handleOpenTag(name, attrs, selfClosed)
	return end, true
}

func (b *p1Builder) handleOpenTag(name string, attrs tagAttrs, selfClosed bool) {
	// An unclosed <head> closes implicitly when <body> or body-content tags open.
	if b.headDepth > 0 && closesHeadImplicitly(name) {
		b.popUntilTag("head")
	}

	if isVoidTag(name) {
		if b.dropDepth == 0 {
			switch name {
			case "br":
				b.emitLineBreak()
			case "hr":
				b.flushBlock()
			}
		}
		return
	}

	// Self-closing non-void tag inside foreign content (or empty <svg/>, <math/>)
	// has no body and does not push onto the stack.
	if selfClosed && (b.foreignDepth > 0 || isForeignRoot(name)) {
		return
	}

	// HTML5 implicit end tags keep malformed markup from poisoning later blocks.
	if closesParagraphImplicitly(name) && b.topTag() == "p" {
		b.popTop()
	}
	if name == "li" && b.topTag() == "li" {
		b.popTop()
	}
	if (name == "option" || name == "optgroup") && b.topTag() == "option" {
		b.popTop()
	}
	if (name == "dt" || name == "dd") && (b.topTag() == "dt" || b.topTag() == "dd") {
		b.popTop()
	}
	if name == "a" && b.topTag() == "a" {
		b.popTop()
	}

	dropped, opensMain, opensArticle, isHead, isHeading := b.classifyOpen(name, attrs)
	if b.dropDepth == 0 {
		if dropped && isInlineTag(name) {
			// Inline dropped elements (e.g. <span aria-hidden="true">*</span>, <svg>,
			// <button>) stay inside the surrounding paragraph instead of splitting it.
		} else if isBlockBoundaryTag(name) || opensMain || opensArticle {
			b.flushBlock()
		}
	}

	if b.dropDepth == 0 && !dropped {
		switch name {
		case "td", "th":
			b.markSpace()
		}
	}

	isForeign := isForeignRoot(name)
	isAnchor := name == "a" && attrs.hasHref && b.dropDepth == 0 && !dropped

	// Skip pushing plain un-attributed inline tags unless an outer dropped/main
	// element of the same name is already open and needs matching close depth.
	needStack := !isInlineTag(name) ||
		dropped ||
		opensMain ||
		isAnchor ||
		b.droppedTagCount[name] > 0 ||
		b.mainTagCount[name] > 0

	if !needStack {
		return
	}

	if dropped {
		b.dropDepth++
		b.droppedTagCount[name]++
	}
	if isHead {
		b.headDepth++
	}
	if isForeign {
		b.foreignDepth++
	}
	if isHeading && b.dropDepth == 0 {
		b.headingDepth++
	}
	if isAnchor {
		if b.anchorDepth == 0 {
			b.anchorSpace = false
		}
		b.anchorDepth++
	}

	entryOpensMain := false
	if opensMain && b.dropDepth == 0 {
		b.mainDepth++
		b.mainOrArticleDepth++
		b.mainTagCount[name]++
		if b.mainDepth == 1 {
			b.nextMainID++
			b.activeMainID = b.nextMainID
			b.mainSizes = append(b.mainSizes, 0)
			entryOpensMain = true
		} else {
			entryOpensMain = true
		}
	}

	entryOpensArticle := false
	if opensArticle && b.dropDepth == 0 {
		b.articleDepth++
		b.mainOrArticleDepth++
		if b.articleDepth == 1 {
			b.nextArticleID++
			b.activeArticleID = b.nextArticleID
			b.articleSizes = append(b.articleSizes, 0)
		}
		entryOpensArticle = true
	}

	b.openCount[name]++
	b.stack = append(b.stack, stackEntry{
		tag:          name,
		dropped:      dropped,
		opensMain:    entryOpensMain,
		opensArticle: entryOpensArticle,
		isHead:       isHead,
		isAnchor:     isAnchor,
		isHeading:    isHeading && b.dropDepth == 0,
		isForeign:    isForeign,
	})
}

func (b *p1Builder) handleCloseTag(name string) {
	if name == "td" || name == "th" {
		if b.dropDepth == 0 {
			b.markSpace()
		}
	}
	if b.openCount[name] == 0 {
		return
	}
	// If the top is an unclosed inline <option> inside <select> or similar,
	// popUntilTag cleans everything up to the matching ancestor in O(1) amortized.
	top := b.stack[len(b.stack)-1]
	if b.dropDepth == 0 && (isBlockBoundaryTag(name) || b.mainTagCount[name] > 0 || top.opensMain || top.opensArticle || top.isHeading) {
		b.flushBlock()
	}
	b.popUntilTag(name)
}

func (b *p1Builder) popUntilTag(name string) {
	for b.openCount[name] > 0 && len(b.stack) > 0 {
		popped := b.popTop()
		if popped.tag == name {
			break
		}
	}
}

func (b *p1Builder) popTop() stackEntry {
	e := b.stack[len(b.stack)-1]
	b.stack = b.stack[:len(b.stack)-1]
	b.openCount[e.tag]--

	if e.isAnchor {
		if b.anchorDepth > 0 {
			b.anchorDepth--
			if b.anchorDepth == 0 {
				b.commitAnchorSpan()
			}
		}
	}
	if e.isHeading && b.headingDepth > 0 {
		b.headingDepth--
	}
	if e.opensMain {
		b.mainTagCount[e.tag]--
		if b.mainDepth > 0 {
			b.mainDepth--
			if b.mainDepth == 0 {
				b.activeMainID = 0
			}
		}
		if b.mainOrArticleDepth > 0 {
			b.mainOrArticleDepth--
		}
	}
	if e.opensArticle {
		if b.articleDepth > 0 {
			b.articleDepth--
			if b.articleDepth == 0 {
				b.activeArticleID = 0
			}
		}
		if b.mainOrArticleDepth > 0 {
			b.mainOrArticleDepth--
		}
	}
	if e.isHead && b.headDepth > 0 {
		b.headDepth--
	}
	if e.isForeign && b.foreignDepth > 0 {
		b.foreignDepth--
	}
	if e.dropped {
		b.droppedTagCount[e.tag]--
		if b.dropDepth > 0 {
			b.dropDepth--
		}
	}
	return e
}

func (b *p1Builder) topTag() string {
	if len(b.stack) == 0 {
		return ""
	}
	return b.stack[len(b.stack)-1].tag
}

func (b *p1Builder) classifyOpen(name string, attrs tagAttrs) (dropped, opensMain, opensArticle, isHead, isHeading bool) {
	isHead = name == "head"
	isHeading = isHeadingTag(name)

	if isRawNonContentTag(name) || isSemanticChromeTag(name) || isHead || name == "title" {
		dropped = true
	} else if name == "header" && b.mainOrArticleDepth == 0 {
		dropped = true
	}

	if !dropped && hasHiddenOrChromeAttrs(name, attrs) {
		dropped = true
	}

	if !dropped {
		opensMain = name == "main" || hasRoleToken(attrs.role, "main")
		opensArticle = name == "article"
	}
	return dropped, opensMain, opensArticle, isHead, isHeading
}

func (b *p1Builder) appendByte(c byte) {
	if isSpaceByte(c) {
		b.markSpace()
		return
	}
	if !b.hasBlockMeta {
		b.hasBlockMeta = true
		b.curIsHeading = b.headingDepth > 0
		b.curMainID = b.activeMainID
		b.curArticleID = b.activeArticleID
	} else if b.headingDepth > 0 {
		b.curIsHeading = true
	}

	if b.space {
		if b.rawBuf.Len() > 0 {
			b.rawBuf.WriteByte(' ')
		}
		b.space = false
	}
	b.rawBuf.WriteByte(c)

	if b.anchorDepth > 0 {
		if b.anchorSpace && b.anchorBuf.Len() > 0 {
			b.anchorBuf.WriteByte(' ')
		}
		b.anchorSpace = false
		b.anchorBuf.WriteByte(c)
	} else {
		if b.nonAnchorSpace && b.nonAnchorBuf.Len() > 0 {
			b.nonAnchorBuf.WriteByte(' ')
		}
		b.nonAnchorSpace = false
		b.nonAnchorBuf.WriteByte(c)
	}
}

func (b *p1Builder) markSpace() {
	b.space = true
	if b.anchorDepth > 0 {
		b.anchorSpace = true
	} else {
		b.nonAnchorSpace = true
	}
}

func (b *p1Builder) emitLineBreak() {
	b.commitCurrentLine()
	b.space = false
	if b.anchorDepth > 0 {
		b.commitAnchorSpan()
		b.anchorSpace = false
	} else if b.nonAnchorBuf.Len() > 0 {
		b.nonAnchorSpace = true
	}
}

func (b *p1Builder) commitCurrentLine() {
	if b.rawBuf.Len() == 0 {
		return
	}
	line, _ := cleanSegment(html.UnescapeString(b.rawBuf.String()))
	b.rawBuf.Reset()
	b.space = false
	if line != "" {
		b.lines = append(b.lines, line)
	}
}

func (b *p1Builder) commitAnchorSpan() {
	if b.anchorBuf.Len() == 0 {
		b.anchorSpace = false
		return
	}
	cleaned, _ := cleanSegment(html.UnescapeString(b.anchorBuf.String()))
	b.anchorBuf.Reset()
	b.anchorSpace = false
	if cleaned != "" {
		if b.curAnchorBytes > 0 {
			// Account for line/space separation between distinct anchor lines if needed;
			// clamped to block text length upon block flush.
			b.curAnchorBytes += len(cleaned)
		} else {
			b.curAnchorBytes = len(cleaned)
		}
	}
	if b.nonAnchorBuf.Len() > 0 {
		b.nonAnchorSpace = true
	}
}

func (b *p1Builder) flushBlock() {
	if b.anchorDepth > 0 && b.anchorBuf.Len() > 0 {
		b.commitAnchorSpan()
	}
	b.commitCurrentLine()

	if len(b.lines) == 0 {
		b.resetBlock()
		return
	}

	text := strings.Join(b.lines, "\n")
	anchorBytes := min(b.curAnchorBytes, len(text))
	nonAnchorWords := 0
	if b.nonAnchorBuf.Len() > 0 {
		_, nonAnchorWords = cleanSegment(html.UnescapeString(b.nonAnchorBuf.String()))
	}

	blk := p1Block{
		text:           text,
		anchorBytes:    anchorBytes,
		nonAnchorWords: nonAnchorWords,
		isHeading:      b.curIsHeading,
		mainID:         b.curMainID,
		articleID:      b.curArticleID,
	}
	b.blocks = append(b.blocks, blk)

	if blk.mainID > 0 {
		idx := blk.mainID - 1
		if b.mainSizes[idx] < mainScopeMinBytes {
			b.mainSizes[idx] = addRegionBytes(b.mainSizes[idx], blk)
		}
	}
	if blk.articleID > 0 {
		idx := blk.articleID - 1
		if b.articleSizes[idx] < mainScopeMinBytes {
			b.articleSizes[idx] = addRegionBytes(b.articleSizes[idx], blk)
		}
	}
	b.resetBlock()
}

func addRegionBytes(cur int, blk p1Block) int {
	if cur == 0 {
		return len(blk.text)
	}
	if blk.isHeading {
		return cur + 2 + len(blk.text)
	}
	return cur + 1 + len(blk.text)
}

func (b *p1Builder) resetBlock() {
	b.lines = b.lines[:0]
	b.rawBuf.Reset()
	b.anchorBuf.Reset()
	b.nonAnchorBuf.Reset()
	b.space = false
	b.anchorSpace = false
	b.nonAnchorSpace = false
	b.curAnchorBytes = 0
	b.curIsHeading = false
	b.curMainID = 0
	b.curArticleID = 0
	b.hasBlockMeta = false
}

func scopeBlocks(blocks []p1Block, mainSizes, articleSizes []int) []p1Block {
	chosenMain := 0
	for i, sz := range mainSizes {
		if sz >= mainScopeMinBytes {
			chosenMain = i + 1
			break
		}
	}
	if chosenMain > 0 {
		out := make([]p1Block, 0, len(blocks))
		for _, blk := range blocks {
			if blk.mainID == chosenMain {
				out = append(out, blk)
			}
		}
		return out
	}

	chosenArticle := 0
	substantialArticles := 0
	for i, sz := range articleSizes {
		if sz >= mainScopeMinBytes {
			chosenArticle = i + 1
			substantialArticles++
		}
	}
	if substantialArticles == 1 {
		out := make([]p1Block, 0, len(blocks))
		for _, blk := range blocks {
			if blk.articleID == chosenArticle {
				out = append(out, blk)
			}
		}
		return out
	}
	return blocks
}

func prunePass2(blocks []p1Block) []p1Block {
	out := make([]p1Block, 0, len(blocks))
	for _, blk := range blocks {
		if !blk.isHeading && len(blk.text) > 0 &&
			blk.anchorBytes*100 >= len(blk.text)*70 &&
			blk.nonAnchorWords < 12 {
			continue
		}
		filtered := dropBoilerplateLines(blk.text)
		if filtered == "" {
			continue
		}
		blk.text = filtered
		out = append(out, blk)
	}
	return out
}

func dropBoilerplateLines(text string) string {
	if !strings.ContainsRune(text, '\n') {
		if isStandaloneBoilerplateLine(text) {
			return ""
		}
		return text
	}
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" || isStandaloneBoilerplateLine(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func renderBlocks(blocks []p1Block, max int) string {
	var b strings.Builder
	for _, blk := range blocks {
		if blk.text == "" {
			continue
		}
		if b.Len() > 0 {
			if blk.isHeading {
				b.WriteString("\n\n")
			} else {
				b.WriteByte('\n')
			}
		}
		b.WriteString(blk.text)
		if b.Len() >= max {
			break
		}
	}
	return Plain(b.String(), max)
}

// safeRawText mirrors Text(s, max) for the raw-text safety fallback, except
// that unterminated comments and unterminated <tag tokens stop cleanly instead
// of turning into literal markup text.
func safeRawText(s string, max int) string {
	if max <= 0 {
		return ""
	}
	budget := 4*max + 64
	var b strings.Builder
	space := false
	for i := 0; i < len(s) && b.Len() < budget; {
		c := s[i]
		if c == '<' {
			if i+1 >= len(s) {
				break
			}
			if isTagStart(s[i+1]) {
				if strings.HasPrefix(s[i:], "<!--") {
					next, ok := skipCommentEnd(s, i+4)
					if !ok {
						break
					}
					i = next
					continue
				}
				c1 := s[i+1]
				if c1 == '!' || c1 == '?' {
					end := strings.IndexByte(s[i+2:], '>')
					if end < 0 {
						break
					}
					i = i + 2 + end + 1
					continue
				}
				if c1 == '/' {
					if i+2 >= len(s) {
						break
					}
					k := i + 2
					for k < len(s) && isHTMLNameByte(s[k]) {
						k++
					}
					end := strings.IndexByte(s[k:], '>')
					if end < 0 {
						break
					}
					t, _ := tagOf(s[i+1 : k+end])
					switch t.kind {
					case cell:
						space = true
					case block:
						if n := b.Len(); n > 0 && b.String()[n-1] != '\n' {
							b.WriteByte('\n')
						}
						space = false
					}
					i = k + end + 1
					continue
				}
				k := i + 1
				for k < len(s) && isHTMLNameByte(s[k]) {
					k++
				}
				_, end, ok := parseTagAttrs(s, k, false)
				if !ok {
					break
				}
				t, _ := tagOf(s[i+1 : k])
				switch t.kind {
				case dropContent:
					name := LowerASCII(s[i+1 : k])
					closeAt := findRawCloseTag(s, end, name)
					if closeAt < 0 {
						return Plain(html.UnescapeString(b.String()), max)
					}
					i = closeAt
					continue
				case cell:
					space = true
				case block:
					if n := b.Len(); n > 0 && b.String()[n-1] != '\n' {
						b.WriteByte('\n')
					}
					space = false
				}
				i = end
				continue
			}
		}
		if isSpaceByte(c) {
			space = true
			i++
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteByte(c)
		i++
	}
	return Plain(html.UnescapeString(b.String()), max)
}

func skipCommentEnd(doc string, pos int) (int, bool) {
	if strings.HasPrefix(doc[pos:], ">") {
		return pos + 1, true
	}
	if strings.HasPrefix(doc[pos:], "->") {
		return pos + 2, true
	}
	rest := doc[pos:]
	a, b := strings.Index(rest, "-->"), strings.Index(rest, "--!>")
	switch {
	case a < 0 && b < 0:
		return len(doc), false
	case b < 0 || (a >= 0 && a < b):
		return pos + a + 3, true
	default:
		return pos + b + 4, true
	}
}

func findRawCloseTag(doc string, pos int, name string) int {
	needle := "</" + name
	for pos < len(doc) {
		k := IndexFold(doc[pos:], needle)
		if k < 0 {
			return -1
		}
		after := pos + k + len(needle)
		if after >= len(doc) {
			return -1
		}
		ch := doc[after]
		if ch == '>' {
			return after + 1
		}
		if isSpaceByte(ch) || ch == '/' {
			end := strings.IndexByte(doc[after:], '>')
			if end < 0 {
				return -1
			}
			return after + end + 1
		}
		pos = after
	}
	return -1
}

func parseTagAttrs(doc string, pos int, keep bool) (attrs tagAttrs, end int, ok bool) {
	i := pos
	for i < len(doc) {
		for i < len(doc) && (isSpaceByte(doc[i]) || doc[i] == '/') {
			i++
		}
		if i >= len(doc) {
			return attrs, 0, false
		}
		if doc[i] == '>' {
			return attrs, i + 1, true
		}
		start := i
		for i < len(doc) && !isSpaceByte(doc[i]) && doc[i] != '=' && doc[i] != '>' && doc[i] != '/' {
			i++
		}
		if i == start {
			i++
			continue
		}
		key := doc[start:i]
		for i < len(doc) && isSpaceByte(doc[i]) {
			i++
		}
		hasVal := false
		val := ""
		if i < len(doc) && doc[i] == '=' {
			hasVal = true
			i++
			for i < len(doc) && isSpaceByte(doc[i]) {
				i++
			}
			if i >= len(doc) {
				return attrs, 0, false
			}
			if q := doc[i]; q == '"' || q == '\'' {
				e := strings.IndexByte(doc[i+1:], q)
				if e < 0 {
					return attrs, 0, false
				}
				val = doc[i+1 : i+1+e]
				i += e + 2
			} else {
				s := i
				for i < len(doc) && !isSpaceByte(doc[i]) && doc[i] != '>' {
					i++
				}
				val = doc[s:i]
			}
		}
		if !keep {
			continue
		}
		switch LowerASCII(key) {
		case "href":
			if !attrs.hasHref {
				attrs.hasHref = true
			}
		case "hidden":
			attrs.hasHidden = true
		case "aria-hidden":
			if attrs.ariaHidden == "" && hasVal {
				attrs.ariaHidden = unescapeAttrIfNeeded(val)
			}
		case "style":
			if attrs.style == "" && hasVal {
				attrs.style = unescapeAttrIfNeeded(val)
			}
		case "role":
			if attrs.role == "" && hasVal {
				attrs.role = unescapeAttrIfNeeded(val)
			}
		case "id":
			if attrs.id == "" && hasVal {
				attrs.id = unescapeAttrIfNeeded(val)
			}
		case "class":
			if attrs.class == "" && hasVal {
				attrs.class = unescapeAttrIfNeeded(val)
			}
		}
	}
	return attrs, 0, false
}

func unescapeAttrIfNeeded(v string) string {
	if strings.IndexByte(v, '&') < 0 {
		return v
	}
	return html.UnescapeString(v)
}

func isSelfClosed(doc string, k, end int) bool {
	if end-2 < k || doc[end-2] != '/' {
		return false
	}
	return end-2 == k || isSpaceByte(doc[end-3]) || doc[end-3] == '"' || doc[end-3] == '\''
}

func isAlphaASCII(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isHTMLNameByte(c byte) bool {
	return isAlphaASCII(c) || (c >= '0' && c <= '9') || c == '-' || c == ':' || c == '_'
}

func hasHiddenOrChromeAttrs(name string, attrs tagAttrs) bool {
	if attrs.hasHidden {
		return true
	}
	if attrs.ariaHidden != "" && strings.EqualFold(strings.TrimSpace(attrs.ariaHidden), "true") {
		return true
	}
	if attrs.style != "" && styleHidesElement(attrs.style) {
		return true
	}
	if attrs.role != "" && hasLandmarkDropRole(attrs.role) {
		return true
	}
	if (attrs.id != "" || attrs.class != "") && name != "html" && name != "body" {
		return isBoilerplateIDOrClass(attrs.id, attrs.class)
	}
	return false
}

func styleHidesElement(style string) bool {
	for style != "" {
		var decl string
		decl, style, _ = strings.Cut(style, ";")
		prop, val, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		prop = strings.TrimSpace(LowerASCII(prop))
		if prop != "display" && prop != "visibility" {
			continue
		}
		val = strings.TrimSpace(LowerASCII(val))
		switch prop {
		case "display":
			if cssKeywordEquals(val, "none") {
				return true
			}
		case "visibility":
			if cssKeywordEquals(val, "hidden") {
				return true
			}
		}
	}
	return false
}

func cssKeywordEquals(val, kw string) bool {
	if !strings.HasPrefix(val, kw) {
		return false
	}
	if len(val) == len(kw) {
		return true
	}
	next := val[len(kw)]
	return isSpaceByte(next) || next == '!' || next == '/'
}

func hasLandmarkDropRole(role string) bool {
	for _, tok := range strings.Fields(LowerASCII(role)) {
		switch tok {
		case "navigation", "banner", "contentinfo", "complementary",
			"dialog", "alertdialog", "search", "menu", "menubar":
			return true
		}
	}
	return false
}

func hasRoleToken(role, target string) bool {
	if role == "" {
		return false
	}
	for _, tok := range strings.Fields(LowerASCII(role)) {
		if tok == target {
			return true
		}
	}
	return false
}

func isBoilerplateIDOrClass(id, class string) bool {
	hasBoilerplate := false
	hasContent := false

	scanAttr := func(s string) {
		if s == "" {
			return
		}
		// Inspect whitespace-separated identifiers without allocating slices on
		// typical short strings.
		for i := 0; i < len(s); {
			for i < len(s) && isSpaceByte(s[i]) {
				i++
			}
			if i >= len(s) {
				break
			}
			j := i
			for j < len(s) && !isSpaceByte(s[j]) {
				j++
			}
			bMatch, cMatch := matchIdentTokens(s[i:j])
			hasBoilerplate = hasBoilerplate || bMatch
			hasContent = hasContent || cMatch
			i = j
		}
	}

	scanAttr(id)
	scanAttr(class)
	return hasBoilerplate && !hasContent
}

// matchIdentTokens inspects a single CSS identifier (from id or class) across
// separator boundaries (-, _, ., /, :) and CamelCase boundaries so compound
// names like "onetrust-consent-sdk", "siteHeader", and "jobDescription" match
// cleanly while avoiding substring false positives like "main" in "domain".
func matchIdentTokens(ident string) (boilerplate, content bool) {
	// Check raw lowercased segment (handles "OneTrust" -> "onetrust",
	// "cookiebanner" -> "cookie*", "jobposting" -> "job*").
	rawLower := LowerASCII(ident)
	checkWord := func(w string) {
		if isBoilerplateWord(w) {
			boilerplate = true
		}
		if isContentWord(w) {
			content = true
		}
	}

	// Split at non-alphanumeric separators first, then also split CamelCase
	// within each alphanumeric run.
	var prevSub string
	for i := 0; i < len(ident); {
		for i < len(ident) && !isAlnumASCII(ident[i]) {
			i++
		}
		if i >= len(ident) {
			break
		}
		j := i
		for j < len(ident) && isAlnumASCII(ident[j]) {
			j++
		}
		seg := rawLower[i:j]
		checkWord(seg)

		// Split ident[i:j] by CamelCase boundaries.
		sStart := i
		for k := i; k < j; k++ {
			if k > sStart && isCamelSplit(ident, k, j) {
				sub := rawLower[sStart:k]
				checkWord(sub)
				if prevSub != "" && isBoilerplateBigram(prevSub, sub) {
					boilerplate = true
				}
				prevSub = sub
				sStart = k
			}
		}
		sub := rawLower[sStart:j]
		checkWord(sub)
		if prevSub != "" && isBoilerplateBigram(prevSub, sub) {
			boilerplate = true
		}
		prevSub = sub
		i = j
	}
	return boilerplate, content
}

func isCamelSplit(s string, idx, end int) bool {
	c := s[idx]
	if c < 'A' || c > 'Z' {
		return false
	}
	prev := s[idx-1]
	// lower/digit -> Upper (e.g. siteHeader, v2Nav)
	if !(prev >= 'A' && prev <= 'Z') {
		return true
	}
	// Upper -> Upper+lower (e.g. GDPRConsent -> GDPR | Consent)
	return idx+1 < end && s[idx+1] >= 'a' && s[idx+1] <= 'z'
}

func isAlnumASCII(c byte) bool {
	return isAlphaASCII(c) || (c >= '0' && c <= '9')
}

func isBoilerplateWord(w string) bool {
	switch w {
	case "cookie", "cookies",
		"consent", "consents",
		"gdpr",
		"onetrust",
		"cookiebot",
		"breadcrumb", "breadcrumbs",
		"navbar",
		"sidebar",
		"newsletter", "newsletters",
		"modal", "modals",
		"popup", "popups",
		"toast", "toasts":
		return true
	}
	return strings.HasPrefix(w, "cookie") ||
		strings.HasPrefix(w, "consent") ||
		strings.HasPrefix(w, "gdpr") ||
		strings.HasPrefix(w, "onetrust") ||
		strings.HasPrefix(w, "cookiebot") ||
		strings.HasPrefix(w, "breadcrumb") ||
		strings.HasPrefix(w, "navbar") ||
		strings.HasPrefix(w, "sidebar") ||
		strings.HasPrefix(w, "newsletter") ||
		strings.HasPrefix(w, "popup")
}

func isBoilerplateBigram(a, b string) bool {
	switch a {
	case "nav":
		return b == "bar"
	case "side":
		return b == "bar"
	case "site":
		return b == "header" || b == "footer"
	case "page":
		return b == "header" || b == "footer"
	case "social":
		return b == "share"
	case "share":
		return b == "buttons" || b == "button"
	case "skip":
		return b == "link" || b == "links" || b == "to"
	case "alert":
		return b == "banner" || b == "banners"
	}
	return false
}

func isContentWord(w string) bool {
	switch w {
	case "job", "jobs",
		"posting", "postings",
		"description", "descriptions",
		"content", "contents",
		"main",
		"article", "articles",
		"role", "roles",
		"career", "careers",
		"vacancy", "vacancies":
		return true
	}
	return strings.HasPrefix(w, "job") ||
		strings.HasPrefix(w, "posting") ||
		strings.HasPrefix(w, "description") ||
		strings.HasPrefix(w, "career") ||
		strings.HasPrefix(w, "vacanc")
}

func isStandaloneBoilerplateLine(line string) bool {
	norm := LowerASCII(strings.TrimSpace(line))
	norm = strings.TrimRight(norm, " \t.!?:;→↑↓←×✕✖")
	norm = strings.TrimSpace(norm)
	switch norm {
	case "skip to main content",
		"skip to content",
		"skip to main",
		"skip navigation",
		"skip to navigation",
		"skip to job description",
		"jump to main content",
		"jump to content",
		"back to top",
		"return to top",
		"scroll to top",
		"accept all",
		"accept cookies",
		"accept all cookies",
		"allow all cookies",
		"reject all",
		"reject cookies",
		"reject all cookies",
		"decline all",
		"decline cookies",
		"cookie settings",
		"cookie preferences",
		"cookie policy",
		"manage cookies",
		"manage cookie preferences",
		"manage cookie settings",
		"manage preferences",
		"manage consent",
		"customize cookies",
		"customise cookies",
		"save preferences",
		"save my preferences",
		"confirm my choices",
		"your privacy choices",
		"we use cookies",
		"toggle navigation",
		"open navigation",
		"open menu",
		"close menu",
		"close modal",
		"close dialog",
		"share this job",
		"share this role",
		"share this posting",
		"copy link",
		"print this page",
		"loading":
		return true
	}
	return false
}

// cleanSegment drops invalid UTF-8 and control characters, collapses all
// Unicode whitespace runs to single ASCII spaces, trims outer spaces, and
// returns both the cleaned string and its word count.
func cleanSegment(s string) (string, int) {
	if isFastCleanASCII(s) {
		return s, countASCIIWordsFast(s)
	}
	var out strings.Builder
	out.Grow(len(s))
	inWord := false
	pendingSpace := false
	words := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		i += size
		if unicode.IsSpace(r) {
			if inWord {
				pendingSpace = true
				inWord = false
			}
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		if pendingSpace {
			out.WriteByte(' ')
			pendingSpace = false
		}
		if !inWord {
			words++
			inWord = true
		}
		out.WriteRune(r)
	}
	return out.String(), words
}

func isFastCleanASCII(s string) bool {
	if len(s) == 0 {
		return true
	}
	if s[0] == ' ' || s[len(s)-1] == ' ' {
		return false
	}
	prevSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f {
			return false
		}
		if c == ' ' {
			if prevSpace {
				return false
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
	}
	return true
}

func countASCIIWordsFast(s string) int {
	if s == "" {
		return 0
	}
	words := 1
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			words++
		}
	}
	return words
}

func isRawNonContentTag(name string) bool {
	switch name {
	case "script", "style", "noscript", "template", "svg", "math",
		"iframe", "canvas", "object", "embed", "select", "option", "button":
		return true
	}
	return false
}

func isSemanticChromeTag(name string) bool {
	switch name {
	case "nav", "footer", "aside", "form", "dialog":
		return true
	}
	return false
}

func isVoidTag(name string) bool {
	switch name {
	case "area", "base", "br", "col", "embed", "hr", "img", "input",
		"link", "meta", "param", "source", "track", "wbr":
		return true
	}
	return false
}

func isForeignRoot(name string) bool {
	return name == "svg" || name == "math"
}

func isHeadingTag(name string) bool {
	switch name {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return true
	}
	return false
}

func isInlineTag(name string) bool {
	switch name {
	case "a", "abbr", "b", "bdi", "bdo", "big", "button", "cite", "code",
		"data", "del", "dfn", "em", "font", "i", "ins", "kbd", "label",
		"mark", "output", "picture", "q", "ruby", "rt", "rp", "s", "samp",
		"select", "small", "span", "strong", "sub", "sup", "svg", "math",
		"time", "u", "var", "video", "audio", "canvas", "iframe", "object",
		"noscript", "template", "option":
		return true
	}
	return false
}

func isBlockBoundaryTag(name string) bool {
	switch name {
	case "p", "div", "li", "ul", "ol", "dl", "dt", "dd", "tr", "table",
		"thead", "tbody", "tfoot", "caption",
		"h1", "h2", "h3", "h4", "h5", "h6", "hgroup",
		"section", "article", "header", "footer", "blockquote", "pre",
		"nav", "aside", "form", "dialog", "fieldset", "figure",
		"figcaption", "main", "address", "details", "summary", "hr":
		return true
	}
	return false
}

func closesParagraphImplicitly(name string) bool {
	switch name {
	case "address", "article", "aside", "blockquote", "details", "dialog",
		"div", "dl", "fieldset", "figcaption", "figure", "footer", "form",
		"h1", "h2", "h3", "h4", "h5", "h6", "header", "hgroup", "hr",
		"main", "nav", "ol", "p", "pre", "section", "table", "ul":
		return true
	}
	return false
}

func closesHeadImplicitly(name string) bool {
	switch name {
	case "body", "main", "article", "section", "header", "nav", "footer",
		"aside", "form", "dialog", "div", "p", "h1", "h2", "h3", "h4",
		"h5", "h6", "ul", "ol", "dl", "table", "blockquote", "pre", "hr":
		return true
	}
	return false
}
