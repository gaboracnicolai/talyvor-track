import { useState } from "react";
import { ExternalLink, Check } from "lucide-react";
import clsx from "clsx";
import type { CustomField } from "~/api/types";
import { Input } from "~/components/ui/Input";
import { Badge } from "~/components/ui/Badge";

interface CustomFieldRowProps {
  field: CustomField;
  value: string | undefined;
  onChange: (value: string) => void;
}

// One row in the IssueDetail "Custom fields" section: label on the
// left, type-specific editor on the right. Editors save on blur for
// free-text inputs and on change for selects/checkboxes — the
// difference matches user expectation (typing should not fire a
// request per keystroke).
export function CustomFieldRow({ field, value, onChange }: CustomFieldRowProps) {
  return (
    <div className="flex items-start gap-3 py-1.5">
      <div className="w-32 shrink-0 text-[10px] font-semibold uppercase tracking-wider text-muted">
        {field.name}
        {field.required ? <span className="ml-1 text-priority-urgent">*</span> : null}
      </div>
      <div className="flex-1">
        <CustomFieldEditor field={field} value={value ?? ""} onChange={onChange} />
      </div>
    </div>
  );
}

interface CustomFieldEditorProps {
  field: CustomField;
  value: string;
  onChange: (value: string) => void;
}

// CustomFieldEditor is the per-type input. Exported so IssueCreate
// can re-use it for the required-fields collection in the new-issue
// dialog. The editor never owns its own onChange; the parent decides
// how to persist (POST during create, PUT for inline edits).
export function CustomFieldEditor({ field, value, onChange }: CustomFieldEditorProps) {
  switch (field.type) {
    case "text":
    case "member":
      return <TextEditor value={value} onCommit={onChange} placeholder={field.type === "member" ? "user id" : ""} />;
    case "number":
      return <TextEditor value={value} onCommit={onChange} inputMode="decimal" />;
    case "url":
      return <URLEditor value={value} onCommit={onChange} />;
    case "date":
      return <DateEditor value={value} onCommit={onChange} />;
    case "checkbox":
      return <CheckboxEditor value={value} onChange={onChange} />;
    case "select":
      return <SelectEditor field={field} value={value} onChange={onChange} />;
    case "multi":
      return <MultiEditor field={field} value={value} onChange={onChange} />;
    default:
      return <span className="text-xs text-muted">Unsupported field type</span>;
  }
}

// ─── per-type editors ──────────────────────────────────────

function TextEditor({
  value,
  onCommit,
  placeholder,
  inputMode,
}: {
  value: string;
  onCommit: (v: string) => void;
  placeholder?: string;
  inputMode?: "text" | "decimal";
}) {
  const [draft, setDraft] = useState(value);
  return (
    <Input
      value={draft}
      placeholder={placeholder}
      inputMode={inputMode}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={() => {
        if (draft !== value) onCommit(draft);
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter") e.currentTarget.blur();
      }}
    />
  );
}

function URLEditor({ value, onCommit }: { value: string; onCommit: (v: string) => void }) {
  const [draft, setDraft] = useState(value);
  return (
    <div className="flex items-center gap-2">
      <Input
        value={draft}
        placeholder="https://…"
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => {
          if (draft !== value) onCommit(draft);
        }}
      />
      {value ? (
        <a
          href={value}
          target="_blank"
          rel="noreferrer noopener"
          className="text-muted hover:text-text"
          title="Open in new tab"
        >
          <ExternalLink size={14} />
        </a>
      ) : null}
    </div>
  );
}

function DateEditor({ value, onCommit }: { value: string; onCommit: (v: string) => void }) {
  // Native <input type="date"> sends YYYY-MM-DD, but the backend expects
  // RFC3339 — convert at the boundary so the store stays strict.
  const dateOnly = value ? value.slice(0, 10) : "";
  return (
    <input
      type="date"
      value={dateOnly}
      onChange={(e) => {
        const v = e.target.value;
        if (!v) {
          onCommit("");
          return;
        }
        onCommit(new Date(v + "T00:00:00Z").toISOString());
      }}
      className="h-9 rounded-md border border-border bg-bg px-3 text-sm focus:outline-none focus:ring-2 focus:ring-accent"
    />
  );
}

function CheckboxEditor({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: string) => void;
}) {
  const checked = value === "true";
  return (
    <button
      type="button"
      onClick={() => onChange(checked ? "false" : "true")}
      className={clsx(
        "flex h-5 w-5 items-center justify-center rounded border",
        checked ? "border-accent bg-accent text-bg" : "border-border bg-bg",
      )}
    >
      {checked ? <Check size={12} strokeWidth={3} /> : null}
    </button>
  );
}

function SelectEditor({
  field,
  value,
  onChange,
}: {
  field: CustomField;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="h-9 w-full rounded-md border border-border bg-bg px-2 text-sm focus:outline-none focus:ring-2 focus:ring-accent"
    >
      <option value="">—</option>
      {field.options.map((o) => (
        <option key={o} value={o}>
          {o}
        </option>
      ))}
    </select>
  );
}

function parseMulti(raw: string): string[] {
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed.filter((p): p is string => typeof p === "string") : [];
  } catch {
    return [];
  }
}

function MultiEditor({
  field,
  value,
  onChange,
}: {
  field: CustomField;
  value: string;
  onChange: (v: string) => void;
}) {
  const selected = parseMulti(value);
  const toggle = (opt: string) => {
    const next = selected.includes(opt)
      ? selected.filter((s) => s !== opt)
      : [...selected, opt];
    onChange(JSON.stringify(next));
  };
  return (
    <div className="flex flex-wrap gap-1">
      {field.options.map((opt) => {
        const active = selected.includes(opt);
        return (
          <button
            key={opt}
            type="button"
            onClick={() => toggle(opt)}
            className={clsx(
              "h-6 rounded-full border px-2 text-xs",
              active
                ? "border-accent bg-accent/10 text-accent"
                : "border-border text-muted hover:text-text",
            )}
          >
            {opt}
          </button>
        );
      })}
    </div>
  );
}

// ─── read-only display (used in compact list rows) ─────────

export function CustomFieldValue({ field, value }: { field: CustomField; value?: string }) {
  if (!value) return <span className="text-muted">—</span>;
  switch (field.type) {
    case "url":
      return (
        <a
          href={value}
          target="_blank"
          rel="noreferrer noopener"
          className="text-accent underline-offset-2 hover:underline"
        >
          {value}
        </a>
      );
    case "checkbox":
      return value === "true" ? <Check size={14} className="text-status-done" /> : <span className="text-muted">—</span>;
    case "date":
      return <span>{value.slice(0, 10)}</span>;
    case "multi": {
      const picks = parseMulti(value);
      return (
        <span className="flex flex-wrap gap-1">
          {picks.map((p) => (
            <Badge key={p}>{p}</Badge>
          ))}
        </span>
      );
    }
    default:
      return <span>{value}</span>;
  }
}
