/**
 * The right column: everything about the selected question, including the
 * branches that hang off its answer.
 *
 * Conditions live here rather than on a separate screen because a rule is only
 * ever understandable next to the question it reads. The panel is also the only
 * place the field id is shown as an editable-looking thing and is not one: ids
 * are minted once and never change, so relabelling a question keeps every answer
 * already collected pointing at the same column.
 */
import { useState } from 'react'
import type { Action, Condition, FieldID, Rule } from '../../../shared/engine'
import { Field as FormField, SensitiveTag } from '../../components/ui'
import {
  ACTIONS,
  OPERATORS,
  addOption,
  addRule,
  type DraftSchema,
  describeAction,
  describeCondition,
  inertActions,
  isChoice,
  needsValue,
  pageLabel,
  pageOfField,
  removeOption,
  removeRule,
  rulesUsingOption,
  updateField,
  updateOption,
  updateRule,
} from './useDraft'

interface Props {
  schema: DraftSchema
  fieldId: FieldID | null
  readOnly: boolean
  onApply: (next: DraftSchema) => void
  onSelect: (id: FieldID | null) => void
}

export function FieldPanel({ schema, fieldId, readOnly, onApply, onSelect }: Props) {
  const field = fieldId ? schema.fields[fieldId] : undefined

  if (!fieldId || !field) {
    return (
      <div className="text-body text-muted">
        <h2 className="mb-2 font-mono text-meta tracking-caps text-faint">FIELD ĐANG CHỌN</h2>
        <p>
          Chưa chọn câu hỏi nào. Bấm vào một thẻ ở cột giữa để sửa nhãn, cờ bắt buộc, cờ nhạy cảm và
          các điều kiện rẽ nhánh của nó.
        </p>
      </div>
    )
  }

  const rules = (schema.rules ?? []).filter((r) => r.when.field === fieldId)
  const home = pageOfField(schema, fieldId)

  return (
    <div className="flex flex-col gap-3">
      <div>
        <h2 className="font-mono text-meta tracking-caps text-faint">FIELD ĐANG CHỌN</h2>
        <p className="id-chip mt-0.5 break-all">{fieldId}</p>
        <p className="id-chip">{home ? pageLabel(schema, home) : 'chưa nằm trên trang nào'}</p>
      </div>

      <FormField
        label="Nhãn"
        hint="Sửa nhãn không sinh id mới — dữ liệu đã thu vẫn thuộc đúng câu hỏi này."
      >
        <input
          className="input"
          value={field.label}
          disabled={readOnly}
          onChange={(e) => onApply(updateField(schema, fieldId, { label: e.target.value }))}
        />
      </FormField>

      <div className="flex flex-wrap gap-x-4 gap-y-1.5">
        <Check
          label="Bắt buộc"
          checked={Boolean(field.required)}
          disabled={readOnly}
          onChange={(v) => onApply(updateField(schema, fieldId, { required: v }))}
        />
        <Check
          label="Nhạy cảm"
          checked={Boolean(field.sensitive)}
          disabled={readOnly}
          onChange={(v) => onApply(updateField(schema, fieldId, { sensitive: v }))}
        />
        <Check
          label="Ẩn tới khi có luật hiện"
          checked={Boolean(field.hidden)}
          disabled={readOnly}
          onChange={(v) => onApply(updateField(schema, fieldId, { hidden: v }))}
        />
      </div>

      {field.sensitive && (
        <p className="flex items-start gap-2 rounded border border-accent/40 bg-accent/5 px-2 py-1.5 text-meta text-accent">
          <SensitiveTag />
          <span>
            Dữ liệu nhạy cảm phải được báo trước cho chủ thể. Biểu mẫu có field nhạy cảm mà chưa bật
            thông báo sẽ bị chặn publish.
          </span>
        </p>
      )}

      <FormField
        label="Loại dữ liệu cá nhân (pii)"
        hint="Để trống nếu câu hỏi không thu dữ liệu cá nhân. Ví dụ: name, phone, email, health."
      >
        <input
          className="input"
          value={field.pii ?? ''}
          disabled={readOnly}
          onChange={(e) =>
            onApply(updateField(schema, fieldId, { pii: e.target.value || undefined }))
          }
        />
      </FormField>

      <TypeSettings schema={schema} fieldId={fieldId} readOnly={readOnly} onApply={onApply} />

      <section className="border-t border-line pt-2.5">
        <h3 className="mb-2 font-mono text-meta tracking-caps text-faint">
          RẼ NHÁNH KHI TRẢ LỜI
        </h3>

        {rules.length === 0 && (
          <p className="mb-2 text-meta text-muted">
            Chưa có điều kiện nào đọc câu trả lời này. Không có điều kiện thì biểu mẫu đi tuần tự
            theo thứ tự trang.
          </p>
        )}

        <div className="flex flex-col gap-2">
          {rules.map((rule) => (
            <RuleCard
              key={rule.id}
              schema={schema}
              rule={rule}
              readOnly={readOnly}
              onApply={onApply}
              onSelect={onSelect}
            />
          ))}
        </div>

        <button
          type="button"
          disabled={readOnly || !home}
          onClick={() => {
            const created = addRule(schema, fieldId)
            onApply(created.schema)
          }}
          className="mt-2 w-full rounded border border-dashed border-line py-1.5 text-meta font-semibold text-muted hover:border-line hover:text-ink disabled:cursor-not-allowed disabled:opacity-50"
        >
          + Thêm điều kiện
        </button>
      </section>

      <p className="rounded border border-dashed border-accent bg-accent/5 px-2 py-1.5 text-meta leading-relaxed text-accent-dark">
        Server tự tính tập field hiển thị khi kiểm required — điều kiện ở đây là nguồn duy nhất.
      </p>
    </div>
  )
}

