import {Copy, Check, Shuffle, RefreshCw} from 'lucide-react';

export const levels = [
  {id: 1, label: 'Почти человек'},
  {id: 2, label: 'Корпоративный сотрудник'},
  {id: 3, label: 'HR-директор 3000'},
];

export const satireNotice = 'Сатирическая интерпретация, а не чтение чужих мыслей. Проверьте текст перед отправкой.';

export function Brand() {
  return <span className="brand"><b aria-hidden="true">b</b>bestregards</span>;
}

export function ModeSwitch({modes, value, onChange}) {
  return (
    <div className="seg" role="group" aria-label="Режим">
      {modes.map(m => (
        <button key={m.id} type="button" aria-pressed={m.id === value} onClick={() => onChange(m.id)}>{m.label}</button>
      ))}
    </div>
  );
}

export function LevelSwitch({value, onChange}) {
  return (
    <div className="level" role="group" aria-label="Уровень">
      {levels.map(l => (
        <button key={l.id} type="button" aria-pressed={l.id === value} onClick={() => onChange(l.id)}>
          <span className="bars" aria-hidden="true">
            {levels.map(b => <i key={b.id} className={b.id <= l.id ? 'on' : ''} />)}
          </span>
          {l.label}
        </button>
      ))}
    </div>
  );
}

export function Memo({result, modeLabel, copied, onCopy, showCopy, onAlternative, alternativeLabel}) {
  const isPair = result.source === 'pair';
  return (
    <div className="memo">
      <div className="memo-head">
        <span className={`tag${isPair ? '' : ' tag-accent'}`}>{isPair ? 'Готовый пример · без AI' : 'AI-перевод'}</span>
        <span className="mono muted">{modeLabel}</span>
      </div>
      <p className="memo-body">{result.text}</p>
      {(showCopy || onAlternative) && (
        <div className="memo-foot">
          {showCopy && (
            <button type="button" className="btn btn-primary" onClick={onCopy}>
              {copied ? <Check size={20} aria-hidden="true" /> : <Copy size={20} aria-hidden="true" />}
              {copied ? 'Скопировано' : 'Скопировать'}
            </button>
          )}
          {onAlternative && (
            <button type="button" className="btn" onClick={onAlternative}>
              <RefreshCw size={18} aria-hidden="true" />{alternativeLabel}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

export function History({items, modes, onOpen, onClear}) {
  if (!items.length) return null;
  const label = id => modes.find(m => m.id === id)?.label ?? id;
  return (
    <section className="history" aria-labelledby="history-title">
      <div className="label">
        <h2 id="history-title">История <span className="mono muted">хранится только на этом устройстве</span></h2>
        <button type="button" className="link" onClick={onClear}>Очистить историю</button>
      </div>
      <ul>
        {items.map(item => (
          <li key={item.time}>
            <span className="tag">{label(item.mode)}</span>
            <p>{item.input}</p>
            <p className="muted">{item.text}</p>
            <button type="button" className="btn" onClick={() => onOpen(item)}>Открыть</button>
          </li>
        ))}
      </ul>
    </section>
  );
}

export function RandomButton({onClick}) {
  return <button type="button" className="btn" onClick={onClick}><Shuffle size={18} aria-hidden="true" />Случайный пример</button>;
}

export const statuses = [
  'Удаляем искренность…',
  'Подавляем желание уволиться…',
  'Добавляем командную синергию…',
  'Повышаем стрессоустойчивость текста…',
  'Вставляем уважение там, где его нет…',
  'Проверяем совместимость с LinkedIn…',
];

export function LoadingMemo({status}) {
  return (
    <div className="memo" role="status">
      <div className="memo-head"><span className="tag">Перевожу</span></div>
      <div className="memo-body">
        <div className="skeleton" /><div className="skeleton" /><div className="skeleton" />
        <p className="status-line">{status}</p>
      </div>
    </div>
  );
}

export function ErrorBox({message, onRetry}) {
  return (
    <div className="error-box" role="alert">
      <strong>Не получилось перевести.</strong>
      <p>{message}</p>
      {onRetry && <button type="button" className="btn" onClick={onRetry}>Повторить</button>}
    </div>
  );
}

const timeFormat = new Intl.DateTimeFormat('ru-RU', {hour: '2-digit', minute: '2-digit'});

export function formatReset(resetsAt) {
  return resetsAt ? timeFormat.format(new Date(resetsAt)) : '';
}

export function LimitMemo({quota, guest, botLink}) {
  const time = formatReset(quota?.resetsAt);
  return (
    <div className="memo" role="status">
      <div className="memo-head">
        <span className="tag">Лимит на сегодня</span>
        <span className="mono muted">Служебная записка</span>
      </div>
      <p className="memo-body">
        На сегодня переводы закончились. {time ? `Лимит обновится в ${time}.` : 'Лимит обновится завтра.'}
        <span className="muted"> Готовые примеры доступны всегда.</span>
      </p>
      {guest && (
        <div className="memo-foot">
          <a className="btn btn-primary" href={botLink}>В Telegram дают 5 переводов в день</a>
        </div>
      )}
    </div>
  );
}

export function QuotaMeter({quota}) {
  if (!quota) return null;
  if (quota.unlimited) {
    return (
      <div className="quota" title="Администратор: без суточного лимита">
        <span className="mono quota-text"><span className="quota-label">Сегодня </span>без лимита</span>
      </div>
    );
  }
  const pips = Array.from({length: quota.limit}, (_, i) => i < quota.remaining);
  return (
    <div className="quota" title="Бесплатные AI-переводы на сегодня">
      <div className="pips" aria-hidden="true">{pips.map((on, i) => <i key={i} className={on ? 'on' : ''} />)}</div>
      <span className="mono quota-text"><span className="quota-label">Сегодня </span>ещё {quota.remaining} из {quota.limit}</span>
    </div>
  );
}
