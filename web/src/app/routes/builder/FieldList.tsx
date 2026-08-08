/**
 * The middle column: the questions on the open page, in order.
 *
 * Order here is layout, not navigation -- where the form goes next is a property
 * of the page (docs/06-deep-dives.md 6.2). Dragging a question therefore never
 * changes a branch, which is why reordering is safe to make this easy.
 *
 * Drag and drop is HTML5 drag events with no library. Every reorder also has a
 * keyboard equivalent on the card, because a builder that can only be operated
 * with a mouse excludes the people most likely to be maintaining a long form.
 */
import { useState } from 'react'
import type { FieldID, PageID } from '../../../shared/engine'
import { SensitiveTag } from '../../components/ui'
import {
  TYPE_LABEL,
  addField,
  type DraftSchema,
  isChoice,
  moveField,
  pageLabel,
  pageOfField,
  removeField,
  rulesForField,
} from './useDraft'

/** Custom MIME types keep a question drag apart from a palette drag, and both
 *  apart from text dropped in from another window. */
export const DRAG_FIELD = 'application/x-collectr-field'
export const DRAG_TYPE = 'application/x-collectr-field-type'

interface Props {
  schema: DraftSchema
  pageId: PageID
  selectedFieldId: FieldID | null
  readOnly: boolean
  onSelect: (id: FieldID | null) => void
  onApply: (next: DraftSchema) => void
}

export function FieldList({ schema, pageId, selectedFieldId, readOnly, onSelect, onApply }: Props) {
  const [dropAt, setDropAt] = useState<number | null>(null)
  const page = schema.pages.find((p) => p.id === pageId)
  const fields = page?.fields ?? []

  function insert(type: string, at: number) {
    const created = addField(schema, pageId, type, at)
    onApply(created.schema)
    onSelect(created.fieldId)
  }

  function accepts(dt: DataTransfer): boolean {
    return dt.types.includes(DRAG_FIELD) || dt.types.includes(DRAG_TYPE)
  }

  function onDrop(e: React.DragEvent, at: number) {
    e.preventDefault()
    setDropAt(null)
    if (readOnly) return
    const fieldId = e.dataTransfer.getData(DRAG_FIELD)
    if (fieldId) {
      // Removing the item first shifts everything after it, so an index taken
      // from the rendered list has to be adjusted when moving downwards.
      const from = fields.indexOf(fieldId)
      const to = from >= 0 && from < at ? at - 1 : at
      onApply(moveField(schema, fieldId, pageId, to))
      onSelect(fieldId)
      return
    }
    const type = e.dataTransfer.getData(DRAG_TYPE)
    if (type) insert(type, at)
  }

  function dropZone(at: number) {
    return (
      <div
        onDragOver={(e) => {
          if (!accepts(e.dataTransfer)) return
          e.preventDefault()
          setDropAt(at)
        }}
        onDragLeave={() => setDropAt((v) => (v === at ? null : v))}
        onDrop={(e) => onDrop(e, at)}
        className={`h-2 rounded transition-colors ${dropAt === at ? 'bg-accent' : ''}`}
        aria-hidden
      />
    )
  }

  if (!page) {
    return (
      <p className="text-body text-muted">
        Chưa chọn trang. Chọn một trang ở cột trái để soạn câu hỏi của trang đó.
      </p>
    )
  }

  // Questions this page's rules act on but which live somewhere else. Shown
  // dimmed rather than hidden: a rule on this page that reveals a field on page
  // 3 is the kind of link that is invisible until it breaks.
  const elsewhere: FieldID[] = []
  for (const r of schema.rules ?? []) {
    if (r.on_page !== pageId) continue
    for (const a of [...(r.then ?? []), ...(r.else ?? [])]) {
      if (a.action === 'goto' || a.action === 'end') continue
      if (!fields.includes(a.target) && !elsewhere.includes(a.target) && schema.fields[a.target]) {
        elsewhere.push(a.target)
      }
    }
  }

  return (
    <div className="flex flex-col">
      {fields.length === 0 && (
        <p className="mb-2 rounded border border-dashed border-line px-3 py-4 text-center text-body text-muted">
          Trang này chưa có câu hỏi nào. Kéo một loại câu hỏi từ cột trái, hoặc bấm nút bên dưới.
        </p>
      )}

      {dropZone(0)}
      {fields.map((fid, i) => (
        <div key={fid}>
          <FieldCard
            schema={schema}
            fieldId={fid}
            index={i}
            count={fields.length}
            selected={fid === selectedFieldId}
            readOnly={readOnly}
            onSelect={() => onSelect(fid)}
            onMove={(delta) => {
              onApply(moveField(schema, fid, pageId, i + delta))
              onSelect(fid)
            }}
            onRemove={() => {
              onApply(removeField(schema, fid))
              if (selectedFieldId === fid) onSelect(null)
            }}
          />
          {dropZone(i + 1)}
        </div>
      ))}

      {elsewhere.map((fid) => (
        <OffPageCard key={fid} schema={schema} fieldId={fid} onSelect={() => onSelect(fid)} />
      ))}

      <button
        type="button"
        disabled={readOnly}
        onClick={() => insert('text', fields.length)}
        onDragOver={(e) => {
          if (!accepts(e.dataTransfer)) return
          e.preventDefault()
          setDropAt(fields.length)
        }}
        onDrop={(e) => onDrop(e, fields.length)}
        className="mt-2 rounded border border-dashed border-line py-2.5 text-center text-meta font-semibold text-muted hover:border-line hover:text-ink disabled:cursor-not-allowed disabled:opacity-50"
      >
        + Thêm câu hỏi hoặc kéo từ cột trái
      </button>
    </div>
  )
}

