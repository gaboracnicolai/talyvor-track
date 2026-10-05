import logoUrl from "~/assets/talyvor-logo-dark-notag.svg";
import markUrl from "~/assets/talyvor-mark-color-dark.svg";

// The brand files are copied from brand-v4/svg, never redrawn: the
// wordmark is drawn, so TALYVOR is never set as live text.

// TrackBrand is the mark and wordmark with the product named beside it
// as an eyebrow — the board's product-UI sidebar header.
export function TrackBrand() {
  return (
    <div className="flex items-center gap-2.5">
      <img src={logoUrl} alt="Talyvor" className="h-5 w-auto" />
      <span className="text-[12px] font-medium uppercase tracking-[0.22em] text-label">Track</span>
    </div>
  );
}

// BrandMark is the mark alone, for a header that already names its page.
export function BrandMark({ className = "h-7 w-7" }: { className?: string }) {
  return <img src={markUrl} alt="Talyvor" className={className} />;
}
