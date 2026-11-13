// Types and a fetch helper for the search endpoint. The dashboard is served by
// the API itself, so requests are same-origin and need no base URL.

export type ContentType = 'video' | 'text';
export type SortBy = 'popularity' | 'relevance';

export interface Content {
  id: number;
  provider: string;
  external_id: string;
  title: string;
  content_type: ContentType;
  tags: string[];
  published_at: string;
  metrics: { views?: number; likes?: number; reading_time?: number; reactions?: number };
  score: number;
  score_breakdown: {
    base: number;
    type_coefficient: number;
    engagement: number;
    recency: number;
    final: number;
  };
  relevance?: number;
}

export interface SearchResponse {
  items: Content[];
  pagination: { page: number; page_size: number; total: number; total_pages: number };
  meta: { sort: SortBy; cached: boolean };
}

export interface SearchParams {
  q: string;
  type: ContentType | '';
  sort: SortBy | '';
  page: number;
  pageSize: number;
}

interface FieldError {
  field: string;
  detail: string;
}

/** A failed request, carrying the API's problem detail when there is one. */
export class ApiError extends Error {
  readonly fields: FieldError[];

  constructor(message: string, fields: FieldError[] = []) {
    super(message);
    this.fields = fields;
  }
}

export async function search(params: SearchParams, signal: AbortSignal): Promise<SearchResponse> {
  const query = new URLSearchParams({ page: String(params.page), page_size: String(params.pageSize) });
  if (params.q) query.set('q', params.q);
  if (params.type) query.set('type', params.type);
  if (params.sort) query.set('sort', params.sort);

  let response: Response;
  try {
    response = await fetch(`/api/v1/search?${query}`, { signal, headers: { Accept: 'application/json' } });
  } catch (cause) {
    if (signal.aborted) throw cause;
    throw new ApiError('Could not reach the API. Is the server running?');
  }

  if (!response.ok) {
    const problem = (await response.json().catch(() => null)) as { detail?: string; errors?: FieldError[] } | null;
    throw new ApiError(problem?.detail ?? `Request failed with status ${response.status}`, problem?.errors);
  }
  return (await response.json()) as SearchResponse;
}