function FieldCard({
  schema,
  fieldId,
  index,
  count,
  selected,
  readOnly,
  onSelect,
  onMove,
  onRemove,
}: {
  schema: DraftSchema
  fieldId: FieldID
  index: number
  count: number
  selected: boolean
  readOnly: boolean
  onSelect: () => void
  onMove: (delta: number) => void
  onRemove: () => void
}) {
  const [confirming, setConfirming] = useState(false)
  const field = schema.fields[fieldId]
  if (!field) return null
  const rules = rulesForField(schema, fieldId)

  return (
    <div
      draggable={!readOnly}
      onDragStart={(e) => {
        e.dataTransfer.setData(DRAG_FIELD, fieldId)
        e.dataTransfer.effectAllowed = 'move'
      }}
      onClick={onSelect}
      className={`rounded bg-surface px-2.5 py-2 ${
        selected ? 'border-2 border-line' : 'border border-faint hover:border-line'
      }`}
    >
      <div className="flex items-start justify-between gap-2">
        <button
          type="button"
          onClick={onSelect}
          aria-pressed={selected}
          className="min-w-0 text-left text-body font-semibold"
        >
          {field.label || <span className="text-muted">Câu hỏi chưa có nhãn</span>}
          {field.required && (
            <span className="text-accent" title="bắt buộc">
              {' '}
              *
            </span>
          )}
          {field.hidden && <span className="id-chip"> · ẩn tới khi có luật hiện</span>}
        </button>
        <div className="shrink-0 text-right">
          <div className="id-chip">
            {TYPE_LABEL[field.type] ?? field.type} · {fieldId}
          </div>
          {!readOnly && (
            <div className="mt-1 flex justify-end gap-1">
              <IconButton label="Lên" disabled={index === 0} onClick={() => onMove(-1)}>
                ↑
              </IconButton>
              <IconButton label="Xuống" disabled={index === count - 1} onClick={() => onMove(1)}>
                ↓
              </IconButton>
              <IconButton
                label={confirming ? 'Xác nhận xoá' : 'Xoá'}
                onClick={() => (confirming ? onRemove() : setConfirming(true))}
              >
                {confirming ? 'xoá?' : '✕'}
              </IconButton>
            </div>
          )}
        </div>
      </div>

      {field.sensitive && (
        <div className="mt-1.5">
          <SensitiveTag />
        </div>
      )}

      <Preview schema={schema} fieldId={fieldId} />

      {confirming && (
        <p role="alert" className="mt-2 text-meta text-overdue">
          Xoá câu hỏi này sẽ bỏ luôn {rules.length} luật gắn với nó. Id{' '}
          <span className="font-mono">{fieldId}</span> không bao giờ được dùng lại — dữ liệu đã thu
          vẫn trỏ tới nó. Bấm lần nữa để xoá.
        </p>
      )}

      {rules.length > 0 && (
        <div className="mt-2 border-t border-dashed border-line pt-1.5 font-mono text-meta text-accent">
          {rules.length} luật rẽ nhánh gắn với field này ↓
        </div>
      )}
    </div>
  )
}

