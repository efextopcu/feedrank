import { Fragment, useState } from 'react';
import type { ReactNode } from 'react';
import type { Content } from './api';

const count = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });
const date = new Intl.DateTimeFormat(undefined, { year: 'numeric', month: 'short', day: 'numeric' });

interface Props {
  items: Content[];
  keyword: string;
  offset: number; // rank of the first item, minus one
  busy: boolean;
}

export function ResultsTable({ items, keyword, offset, busy }: Props) {
  const [open, setOpen] = useState<number | null>(null);
  const maxScore = Math.max(...items.map((i) => i.score), 1);
  const showRelevance = keyword !== '';
  const columns = showRelevance ? 8 : 7;

  return (
    <div className="table-wrap">
      <table aria-busy={busy}>
        <thead>
          <tr>
            <th scope="col" className="rank">#</th>
            <th scope="col">Title</th>
            <th scope="col">Type</th>
            <th scope="col" className="wide-only">Engagement</th>
            <th scope="col" className="wide-only">Published</th>
            {showRelevance && <th scope="col" className="num">Relevance</th>}
            <th scope="col" className="num">Score</th>
            <th scope="col" className="toggle-col">
              <span className="visually-hidden">Details</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {items.map((item, n) => {
            const expanded = open === item.id;
            return (
              <Fragment key={item.id}>
                {/* The whole row toggles the breakdown; the chevron button
                    is the keyboard and screen-reader way to do the same. */}
                <tr
                  className={`row${expanded ? ' expanded' : ''}`}
                  onClick={() => setOpen(expanded ? null : item.id)}
                >
                  <td className="rank">{offset + n + 1}</td>
                  <td>
                    <div className="title">{highlight(item.title, keyword)}</div>
                    <div className="meta">
                      <span className="provider">{item.provider}</span>
                      {item.tags.map((tag) => (
                        <span key={tag} className="tag">
                          {highlight(tag, keyword)}
                        </span>
                      ))}
                    </div>
                  </td>
                  <td>
                    <span className={`badge ${item.content_type}`}>{item.content_type}</span>
                  </td>
                  <td className="muted wide-only">{metrics(item)}</td>
                  <td className="muted wide-only nowrap">{date.format(new Date(item.published_at))}</td>
                  {showRelevance && <td className="num muted">{(item.relevance ?? 0).toFixed(3)}</td>}
                  <td className="num">
                    <div className="score">
                      {item.score.toFixed(2)}
                      <span className="track" aria-hidden="true">
                        <span style={{ width: `${(item.score / maxScore) * 100}%` }} />
                      </span>
                    </div>
                  </td>
                  <td className="toggle-col">
                    <button
                      type="button"
                      className="chevron"
                      aria-expanded={expanded}
                      aria-label={`${expanded ? 'Hide' : 'Show'} the score breakdown for ${item.title}`}
                    >
                      <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
                        <path d="m4 6 4 4 4-4" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
                      </svg>
                    </button>
                  </td>
                </tr>
                {expanded && (
                  <tr className="breakdown">
                    <td colSpan={columns}>
                      <Formula item={item} />
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function metrics({ content_type, metrics: m }: Content): string {
  return content_type === 'video'
    ? `${count.format(m.views ?? 0)} views · ${count.format(m.likes ?? 0)} likes`
    : `${m.reading_time ?? 0} min read · ${count.format(m.reactions ?? 0)} reactions`;
}

/** The score written out as the formula that produced it. */
function Formula({ item }: { item: Content }) {
  const b = item.score_breakdown;
  const terms = [
    { label: 'base', value: b.base.toFixed(2) },
    { label: 'type', value: `× ${b.type_coefficient.toFixed(1)}` },
    { label: 'engagement', value: `+ ${b.engagement.toFixed(2)}` },
    { label: 'recency', value: `+ ${b.recency}` },
    { label: 'score', value: `= ${b.final.toFixed(2)}` },
  ];
  return (
    <div className="formula-wrap">
      <p className="formula-caption">How this score is calculated</p>
      <dl className="formula">
        {terms.map((t) => (
          <div key={t.label}>
            <dt>{t.label}</dt>
            <dd>{t.value}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

/** Wraps each occurrence of the keyword's words in <mark>. */
function highlight(text: string, keyword: string): ReactNode {
  const words = keyword
    .split(/\s+/)
    .filter((w) => w.length > 1 && w.toLowerCase() !== 'or' && !w.startsWith('-'))
    .map((w) => w.replace(/[^\p{L}\p{N}]/gu, ''))
    .filter(Boolean);
  if (words.length === 0) return text;

  const pattern = new RegExp(`(${words.join('|')})`, 'giu');
  return text.split(pattern).map((part, i) => (i % 2 === 1 ? <mark key={i}>{part}</mark> : part));
}
