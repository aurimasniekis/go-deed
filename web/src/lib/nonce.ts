/**
 * The per-request CSP nonce the server injects into <meta name="deed-csp-nonce"> (internal/webui). Mantine uses it for
 * the <style> elements it creates; the CSP allows `style-src-elem 'self' 'nonce-…'`.
 */
export function cspNonce(): string {
  return document.querySelector<HTMLMetaElement>('meta[name="deed-csp-nonce"]')?.content ?? ''
}