/** A question a rule on this page touches, but which sits on another page. */
function OffPageCard({
  schema,
  fieldId,
  onSelect,
}: {
  schema: DraftSchema
  fieldId: FieldID
  onSelect: () => void
}) {
  const field = schema.fields[fieldId]
  const home = pageOfField(schema, fieldId)
  if (!field) return null
  return (
    <button
      type="button"
      onClick={onSelect}
      className="mt-1 flex w-full items-start justify-between gap-2 rounded border border-dashed border-line bg-surface/60 px-2.5 py-2 text-left"
    >
      <span className="text-body font-semibold text-muted">
        {field.label || fieldId}
        {/* Not a decorative link: hiện/ẩn only applies within the page the rule
            runs on, so a target sitting elsewhere is a rule that never fires. */}
        <span className="block text-meta font-normal text-overdue">
          luật ở trang này nhắm tới field này nhưng khác trang nên không chạy
        </span>
      </span>
      <span className="shrink-0 text-right">
        {field.sensitive && <SensitiveTag />}
        <span className="id-chip block">{home ? pageLabel(schema, home) : 'chưa đặt trang'}</span>
      </span>
    </button>
  )
}

/** A sketch of what the respondent sees. Not an input: this column is for
 *  arranging questions, and a working control here invites answering the form
 *  instead of building it. */
function Preview({ schema, fieldId }: { schema: DraftSchema; fieldId: FieldID }) {
  const field = schema.fields[fieldId]
  if (!field) return null

  if (isChoice(field.type)) {
    const options = field.options ?? []
    return (
      <div className="mt-2 flex flex-wrap gap-1.5">
        {options.length === 0 && (
          <span className="text-meta text-overdue">
            Chưa có lựa chọn nào — publish sẽ bị chặn.
          </span>
        )}
        {options.map((o) => (
          <span
            key={o.id}
            className="rounded-full border border-faint px-2 py-0.5 text-meta"
            title={o.id}
          >
            {o.label || o.id}
          </span>
        ))}
      </div>
    )
  }

  if (field.type === 'rating') {
    return (
      <div className="mt-2 flex gap-1" aria-hidden>
        {Array.from({ length: Math.max(2, Math.min(field.scale ?? 5, 10)) }, (_, i) => (
          <span key={i} className="rounded border border-faint px-1.5 text-meta">
            {i + 1}
          </span>
        ))}
      </div>
    )
  }

  const width = field.type === 'date' ? 'w-24' : field.multiline ? 'w-full' : 'w-1/2'
  const height = field.multiline ? 'h-6' : 'h-2.5'
  return <div className={`mt-2 rounded bg-chrome ${width} ${height}`} aria-hidden />
}

function IconButton({
  children,
  label,
  disabled,
  onClick,
}: {
  children: React.ReactNode
  label: string
  disabled?: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={(e) => {
        e.stopPropagation()
        onClick()
      }}
      className="rounded border border-faint px-1 text-meta leading-4 hover:border-line disabled:opacity-30"
    >
      {children}
    </button>
  )
}
