import { apiClient } from './client';
import { normalizeUserRequest, type UserRequestDetail } from './users';

export interface ActivityRequest extends UserRequestDetail {
  promptPreview: string;
  responsePreview: string;
  sessionId: string;
  sessionSource: string;
  previousResponseId: string;
  responseId: string;
  captureState: string;
  requestContentBytes: number;
  responseContentBytes: number;
}
export interface ActivityFilters {
  query: string;
  model: string;
  status: string;
  since: string;
  sessionId: string;
}
export interface ActivityChunk {
  sequence: number;
  direction: string;
  format: string;
  text: string;
}
export interface ActivityContentPage {
  chunks: ActivityChunk[];
  nextAfter: number;
  complete: boolean;
  captureState: string;
}
export type ActivityDirection = 'request' | 'response';
const object = (value: unknown): Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
const text = (value: unknown) => (typeof value === 'string' ? value : '');
const number = (value: unknown) =>
  typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : 0;

export function normalizeActivityRequest(value: unknown): ActivityRequest {
  const raw = object(value);
  return {
    ...normalizeUserRequest(value),
    promptPreview: text(raw.prompt_preview),
    responsePreview: text(raw.response_preview),
    sessionId: text(raw.session_id),
    sessionSource: text(raw.session_source),
    previousResponseId: text(raw.previous_response_id),
    responseId: text(raw.response_id),
    captureState: text(raw.capture_state),
    requestContentBytes: number(raw.request_content_bytes),
    responseContentBytes: number(raw.response_content_bytes),
  };
}
export function normalizeActivityContent(value: unknown): ActivityContentPage {
  const raw = object(value);
  return {
    chunks: (Array.isArray(raw.chunks) ? raw.chunks : []).map((value) => {
      const chunk = object(value);
      return {
        sequence: number(chunk.sequence),
        direction: text(chunk.direction),
        format: text(chunk.format),
        text: text(chunk.text),
      };
    }),
    nextAfter: number(raw.next_after),
    complete: raw.complete === true,
    captureState: text(raw.capture_state),
  };
}
export function activityQuery(filters: ActivityFilters, before = 0) {
  const since = filters.since ? new Date(filters.since) : null;
  return {
    limit: 50,
    ...(before ? { before } : {}),
    ...(filters.query.trim() ? { q: filters.query.trim() } : {}),
    ...(filters.model.trim() ? { model: filters.model.trim() } : {}),
    ...(filters.status ? { status: filters.status } : {}),
    ...(since && !Number.isNaN(since.getTime()) ? { since: since.toISOString() } : {}),
    ...(filters.sessionId ? { session_id: filters.sessionId } : {}),
  };
}
export const activityApi = {
  async list(userId: string, filters: ActivityFilters, before = 0, signal?: AbortSignal) {
    const raw = object(
      await apiClient.get('/users/' + encodeURIComponent(userId) + '/requests', {
        params: activityQuery(filters, before),
        signal,
      })
    );
    return {
      requests: (Array.isArray(raw.requests) ? raw.requests : []).map(normalizeActivityRequest),
      nextBefore: number(raw.next_before),
    };
  },
  async detail(id: string, signal?: AbortSignal) {
    const raw = object(await apiClient.get('/requests/' + encodeURIComponent(id), { signal }));
    return normalizeActivityRequest(raw.request);
  },
  async content(id: string, direction: ActivityDirection, after = 0, signal?: AbortSignal) {
    return normalizeActivityContent(
      await apiClient.get('/requests/' + encodeURIComponent(id) + '/content', {
        params: { direction, after, limit: 8 },
        signal,
      })
    );
  },
};

/** Page boundaries can bisect JSON strings, so no separators are inserted. */
export function appendActivityChunks(
  previous: ActivityChunk[],
  incoming: ActivityChunk[]
): ActivityChunk[] {
  const byId = new Map(previous.map((chunk) => [chunk.sequence, chunk]));
  for (const chunk of incoming) byId.set(chunk.sequence, chunk);
  return Array.from(byId.values()).sort((a, b) => a.sequence - b.sequence);
}

export function responseConversation(value: unknown): unknown {
  const root = object(value);
  if (Array.isArray(root.output))
    return { messages: root.output.map((item) => ({ role: 'assistant', ...object(item) })) };
  if (Array.isArray(root.choices))
    return {
      messages: root.choices.map((choice) => {
        const item = object(choice);
        return item.message ?? { role: 'assistant', content: item.text ?? item };
      }),
    };
  if (Array.isArray(root.candidates))
    return {
      messages: root.candidates.map((candidate) => ({
        role: 'assistant',
        ...object(object(candidate).content),
      })),
    };
  if (root.content != null)
    return { messages: [{ role: root.role ?? 'assistant', content: root.content }] };
  if (typeof root.text === 'string')
    return { messages: [{ role: 'assistant', content: root.text }] };
  return value;
}

