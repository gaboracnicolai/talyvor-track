import { useUIStore } from "~/stores/ui";
import { IssueList } from "~/components/issue/IssueList";
import { IssueDetail } from "~/components/issue/IssueDetail";
import { IssueCreate } from "~/components/issue/IssueCreate";

interface IssuesPageProps {
  createOpen: boolean;
  setCreateOpen: (open: boolean) => void;
}

export function IssuesPage({ createOpen, setCreateOpen }: IssuesPageProps) {
  const selectedId = useUIStore((s) => s.selectedIssueId);
  const setSelectedId = useUIStore((s) => s.setSelectedIssueId);
  return (
    <div className="h-full">
      <IssueList onOpen={setSelectedId} />
      <IssueDetail issueId={selectedId} onClose={() => setSelectedId(null)} />
      <IssueCreate open={createOpen} onClose={() => setCreateOpen(false)} />
    </div>
  );
}
