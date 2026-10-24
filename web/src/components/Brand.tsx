/** The Deed mark (web/public/logo.svg), inline so it needs no extra request and follows the theme's focus styles. */
export function BrandMark({ size = 24 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 64 64" aria-hidden focusable="false">
      <g fill="none" stroke="#4263eb" strokeWidth="5.5" strokeLinecap="round" strokeLinejoin="round">
        <path d="M17 12v40" />
        <path d="M17 12h12a20 20 0 0 1 0 40H17" />
        <path d="M17 32h17" strokeDasharray="0.1 8" />
      </g>
      <g fill="#4263eb">
        <circle cx="17" cy="12" r="6" />
        <circle cx="17" cy="52" r="6" />
        <circle cx="49" cy="32" r="6" />
      </g>
      <circle cx="17" cy="32" r="4.5" fill="#12b886" />
    </svg>
  )
}
