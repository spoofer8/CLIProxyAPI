import { describe, expect, spyOn, test } from 'bun:test';
import { apiClient } from '../src/services/api/client';
import {
  activityApi,
  activityQuery,
  appendActivityChunks,
  assembleActivityEvents,
  normalizeActivityRequest,
  parseActivityContent,
  responseConversation,
  type ActivityChunk,
} from '../src/services/api/activity';

const chunk = (sequence: number, text: string, format = 'json'): ActivityChunk => ({
  sequence,
  text,
  format,
  direction: 'response',
});

describe('complete activity content', () => {
  test('reassembles JSON split across pages without inserting bytes and deduplicates retries', () => {
    const first = [chunk(10, '{"messages":[{"role":"user","content":"first ')];
    expect(parseActivityContent(first, 'request', false).body).toBeNull();
    const full = appendActivityChunks(first, [chunk(10, first[0].text), chunk(11, 'and last"}]}')]);
    expect(full).toHaveLength(2);
    expect(parseActivityContent(full, 'request', true)).toMatchObject({
      body: { messages: [{ role: 'user', content: 'first and last' }] },
      partial: false,
    });
  });

  test('incremental JSONL waits for the complete line and preserves all raw events', () => {
    const events =
      '{"type":"response.output_text.delta","delta":"hello "}\n{"type":"response.output_text.delta","delta":"world"}\n';
    const first = parseActivityContent([chunk(1, events.slice(0, 70), 'jsonl')], 'response', false);
    expect(first.body).toEqual({ messages: [{ role: 'assistant', content: 'hello ' }] });
    const full = parseActivityContent(
      [chunk(1, events.slice(0, 70), 'jsonl'), chunk(2, events.slice(70), 'jsonl')],
      'response',
      true
    );
    expect(full.body).toEqual({ messages: [{ role: 'assistant', content: 'hello world' }] });
    expect(full.raw).toBe(events);
  });

  test('assembles OpenAI, Anthropic, and Gemini text and tool arguments', () => {
    const openai = assembleActivityEvents([
      {
        choices: [
          {
            index: 0,
            delta: {
              content: 'Open',
              tool_calls: [{ index: 0, function: { name: 'lookup', arguments: '{"city":' } }],
            },
          },
        ],
      },
      {
        choices: [
          {
            index: 0,
            delta: {
              content: 'AI',
              tool_calls: [{ index: 0, function: { arguments: '"London"}' } }],
            },
          },
        ],
      },
    ]);
    expect(JSON.stringify(openai)).toContain('OpenAI');
    expect(JSON.stringify(openai)).toContain('London');
    const claude = assembleActivityEvents([
      {
        type: 'content_block_start',
        index: 0,
        content_block: { type: 'tool_use', name: 'run', input: {} },
      },
      { type: 'content_block_delta', index: 0, delta: { partial_json: '{"command":"test"}' } },
      { type: 'content_block_delta', index: 1, delta: { text: 'Claude answer' } },
    ]);
    expect(JSON.stringify(claude)).toContain('Claude answer');
    expect(JSON.stringify(claude)).toContain('command');
    const gemini = assembleActivityEvents([
      { candidates: [{ index: 0, content: { parts: [{ text: 'Gemini ' }] } }] },
      { candidates: [{ index: 0, content: { parts: [{ text: 'answer' }] } }] },
    ]);
    expect(gemini).toEqual({ messages: [{ role: 'assistant', content: 'Gemini answer' }] });
  });

  test('prefers canonical final response without duplicating streamed text', () => {
    expect(
      assembleActivityEvents([
        { type: 'response.output_text.delta', delta: 'answer' },
        {
          type: 'response.completed',
          response: {
            output: [{ role: 'assistant', content: [{ type: 'output_text', text: 'answer' }] }],
          },
        },
      ])
    ).toEqual({
      messages: [{ role: 'assistant', content: [{ type: 'output_text', text: 'answer' }] }],
    });
    expect(
      responseConversation({ choices: [{ message: { role: 'assistant', content: 'reply' } }] })
    ).toEqual({ messages: [{ role: 'assistant', content: 'reply' }] });
  });

  test('normalizes durable metadata without treating unavailable token usage as zero', () => {
    const request = normalizeActivityRequest({
      id: 'request',
      capture_state: 'interrupted',
      session_id: 'explicit',
      request_content_bytes: 5000000,
      prompt_preview: 'question',
      total_tokens: null,
    });
    expect(request.captureState).toBe('interrupted');
    expect(request.requestContentBytes).toBe(5000000);
    expect(request.sessionId).toBe('explicit');
    expect(request.totalTokens).toBeNull();
  });

  test('search parameters are bounded server queries and content uses authenticated cancellable client', async () => {
    expect(
      activityQuery(
        {
          query: ' question ',
          model: ' GPT ',
          status: 'error',
          sessionId: 'case-Sensitive',
          since: 'invalid',
        },
        20
      )
    ).toEqual({
      limit: 50,
      q: 'question',
      model: 'GPT',
      status: 'error',
      session_id: 'case-Sensitive',
      before: 20,
    });
    const get = spyOn(apiClient, 'get').mockResolvedValue({
      chunks: [{ sequence: 1, direction: 'request', format: 'json', text: '{}' }],
      next_after: 0,
      complete: true,
    });
    try {
      const controller = new AbortController();
      const result = await activityApi.content('request/id', 'request', 123, controller.signal);
      expect(get).toHaveBeenCalledWith('/requests/request%2Fid/content', {
        params: { direction: 'request', after: 123, limit: 8 },
        signal: controller.signal,
      });
      expect(result.complete).toBe(true);
    } finally {
      get.mockRestore();
    }
  });
});
