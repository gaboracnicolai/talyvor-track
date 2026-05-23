import { Search, Plus } from "lucide-react";
import { useUIStore } from "~/stores/ui";
import { Button } from "~/components/ui/Button";
import { Kbd } from "~/components/ui/Kbd";

interface HeaderProps {
  title: string;
  onCreate?: () => void;
}

export function Header({ title, onCreate }: HeaderProps) {
  const openPalette = useUIStore((s) => s.setCommandPaletteOpen);
  return (
    <header className="flex h-12 shrink-0 items-center justify-between border-b border-border bg-surface px-4">
      <h1 className="text-sm font-semibold">{title}</h1>
      <div className="flex items-center gap-2">
        <button
          onClick={() => openPalette(true)}
          className="flex h-8 items-center gap-2 rounded-md border border-border bg-bg px-3 text-xs text-muted hover:bg-border/40"
        >
          <Search size={12} />
          <span>Search…</span>
          <Kbd>⌘K</Kbd>
        </button>
        {onCreate ? (
          <Button size="sm" onClick={onCreate}>
            <Plus size={14} /> New
          </Button>
        ) : null}
      </div>
    </header>
  );
}
