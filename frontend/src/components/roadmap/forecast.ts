import type { RoadmapProject } from "~/api/types";

// ForecastView is what a roadmap row says about when a project will
// finish: a short line for the row, a longer explanation for its
// tooltip, and whether the safe date runs past the project's target.
export interface ForecastView {
  text: string;
  detail: string;
  late: boolean;
  likely?: Date;
  safe?: Date;
}

// describeForecast turns the server's forecast into row copy. It returns
// null when there is nothing worth saying (no forecast, or no issues).
export function describeForecast(project: RoadmapProject): ForecastView | null {
  const f = project.forecast;
  if (!f || project.issue_count === 0) return null;
  const pace = `${f.finished_in_history} finished in the last ${plural(f.history_weeks, "week")}`;

  switch (f.status) {
    case "nothing_open":
      return { text: "All work finished", detail: "No open issues left in this project.", late: false };
    case "no_history":
      return {
        text: "No forecast yet",
        detail: `Nothing in this project was finished in the last ${plural(f.history_weeks, "week")}, so there is no pace to project its ${plural(f.remaining, "open issue")} from.`,
        late: false,
      };
    case "too_far":
      return {
        text: "Over 5 years at this pace",
        detail: `${plural(f.remaining, "open issue")} at this project's pace (${pace}).`,
        late: !!project.target_date,
      };
    case "forecast": {
      if (!f.likely) return null;
      const likely = new Date(f.likely);
      const safe = f.safe ? new Date(f.safe) : undefined;
      const target = project.target_date ? new Date(project.target_date) : undefined;
      const late = !!target && (safe ?? likely) > target;
      const safeText = safe ? `85% likely by ${formatDay(safe)}` : "85% date is over 5 years out";
      const targetText = target
        ? late
          ? ` That is after the target of ${formatDay(target, false)}.`
          : ` The target is ${formatDay(target, false)}.`
        : "";
      return {
        text: `Likely done ${formatDay(likely)}`,
        detail: `${plural(f.remaining, "open issue")} at this project's pace (${pace}); ${safeText}.${targetText}`,
        late,
        likely,
        safe,
      };
    }
  }
}

// Forecast dates are whole UTC days, so they are shown in UTC to keep the
// day from shifting in timezones west of Greenwich. A project's target is
// a date the user picked, so it is shown in their own timezone.
function formatDay(d: Date, utc = true): string {
  const opts: Intl.DateTimeFormatOptions = { month: "short", day: "numeric" };
  if (utc) opts.timeZone = "UTC";
  if (d.getFullYear() !== new Date().getFullYear()) opts.year = "numeric";
  return d.toLocaleDateString(undefined, opts);
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}
