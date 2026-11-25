package htmltext

import (
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

func TestMainTextNonJSONLDCareerPage(t *testing.T) {
	t.Parallel()

	page := `<!DOCTYPE html>
<html lang="en">
<head>
  <title>Senior Go Engineer | Acme Careers</title>
  <style>.nav { display: flex; }</style>
  <script>window.__DATA__ = {"secret": "ignore me"};</script>
</head>
<body class="has-sidebar modal-open">
  <a class="skip-link" href="#main">Skip to main content</a>
  <header class="site-header">
    <div class="logo">Acme Corp</div>
    <nav aria-label="Primary">
      <a href="/about">About</a>
      <nav class="sub-nav"><a href="/team">Team</a></nav>
      <a href="/careers">Careers</a>
    </nav>
  </header>

  <div id="onetrust-consent-sdk" class="cookie-banner">
    <p>We use cookies to improve your experience.</p>
    <button type="button">Accept all cookies</button>
    <button type="button">Manage cookie preferences</button>
  </div>

  <main>
    <div class="breadcrumb"><a href="/">Home</a> / <a href="/jobs">Jobs</a> / Engineering</div>
    <header class="job-header">
      <h1>Senior Go Engineer</h1>
      <p>San Francisco, CA · Full-time</p>
    </header>

    <div class="social-share">
      <a href="https://linkedin.com/share">Share on LinkedIn</a>
      <a href="https://x.com/share">Share on X</a>
    </div>

    <h2>About the Role</h2>
    <p>You will design high-throughput ingestion pipelines and read more on <a href="/eng-blog">our engineering blog</a> as you scale distributed services.</p>

    <h2>Responsibilities</h2>
    <ul>
      <li>Architect deterministic Go backend services with strict memory bounds</li>
      <li>Review <svg width="12" height="12"><path d="M0 0"/></svg> production telemetry and mentor engineers</li>
    </ul>

    <aside class="related-jobs">
      <h3>Similar Openings</h3>
      <ul>
        <li><a href="/jobs/2">Staff Security Engineer</a></li>
      </ul>
    </aside>

    <form id="application-form">
      <label>Full Name</label>
      <input type="text" name="name">
      <select><option>United States</option><option>Canada</option></select>
      <button type="submit">Submit Application</button>
    </form>
  </main>

  <footer class="site-footer">
    <p>Copyright 2026 Acme Corp</p>
    <a href="/privacy">Privacy Policy</a>
  </footer>
</body>
</html>`

	got := MainText(page, 1<<16)
	want := strings.Join([]string{
		"Senior Go Engineer",
		"San Francisco, CA · Full-time",
		"",
		"About the Role",
		"You will design high-throughput ingestion pipelines and read more on our engineering blog as you scale distributed services.",
		"",
		"Responsibilities",
		"Architect deterministic Go backend services with strict memory bounds",
		"Review production telemetry and mentor engineers",
	}, "\n")

	if got != want {
		t.Fatalf("MainText mismatch:\ngot:\n%s\n\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "#") {
		t.Fatalf("unexpected markdown heading prefix in plain output: %q", got)
	}
	if strings.ContainsAny(got, "<>") {
		t.Fatalf("markup leaked: %q", got)
	}
}

func TestMainTextPass1FurnitureStripping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "comments and raw non-content elements dropped",
			in: `<!-- hidden comment --><!--> <!--->
<script>var bad = 1;</script>
<STYLE>body { color: red; }</STYLE>
<noscript>Enable JS</noscript>
<template><div>Template content</div></template>
<svg><title>Icon</title><path d="M0 0"/></svg>
<svg class="icon" />
<math><mi>x</mi></math>
<iframe src="https://example.com">frame fallback</iframe>
<canvas>no canvas</canvas>
<object data="x.swf"><p>flash fallback</p></object>
<embed src="movie.swf">
<select><option>Opt A</option><option>Opt B</option></select>
<datalist><option value="Go"><option value="Rust"></datalist>
<button>Apply Now</button>
<p>Real job text</p>`,
			want: "Real job text",
		},
		{
			name: "semantic chrome tags nav footer aside form dialog",
			in: `<nav>nav text</nav>
<aside>aside text</aside>
<dialog open>dialog text</dialog>
<form><label>Name</label></form>
<footer>footer text</footer>
<p>Kept body</p>`,
			want: "Kept body",
		},
		{
			name: "nested nav and nested boilerplate div depth tracking",
			in: `<nav>outer1<nav>inner</nav>outer2</nav>
<div class="sidebar"><div>inner div</div>still inside sidebar</div>
<span class="cookie"><span>inner span</span>still inside cookie</span>
<p>Kept paragraph</p>`,
			want: "Kept paragraph",
		},
		{
			name: "header dropped outside main/article but kept inside main and article",
			in: `<header><h1>Site Banner</h1><p>Sign In</p></header>
<main>
  <header><h1>Platform Engineer</h1></header>
  <p>Build reliable storage engines across distributed regions with clear operational observability and automated failovers.</p>
</main>`,
			want: "Platform Engineer\nBuild reliable storage engines across distributed regions with clear operational observability and automated failovers.",
		},
		{
			name: "header kept inside substantial article",
			in: `<header>Global Header</header>
<article>
  <header><h1>Backend Engineer</h1></header>
  <p>Join our core infrastructure team to build low-latency APIs, distributed caches, and resilient indexing workflows.</p>
</article>`,
			want: "Backend Engineer\nJoin our core infrastructure team to build low-latency APIs, distributed caches, and resilient indexing workflows.",
		},
		{
			name: "hidden aria-hidden and inline style visibility rules",
			in: `<div hidden>hidden attr</div>
<div hidden="">empty hidden</div>
<div aria-hidden="true">sr hidden</div>
<div aria-hidden=" TRUE ">sr upper</div>
<div aria-hidden="false">sr visible</div>
<div style="display:none">css none</div>
<div style="color:red; DISPLAY : none !important;">css none important</div>
<div style="visibility: hidden;">css vis hidden</div>
<div style="display&#58;none">entity hidden</div>
<div style="display:block; visibility:visible">css visible</div>`,
			want: "sr visible\ncss visible",
		},
		{
			name: "aria landmark roles dropped",
			in: `<div role="navigation">nav role</div>
<div role="banner">banner role</div>
<div role="contentinfo">footer role</div>
<div role="complementary">aside role</div>
<div role="dialog">dialog role</div>
<div role="alertdialog">alertdialog role</div>
<div role="search">search role</div>
<div role="menu">menu role</div>
<div role="menubar">menubar role</div>
<div role="region">kept region</div>`,
			want: "kept region",
		},
		{
			name: "boilerplate class/id tokens vs content-token exceptions",
			in: `<div class="cookie-banner">drop cookie</div>
<div id="gdprConsent">drop gdpr</div>
<div class="OneTrust">drop onetrust</div>
<div class="cookiebot-widget">drop cookiebot</div>
<div class="breadcrumbs">drop breadcrumb</div>
<div class="navbar-top">drop navbar</div>
<div class="nav-bar-inner">drop nav-bar</div>
<div class="side-bar">drop side-bar</div>
<div class="siteHeader">drop site-header</div>
<div class="site-footer">drop site-footer</div>
<div class="page-header">drop page-header</div>
<div class="page-footer">drop page-footer</div>
<div class="social-share">drop social-share</div>
<div class="share-buttons">drop share-buttons</div>
<div class="newsletter-box">drop newsletter</div>
<div class="skip-link">drop skip-link</div>
<div class="skip-to-bar">drop skip-to</div>
<div class="consent-box">drop consent</div>
<div class="modal-backdrop">drop modal</div>
<div class="popup-box">drop popup</div>
<div class="toast-msg">drop toast</div>
<div class="alert-banner">drop alert-banner</div>
<div class="cookie-domain">drop domain without false main match</div>
<div class="multimodal-ai">keep multimodal</div>
<div class="sidebar job-description">keep job-description</div>
<div id="job-content" class="modal">keep job-content</div>
<div class="posting-popup">keep posting</div>
<div class="main-toast">keep main</div>
<div class="article-newsletter">keep article</div>
<div class="role-sidebar">keep role</div>
<div class="career-modal">keep career</div>
<div class="vacancy-banner cookie">keep vacancy</div>`,
			want: strings.Join([]string{
				"keep multimodal",
				"keep job-description",
				"keep job-content",
				"keep posting",
				"keep main",
				"keep article",
				"keep role",
				"keep career",
				"keep vacancy",
			}, "\n"),
		},
		{
			name: "headings h1-h6 lists and table cells formatting",
			in:   `<p>Intro</p><h1>H1</h1><h2>H2</h2><h3>H3</h3><h4>H4</h4><h5>H5</h5><h6>H6</h6><ul><li>One</li><li>Two</li></ul><table><tr><th>Col1</th><th>Col2</th></tr><tr><td>A</td><td>B</td></tr></table>`,
			want: "Intro\n\nH1\n\nH2\n\nH3\n\nH4\n\nH5\n\nH6\nOne\nTwo\nCol1 Col2\nA B",
		},
		{
			name: "inline dropped elements keep paragraph on one line",
			in:   `<p>Build <span aria-hidden="true">*</span> reliable <button>Copy</button> APIs.</p>`,
			want: "Build reliable APIs.",
		},
		{
			name: "entities unescaped once after tag stripping",
			in:   `<p>Tom &amp; Jerry &lt;code&gt; &copy; 2026</p>`,
			want: "Tom & Jerry <code> © 2026",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := MainText(tc.in, 1<<16)
			if got != tc.want {
				t.Fatalf("MainText mismatch:\ngot:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestMainTextPass2ScopingAndPruning(t *testing.T) {
	t.Parallel()

	t.Run("scopes to role=main when >= 120 bytes", func(t *testing.T) {
		t.Parallel()
		in := `<div class="hero">Welcome to our global career portal! Join thousands of builders worldwide.</div>
<div role="main">
  <h1>Staff Rust Engineer</h1>
  <p>You will design low-latency storage engines, memory-safe parsers, and deterministic indexing pipelines for developer tools.</p>
</div>`
		got := MainText(in, 1<<16)
		want := "Staff Rust Engineer\nYou will design low-latency storage engines, memory-safe parsers, and deterministic indexing pipelines for developer tools."
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("multiple substantial articles do not scope to first article", func(t *testing.T) {
		t.Parallel()
		art1 := strings.Repeat("First article job summary details. ", 5)
		art2 := strings.Repeat("Second article job summary details. ", 5)
		in := "<article><p>" + art1 + "</p></article><article><p>" + art2 + "</p></article>"
		got := MainText(in, 1<<16)
		if !strings.Contains(got, "First article") || !strings.Contains(got, "Second article") {
			t.Fatalf("expected both substantial articles kept, got %q", got)
		}
	})

	t.Run("main 119 vs 120 byte threshold", func(t *testing.T) {
		t.Parallel()
		below := `<p>Outside intro</p><main><p>` + strings.Repeat("a", 119) + `</p></main>`
		if got := MainText(below, 1<<16); !strings.Contains(got, "Outside intro") {
			t.Fatalf("expected 119-byte <main> not to scope out outside text, got %q", got)
		}
		exact := `<p>Outside intro</p><main><p>` + strings.Repeat("a", 120) + `</p></main>`
		if got := MainText(exact, 1<<16); strings.Contains(got, "Outside intro") {
			t.Fatalf("expected 120-byte <main> to scope out outside text, got %q", got)
		}
	})

	t.Run("undersized main falls through to single substantial article", func(t *testing.T) {
		t.Parallel()
		in := `<main><p>Tiny</p></main>
<div class="noise">Random banner text outside article</div>
<article>
  <h1>Principal Engineer</h1>
  <p>Lead our core distributed consensus and replication layer across multi-region clusters with strong reliability guarantees.</p>
</article>`
		got := MainText(in, 1<<16)
		want := "Principal Engineer\nLead our core distributed consensus and replication layer across multi-region clusters with strong reliability guarantees."
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("link-density block pruned vs inline-link paragraph and heading link kept", func(t *testing.T) {
		t.Parallel()
		in := `<main>
  <h2><a href="#role">Role Overview</a></h2>
  <p>We are hiring engineers to build scalable developer infrastructure. Read about <a href="/eng">our engineering team</a> and apply today.</p>
  <div class="links">
    <a href="/about">About Us</a> | <a href="/blog">Engineering Blog</a> | <a href="/press">Press Kit</a> | <a href="/terms">Terms of Service</a>
  </div>
  <ul>
    <li><a href="/privacy">Privacy Policy</a></li>
    <li><a href="/security">Security Overview</a></li>
    <li>Design resilient APIs with <a href="/docs">clear interface contracts</a> across Go and Rust services.</li>
  </ul>
</main>`
		got := MainText(in, 1<<16)
		want := "Role Overview\nWe are hiring engineers to build scalable developer infrastructure. Read about our engineering team and apply today.\nDesign resilient APIs with clear interface contracts across Go and Rust services."
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("link-dense anchor with >= 12 non-anchor words is kept", func(t *testing.T) {
		t.Parallel()
		in := `<p>In this role you will partner closely with product, design, security, and platform teams to publish <a href="/spec">a remarkably long specification document covering every distributed consensus invariant and operational runbook across global regions</a> for engineers.</p>`
		got := MainText(in, 1<<16)
		if !strings.Contains(got, "remarkably long specification") {
			t.Fatalf("expected paragraph with >= 12 non-anchor words to be kept, got %q", got)
		}
	})

	t.Run("standalone boilerplate lines dropped without touching real JD lines", func(t *testing.T) {
		t.Parallel()
		in := `<main>
  <p>Skip to main content</p>
  <p>Accept all cookies!</p>
  <p>Manage cookie preferences</p>
  <p>You will manage cookie-free session tokens and build resilient microservices across our distributed backend infrastructure.</p>
</main>`
		got := MainText(in, 1<<16)
		want := "You will manage cookie-free session tokens and build resilient microservices across our distributed backend infrastructure."
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestMainTextSafetyFallbacks(t *testing.T) {
	t.Parallel()

	t.Run("pass 2 >65% loss guard falls back to pass 1", func(t *testing.T) {
		t.Parallel()
		outside := strings.Repeat("Detailed job requirements and engineering responsibilities live outside main. ", 5)
		inside := strings.Repeat("Role summary details here. ", 5) // 135 bytes >= 120, but < 35% of pass1 (~510 bytes)
		in := "<section><p>" + outside + "</p></section><main><p>" + inside + "</p></main>"
		got := MainText(in, 1<<16)
		if !strings.Contains(got, "Detailed job requirements") || !strings.Contains(got, "Role summary details") {
			t.Fatalf("expected Pass 1 fallback when Pass 2 drops >65%%, got %q", got)
		}
	})

	t.Run("form wrapper >85% loss guard falls back to raw text", func(t *testing.T) {
		t.Parallel()
		jd := "Senior Backend Engineer\nBuild high-throughput distributed pipelines in Go, manage PostgreSQL schemas, and maintain sub-10ms API latencies across multi-region clusters."
		in := `<form id="aspnetForm" action="/apply"><h1>Senior Backend Engineer</h1><p>Build high-throughput distributed pipelines in Go, manage PostgreSQL schemas, and maintain sub-10ms API latencies across multi-region clusters.</p></form>`
		got := MainText(in, 1<<16)
		if got != jd {
			t.Fatalf("expected raw Text fallback for <form>-wrapped JD:\ngot:  %q\nwant: %q", got, jd)
		}
	})

	t.Run("tiny chrome-only page below 120 raw bytes stays empty", func(t *testing.T) {
		t.Parallel()
		in := `<nav><a href="/">Home</a> | <a href="/jobs">Jobs</a> | <a href="/login">Sign In</a></nav>`
		if got := MainText(in, 1<<16); got != "" {
			t.Fatalf("expected empty string for tiny chrome-only page, got %q", got)
		}
	})
}

func TestMainTextCapsAndUTF8Boundaries(t *testing.T) {
	t.Parallel()

	if MainText("<p>abc</p>", 0) != "" || MainText("<p>abc</p>", -5) != "" {
		t.Fatal("non-positive max must return empty string")
	}

	got := MainText("<p>"+strings.Repeat("é", 1000)+"</p>", 101)
	if !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8 at boundary: %q", got)
	}
	if len(got) > 101 {
		t.Fatalf("len %d exceeds 101", len(got))
	}

	// Large page with >500KB of <nav>/<script>/<svg> prefix must still reach <main> even when max is small.
	prefix := strings.Repeat(`<nav><a href="/x">Navigation item link</a></nav><script>var x=1;</script><svg><path d="M0 0"/></svg>`, 6000)
	mainBody := "<main><h1>Target Role</h1><p>Found past large nav prefix with plenty of description text to satisfy minimum threshold.</p></main>"
	gotLarge := MainText(prefix+mainBody, 512)
	if !strings.Contains(gotLarge, "Target Role") || !strings.Contains(gotLarge, "Found past large nav prefix") {
		t.Fatalf("failed to reach <main> past large prefix: %q", gotLarge)
	}
}

func TestMainTextHostileAndLinearTime(t *testing.T) {
	t.Parallel()

	hostileNoMarkup := map[string]string{
		"2M open angle brackets":      strings.Repeat("<a ", 2_000_000),
		"500k unclosed scripts":       strings.Repeat("<script>", 500_000),
		"500k comment openers":        strings.Repeat("<!--", 500_000),
		"unclosed tag after text":     "hello world <div class=\"unclosed",
		"trailing lt after text":      "hello world <",
		"unclosed comment after text": "hello world <!-- unclosed",
		"unclosed quote in tag":       "<p>keep</p><div title=\"broken > <script>bad()</script>",
		"1M deep nesting div":         strings.Repeat("<div>", 1_000_000) + "end" + strings.Repeat("</div>", 1_000_000),
		"500k unmatched close nav":    strings.Repeat("<div>", 500_000) + strings.Repeat("</nav>", 500_000),
		"1M repeated unclosed attr q": strings.Repeat(`<a x="`, 1_000_000),
		"huge attr 10MB":              `<a href="` + strings.Repeat("x", 10_000_000) + `">t</a>`,
		"nul and invalid utf8 mass":   strings.Repeat("\x00\xff", 2_000_000),
		"3M whitespace":               strings.Repeat(" \n\t", 3_000_000),
		"3M inline bold tags":         strings.Repeat("<b>", 3_000_000) + "tail",
		"control chars inside entity": "<p>ok&#x00;&#x07;&#x1b;stillok</p>",
	}

	for name, in := range hostileNoMarkup {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			got := MainText(in, 1<<16)
			if d := time.Since(start); d > 5*time.Second {
				t.Fatalf("took %s: not linear time", d)
			}
			if len(got) > 1<<16 {
				t.Fatalf("len %d exceeds cap", len(got))
			}
			if !utf8.ValidString(got) {
				t.Fatalf("invalid UTF-8 output")
			}
			if strings.ContainsAny(got, "<>") {
				t.Fatalf("markup leaked on hostile input: %q", got[:min(len(got), 100)])
			}
			for _, r := range got {
				if r != '\n' && unicode.IsControl(r) {
					t.Fatalf("control rune %U leaked in %q", r, got)
				}
			}
		})
	}

	// Verify 2M literal "< ", 2M "<", and entity soup stay bounded & linear too.
	for name, in := range map[string]string{
		"2M literal lt":      strings.Repeat("< ", 2_000_000),
		"2M raw angle lt":    strings.Repeat("<", 2_000_000),
		"500k entity soup":   strings.Repeat("&amp;&lt;&#x41;&bogus;", 500_000),
		"4M plain words":     strings.Repeat("word ", 4_000_000),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			got := MainText(in, 1<<16)
			if d := time.Since(start); d > 5*time.Second {
				t.Fatalf("took %s: not linear time", d)
			}
			if len(got) > 1<<16 {
				t.Fatalf("len %d exceeds cap", len(got))
			}
			if !utf8.ValidString(got) {
				t.Fatalf("invalid UTF-8 output")
			}
		})
	}

	// Deep nesting within 4MiB scanCap preserves inner text.
	deepWithinCap := strings.Repeat("<div>", 200_000) + "end" + strings.Repeat("</div>", 200_000)
	if got := MainText(deepWithinCap, 1<<16); got != "end" {
		t.Fatalf("deep nesting within cap: got %q, want %q", got, "end")
	}
}
