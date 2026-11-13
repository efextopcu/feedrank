import { useEffect, useRef, useState } from 'react';
import { ApiError, search } from './api';
import type { ContentType, SearchResponse, SortBy } from './api';
import { ResultsTable } from './ResultsTable';
import { Segmented } from './Segmented';

const PAGE_SIZE = 20;

export default function App() {
  const [input, setInput] = useState('');
  const keyword = useDebounced(input.trim(), 250);
  const [type, setType] = useState<ContentType | ''>('');
  const [sort, setSort] = useState<SortBy | ''>('');
  const [page, setPage] = useState(1);

  const [data, setData] = useState<SearchResponse | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [loading, setLoading] = useState(true);
  const searchBox = useSearchShortcut();

  // Relevance needs a keyword, so clearing the search box falls back to the
  // server's default order instead of sending a request it would reject.
  const appliedSort = sort === 'relevance' && !keyword ? '' : sort;

  useEffect(() => {
    // Aborting the previous request means a slow response can never
    // overwrite the results of a newer one.
    const controller = new AbortController();
    setLoading(true);
    search({ q: keyword, type, sort: appliedSort, page, pageSize: PAGE_SIZE }, controller.signal)
      .then((response) => {
        setData(response);
        setError(null);
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        setError(cause instanceof ApiError ? cause : new ApiError(String(cause)));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [keyword, type, appliedSort, page]);

  const shownSort = data?.meta.sort ?? 'popularity';
  const { total = 0, total_pages: totalPages = 0 } = data?.pagination ?? {};
  const first = (page - 1) * PAGE_SIZE + 1;
  const last = first + (data?.items.length ?? 0) - 1;

  const update = <T,>(set: (value: T) => void) => (value: T) => {
    set(value);
    setPage(1);
  };

  return (
    <>
      <div className={`progress${loading ? ' active' : ''}`} aria-hidden="true" />
      <main className="app">
        <header className="header">
          <div>
            <h1>Feedrank</h1>
            <p className="muted">
              Videos and articles from two providers, ranked by one formula. Open a row to see its score worked out.
            </p>
          </div>
          <a className="ghost-link" href="/docs">
            API docs <span aria-hidden="true">↗</span>
          </a>
        </header>

        <section className="controls" aria-label="Search controls">
          <label className="search">
            <SearchIcon />
            <input
              ref={searchBox}
              type="search"
              aria-label="Search"
              placeholder="Search titles and tags…"
              value={input}
              onChange={(event) => update(setInput)(event.target.value)}
              onKeyDown={(event) => event.key === 'Escape' && update(setInput)('')}
            />
            <kbd aria-hidden="true">/</kbd>
          </label>

          <Segmented
            label="Content type"
            value={type}
            onChange={update(setType)}
            options={[
              { value: '', label: 'All' },
              { value: 'video', label: 'Video' },
              { value: 'text', label: 'Text' },
            ]}
          />

          <Segmented
            label="Sort by"
            value={shownSort}
            onChange={update(setSort)}
            options={[
              { value: 'popularity', label: 'Popularity' },
              {
                value: 'relevance',
                label: 'Relevance',
                disabled: !keyword,
                title: keyword ? undefined : 'Type a search term to sort by relevance',
              },
            ]}
          />
        </section>

        {error && (
          <div className="error" role="alert">
            <strong>{error.message}</strong>
            {error.fields.length > 0 && (
              <ul>
                {error.fields.map((f) => (
                  <li key={f.field}>
                    <code>{f.field}</code> {f.detail}
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}

        {data && data.items.length > 0 && (
          <>
            <ResultsTable items={data.items} keyword={keyword} offset={first - 1} busy={loading} />
            <nav className="pagination" aria-label="Pages">
              <span className="muted">
                {first}–{last} of {total}
                {data.meta.cached && <span className="pill" title="Served from the Redis cache">cached</span>}
              </span>
              <button type="button" disabled={page <= 1 || loading} onClick={() => setPage(page - 1)}>
                ← Previous
              </button>
              <button type="button" disabled={page >= totalPages || loading} onClick={() => setPage(page + 1)}>
                Next →
              </button>
            </nav>
          </>
        )}

        {data && data.items.length === 0 && !error && (
          <div className="empty">
            <p>{keyword ? `Nothing matches “${keyword}”.` : 'No content yet.'}</p>
            <p className="muted small">
              {keyword ? 'Try fewer words, or another type.' : 'The first ingestion runs at start-up.'}
            </p>
          </div>
        )}
      </main>
    </>
  );
}

/** Returns value once it has stopped changing for delay milliseconds. */
function useDebounced<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay);
    return () => window.clearTimeout(timer);
  }, [value, delay]);
  return debounced;
}

/** Focuses the returned input when "/" is pressed anywhere else on the page. */
function useSearchShortcut() {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      const typing = event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement;
      if (event.key === '/' && !typing) {
        event.preventDefault();
        ref.current?.focus();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
  return ref;
}

function SearchIcon() {
  return (
    <svg viewBox="0 0 20 20" width="16" height="16" aria-hidden="true">
      <circle cx="9" cy="9" r="6" fill="none" stroke="currentColor" strokeWidth="1.8" />
      <path d="m13.5 13.5 3.5 3.5" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
    </svg>
  );
}
