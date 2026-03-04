import { useState } from "react";

interface TaskCreateDialogProps {
  open: boolean;
  onClose: () => void;
  onCreate: (title: string, description: string) => Promise<void>;
}

export function TaskCreateDialog({
  open,
  onClose,
  onCreate,
}: TaskCreateDialogProps) {
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [priority, setPriority] = useState(0);
  const [submitting, setSubmitting] = useState(false);

  if (!open) return null;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!title.trim() || !description.trim()) return;

    setSubmitting(true);
    try {
      await onCreate(title.trim(), description.trim());
      setTitle("");
      setDescription("");
      setPriority(0);
      onClose();
    } catch (err) {
      console.error("Failed to create task:", err);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-black/60 backdrop-blur-sm"
        onClick={onClose}
      />

      {/* Dialog */}
      <div className="relative z-10 w-full max-w-lg rounded-xl bg-gray-800 p-6 shadow-2xl border border-gray-700">
        <h2 className="text-lg font-semibold text-gray-100 mb-4">
          New Task
        </h2>

        <form onSubmit={handleSubmit} className="space-y-4">
          {/* Title */}
          <div>
            <label
              htmlFor="task-title"
              className="block text-sm font-medium text-gray-300 mb-1"
            >
              Title <span className="text-red-400">*</span>
            </label>
            <input
              id="task-title"
              type="text"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="e.g., Implement user authentication"
              className="w-full rounded-lg bg-gray-900 border border-gray-600 px-3 py-2 text-gray-100 placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent"
              required
              autoFocus
            />
          </div>

          {/* Description */}
          <div>
            <label
              htmlFor="task-description"
              className="block text-sm font-medium text-gray-300 mb-1"
            >
              Description <span className="text-red-400">*</span>
            </label>
            <textarea
              id="task-description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Describe the task in detail..."
              rows={4}
              className="w-full rounded-lg bg-gray-900 border border-gray-600 px-3 py-2 text-gray-100 placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent resize-y"
              required
            />
          </div>

          {/* Priority */}
          <div>
            <label
              htmlFor="task-priority"
              className="block text-sm font-medium text-gray-300 mb-1"
            >
              Priority
            </label>
            <select
              id="task-priority"
              value={priority}
              onChange={(e) => setPriority(Number(e.target.value))}
              className="w-full rounded-lg bg-gray-900 border border-gray-600 px-3 py-2 text-gray-100 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent"
            >
              <option value={0}>0 - Low</option>
              <option value={1}>1 - Normal</option>
              <option value={2}>2 - High</option>
              <option value={3}>3 - Critical</option>
            </select>
          </div>

          {/* Actions */}
          <div className="flex justify-end gap-3 pt-2">
            <button
              type="button"
              onClick={onClose}
              className="px-4 py-2 rounded-lg text-gray-300 hover:text-gray-100 hover:bg-gray-700 transition-colors"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={submitting || !title.trim() || !description.trim()}
              className="px-4 py-2 rounded-lg bg-blue-600 text-white font-medium hover:bg-blue-500 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              {submitting ? "Creating..." : "Create Task"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
