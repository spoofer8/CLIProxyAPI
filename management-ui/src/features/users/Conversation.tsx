import { useTranslation } from 'react-i18next';
import { Fragment, useState, type ReactNode } from 'react';
import { Button } from '@/components/ui/Button';
import { isContentRecord, prettyContent } from './logic';
import styles from './UsersPage.module.scss';

function Incremental({
  items,
  render,
}: {
  items: unknown[];
  render: (item: unknown, index: number) => ReactNode;
}) {
  const { t } = useTranslation();
  const [visible, setVisible] = useState(50);
  return (
    <>
      {items.slice(0, visible).map((item, index) => (
        <Fragment key={index}>{render(item, index)}</Fragment>
      ))}
      {visible < items.length && (
        <Button variant="secondary" size="sm" onClick={() => setVisible((count) => count + 50)}>
          {t('users.activity_show_more', { count: items.length - visible })}
        </Button>
      )}
    </>
  );
}

function JsonBlock({ title, value }: { title: string; value: unknown }) {
  return (
    <details className={styles.jsonBlock}>
      <summary>{title}</summary>
      <pre>{prettyContent(value)}</pre>
    </details>
  );
}

function TextContent({ text }: { text: string }) {
  return (
    <>
      {text.split(/```[^\n]*\n([\s\S]*?)```/g).map(
        (part, index) =>
          part &&
          (index % 2 ? (
            <pre className={styles.codeBlock} key={index}>
              {part}
            </pre>
          ) : (
            <div className={styles.messageText} key={index}>
              {part}
            </div>
          ))
      )}
    </>
  );
}

function Content({ value, depth = 0 }: { value: unknown; depth?: number }) {
  const { t } = useTranslation();
  if (value == null) return null;
  if (depth > 12) return <JsonBlock title={t('users.structured_content')} value={value} />;
  if (typeof value === 'string') return <TextContent text={value} />;
  if (Array.isArray(value))
    return (
      <>
        <Incremental
          items={value}
          render={(part, index) => <Content key={index} value={part} depth={depth + 1} />}
        />
      </>
    );
  if (!isContentRecord(value)) return <TextContent text={String(value)} />;
  const type = typeof value.type === 'string' ? value.type : '';
  if (value.text != null) return <TextContent text={String(value.text)} />;
  if (value.refusal != null) return <TextContent text={String(value.refusal)} />;
  if (type === 'tool_use' || type === 'function_call' || value.functionCall) {
    const call = isContentRecord(value.functionCall) ? value.functionCall : value;
    return (
      <JsonBlock
        title={[t('users.tool_call'), call.name].filter(Boolean).join(' · ')}
        value={call.input ?? call.args ?? call.arguments ?? call}
      />
    );
  }
  if (type === 'tool_result' || type === 'function_call_output' || value.functionResponse) {
    const output = isContentRecord(value.functionResponse) ? value.functionResponse : value;
    return (
      <JsonBlock
        title={[t('users.tool_result'), output.name ?? output.tool_use_id ?? output.call_id]
          .filter(Boolean)
          .join(' · ')}
        value={output.response ?? output.output ?? output.content ?? output}
      />
    );
  }
  if (
    /image|audio|video|file/.test(type) ||
    value.inlineData ||
    value.inline_data ||
    value.fileData ||
    value.file_data
  ) {
    return (
      <div className={styles.attachment}>
        <p>{t('users.attachment')}</p>
        <JsonBlock title={t('users.attachment_metadata')} value={value} />
      </div>
    );
  }
  if (value.content != null || value.parts != null)
    return <Content value={value.content ?? value.parts} depth={depth + 1} />;
  return <JsonBlock title={t('users.structured_content')} value={value} />;
}

function Message({
  role,
  value,
  extra = {},
}: {
  role: string;
  value: unknown;
  extra?: Record<string, unknown>;
}) {
  const { t } = useTranslation();
  const known = ['user', 'assistant', 'system', 'developer', 'tool', 'function'].includes(role)
    ? role
    : 'other';
  const calls = Array.isArray(extra.tool_calls) ? extra.tool_calls : [];
  return (
    <section className={styles.message} data-role={known}>
      <h4>
        {t('users.role_' + known)}
        {typeof extra.name === 'string' ? ' · ' + extra.name : ''}
      </h4>
      <Content value={value} />
      <Incremental
        items={calls}
        render={(item, index) => {
          const call = isContentRecord(item) ? item : {};
          const fn = isContentRecord(call.function) ? call.function : call;
          return (
            <JsonBlock
              key={index}
              title={[t('users.tool_call'), fn.name ?? call.id].filter(Boolean).join(' · ')}
              value={fn.arguments ?? item}
            />
          );
        }}
      />
      {extra.function_call != null && (
        <JsonBlock title={t('users.tool_call')} value={extra.function_call} />
      )}
      {typeof extra.tool_call_id === 'string' && (
        <p className={styles.hint}>
          {t('users.tool_call')} · {extra.tool_call_id}
        </p>
      )}
    </section>
  );
}

export function Conversation({ body }: { body: unknown }) {
  const { t } = useTranslation();
  if (!body) return <p className={styles.empty}>{t('users.body_unavailable')}</p>;
  if (!isContentRecord(body)) return <Message role="other" value={body} />;
  if (body._preview && (body.content || body.latest_user_content)) {
    const content = isContentRecord(body.content) ? body.content : {};
    const latest = body.latest_user_content ?? content.latest_user_content ?? body.content;
    const extra = isContentRecord(latest) ? latest : {};
    return (
      <>
        <p className={styles.notice}>{t('users.shortened')}</p>
        <Message
          role={typeof extra.role === 'string' ? extra.role : 'user'}
          value={extra.content ?? extra.parts ?? latest}
          extra={extra}
        />
      </>
    );
  }
  const messages = body.messages ?? body.output ?? body.input ?? body.contents ?? body.prompt;
  const systems = [
    body.system,
    body.instructions,
    body.systemInstruction ?? body.system_instruction,
  ].filter((value) => value != null);
  const entries = Array.isArray(messages) ? messages : messages != null ? [messages] : [];
  const hasTools = Array.isArray(body.tools) && body.tools.length > 0;
  const hasFunctions = Array.isArray(body.functions) && body.functions.length > 0;
  return (
    <div className={styles.conversation}>
      {systems.map((value, index) => (
        <Message key={'system' + index} role="system" value={value} />
      ))}
      <Incremental
        items={entries}
        render={(entry, index) => {
          if (!isContentRecord(entry)) return <Message key={index} role="user" value={entry} />;
          const role =
            entry.role === 'model'
              ? 'assistant'
              : typeof entry.role === 'string'
                ? entry.role
                : /tool|function/.test(String(entry.type ?? ''))
                  ? 'tool'
                  : 'user';
          return (
            <Message
              key={index}
              role={role}
              value={
                entry.content ??
                entry.parts ??
                (entry.tool_calls || entry.function_call ? null : entry)
              }
              extra={entry}
            />
          );
        }}
      />
      {hasTools && <JsonBlock title={t('users.available_tools')} value={body.tools} />}
      {hasFunctions && <JsonBlock title={t('users.available_functions')} value={body.functions} />}
      {!systems.length && !entries.length && !hasTools && !hasFunctions && (
        <JsonBlock title={t('users.request_payload')} value={body} />
      )}
    </div>
  );
}
