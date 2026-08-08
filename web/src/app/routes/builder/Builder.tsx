/**
 * The form builder: three tabs over one draft.
 *
 * Soạn arranges the questions, Sơ đồ shows where the answers send people, and
 * Publish freezes the draft into a version that can never be edited again. They
 * are tabs rather than screens because they are three views of the same object,
 * and a person moves between them several times per edit.
 *
 * Nothing here can touch a published version. The chrome says so out loud: the
 * single most expensive misunderstanding available in a form builder is thinking
 * you are fixing the form people are filling in right now.
 */
import { useState } from 'react'
import { useParams } from 'react-router'
import type { FieldID, PageID } from '../../../shared/engine'
import { Empty, ErrorBanner, Loading, StatusPill } from '../../components/ui'
import { can, useMe } from '../../lib/session'
import { DRAG_TYPE, FieldList } from './FieldList'
import { FieldPanel } from './FieldPanel'
import { FlowDiagram } from './FlowDiagram'
import { PublishDialog } from './PublishDialog'
import {
  FIELD_TYPES,
  addPage,
  type DraftSchema,
  movePage,
  removePage,
  sensitiveFieldsOnPage,
  updatePage,
  useDraftEditor,
  useForm,
  usePublishPreview,
  useVersions,
  openingSchema,
} from './useDraft'

type Tab = 'edit' | 'flow' | 'publish'

const TABS: { id: Tab; label: string }[] = [
  { id: 'edit', label: 'Soạn' },
  { id: 'flow', label: 'Sơ đồ' },
  { id: 'publish', label: 'Publish' },
]

export function Builder() {
  const { formId } = useParams()
  const me = useMe()
  const form = useForm(formId)
  const versions = useVersions(formId)

  // Permission comes from the API, never from a role name compared here: the
  // server is what enforces it, and a second copy of the rule drifts.
  const writable = can(me.data, 'form.write')
  const publishable = can(me.data, 'form.publish')

  const editor = useDraftEditor(formId, openingSchema(form.data), writable)
  const [tab, setTab] = useState<Tab>('edit')
  const [pageId, setPageId] = useState<PageID | null>(null)
  const [fieldId, setFieldId] = useState<FieldID | null>(null)
  const [simulatorOpen, setSimulatorOpen] = useState(false)

  const preview = usePublishPreview(formId, tab === 'flow')

  if (form.isPending) return <Loading />
  if (form.isError) return <div className="p-6"><ErrorBanner error={form.error} retry={() => void form.refetch()} /></div>
  if (!form.data) return null

  const live = versions.data?.find((v) => v.id === form.data.live_version_id) ?? null
  const nextNo = (versions.data ?? []).reduce((m, v) => Math.max(m, v.version_no), 0) + 1
  const schema = editor.schema

  return (
    <div className="flex h-screen min-h-0 flex-col">
      <header className="flex shrink-0 items-center gap-3 border-b border-line bg-surface px-4 py-2">
        <div className="min-w-0">
          <h1 className="truncate text-body font-semibold">{form.data.title}</h1>
          <p className="id-chip">
            {form.data.public_id} · nháp ·{' '}
            {live ? `dựa trên v${live.version_no}` : 'chưa từng publish'}
          </p>
        </div>

        <SaveState editor={editor} writable={writable} />

        <div className="flex-1" />

        <button
          type="button"
          className="btn py-1 text-meta"
          onClick={() => {
            setSimulatorOpen(true)
            setTab('flow')
          }}
        >
          Xem trước
        </button>
        <button
          type="button"
          className="btn py-1 text-meta"
          onClick={() => {
            setTab('flow')
            void preview.refetch()
          }}
        >
          Kiểm tra rẽ nhánh
        </button>
        <button type="button" className="btn-primary py-1 text-meta" onClick={() => setTab('publish')}>
          Publish v{nextNo}
        </button>
      </header>

      <nav
        aria-label="Chế độ xem"
        className="flex shrink-0 gap-1 border-b border-line bg-surface px-4"
      >
        {TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            aria-current={tab === t.id ? 'page' : undefined}
            onClick={() => setTab(t.id)}
            className={`-mb-px border-b-2 px-3 py-1.5 text-meta font-semibold ${
              tab === t.id ? 'border-line text-ink' : 'border-transparent text-muted hover:text-ink'
            }`}
          >
            {t.label}
          </button>
        ))}
      </nav>

      {!writable && (
        <p role="status" className="shrink-0 bg-duesoon/10 px-4 py-1 text-meta text-duesoon">
          Chỉ xem: tài khoản này không có quyền <span className="font-mono">form.write</span>.
        </p>
      )}

      {openingSchema(form.data) === null ? (
        <div className="p-6">
          <Empty
            title="Chưa nạp được schema nháp"
            hint={
              <>
                <span className="font-mono">GET /api/v1/forms/{formId}</span> hiện chỉ trả metadata
                của biểu mẫu, không kèm schema nháp. Trình soạn không thể đoán nội dung nháp — lưu
                một schema rỗng đè lên sẽ mất toàn bộ câu hỏi đang có. Cần API trả trường{' '}
                <span className="font-mono">draft</span> (hoặc{' '}
                <span className="font-mono">GET /api/v1/forms/{'{id}'}/draft</span>).
              </>
            }
          />
        </div>
      ) : !schema ? (
        <Loading label="Đang mở bản nháp…" />
      ) : (
        <div className="min-h-0 flex-1 overflow-hidden">
          {tab === 'edit' && (
            <EditTab
              schema={schema}
              pageId={pageId}
              fieldId={fieldId}
              readOnly={!writable}
              onPage={setPageId}
              onField={setFieldId}
              onApply={(next) => editor.apply(() => next)}
            />
          )}

          {tab === 'flow' && (
            <div className="h-full min-h-0 overflow-hidden p-4">
              <FlowDiagram
                schema={schema}
                title={form.data.title}
                validation={preview.data?.validation}
                checking={preview.isFetching}
                onCheck={() => void preview.refetch()}
                simulatorOpen={simulatorOpen}
                onToggleSimulator={() => setSimulatorOpen((v) => !v)}
              />
            </div>
          )}

          {tab === 'publish' && formId && (
            <div className="h-full overflow-y-auto p-4">
              <PublishDialog
                formId={formId}
                form={form.data}
                schema={schema}
                versions={versions.data}
                canPublish={publishable && writable}
                dirty={editor.dirty}
                onSaveFirst={editor.saveNow}
                onBack={() => setTab('edit')}
              />
            </div>
          )}
        </div>
      )}
    </div>
  )
}

