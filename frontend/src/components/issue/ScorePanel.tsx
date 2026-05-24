import { useEffect, useState } from "react";
import { Trash2 } from "lucide-react";
import clsx from "clsx";
import { Button } from "~/components/ui/Button";
import { Input } from "~/components/ui/Input";
import {
  formatScore,
  useDeleteScore,
  useIssueScore,
  useSetScore,
} from "~/hooks/useScoring";
import type { ScoringMethod } from "~/api/types";

interface ScorePanelProps {
  issueID: string;
  onClose?: () => void;
}

// Impact buckets for the RICE form. The Reach-Impact-Confidence-
// Effort framework treats Impact as a discrete signal — values are
// the canonical 5-point ladder from the original Intercom paper.
const impactBuckets: { value: number; label: string }[] = [
  { value: 0.25, label: "Minimal" },
  { value: 0.5, label: "Low" },
  { value: 1, label: "Medium" },
  { value: 2, label: "High" },
  { value: 3, label: "Massive" },
];

// Live local form state for both methods. We seed from the loaded
// score (if any) on mount + whenever the server returns a fresh
// row; subsequent edits stay local until the user hits Save.
interface FormState {
  method: ScoringMethod;
  riceReach: number;
  riceImpact: number;
  riceConfidence: number;
  riceEffort: number;
  iceImpact: number;
  iceConfidence: number;
  iceEase: number;
  notes: string;
}

const defaultState: FormState = {
  method: "rice",
  riceReach: 100,
  riceImpact: 1,
  riceConfidence: 80,
  riceEffort: 1,
  iceImpact: 5,
  iceConfidence: 5,
  iceEase: 5,
  notes: "",
};

export function ScorePanel({ issueID, onClose }: ScorePanelProps) {
  const existing = useIssueScore(issueID);
  const setScore = useSetScore(issueID);
  const deleteScore = useDeleteScore(issueID);
  const [form, setForm] = useState<FormState>(defaultState);

  // Re-seed local state whenever the server returns a fresh score.
  // The "or default" branch handles the "no score yet" case (the
  // query 404s; existing.data is undefined).
  useEffect(() => {
    const d = existing.data;
    if (!d) return;
    setForm({
      method: d.method,
      riceReach: d.rice?.reach ?? defaultState.riceReach,
      riceImpact: d.rice?.impact ?? defaultState.riceImpact,
      riceConfidence: d.rice?.confidence ?? defaultState.riceConfidence,
      riceEffort: d.rice?.effort ?? defaultState.riceEffort,
      iceImpact: d.ice?.impact ?? defaultState.iceImpact,
      iceConfidence: d.ice?.confidence ?? defaultState.iceConfidence,
      iceEase: d.ice?.ease ?? defaultState.iceEase,
      notes: d.notes,
    });
  }, [existing.data]);

  // Live preview of the score — the backend rounds the same way, so
  // this stays consistent after save.
  const liveScore =
    form.method === "rice"
      ? Math.round(
          ((form.riceReach * form.riceImpact * (form.riceConfidence / 100)) /
            Math.max(form.riceEffort, 0.001)) *
            10,
        ) / 10
      : Math.round(form.iceImpact * form.iceConfidence * form.iceEase);

  const save = () => {
    if (form.method === "rice") {
      setScore.mutate(
        {
          method: "rice",
          rice: {
            reach: form.riceReach,
            impact: form.riceImpact,
            confidence: form.riceConfidence,
            effort: form.riceEffort,
          },
          notes: form.notes,
        },
        { onSuccess: () => onClose?.() },
      );
    } else {
      setScore.mutate(
        {
          method: "ice",
          ice: {
            impact: form.iceImpact,
            confidence: form.iceConfidence,
            ease: form.iceEase,
          },
          notes: form.notes,
        },
        { onSuccess: () => onClose?.() },
      );
    }
  };

  return (
    <div className="space-y-3 rounded-md border border-border bg-bg/40 p-3">
      <Tabs
        method={form.method}
        onChange={(m) => setForm((s) => ({ ...s, method: m }))}
      />

      {form.method === "rice" ? (
        <RICEForm form={form} setForm={setForm} />
      ) : (
        <ICEForm form={form} setForm={setForm} />
      )}

      <div className="rounded-md bg-bg p-3 text-center">
        <div className="text-[10px] uppercase tracking-wider text-muted">
          {form.method.toUpperCase()} score
        </div>
        <div className="font-mono text-2xl text-accent">
          {formatScore(liveScore, form.method)}
        </div>
      </div>

      <Input
        placeholder="Notes (optional)"
        value={form.notes}
        onChange={(e) => setForm((s) => ({ ...s, notes: e.target.value }))}
      />

      <div className="flex items-center justify-between">
        {existing.data ? (
          <button
            onClick={() => deleteScore.mutate()}
            className="flex items-center gap-1 text-xs text-priority-urgent hover:underline"
          >
            <Trash2 size={12} /> Clear score
          </button>
        ) : (
          <span />
        )}
        <div className="flex gap-2">
          {onClose ? (
            <Button variant="ghost" size="sm" onClick={onClose}>
              Cancel
            </Button>
          ) : null}
          <Button size="sm" onClick={save} disabled={setScore.isPending}>
            {setScore.isPending ? "Saving…" : "Save score"}
          </Button>
        </div>
      </div>
    </div>
  );
}

