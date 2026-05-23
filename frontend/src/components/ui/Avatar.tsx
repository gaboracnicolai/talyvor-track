import * as RA from "@radix-ui/react-avatar";
import clsx from "clsx";

interface AvatarProps {
  name: string;
  url?: string;
  size?: "sm" | "md";
  className?: string;
}

// Radix Avatar wrapper. Falls back to initials when no image is set.
export function Avatar({ name, url, size = "sm", className }: AvatarProps) {
  const initials = name
    .split(" ")
    .map((p) => p[0])
    .slice(0, 2)
    .join("")
    .toUpperCase();
  return (
    <RA.Root
      className={clsx(
        "inline-flex shrink-0 select-none items-center justify-center overflow-hidden rounded-full bg-surface",
        size === "sm" ? "h-6 w-6 text-[10px]" : "h-8 w-8 text-xs",
        className,
      )}
    >
      {url ? <RA.Image src={url} alt={name} className="h-full w-full object-cover" /> : null}
      <RA.Fallback className="font-medium text-muted">{initials || "?"}</RA.Fallback>
    </RA.Root>
  );
}
