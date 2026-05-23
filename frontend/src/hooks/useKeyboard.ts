import { useEffect } from "react";

// Tiny keyboard-shortcut hook. Pass a map of key → handler; handlers
// fire when the key is pressed AND no input/textarea is focused. Hold
// shift/cmd/ctrl as a modifier by including them in the key string
// ("mod+k", "shift+i") — see App.tsx for the cmd+k case which is
// bound directly (because it must beat input focus).
export type KeyMap = Record<string, (e: KeyboardEvent) => void>;

const isTypingTarget = (target: EventTarget | null): boolean => {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target.tagName === "INPUT" ||
    target.tagName === "TEXTAREA" ||
    target.isContentEditable
  );
};

export function useKeyboard(map: KeyMap, deps: unknown[] = []) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (isTypingTarget(e.target)) return;
      const handler = map[e.key.toLowerCase()];
      if (handler) {
        handler(e);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
}
