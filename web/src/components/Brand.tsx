export function BrandIcon({ className = '' }: { className?: string }) {
  return <img className={`brand-icon ${className}`} src="/brand/icon-64.png" srcSet="/brand/icon-64.png 1x, /brand/icon-128.png 2x" alt="" aria-hidden="true" width={28} height={28}/>
}

export function BrandLogo() {
  return <span className="brand-logo" role="img" aria-label="herdrx">
    <BrandIcon/>
    <span className="brand-wordmark" aria-hidden="true"/>
  </span>
}