/* --------------------------------------------------------- type-specific bits */

function TypeSettings({
  schema,
  fieldId,
  readOnly,
  onApply,
}: {
  schema: DraftSchema
  fieldId: FieldID
  readOnly: boolean
  onApply: (next: DraftSchema) => void
}) {
  const field = schema.fields[fieldId]
  if (!field) return null

  if (isChoice(field.type)) {
    const options = field.options ?? []
    return (
      <section>
        <h3 className="mb-1.5 font-mono text-meta tracking-caps text-faint">LỰA CHỌN</h3>
        <div className="flex flex-col gap-1.5">
          {options.map((o) => {
            const used = rulesUsingOption(schema, fieldId, o.id)
            return (
              <div key={o.id}>
                <div className="flex items-center gap-1.5">
                  <input
                    className="input py-1 text-body"
                    value={o.label}
                    disabled={readOnly}
                    aria-label={`Nhãn lựa chọn ${o.id}`}
                    onChange={(e) => onApply(updateOption(schema, fieldId, o.id, e.target.value))}
                  />
                  <button
                    type="button"
                    aria-label={`Bỏ lựa chọn ${o.label || o.id}`}
                    disabled={readOnly || used.length > 0}
                    title={
                      used.length > 0
                        ? 'Còn luật rẽ nhánh so sánh với lựa chọn này — sửa luật trước.'
                        : 'Bỏ lựa chọn'
                    }
                    onClick={() => onApply(removeOption(schema, fieldId, o.id))}
                    className="shrink-0 rounded border border-faint px-1.5 text-meta hover:border-line disabled:opacity-30"
                  >
                    ✕
                  </button>
                </div>
                <p className="id-chip">
                  {o.id}
                  {used.length > 0 && ` · ${used.length} luật đang dùng`}
                </p>
              </div>
            )
          })}
        </div>
        <button
          type="button"
          disabled={readOnly}
          onClick={() => onApply(addOption(schema, fieldId).schema)}
          className="btn mt-1.5 w-full py-1 text-meta"
        >
          + Lựa chọn
        </button>
        <p className="mt-1 text-meta text-muted">
          Câu trả lời lưu id lựa chọn, không lưu nhãn. Sửa nhãn an toàn; bỏ hẳn một lựa chọn là thay
          đổi phá vỡ.
        </p>
      </section>
    )
  }

  if (field.type === 'rating') {
    return (
      <FormField label="Thang điểm" hint="Từ 2 đến 10.">
        <input
          className="input"
          type="number"
          min={2}
          max={10}
          value={field.scale ?? 5}
          disabled={readOnly}
          onChange={(e) =>
            onApply(updateField(schema, fieldId, { scale: Number(e.target.value) || 5 }))
          }
        />
      </FormField>
    )
  }

  if (field.type === 'text') {
    return (
      <Check
        label="Nhiều dòng"
        checked={Boolean(field.multiline)}
        disabled={readOnly}
        onChange={(v) => onApply(updateField(schema, fieldId, { multiline: v }))}
      />
    )
  }

  if (field.type === 'file') {
    return (
      <FormField
        label="Loại tệp nhận (accept)"
        hint="Cách nhau bằng dấu phẩy. Server kiểm bằng magic bytes, không tin phần mở rộng."
      >
        <input
          className="input"
          value={(field.accept ?? []).join(', ')}
          disabled={readOnly}
          onChange={(e) =>
            onApply(
              updateField(schema, fieldId, {
                accept: e.target.value
                  .split(',')
                  .map((s) => s.trim())
                  .filter(Boolean),
              }),
            )
          }
        />
      </FormField>
    )
  }

  return null
}

/* -------------------------------------------------------------- rule editing */

