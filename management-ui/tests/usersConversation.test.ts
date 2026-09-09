import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import { createInstance } from 'i18next';
import { Conversation } from '../src/features/users/Conversation';
import { parsePreview } from '../src/features/users/logic';
import en from '../src/i18n/locales/en.json';

const i18n = createInstance();
await i18n.init({ lng: 'en', resources: { en: { translation: en } } });
const render = (body: unknown) =>
  renderToStaticMarkup(
    createElement(I18nextProvider, { i18n }, createElement(Conversation, { body }))
  );

describe('retained conversation rendering', () => {
  test('renders submitted HTML as text and never fetches attachment URLs', () => {
    const markup = render({
      messages: [
        {
          role: 'user',
          content: [
            {
              type: 'text',
              text: '<script>alert(1)</script><img src="https://example.invalid/tracker">',
            },
            { type: 'image_url', image_url: { url: 'https://example.invalid/image' } },
          ],
        },
      ],
    });
    expect(markup).not.toContain('<script');
    expect(markup).not.toContain('<img');
    expect(markup).not.toContain('<iframe');
    expect(markup).toContain('&lt;script&gt;');
    expect(markup).toContain('Attachment (not loaded)');
  });

  test('shows fenced code, OpenAI tools, Anthropic tool results and Gemini roles', () => {
    const markup = render({
      messages: [
        { role: 'user', content: 'Example\n```js\nconsole.log("hello")\n```' },
        {
          role: 'assistant',
          content: null,
          tool_calls: [{ function: { name: 'lookup', arguments: '{"city":"London"}' } }],
        },
        {
          role: 'user',
          content: [{ type: 'tool_result', tool_use_id: 'call_1', content: 'result' }],
        },
        { role: 'model', parts: [{ text: 'Gemini answer' }] },
      ],
    });
    expect(markup).toContain('<pre');
    expect(markup).toContain('console.log');
    expect(markup).toContain('Tool call · lookup');
    expect(markup).toContain('London');
    expect(markup).toContain('Tool result · call_1');
    expect(markup).toContain('data-role="assistant"');
    expect(markup).toContain('Gemini answer');
  });

  test('shows shortened latest-content previews and omission notices', () => {
    const body = parsePreview(
      '{"_preview":true,"content":{"latest_user_content":{"role":"user","content":"latest prompt"}}}'
    );
    const markup = render(body);
    expect(markup).toContain('A shortened preview');
    expect(markup).toContain('latest prompt');
    expect(render(null)).toContain('No request content was retained');
    expect(parsePreview('plain text')).toBe('plain text');
  });

  test('caps conversation rendering with a visible notice and retains latest entries', () => {
    const markup = render({
      messages: Array.from({ length: 205 }, (_, index) => ({
        role: 'user',
        content: 'message-number-' + index + '-end',
      })),
    });
    expect(markup).toContain('Showing the latest 200');
    expect(markup).not.toContain('message-number-0-end');
    expect(markup).toContain('message-number-204-end');
  });
});
