/**
 * The publish tab: what changes, and what it does to data already collected.
 *
 * Publishing is the one irreversible action in the builder. A published version
 * is immutable -- there is no editing it afterwards, only publishing another one
 * -- so this screen exists to make the consequences legible *before* the click,
 * not to confirm that the reader meant to click.
 *
 * The classification is the server's (POST /draft/validate returns the same diff
 * the publish will run). Re-deriving breaking vs non-breaking here would give a
 * screen that promises one thing and an API that does another.
 */
import { ErrorBanner, Loading, StatusPill } from '../../components/ui'
import {
  CLASS_BLOCKED,
  CLASS_BREAKING,
  type Change,
  type DraftSchema,
  type FormDetail,
  SEVERITY_ERROR,
  type VersionRow,
  changeMark,
  changeText,
  hasSensitive,
  issueText,
  usePublish,
  usePublishPreview,
} from './useDraft'

interface Props {
  formId: string
  form: FormDetail
  schema: DraftSchema
  versions: VersionRow[] | undefined
  canPublish: boolean
  dirty: boolean
  onSaveFirst: () => void
  onBack: () => void
}

export function PublishDialog({
  formId,
  form,
  schema,
  versions,
  canPublish,
  dirty,
  onSaveFirst,
  onBack,
}: Props) {
  const preview = usePublishPreview(formId, true)
  const publish = usePublish(formId)

  const live = versions?.find((v) => v.id === form.live_version_id) ?? null
  const nextNo = (versions ?? []).reduce((m, v) => Math.max(m, v.version_no), 0) + 1

  const validation = preview.data?.validation
  const diff = preview.data?.diff
  const issues = validation?.issues ?? []
  const errors = issues.filter((i) => i.severity === SEVERITY_ERROR)
  const warnings = issues.filter((i) => i.severity !== SEVERITY_ERROR)
  const changes = diff?.changes ?? []
  const breaking = changes.filter((c) => c.class === CLASS_BREAKING || c.class === CLASS_BLOCKED)
  const others = changes.filter((c) => c.class !== CLASS_BREAKING && c.class !== CLASS_BLOCKED)

  const blocked = errors.length > 0 || Boolean(diff?.blocked)
  const published = publish.data

  return (
    <div className="mx-auto w-full max-w-lg">
      <div className="rounded border border-line bg-surface">
        <header className="border-b border-line px-4 py-3">
          <h2 className="font-display text-[17px] font-semibold">Publish v{nextNo}</h2>
          <p className="id-chip">
            {live ? `so với v${live.version_no} live` : 'chưa từng publish'} · published version là
            bất biến
          </p>
        </header>

        <div className="flex flex-col gap-3 px-4 py-4">
          {published ? (
            <div role="status" className="rounded border border-ok bg-ok/5 px-3 py-2.5">
              <p className="text-body font-semibold text-ok">
                Đã publish v{published.version_no}.
              </p>
              <p className="mt-1 text-meta leading-relaxed">
                Version này giờ là bản bất biến: không sửa được nữa, chỉ publish thêm version mới.
                Mọi link đang trỏ tới biểu mẫu tự dùng bản live mới nhất, không cần đổi gì. Bản ghi
                đã thu vẫn gắn với version mà người trả lời đã nhìn thấy.
              </p>
              <p className="id-chip mt-1">{published.version_id}</p>
              <button type="button" className="btn mt-2" onClick={onBack}>
                Về bản nháp
              </button>
            </div>
          ) : (
            <>
              {dirty && (
                <div
                  role="alert"
                  className="rounded border border-duesoon/50 bg-duesoon/5 px-3 py-2 text-meta text-duesoon"
                >
                  <p className="font-semibold">Còn thay đổi chưa lưu.</p>
                  <p className="mt-0.5">
                    So sánh bên dưới tính trên bản nháp đã lưu ở server, chưa có phần bạn vừa sửa.
                  </p>
                  <button type="button" className="btn mt-1.5 py-1 text-meta" onClick={onSaveFirst}>
                    Lưu nháp rồi so sánh lại
                  </button>
                </div>
              )}

              {preview.isPending && <Loading label="Đang so sánh nháp với bản live…" />}
              {preview.isError && <ErrorBanner error={preview.error} retry={() => void preview.refetch()} />}

              {errors.length > 0 && (
                <section className="rounded border-2 border-overdue bg-overdue/5 p-3">
                  <h3 className="mb-1.5 text-meta font-semibold text-overdue">
                    {errors.length} lỗi chặn publish
                  </h3>
                  <ul className="flex flex-col gap-1.5">
                    {errors.map((i, n) => (
                      <li key={n} className="text-meta leading-relaxed">
                        {issueText(schema, i)}
                        {i.target && <span className="id-chip"> {i.target}</span>}
                      </li>
                    ))}
                  </ul>
                  <p className="mt-2 text-meta text-overdue/80">
                    Sửa ở tab Soạn rồi quay lại. Chặn ở đây rẻ hơn nhiều so với một biểu mẫu live mà
                    người trả lời không bao giờ gửi được.
                  </p>
                </section>
              )}

              {breaking.length > 0 && (
                <section className="rounded border-2 border-accent bg-accent/5 p-3">
                  <h3 className="mb-1.5 text-meta font-semibold text-accent">
                    {breaking.length} thay đổi phá vỡ (breaking)
                  </h3>
                  <ul className="flex flex-col gap-1.5">
                    {breaking.map((c, n) => (
                      <li key={n} className="text-meta leading-relaxed">
                        {changeText(schema, c)}
                      </li>
                    ))}
                  </ul>
                  <p className="mt-2 text-meta text-accent-dark">
                    Không có thay đổi nào ghi đè dữ liệu đã thu. Cái đổi là cách bảng dữ liệu hiển
                    thị bản ghi cũ — chúng được thu dưới một văn bản đồng ý cụ thể và không được
                    viết lại.
                  </p>
                </section>
              )}

              {preview.data && (
                <section>
                  <h3 className="mb-2 font-mono text-meta tracking-caps text-faint">
                    THAY ĐỔI KHÁC
                  </h3>
                  {others.length === 0 && breaking.length === 0 ? (
                    <p className="text-meta text-muted">
                      {live
                        ? 'Schema field không khác gì bản live.'
                        : 'Đây là version đầu tiên, nên không có gì để so sánh.'}
                    </p>
                  ) : (
                    <ul className="flex flex-col gap-1.5">
                      {others.map((c, n) => (
                        <ChangeRow key={n} schema={schema} change={c} />
                      ))}
                    </ul>
                  )}
                  <RuleDelta schema={schema} live={live} />
                </section>
              )}

              {warnings.length > 0 && (
                <section className="rounded border border-duesoon/50 bg-duesoon/5 px-3 py-2">
                  <h3 className="mb-1 text-meta font-semibold text-duesoon">
                    {warnings.length} cảnh báo — publish vẫn chạy
                  </h3>
                  <ul className="flex flex-col gap-1">
                    {warnings.map((i, n) => (
                      <li key={n} className="text-meta leading-relaxed">
                        {issueText(schema, i)}
                      </li>
                    ))}
                  </ul>
                </section>
              )}

              <section className="rounded border border-line px-3 py-2.5">
                <h3 className="mb-1 text-meta font-semibold">Văn bản đồng ý</h3>
                <p className="text-meta leading-relaxed text-muted">
                  Version này khai báo {(schema.consent?.purposes ?? []).length} mục đích xử lý
                  {hasSensitive(schema) &&
                    (schema.consent?.sensitive_notice_required
                      ? ', kèm thông báo dữ liệu nhạy cảm'
                      : ', nhưng chưa bật thông báo dữ liệu nhạy cảm')}
                  . Publish biểu mẫu không đụng tới văn bản đồng ý. Nếu bạn sửa văn bản đó, người
                  đang điền dở sẽ nhận <span className="font-mono text-meta">409 consent_document_changed</span>{' '}
                  và phải đọc lại — không ghi nhận đồng ý theo văn bản cũ.
                </p>
              </section>

              <section className="rounded border border-dashed border-line px-3 py-2.5">
                <h3 className="mb-1 text-meta font-semibold">Link đang trỏ tới biểu mẫu này</h3>
                <p className="text-meta leading-relaxed text-muted">
                  Không cần đổi link nào — link luôn trỏ tới version live mới nhất. Người đang mở
                  biểu mẫu ở version cũ vẫn gửi được: version cũ bất biến nên không có gì mơ hồ để
                  giải quyết.
                </p>
                <p className="id-chip mt-1">
                  danh sách link cụ thể chưa có API lọc theo form — xem ở màn Link &amp; QR
                </p>
              </section>

              {publish.isError && <ErrorBanner error={publish.error} />}

              <div className="flex items-center justify-end gap-2 pt-1">
                <span className="mr-auto">
                  {blocked ? (
                    <StatusPill tone="overdue">chặn publish</StatusPill>
                  ) : preview.data ? (
                    <StatusPill tone="ok">sẵn sàng publish</StatusPill>
                  ) : null}
                </span>
                <button type="button" className="btn" onClick={onBack}>
                  Quay lại nháp
                </button>
                <button
                  type="button"
                  className="btn-primary"
                  disabled={!canPublish || blocked || publish.isPending || preview.isPending}
                  title={canPublish ? undefined : 'Tài khoản này không có quyền form.publish'}
                  onClick={() => publish.mutate()}
                >
                  {publish.isPending ? 'Đang publish…' : `Publish v${nextNo}`}
                </button>
              </div>
              {!canPublish && (
                <p className="text-right text-meta text-muted">
                  Tài khoản này không có quyền <span className="font-mono">form.publish</span>.
                </p>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}

function ChangeRow({ schema, change }: { schema: DraftSchema; change: Change }) {
  return (
    <li className="flex gap-2 text-meta leading-relaxed">
      <span className="w-12 shrink-0 font-mono text-meta text-faint">{changeMark(change.kind)}</span>
      <span>{changeText(schema, change)}</span>
    </li>
  )
}

/**
 * Rules and pages, counted.
 *
 * The server's diff only walks fields, so a new branch shows up nowhere in the
 * list above. A count taken from the versions endpoint is not a description of
 * what changed, but it is true, and silently showing nothing would let a
 * rewritten branch reach respondents unannounced.
 */
function RuleDelta({ schema, live }: { schema: DraftSchema; live: VersionRow | null }) {
  if (!live) return null
  const draftRules = (schema.rules ?? []).length
  const draftFields = Object.keys(schema.fields).length
  if (draftRules === live.rule_count && draftFields === live.field_count) return null

  return (
    <p className="mt-2 border-t border-dashed border-line pt-2 text-meta text-muted">
      Luật rẽ nhánh: v{live.version_no} có {live.rule_count} · nháp có {draftRules}. Field: v
      {live.version_no} có {live.field_count} · nháp có {draftFields}.{' '}
      <span className="text-meta">
        (API diff hiện chỉ phân loại thay đổi ở field, chưa liệt kê từng luật.)
      </span>
    </p>
  )
}
