import { useState } from "react";
import { Send } from "lucide-react";
import { Button } from "~/components/ui/Button";
import { Input } from "~/components/ui/Input";

interface SubmitFormProps {
  defaultEmail?: string;
  defaultName?: string;
  pending?: boolean;
  onSubmit: (vars: {
    title: string;
    description: string;
    author_name: string;
    author_email: string;
  }) => void;
}

// Public-board "Submit idea" form. Lives in a small card on the
// right side of the page. Required fields: title + email; the
// description is optional markdown that the public list renders as
// pre-wrap text (HTML stripped server-side).
export function SubmitForm({
  defaultEmail = "",
  defaultName = "",
  pending,
  onSubmit,
}: SubmitFormProps) {
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [name, setName] = useState(defaultName);
  const [email, setEmail] = useState(defaultEmail);
  const [error, setError] = useState<string | null>(null);

  const submit = () => {
    setError(null);
    if (!title.trim()) {
      setError("Please enter a title");
      return;
    }
    if (!email.trim()) {
      setError("Email is required");
      return;
    }
    onSubmit({
      title: title.trim(),
      description: description.trim(),
      author_name: name.trim() || "Anonymous",
      author_email: email.trim(),
    });
    // Reset only the post fields; keep name/email for the next idea.
    setTitle("");
    setDescription("");
  };

  return (
    <div className="space-y-2 rounded-md border border-border bg-surface p-3">
      <h2 className="text-sm font-semibold">Submit an idea</h2>
      <Input
        placeholder="What should we build?"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
      />
      <textarea
        placeholder="Tell us more (optional)"
        value={description}
        onChange={(e) => setDescription(e.target.value)}
        className="h-20 w-full resize-none rounded-md border border-border bg-bg px-3 py-2 text-sm placeholder:text-muted focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent"
      />
      <Input
        placeholder="Your name (optional)"
        value={name}
        onChange={(e) => setName(e.target.value)}
      />
      <Input
        type="email"
        placeholder="your@email.com"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
      />
      {error ? <p className="text-xs text-priority-urgent">{error}</p> : null}
      <Button onClick={submit} disabled={pending} className="w-full">
        <Send size={12} /> {pending ? "Posting…" : "Post idea"}
      </Button>
      <p className="text-[10px] text-muted">
        Limit: 3 posts per email per 24h. Be specific so others can vote.
      </p>
    </div>
  );
}
