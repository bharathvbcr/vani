// The popup's links to a scraped job. The scraper already returns "" for any link that is not http(s)
// (scraper.ts resolveUrl); the popup checks again, because the list reaches it as a message and a link
// it renders is one click from window.open. A job with no usable link gets plain text and no Open or
// Copy-link button, never an anchor with an empty or foreign-scheme href.

/** True only for an absolute http(s) URL; the parser drops leading whitespace and folds the scheme's case. */
export function isHttpUrl(url: unknown): url is string {
  if (typeof url !== "string" || url === "") return false;
  try {
    const p = new URL(url).protocol;
    return p === "http:" || p === "https:";
  } catch {
    return false;
  }
}

type Doc = Pick<Document, "createElement">;

/** The job's title: an anchor to its link, or a span when it has no usable one. */
export function titleElement(doc: Doc, title: string, url: string): HTMLElement {
  if (!isHttpUrl(url)) {
    const span = doc.createElement("span");
    span.textContent = title;
    return span;
  }
  const a = doc.createElement("a");
  a.href = url;
  a.target = "_blank";
  a.textContent = title;
  return a;
}

/** The "Open role" button, or null when the job has no usable link. `open` is window.open in the popup. */
export function openButton(doc: Doc, url: string, open: (url: string, target: string) => unknown): HTMLButtonElement | null {
  if (!isHttpUrl(url)) return null;
  const b = doc.createElement("button");
  b.className = "sm primary";
  b.textContent = "Open role";
  b.onclick = () => { open(url, "_blank"); };
  return b;
}