function RuleCard({
  schema,
  rule,
  readOnly,
  onApply,
  onSelect,
}: {
  schema: DraftSchema
  rule: Rule
  readOnly: boolean
  onApply: (next: DraftSchema) => void
  onSelect: (id: FieldID | null) => void
}) {
  const [open, setOpen] = useState(false)
  const inert = inertActions(schema, rule)

  function setWhen(patch: Partial<Condition>) {
    onApply(updateRule(schema, rule.id, { when: { ...rule.when, ...patch } }))
  }
  function setBranch(branch: 'then' | 'else', actions: Action[]) {
    onApply(updateRule(schema, rule.id, branch === 'then' ? { then: actions } : { else: actions }))
  }

  return (
    <div className="rounded border border-line px-2 py-1.5">
      <div className="flex items-start justify-between gap-2">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          className="min-w-0 flex-1 text-left"
        >
          <div className="font-mono text-meta text-muted">{describeCondition(schema, rule.when)}</div>
          {[...(rule.then ?? [])].map((a, i) => (
            <div key={`t${i}`} className="text-meta font-semibold">
              {describeAction(schema, a)}
            </div>
          ))}
          {(rule.else ?? []).length > 0 && (
            <div className="mt-1 font-mono text-meta text-muted">NGƯỢC LẠI</div>
          )}
          {[...(rule.else ?? [])].map((a, i) => (
            <div key={`e${i}`} className="text-meta font-semibold">
              {describeAction(schema, a)}
            </div>
          ))}
          {(rule.then ?? []).length === 0 && (rule.else ?? []).length === 0 && (
            <div className="text-meta text-overdue">Chưa có hành động nào — luật này không làm gì.</div>
          )}
          {inert.length > 0 && (
            <div role="alert" className="mt-1 text-meta leading-snug text-overdue">
              {inert.length} hành động nhắm tới field ở trang khác nên sẽ không chạy: hiện/ẩn/bắt
              buộc chỉ tác động trong đúng trang mà luật chạy ({pageLabel(schema, rule.on_page)}).
              Dùng “nhảy tới trang” nếu muốn điều hướng.
            </div>
          )}
        </button>
        <div className="flex shrink-0 gap-1">
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            className="rounded border border-faint px-1 text-meta hover:border-line"
          >
            {open ? 'xong' : 'sửa'}
          </button>
          {!readOnly && (
            <button
              type="button"
              aria-label="Xoá luật"
              onClick={() => onApply(removeRule(schema, rule.id))}
              className="rounded border border-faint px-1 text-meta hover:border-line"
            >
              ✕
            </button>
          )}
        </div>
      </div>

      {open && (
        <div className="mt-2 flex flex-col gap-2 border-t border-line pt-2">
          <div className="flex items-center gap-1.5">
            <span className="font-mono text-meta text-faint">NẾU</span>
            <select
              className="input py-1 text-meta"
              aria-label="Toán tử"
              value={rule.when.op}
              disabled={readOnly}
              onChange={(e) => setWhen({ op: e.target.value })}
            >
              {OPERATORS.map((o) => (
                <option key={o.op} value={o.op}>
                  {o.label}
                </option>
              ))}
            </select>
          </div>

          {needsValue(rule.when.op) && (
            <ValueEditor schema={schema} rule={rule} readOnly={readOnly} onChange={setWhen} />
          )}

          <ActionEditor
            title="THÌ"
            schema={schema}
            actions={rule.then ?? []}
            readOnly={readOnly}
            onChange={(a) => setBranch('then', a)}
            onSelect={onSelect}
          />
          <ActionEditor
            title="NGƯỢC LẠI"
            schema={schema}
            actions={rule.else ?? []}
            readOnly={readOnly}
            onChange={(a) => setBranch('else', a)}
            onSelect={onSelect}
          />
          <p className="id-chip">
            {rule.id} · chạy trên {pageLabel(schema, rule.on_page)}
          </p>
        </div>
      )}
    </div>
  )
}