/* ---------------------------------------------------------------- edit tab */

function EditTab({
  schema,
  pageId,
  fieldId,
  readOnly,
  onPage,
  onField,
  onApply,
}: {
  schema: DraftSchema
  pageId: PageID | null
  fieldId: FieldID | null
  readOnly: boolean
  onPage: (id: PageID | null) => void
  onField: (id: FieldID | null) => void
  onApply: (next: DraftSchema) => void
}) {
  // Derived rather than stored: a page or field can disappear under the
  // selection when a rule deletes it, and a dangling selection renders an empty
  // panel with no explanation.
  const activePage =
    pageId && schema.pages.some((p) => p.id === pageId) ? pageId : (schema.pages[0]?.id ?? null)
  const activeField = fieldId && schema.fields[fieldId] ? fieldId : null
  const page = activePage ? schema.pages.find((p) => p.id === activePage) : undefined
  const [confirmPage, setConfirmPage] = useState<PageID | null>(null)

  return (
    <div className="flex h-full min-h-0">
      <aside className="w-44 shrink-0 overflow-y-auto border-r border-line bg-panel px-2.5 py-3">
        <h2 className="mb-1.5 font-mono text-[8px] tracking-caps text-faint">LOẠI CÂU HỎI</h2>
        <div className="flex flex-col gap-1">
          {FIELD_TYPES.map((t) => (
            <div
              key={t.type}
              draggable={!readOnly && Boolean(activePage)}
              onDragStart={(e) => {
                e.dataTransfer.setData(DRAG_TYPE, t.type)
                e.dataTransfer.effectAllowed = 'copy'
              }}
              title={t.hint}
              className="cursor-grab rounded border border-dashed border-line px-2 py-1 text-meta hover:border-line active:cursor-grabbing"
            >
              {t.label}
            </div>
          ))}
        </div>
        <p className="mt-1.5 text-meta leading-snug text-muted">
          Kéo vào cột giữa, hoặc dùng nút “+ Thêm câu hỏi”.
        </p>

        <h2 className="mb-1.5 mt-4 font-mono text-[8px] tracking-caps text-faint">TRANG</h2>
        <div className="flex flex-col gap-1">
          {schema.pages.map((p, i) => {
            const active = p.id === activePage
            const sensitive = sensitiveFieldsOnPage(schema, p.id).length > 0
            return (
              <div key={p.id}>
                <button
                  type="button"
                  onClick={() => onPage(p.id)}
                  aria-current={active ? 'true' : undefined}
                  className={`w-full rounded px-2 py-1 text-left text-meta ${
                    active
                      ? 'border border-line font-semibold'
                      : 'border border-dashed border-line hover:border-line'
                  }`}
                >
                  {i + 1} · {p.title || 'chưa đặt tên'}
                  <span className="id-chip block">
                    {p.fields.length} field{sensitive && ' · ◆ nhạy cảm'}
                  </span>
                </button>
                {active && !readOnly && (
                  <div className="mt-1 flex items-center gap-1">
                    <input
                      className="input py-0.5 text-meta"
                      aria-label={`Tên trang ${i + 1}`}
                      value={p.title}
                      onChange={(e) => onApply(updatePage(schema, p.id, { title: e.target.value }))}
                    />
                    <button
                      type="button"
                      aria-label="Đưa trang lên"
                      disabled={i === 0}
                      onClick={() => onApply(movePage(schema, p.id, -1))}
                      className="rounded border border-faint px-1 text-meta disabled:opacity-30"
                    >
                      ↑
                    </button>
                    <button
                      type="button"
                      aria-label="Đưa trang xuống"
                      disabled={i === schema.pages.length - 1}
                      onClick={() => onApply(movePage(schema, p.id, 1))}
                      className="rounded border border-faint px-1 text-meta disabled:opacity-30"
                    >
                      ↓
                    </button>
                    <button
                      type="button"
                      aria-label={confirmPage === p.id ? 'Xác nhận xoá trang' : 'Xoá trang'}
                      onClick={() => {
                        if (confirmPage !== p.id) {
                          setConfirmPage(p.id)
                          return
                        }
                        onApply(removePage(schema, p.id))
                        setConfirmPage(null)
                        onPage(null)
                        onField(null)
                      }}
                      className="shrink-0 rounded border border-faint px-1 text-meta"
                    >
                      {confirmPage === p.id ? 'xoá?' : '✕'}
                    </button>
                  </div>
                )}
                {active && confirmPage === p.id && (
                  <p role="alert" className="mt-1 text-meta leading-snug text-overdue">
                    Xoá trang này sẽ xoá luôn {p.fields.length} câu hỏi trên đó và mọi luật chạy
                    trên nó. Id các câu hỏi không bao giờ được dùng lại. Bấm lần nữa để xoá.
                  </p>
                )}
                {active && (
                  <label className="mt-1 block text-meta text-muted">
                    {/* Default navigation belongs to the page, not to a rule.
                        Without it a respondent sent down one branch falls
                        through into the next branch declared below it. */}
                    Xong trang này thì đi tới
                    <select
                      className="input mt-0.5 py-0.5 text-meta"
                      value={p.next ?? ''}
                      disabled={readOnly}
                      onChange={(e) =>
                        onApply(updatePage(schema, p.id, { next: e.target.value || undefined }))
                      }
                    >
                      <option value="">trang kế tiếp theo thứ tự</option>
                      {schema.pages
                        .filter((x) => x.id !== p.id)
                        .map((x) => (
                          <option key={x.id} value={x.id}>
                            {schema.pages.indexOf(x) + 1} · {x.title || 'chưa đặt tên'}
                          </option>
                        ))}
                    </select>
                  </label>
                )}
              </div>
            )
          })}
        </div>
        <button
          type="button"
          disabled={readOnly}
          onClick={() => {
            const created = addPage(schema)
            onApply(created.schema)
            onPage(created.pageId)
          }}
          className="mt-1.5 w-full rounded border border-dashed border-line py-1 text-meta font-semibold text-muted hover:border-line hover:text-ink disabled:opacity-50"
        >
          + Trang
        </button>
        <p className="mt-1.5 text-meta leading-snug text-muted">
          Thứ tự trang là bố cục. Đi đâu sau trang này là thuộc tính của trang, đặt bằng luật rẽ
          nhánh.
        </p>
      </aside>

      <div className="min-w-0 flex-1 overflow-y-auto bg-canvas px-4 py-3">
        {page ? (
          <>
            <div className="mb-2 flex items-baseline gap-2">
              <h2 className="text-body font-semibold">{page.title || 'Trang chưa đặt tên'}</h2>
              <span className="id-chip">{page.id}</span>
              {page.next && (
                <StatusPill tone="accent">
                  đi tiếp tới {schema.pages.findIndex((x) => x.id === page.next) + 1}
                </StatusPill>
              )}
            </div>
            {activePage && (
              <FieldList
                schema={schema}
                pageId={activePage}
                selectedFieldId={activeField}
                readOnly={readOnly}
                onSelect={onField}
                onApply={onApply}
              />
            )}
          </>
        ) : (
          <Empty
            title="Biểu mẫu chưa có trang nào"
            hint="Một biểu mẫu cần ít nhất một trang trước khi publish. Bấm “+ Trang” ở cột trái."
          />
        )}
      </div>

      <aside className="w-72 shrink-0 overflow-y-auto border-l border-line bg-surface px-3 py-3">
        <FieldPanel
          schema={schema}
          fieldId={activeField}
          readOnly={readOnly}
          onApply={onApply}
          onSelect={onField}
        />
      </aside>
    </div>
  )
}

function SaveState({
  editor,
  writable,
}: {
  editor: ReturnType<typeof useDraftEditor>
  writable: boolean
}) {
  if (!writable) return null
  if (editor.saveError) {
    return (
      <span role="alert" className="text-meta font-semibold text-overdue">
        Lưu nháp thất bại — thử lại
        <button type="button" className="ml-1 underline" onClick={editor.saveNow}>
          lưu ngay
        </button>
      </span>
    )
  }
  if (editor.saving) return <span className="id-chip">đang lưu…</span>
  if (editor.dirty) {
    return (
      <button type="button" className="btn py-0.5 text-meta" onClick={editor.saveNow}>
        Lưu nháp
      </button>
    )
  }
  if (editor.savedAt) {
    return (
      <span className="id-chip">
        đã lưu {new Date(editor.savedAt).toLocaleTimeString('vi-VN', { timeStyle: 'short' })}
      </span>
    )
  }
  return null
}
