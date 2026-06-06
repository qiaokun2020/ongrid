import { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { CalendarClock, FileText, Plus, RefreshCw, Settings } from 'lucide-react';
import { cn } from '@/lib/cn';
import { relativeTime } from '@/lib/format';
import { usePoll } from '@/lib/usePoll';
import { usePermissions } from '@/store/me';
import { useI18n } from '@/i18n/locale';
import { generateNow, listReports, type ReportListItem, type ReportStatus } from '@/api/reports';

const POLL_MS = 20_000;

const STATUS_STYLE: Record<ReportStatus, string> = {
  ready: 'bg-emerald-500/15 text-emerald-300 border-emerald-500/30',
  generating: 'bg-indigo-500/15 text-indigo-300 border-indigo-500/30',
  pending: 'bg-zinc-700/40 text-zinc-300 border-zinc-600/40',
  failed: 'bg-red-500/15 text-red-300 border-red-500/30',
};

export default function ReportsPage() {
  const { tr } = useI18n();
  const { canMutate } = usePermissions();
  const navigate = useNavigate();
  const [items, setItems] = useState<ReportListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [generating, setGenerating] = useState(false);

  const load = useCallback(async () => {
    try {
      const res = await listReports({ limit: 50 });
      setItems(res.reports ?? []);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);
  usePoll(load, POLL_MS);

  const onGenerate = useCallback(async () => {
    setGenerating(true);
    try {
      const rpt = await generateNow({ kind: 'weekly' });
      await load();
      navigate(`/reports/${rpt.id}`);
    } finally {
      setGenerating(false);
    }
  }, [load, navigate]);

  return (
    <div className="mx-auto max-w-5xl px-4 py-6">
      <div className="mb-5 flex items-center justify-between">
        <div className="flex items-center gap-2">
          <FileText size={20} className="text-indigo-400" />
          <h1 className="text-lg font-semibold text-zinc-100">{tr('报告', 'Reports')}</h1>
        </div>
        <div className="flex items-center gap-2">
          <Link
            to="/reports/schedules"
            className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 px-3 py-1.5 text-xs text-zinc-200 hover:border-zinc-500 hover:bg-zinc-800"
          >
            <Settings size={13} /> {tr('排程', 'Schedules')}
          </Link>
          {canMutate && (
            <button
              type="button"
              onClick={() => void onGenerate()}
              disabled={generating}
              className="inline-flex items-center gap-1.5 rounded-md border border-indigo-600 bg-indigo-600/20 px-3 py-1.5 text-xs text-indigo-200 hover:bg-indigo-600/30 disabled:opacity-50"
            >
              <Plus size={13} /> {tr('立即生成', 'Generate now')}
            </button>
          )}
        </div>
      </div>

      {loading ? (
        <div className="py-16 text-center text-sm text-zinc-500">
          <RefreshCw size={18} className="mx-auto mb-2 animate-spin" />
          {tr('加载中…', 'Loading…')}
        </div>
      ) : items.length === 0 ? (
        <div className="rounded-lg border border-dashed border-zinc-800 py-16 text-center">
          <CalendarClock size={28} className="mx-auto mb-3 text-zinc-600" />
          <p className="text-sm text-zinc-400">{tr('还没有报告', 'No reports yet')}</p>
          <p className="mt-1 text-xs text-zinc-600">
            {tr('排一个日报/周报，或点「立即生成」。', 'Schedule a daily/weekly report, or click Generate now.')}
          </p>
        </div>
      ) : (
        <div className="space-y-2">
          {items.map((r) => (
            <Link
              key={r.id}
              to={`/reports/${r.id}`}
              className="block rounded-lg border border-zinc-800 bg-zinc-900/40 p-3.5 hover:border-zinc-700"
            >
              <div className="flex items-center justify-between gap-3">
                <span className="truncate font-medium text-zinc-100">{r.title}</span>
                <span
                  className={cn(
                    'shrink-0 rounded border px-1.5 py-0.5 text-[11px] font-medium',
                    STATUS_STYLE[r.status],
                  )}
                >
                  {r.status}
                </span>
              </div>
              {r.summary && <p className="mt-1 truncate text-sm text-zinc-400">{r.summary}</p>}
              <div className="mt-1.5 text-xs text-zinc-600">
                {r.generated_at
                  ? tr(`生成于 ${relativeTime(r.generated_at)}`, `Generated ${relativeTime(r.generated_at)}`)
                  : tr(`创建于 ${relativeTime(r.created_at)}`, `Created ${relativeTime(r.created_at)}`)}
              </div>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
