import { Fragment, useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';
import type {
  ActionsSummary,
  HeroStat,
  KeyIncident,
  Paragraph,
  ReportContent as ReportContentT,
} from '@/api/reports';

// ReportContent renders a ContentJSON report body — the rich in-app
// view (HLD-014 §前端渲染). Zero chart deps: count-up via rAF, sparkline
// as inline SVG, entity chips via token parsing.

// --- count-up hook (rAF, no deps) ---
function useCountUp(target: number, durationMs = 800): number {
  const [val, setVal] = useState(0);
  const startRef = useRef<number | null>(null);
  useEffect(() => {
    startRef.current = null;
    let raf = 0;
    const step = (ts: number) => {
      if (startRef.current === null) startRef.current = ts;
      const p = Math.min(1, (ts - startRef.current) / durationMs);
      // easeOutCubic
      const eased = 1 - Math.pow(1 - p, 3);
      setVal(target * eased);
      if (p < 1) raf = requestAnimationFrame(step);
      else setVal(target);
    };
    raf = requestAnimationFrame(step);
    return () => cancelAnimationFrame(raf);
  }, [target, durationMs]);
  return val;
}

function fmtNum(v: number): string {
  return Number.isInteger(v) ? String(v) : v.toFixed(1);
}

// --- inline SVG sparkline ---
function Sparkline({ points, className }: { points: number[]; className?: string }) {
  if (!points || points.length < 2) return null;
  const w = 60;
  const h = 16;
  const max = Math.max(...points, 1);
  const min = Math.min(...points, 0);
  const span = max - min || 1;
  const step = w / (points.length - 1);
  const d = points
    .map((p, i) => `${i === 0 ? 'M' : 'L'}${(i * step).toFixed(1)},${(h - ((p - min) / span) * h).toFixed(1)}`)
    .join(' ');
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} className={className} aria-hidden="true">
      <path d={d} fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function HeroCard({ stat }: { stat: HeroStat }) {
  const animated = useCountUp(stat.value);
  const delta = stat.delta_pct;
  // Lower-is-better metrics (incidents/mttr) get green on a drop; we keep
  // it simple — down = green, up = red. Neutral when ~0.
  const deltaColor =
    delta === undefined ? '' : delta < 0 ? 'text-emerald-400' : delta > 0 ? 'text-red-400' : 'text-zinc-500';
  const arrow = delta === undefined ? '' : delta < 0 ? '↓' : delta > 0 ? '↑' : '→';
  return (
    <div className="rounded-lg border border-zinc-800 bg-zinc-900/50 p-3">
      <div className="flex items-baseline gap-1">
        <span className="text-2xl font-semibold tabular-nums text-zinc-100">{fmtNum(animated)}</span>
        {stat.unit && <span className="text-xs text-zinc-500">{stat.unit}</span>}
      </div>
      <div className="mt-0.5 text-[11px] uppercase tracking-wide text-zinc-500">{stat.label}</div>
      <div className="mt-1.5 flex items-center justify-between">
        {stat.sparkline && stat.sparkline.length >= 2 ? (
          <Sparkline points={stat.sparkline} className="text-indigo-400" />
        ) : (
          <span />
        )}
        {delta !== undefined && (
          <span className={cn('text-[11px] font-medium tabular-nums', deltaColor)}>
            {arrow}
            {Math.abs(delta).toFixed(0)}%
          </span>
        )}
      </div>
    </div>
  );
}

// EntityText parses {{entity:kind:id|name}} tokens into clickable chips.
const ENTITY_RE = /\{\{entity:([a-z]+):(\d+)\|([^}]*)\}\}/g;

function entityHref(kind: string, id: string): string | null {
  switch (kind) {
    case 'edge':
      return `/devices/${id}`;
    case 'incident':
      return `/alerts/incidents/${id}`;
    default:
      return null;
  }
}

function EntityText({ text }: { text: string }) {
  const parts: React.ReactNode[] = [];
  let last = 0;
  let m: RegExpExecArray | null;
  ENTITY_RE.lastIndex = 0;
  let i = 0;
  while ((m = ENTITY_RE.exec(text)) !== null) {
    if (m.index > last) parts.push(<Fragment key={`t${i}`}>{text.slice(last, m.index)}</Fragment>);
    const [, kind, id, name] = m;
    const href = entityHref(kind, id);
    parts.push(
      href ? (
        <Link
          key={`e${i}`}
          to={href}
          className="mx-0.5 inline-flex items-center rounded border border-indigo-500/40 bg-indigo-500/10 px-1 py-0.5 text-[12px] text-indigo-300 hover:bg-indigo-500/20"
        >
          {name}
        </Link>
      ) : (
        <span key={`e${i}`} className="mx-0.5 rounded bg-zinc-800 px-1 text-[12px] text-zinc-300">
          {name}
        </span>
      ),
    );
    last = m.index + m[0].length;
    i++;
  }
  if (last < text.length) parts.push(<Fragment key="tail">{text.slice(last)}</Fragment>);
  return <>{parts}</>;
}

