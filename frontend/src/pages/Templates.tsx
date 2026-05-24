import { useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { Button } from "~/components/ui/Button";
import { Input } from "~/components/ui/Input";
import {
  useCreateTemplate,
  useDeleteTemplate,
  useTemplates,
  useUpdateTemplate,
} from "~/hooks/useTemplates";
import type { IssueTemplate } from "~/api/types";
import clsx from "clsx";

// Two-pane editor: template list on the left, body editor with
// markdown preview on the right.
export function TemplatesPage() {
  const { data, isLoading } = useTemplates();
  const create = useCreateTemplate();
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const selected = data?.find((t) => t.id === selectedId) ?? null;

  return (
    <div className="flex h-full">
      <aside className="flex w-64 shrink-0 flex-col border-r border-border bg-surface">
        <div className="flex items-center justify-between border-b border-border px-3 py-2">
          <h2 className="text-sm font-semibold">Templates</h2>
          <button
            onClick={() =>
              create.mutate(
                {
                  name: "New template",
                  icon: "📋",
                  body: "## Description\n\n",
                  default_status: "backlog",
                  default_priority: 3,
                  default_labels: [],
                  field_defaults: {},
                },
                {
                  onSuccess: (t) => setSelectedId(t.id),
                },
              )
            }
            className="flex items-center gap-1 text-xs text-muted hover:text-text"
          >
            <Plus size={12} /> New
          </button>
        </div>
        <div className="flex-1 overflow-y-auto p-1">
          {isLoading ? (
            <div className="px-2 py-3 text-xs text-muted">Loading…</div>
          ) : (data ?? []).length === 0 ? (
            <div className="px-2 py-3 text-xs text-muted">
              No templates yet. Click "New" to create one.
            </div>
          ) : (
            (data ?? []).map((t) => (
              <button
                key={t.id}
                onClick={() => setSelectedId(t.id)}
                className={clsx(
                  "flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm",
                  selectedId === t.id
                    ? "bg-bg text-text"
                    : "text-muted hover:bg-bg/60 hover:text-text",
                )}
              >
                <span>{t.icon}</span>
                <span className="truncate">{t.name}</span>
              </button>
            ))
          )}
        </div>
      </aside>
      <main className="flex-1 overflow-y-auto p-4">
        {selected ? (
          <Editor key={selected.id} template={selected} onDeleted={() => setSelectedId(null)} />
        ) : (
          <div className="flex h-full items-center justify-center text-sm text-muted">
            Select a template to edit.
          </div>
        )}
      </main>
    </div>
  );
}

function Editor({
  template,
  onDeleted,
}: {
  template: IssueTemplate;
  onDeleted: () => void;
}) {
  const update = useUpdateTemplate();
  const remove = useDeleteTemplate();
  const [name, setName] = useState(template.name);
  const [icon, setIcon] = useState(template.icon);
  const [description, setDescription] = useState(template.description);
  const [titleFormat, setTitleFormat] = useState(template.title_format);
  const [body, setBody] = useState(template.body);
  const [priority, setPriority] = useState(template.default_priority);
  const [labels, setLabels] = useState((template.default_labels ?? []).join(", "));

  const save = () =>
    update.mutate({
      id: template.id,
      updates: {
        name,
        icon,
        description,
        title_format: titleFormat,
        body,
        default_priority: priority,
        default_labels: labels.split(",").map((l) => l.trim()).filter(Boolean),
      },
    });

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <input
            value={icon}
            onChange={(e) => setIcon(e.target.value)}
            className="h-9 w-12 rounded-md border border-border bg-bg px-2 text-center text-lg focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent"
          />
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="flex items-center gap-2">
          <Button variant="danger" size="sm" onClick={() => {
            remove.mutate(template.id, { onSuccess: onDeleted });
          }}>
            <Trash2 size={12} /> Delete
          </Button>
          <Button size="sm" onClick={save} disabled={update.isPending}>
            {update.isPending ? "Saving…" : "Save"}
          </Button>
        </div>
      </div>

      <Field label="Description">
        <Input value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>

      <Field label="Title prefix">
        <Input
          value={titleFormat}
          placeholder="[Bug] "
          onChange={(e) => setTitleFormat(e.target.value)}
        />
      </Field>

      <Field label="Body (markdown)">
        <div className="grid grid-cols-1 gap-2 md:grid-cols-2">
          <textarea
            value={body}
            onChange={(e) => setBody(e.target.value)}
            className="h-72 w-full resize-none rounded-md border border-border bg-bg px-3 py-2 font-mono text-xs focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent"
          />
          <div className="h-72 overflow-y-auto rounded-md border border-border bg-surface p-3 text-xs">
            <MarkdownPreview source={body} />
          </div>
        </div>
      </Field>

      <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
        <Field label="Default priority">
          <select
            value={priority}
            onChange={(e) => setPriority(parseInt(e.target.value, 10))}
            className="h-9 w-full rounded-md border border-border bg-bg px-2 text-sm"
          >
            <option value={0}>No priority</option>
            <option value={1}>Urgent</option>
            <option value={2}>High</option>
            <option value={3}>Medium</option>
            <option value={4}>Low</option>
          </select>
        </Field>
        <Field label="Default labels (comma-separated)">
          <Input value={labels} onChange={(e) => setLabels(e.target.value)} />
        </Field>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-muted">
        {label}
      </div>
      {children}
    </div>
  );
}

// MarkdownPreview renders a tiny subset of markdown — enough for the
// canonical templates (## headers, lists, checkboxes, code spans).
// Going full CommonMark would balloon the bundle for one preview
// pane; this is intentional minimum-viable.
function MarkdownPreview({ source }: { source: string }) {
  const lines = source.split("\n");
  return (
    <div className="space-y-1 leading-relaxed">
      {lines.map((line, i) => renderLine(line, i))}
    </div>
  );
}

function renderLine(line: string, key: number): React.ReactNode {
  if (line.startsWith("## ")) {
    return (
      <h3 key={key} className="mt-2 text-sm font-semibold text-text">
        {line.slice(3)}
      </h3>
    );
  }
  if (line.startsWith("# ")) {
    return (
      <h2 key={key} className="mt-2 text-base font-bold text-text">
        {line.slice(2)}
      </h2>
    );
  }
  if (/^- \[ \] /.test(line)) {
    return (
      <div key={key} className="flex items-center gap-2">
        <input type="checkbox" disabled />
        <span>{line.slice(6)}</span>
      </div>
    );
  }
  if (line.startsWith("- ")) {
    return (
      <div key={key} className="ml-3 list-disc">
        • {line.slice(2)}
      </div>
    );
  }
  if (/^<!--.*-->$/.test(line.trim())) {
    return (
      <div key={key} className="italic text-muted">
        {line.replace(/<!--\s?|\s?-->/g, "")}
      </div>
    );
  }
  if (line.trim() === "") {
    return <div key={key} className="h-2" />;
  }
  return (
    <div key={key} className="text-text">
      {line}
    </div>
  );
}