function ValueEditor({
  schema,
  rule,
  readOnly,
  onChange,
}: {
  schema: DraftSchema
  rule: Rule
  readOnly: boolean
  onChange: (patch: Partial<Condition>) => void
}) {
  const field = schema.fields[rule.when.field]
  const options = field?.options ?? []
  const op = rule.when.op

  if (op === 'between') {
    const pair = Array.isArray(rule.when.value) ? rule.when.value : ['', '']
    return (
      <div className="flex items-center gap-1.5">
        <input
          className="input py-1 text-meta"
          aria-label="Cận dưới"
          value={String(pair[0] ?? '')}
          disabled={readOnly}
          onChange={(e) => onChange({ value: [e.target.value, String(pair[1] ?? '')] })}
        />
        <span className="text-meta text-muted">–</span>
        <input
          className="input py-1 text-meta"
          aria-label="Cận trên"
          value={String(pair[1] ?? '')}
          disabled={readOnly}
          onChange={(e) => onChange({ value: [String(pair[0] ?? ''), e.target.value] })}
        />
      </div>
    )
  }

  if ((op === 'in' || op === 'not_in') && options.length > 0) {
    const chosen = new Set((Array.isArray(rule.when.value) ? rule.when.value : []).map(String))
    return (
      <div className="flex flex-col gap-1">
        {options.map((o) => (
          <Check
            key={o.id}
            label={o.label || o.id}
            checked={chosen.has(o.id)}
            disabled={readOnly}
            onChange={(v) => {
              const next = new Set(chosen)
              if (v) next.add(o.id)
              else next.delete(o.id)
              onChange({ value: [...next] })
            }}
          />
        ))}
      </div>
    )
  }

  if (options.length > 0) {
    return (
      <select
        className="input py-1 text-meta"
        aria-label="Giá trị so sánh"
        value={String(rule.when.value ?? '')}
        disabled={readOnly}
        onChange={(e) => onChange({ value: e.target.value })}
      >
        <option value="">— chọn đáp án —</option>
        {options.map((o) => (
          <option key={o.id} value={o.id}>
            {o.label || o.id}
          </option>
        ))}
      </select>
    )
  }

  return (
    <input
      className="input py-1 text-meta"
      aria-label="Giá trị so sánh"
      type={field?.type === 'date' ? 'date' : 'text'}
      value={String(rule.when.value ?? '')}
      disabled={readOnly}
      onChange={(e) => onChange({ value: e.target.value })}
    />
  )
}

function ActionEditor({
  title,
  schema,
  actions,
  readOnly,
  onChange,
  onSelect,
}: {
  title: string
  schema: DraftSchema
  actions: Action[]
  readOnly: boolean
  onChange: (actions: Action[]) => void
  onSelect: (id: FieldID | null) => void
}) {
  function patch(i: number, next: Action) {
    onChange(actions.map((a, j) => (j === i ? next : a)))
  }

  return (
    <div>
      <div className="mb-1 font-mono text-meta tracking-caps text-faint">{title}</div>
      <div className="flex flex-col gap-1.5">
        {actions.map((a, i) => (
          <div key={i} className="flex items-center gap-1">
            <select
              className="input py-1 text-meta"
              aria-label="Hành động"
              value={a.action}
              disabled={readOnly}
              onChange={(e) => {
                const action = e.target.value
                const target =
                  action === 'goto'
                    ? (schema.pages[0]?.id ?? '')
                    : action === 'end'
                      ? ''
                      : (Object.keys(schema.fields)[0] ?? '')
                patch(i, { action, target })
              }}
            >
              {ACTIONS.map((x) => (
                <option key={x.action} value={x.action}>
                  {x.label}
                </option>
              ))}
            </select>

            {a.action === 'goto' && (
              <select
                className="input py-1 text-meta"
                aria-label="Trang đích"
                value={a.target}
                disabled={readOnly}
                onChange={(e) => patch(i, { ...a, target: e.target.value })}
              >
                {schema.pages.map((p, pi) => (
                  <option key={p.id} value={p.id}>
                    {pi + 1} · {p.title || 'chưa đặt tên'}
                  </option>
                ))}
              </select>
            )}

            {a.action !== 'goto' && a.action !== 'end' && (
              <select
                className="input py-1 text-meta"
                aria-label="Field đích"
                value={a.target}
                disabled={readOnly}
                onChange={(e) => {
                  patch(i, { ...a, target: e.target.value })
                  onSelect(e.target.value)
                }}
              >
                {schema.pages.map((p, pi) => (
                  <optgroup key={p.id} label={`${pi + 1} · ${p.title || 'chưa đặt tên'}`}>
                    {p.fields.map((fid) => (
                      <option key={fid} value={fid}>
                        {schema.fields[fid]?.label || fid}
                      </option>
                    ))}
                  </optgroup>
                ))}
              </select>
            )}

            {!readOnly && (
              <button
                type="button"
                aria-label="Bỏ hành động"
                onClick={() => onChange(actions.filter((_, j) => j !== i))}
                className="shrink-0 rounded border border-faint px-1 text-meta hover:border-line"
              >
                ✕
              </button>
            )}
          </div>
        ))}
      </div>
      <button
        type="button"
        disabled={readOnly}
        onClick={() =>
          onChange([...actions, { action: 'show', target: Object.keys(schema.fields)[0] ?? '' }])
        }
        className="mt-1 rounded border border-dashed border-faint px-2 py-0.5 text-meta text-muted hover:border-line hover:text-ink disabled:opacity-50"
      >
        + hành động
      </button>
    </div>
  )
}

function Check({
  label,
  checked,
  disabled,
  onChange,
}: {
  label: string
  checked: boolean
  disabled?: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <label className="flex items-center gap-1.5 text-meta">
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
        className="size-3 accent-ink"
      />
      {label}
    </label>
  )
}