function Tabs({
  method,
  onChange,
}: {
  method: ScoringMethod;
  onChange: (m: ScoringMethod) => void;
}) {
  return (
    <div className="inline-flex rounded-md border border-border bg-bg p-0.5">
      {(["rice", "ice"] as ScoringMethod[]).map((m) => (
        <button
          key={m}
          onClick={() => onChange(m)}
          className={clsx(
            "h-7 rounded px-3 text-xs",
            method === m ? "bg-surface text-text" : "text-muted hover:text-text",
          )}
        >
          {m.toUpperCase()}
        </button>
      ))}
    </div>
  );
}

function RICEForm({
  form,
  setForm,
}: {
  form: FormState;
  setForm: React.Dispatch<React.SetStateAction<FormState>>;
}) {
  return (
    <div className="space-y-3">
      <Field label="Reach" sub="users per quarter">
        <input
          type="number"
          min={0}
          value={form.riceReach}
          onChange={(e) =>
            setForm((s) => ({ ...s, riceReach: parseFloat(e.target.value) || 0 }))
          }
          className="h-9 w-full rounded-md border border-border bg-bg px-2 text-sm focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent"
        />
      </Field>
      <Field label="Impact" sub="qualitative bucket">
        <div className="flex flex-wrap gap-1">
          {impactBuckets.map((b) => (
            <button
              key={b.value}
              onClick={() => setForm((s) => ({ ...s, riceImpact: b.value }))}
              className={clsx(
                "h-7 rounded border px-2 text-xs",
                form.riceImpact === b.value
                  ? "border-accent bg-accent/10 text-accent"
                  : "border-border text-muted hover:text-text",
              )}
            >
              {b.label} ({b.value}×)
            </button>
          ))}
        </div>
      </Field>
      <Field label="Confidence" sub={`${form.riceConfidence}%`}>
        <ConfidenceSlider
          value={form.riceConfidence}
          onChange={(v) => setForm((s) => ({ ...s, riceConfidence: v }))}
        />
      </Field>
      <Field label="Effort" sub="person-months">
        <input
          type="number"
          step="0.1"
          min={0.1}
          value={form.riceEffort}
          onChange={(e) =>
            setForm((s) => ({ ...s, riceEffort: parseFloat(e.target.value) || 0.1 }))
          }
          className="h-9 w-full rounded-md border border-border bg-bg px-2 text-sm focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent"
        />
      </Field>
    </div>
  );
}

function ICEForm({
  form,
  setForm,
}: {
  form: FormState;
  setForm: React.Dispatch<React.SetStateAction<FormState>>;
}) {
  return (
    <div className="space-y-3">
      <Field label="Impact" sub={`${form.iceImpact}/10`}>
        <Slider
          value={form.iceImpact}
          onChange={(v) => setForm((s) => ({ ...s, iceImpact: v }))}
        />
      </Field>
      <Field label="Confidence" sub={`${form.iceConfidence}/10`}>
        <Slider
          value={form.iceConfidence}
          onChange={(v) => setForm((s) => ({ ...s, iceConfidence: v }))}
        />
      </Field>
      <Field label="Ease" sub={`${form.iceEase}/10`}>
        <Slider
          value={form.iceEase}
          onChange={(v) => setForm((s) => ({ ...s, iceEase: v }))}
        />
      </Field>
    </div>
  );
}

function Field({
  label,
  sub,
  children,
}: {
  label: string;
  sub?: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <div className="mb-1 flex items-center justify-between text-[10px] font-semibold uppercase tracking-wider text-muted">
        <span>{label}</span>
        {sub ? <span className="text-muted">{sub}</span> : null}
      </div>
      {children}
    </div>
  );
}

function Slider({
  value,
  onChange,
}: {
  value: number;
  onChange: (v: number) => void;
}) {
  return (
    <input
      type="range"
      min={1}
      max={10}
      step={0.5}
      value={value}
      onChange={(e) => onChange(parseFloat(e.target.value))}
      className="w-full accent-accent"
    />
  );
}

function ConfidenceSlider({
  value,
  onChange,
}: {
  value: number;
  onChange: (v: number) => void;
}) {
  // Background colour ramps red → green so the user senses
  // confidence visually as they drag. The track itself uses the
  // accent colour; the surround tints with the confidence value.
  const tint =
    value < 30
      ? "border-priority-urgent/40"
      : value < 70
        ? "border-priority-medium/40"
        : "border-status-done/40";
  return (
    <div className={clsx("rounded-md border bg-bg p-1", tint)}>
      <input
        type="range"
        min={0}
        max={100}
        step={5}
        value={value}
        onChange={(e) => onChange(parseFloat(e.target.value))}
        className="w-full accent-accent"
      />
    </div>
  );
}
