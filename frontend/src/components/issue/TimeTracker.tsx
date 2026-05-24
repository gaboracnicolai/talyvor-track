import { useState } from "react";
import { Play, Square, Clock, Plus } from "lucide-react";
import { Button } from "~/components/ui/Button";
import { Input } from "~/components/ui/Input";
import { TimeEntry } from "./TimeEntry";
import {
  formatDuration,
  useDeleteTimeEntry,
  useIssueTimeEntries,
  useLogTime,
  useStartTimer,
  useStopTimer,
  useTimer,
} from "~/hooks/useTimeTracking";

interface TimeTrackerProps {
  issueID: string;
}

// Self-contained time-tracking panel for IssueDetail. Combines the
// running-timer state, the start/stop controls, the per-issue
// entries list, and the "log time manually" form. Renders nothing
// when the workspace member ID hasn't been set (no auth yet) — the
// settings page is the user's escape hatch to set it.
export function TimeTracker({ issueID }: TimeTrackerProps) {
  const timer = useTimer();
  const start = useStartTimer();
  const stop = useStopTimer();
  const entries = useIssueTimeEntries(issueID);
  const remove = useDeleteTimeEntry(issueID);
  const [adding, setAdding] = useState(false);

  const runningForThisIssue =
    timer.data?.running && timer.data.issue_id === issueID;
  const runningElsewhere =
    timer.data?.running && timer.data.issue_id !== issueID;

  const total = entries.data?.summary.total_sec ?? 0;
  const billable = entries.data?.summary.billable_sec ?? 0;

  return (
    <div className="border-t border-border pt-4">
      <div className="mb-2 flex items-center justify-between">
        <div className="flex items-center gap-2 text-[10px] font-semibold uppercase tracking-wider text-muted">
          <Clock size={12} />
          Time tracked
          {total > 0 ? (
            <span className="text-text">{formatDuration(total)}</span>
          ) : null}
          {billable > 0 && billable !== total ? (
            <span>· {formatDuration(billable)} billable</span>
          ) : null}
        </div>
        <button
          onClick={() => setAdding((v) => !v)}
          className="flex items-center gap-1 text-xs text-muted hover:text-text"
        >
          <Plus size={12} /> Log manually
        </button>
      </div>

      <TimerControl
        runningHere={!!runningForThisIssue}
        runningElsewhere={!!runningElsewhere}
        elapsed={runningForThisIssue ? timer.elapsed : 0}
        onStart={(description) => start.mutate({ issueID, description })}
        onStop={() => stop.mutate()}
      />

      {adding ? (
        <ManualLogForm
          issueID={issueID}
          onDone={() => setAdding(false)}
        />
      ) : null}

      {entries.data && entries.data.entries.length > 0 ? (
        <div className="mt-3 space-y-1">
          {entries.data.entries.map((e) => (
            <TimeEntry
              key={e.id}
              entry={e}
              onDelete={() => remove.mutate(e.id)}
            />
          ))}
        </div>
      ) : (
        <div className="mt-3 text-xs text-muted">No entries yet.</div>
      )}
    </div>
  );
}

function TimerControl({
  runningHere,
  runningElsewhere,
  elapsed,
  onStart,
  onStop,
}: {
  runningHere: boolean;
  runningElsewhere: boolean;
  elapsed: number;
  onStart: (description: string) => void;
  onStop: () => void;
}) {
  const [description, setDescription] = useState("");
  if (runningHere) {
    return (
      <div className="flex items-center gap-2 rounded-md border border-accent/40 bg-accent/5 p-2">
        <span className="font-mono text-sm text-accent">
          {formatDuration(elapsed)}
        </span>
        <span className="flex-1 text-xs text-muted">Timer running…</span>
        <Button size="sm" variant="danger" onClick={onStop}>
          <Square size={12} /> Stop
        </Button>
      </div>
    );
  }
  return (
    <div className="flex items-center gap-2">
      <Input
        placeholder="What are you working on?"
        value={description}
        onChange={(e) => setDescription(e.target.value)}
      />
      <Button
        size="sm"
        onClick={() => onStart(description)}
        disabled={runningElsewhere}
        title={runningElsewhere ? "Stop the other running timer first" : undefined}
      >
        <Play size={12} /> Start
      </Button>
    </div>
  );
}

function ManualLogForm({ issueID, onDone }: { issueID: string; onDone: () => void }) {
  const log = useLogTime(issueID);
  const [description, setDescription] = useState("");
  const [minutes, setMinutes] = useState(30);
  const [billable, setBillable] = useState(true);

  const submit = () => {
    if (minutes <= 0) return;
    const now = new Date();
    const start = new Date(now.getTime() - minutes * 60_000);
    log.mutate(
      {
        description,
        started_at: start.toISOString(),
        stopped_at: now.toISOString(),
        billable,
      },
      {
        onSuccess: () => {
          setDescription("");
          setMinutes(30);
          onDone();
        },
      },
    );
  };

  return (
    <div className="mt-3 space-y-2 rounded-md border border-border bg-bg/40 p-3">
      <Input
        placeholder="Description"
        value={description}
        onChange={(e) => setDescription(e.target.value)}
      />
      <div className="flex items-center gap-3 text-xs">
        <label className="flex items-center gap-1 text-muted">
          Minutes
          <input
            type="number"
            min={1}
            value={minutes}
            onChange={(e) => setMinutes(parseInt(e.target.value, 10) || 0)}
            className="w-16 rounded border border-border bg-bg px-2 py-1 text-sm"
          />
        </label>
        <label className="flex items-center gap-1 text-muted">
          <input
            type="checkbox"
            checked={billable}
            onChange={(e) => setBillable(e.target.checked)}
          />
          Billable
        </label>
        <div className="ml-auto flex gap-2">
          <Button size="sm" variant="ghost" onClick={onDone}>
            Cancel
          </Button>
          <Button size="sm" onClick={submit} disabled={log.isPending}>
            {log.isPending ? "Saving…" : "Log time"}
          </Button>
        </div>
      </div>
    </div>
  );
}