const SEV_DOT: Record<string, string> = {
  critical: 'bg-red-500',
  warning: 'bg-amber-500',
  info: 'bg-sky-500',
};

function IncidentRow({ ki }: { ki: KeyIncident }) {
  return (
    <Link
      to={`/alerts/incidents/${ki.id}`}
      className="flex items-center gap-2 rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-2 text-sm hover:border-zinc-700"
    >
      <span className={cn('h-2 w-2 shrink-0 rounded-full', SEV_DOT[ki.severity] ?? 'bg-zinc-600')} />
      <span className="text-zinc-400">I-{ki.id}</span>
      <span className="flex-1 truncate text-zinc-200">{ki.title}</span>
      {ki.root_cause_snippet && (
        <span className="hidden truncate text-xs text-zinc-500 md:inline">{ki.root_cause_snippet}</span>
      )}
      <span className="shrink-0 text-xs text-zinc-500">
        {ki.duration_min}m · {ki.status}
      </span>
    </Link>
  );
}

function ActionsPanel({ a }: { a: ActionsSummary }) {
  const { tr } = useI18n();
  return (
    <div className="rounded-lg border border-zinc-800 bg-zinc-900/40 p-3 text-sm text-zinc-300">
      <div>
        {tr('变更动作', 'Mutating')}: <span className="font-medium text-zinc-100">{a.mutating_total}</span>
        {a.mutating_total > 0 && (
          <span className="text-zinc-500">
            {' '}
            ({tr('已批准', 'approved')} {a.mutating_approved})
          </span>
        )}
        {' · '}
        {tr('只读诊断', 'Read-only')}: <span className="font-medium text-zinc-100">{a.safe_total}</span>
      </div>
      {a.by_tool && a.by_tool.length > 0 && (
        <div className="mt-1.5 flex flex-wrap gap-1.5">
          {a.by_tool.map((t) => (
            <span key={t.tool} className="rounded bg-zinc-800 px-1.5 py-0.5 text-xs text-zinc-400">
              {t.tool} ×{t.count}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

export function ReportContentView({ content }: { content: ReportContentT }) {
  const { tr } = useI18n();
  const paras: Paragraph[] = content.narrative?.paragraphs ?? [];
  return (
    <div className="space-y-6">
      {content.hero && content.hero.length > 0 && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          {content.hero.map((h) => (
            <HeroCard key={h.key} stat={h} />
          ))}
        </div>
      )}

      {content.narrative?.headline && (
        <section>
          <h2 className="mb-2 text-base font-semibold text-zinc-100">📝 {content.narrative.headline}</h2>
          <div className="space-y-2 text-sm leading-relaxed text-zinc-300">
            {paras.map((p, i) => (
              <p key={i}>
                <EntityText text={p.text} />
              </p>
            ))}
          </div>
        </section>
      )}

      {content.key_incidents && content.key_incidents.length > 0 && (
        <section>
          <h3 className="mb-2 text-sm font-medium text-zinc-400">🚨 {tr('关键 incidents', 'Key incidents')}</h3>
          <div className="space-y-1.5">
            {content.key_incidents.map((ki) => (
              <IncidentRow key={ki.id} ki={ki} />
            ))}
          </div>
        </section>
      )}

      {content.actions_summary && (
        <section>
          <h3 className="mb-2 text-sm font-medium text-zinc-400">⚡ {tr('Agent 执行动作', 'Agent actions')}</h3>
          <ActionsPanel a={content.actions_summary} />
        </section>
      )}

      {content.advice && content.advice.length > 0 && (
        <section>
          <h3 className="mb-2 text-sm font-medium text-zinc-400">🎯 {tr('下一步建议', 'Recommendations')}</h3>
          <ul className="space-y-1.5 text-sm text-zinc-300">
            {content.advice.map((a, i) => (
              <li key={i} className="flex gap-2">
                <span className="text-indigo-400">•</span>
                <span>
                  <EntityText text={a.text} />
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}
