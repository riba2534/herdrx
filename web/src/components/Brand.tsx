export function BrandIcon({ className = '' }: { className?: string }) {
  return <img className={`brand-icon ${className}`} src="/brand/v2/icon-64.png" srcSet="/brand/v2/icon-64.png 1x, /brand/v2/icon-128.png 2x, /brand/v2/icon-192.png 3x" alt="" aria-hidden="true" width={28} height={28}/>
}

export function BrandLogo() {
  return <span className="brand-logo" role="img" aria-label="herdrx">
    <BrandIcon/>
    <span className="brand-wordmark" aria-hidden="true"/>
  </span>
}