/** Assemble common stream formats; the JSON view retains every original event. */
export function assembleActivityEvents(events: unknown[]): unknown {
  const messages: unknown[] = [];
  const texts = new Map<string, string>();
  const tools = new Map<string, Record<string, unknown>>();
  let final: unknown;
  const append = (key: string, value: unknown) => {
    if (typeof value === 'string') texts.set(key, (texts.get(key) ?? '') + value);
  };
  for (const value of events) {
    const event = object(value);
    const response = object(event.response);
    if (Array.isArray(response.output) && response.output.length)
      final = responseConversation(response);
    if (event.type === 'response.output_text.delta')
      append('response:' + String(event.output_index ?? 0), event.delta);
    if (event.type === 'response.function_call_arguments.delta') {
      const key = text(event.item_id) || String(event.output_index ?? 0);
      const prior = tools.get(key) ?? { type: 'function_call', id: key };
      tools.set(key, { ...prior, arguments: text(prior.arguments) + text(event.delta) });
    }
    if (event.type === 'response.output_item.done') {
      const item = object(event.item);
      if (item.type === 'function_call') tools.set(text(item.id), item);
    }
    if (Array.isArray(event.choices))
      for (const rawChoice of event.choices) {
        const choice = object(rawChoice);
        const delta = object(choice.delta);
        const key = 'choice:' + String(choice.index ?? 0);
        if (choice.message) messages.push(choice.message);
        append(key, delta.content ?? choice.text);
        if (Array.isArray(delta.tool_calls))
          for (const rawCall of delta.tool_calls) {
            const call = object(rawCall);
            const fn = object(call.function);
            const toolKey = key + ':tool:' + String(call.index ?? 0);
            const prior = tools.get(toolKey) ?? { type: 'function_call' };
            tools.set(toolKey, {
              ...prior,
              name: fn.name ?? prior.name,
              arguments: text(prior.arguments) + text(fn.arguments),
              id: call.id ?? prior.id,
            });
          }
      }
    if (event.type === 'content_block_start') {
      const block = object(event.content_block);
      const key = 'claude:' + String(event.index ?? 0);
      if (block.type === 'tool_use') tools.set(key, block);
      else append(key, block.text);
    }
    if (event.type === 'content_block_delta') {
      const delta = object(event.delta);
      const key = 'claude:' + String(event.index ?? 0);
      append(key, delta.text ?? delta.thinking);
      if (delta.partial_json != null) {
        const prior = tools.get(key) ?? { type: 'tool_use' };
        tools.set(key, {
          ...prior,
          input: undefined,
          arguments: text(prior.arguments) + text(delta.partial_json),
        });
      }
    }
    if (Array.isArray(event.candidates))
      for (const rawCandidate of event.candidates) {
        const candidate = object(rawCandidate);
        const content = object(candidate.content);
        const key = 'gemini:' + String(candidate.index ?? 0);
        if (Array.isArray(content.parts))
          for (const rawPart of content.parts) {
            const part = object(rawPart);
            if (part.text != null) append(key, part.text);
            else messages.push({ role: 'assistant', content: part });
          }
      }
    if (typeof event.text === 'string' && !event.type) append('generic', event.text);
  }
  if (final) return final;
  for (const content of texts.values()) messages.push({ role: 'assistant', content });
  for (const tool of tools.values()) messages.push({ role: 'assistant', content: tool });
  return messages.length ? { messages } : { events };
}

export function parseActivityContent(
  chunks: ActivityChunk[],
  direction: ActivityDirection,
  complete: boolean
): { body: unknown; raw: string; partial: boolean } {
  const raw = chunks.map((chunk) => chunk.text).join('');
  const format = chunks[0]?.format;
  if (format === 'jsonl') {
    const lines = raw.split('\n');
    if (!complete && !raw.endsWith('\n')) lines.pop();
    const events = lines
      .filter((line) => line.trim())
      .flatMap((line): unknown[] => {
        try {
          return [JSON.parse(line) as unknown];
        } catch {
          return [];
        }
      });
    return { body: assembleActivityEvents(events), raw, partial: !complete };
  }
  try {
    const value: unknown = JSON.parse(raw);
    return {
      body: direction === 'response' ? responseConversation(value) : value,
      raw,
      partial: !complete,
    };
  } catch {
    return { body: null, raw, partial: !complete };
  }
}
