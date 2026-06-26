// download.ts — shared browser-download helpers (de-duplicating the object-URL anchor idiom +
// Content-Disposition filename parse that were previously inlined in topologyStore.exportArtifacts /
// downloadDeployScript and controllerStore.downloadManualNodeBundle).

// parseContentDispositionFilename extracts the download filename from a response's Content-Disposition
// header. The RFC 5987 `filename*=UTF-8''<percent-encoded>` form (group 1) is percent-decoded; the
// plain `filename="..."` form (group 2) is taken VERBATIM — it is NOT percent-encoded, so decoding it
// would corrupt a value with a literal '%' (and `decodeURIComponent` throws on a malformed escape,
// which would reject the whole download). Falls back to `fallback` when the header is absent/unmatched.
export function parseContentDispositionFilename(res: Response, fallback: string): string {
  const disposition = res.headers.get('Content-Disposition') || '';
  const m = disposition.match(/filename\*=UTF-8''([^;]+)|filename="?([^";]+)"?/i);
  if (m?.[1]) {
    try {
      return decodeURIComponent(m[1]);
    } catch {
      return m[1]; // malformed percent-encoding — use the raw value rather than throwing
    }
  }
  return m?.[2] || fallback;
}

// triggerBrowserDownload saves a Blob to the user's downloads via a transient object-URL anchor.
export function triggerBrowserDownload(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}
