import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams } from 'react-router'
import { api, RequestFailed } from '../../lib/api'
import { can, useMe } from '../../lib/session'
import {
  Card,
  dateTime,
  Empty,
  ErrorBanner,
  Loading,
  num,
  PageHeader,
  StatusPill,
} from '../../components/ui'
import { shortId } from './rights'

/**
 * A link as `GET /api/v1/links/{id}` returns it.
 *
 * `legal_hold` and `clicks` are optional because the Go presenter does not send
 * them yet. Who froze a link, when, and on what authority is the entire point of
 * the record, so the screen asks for it and says plainly when it is missing
 * rather than leaving a frozen link looking like an unexplained outage.
 */
interface LinkResponse {
  id: string
  code: string
  short_url: string
  qr_url: string
  target_url?: string
  form_id?: string | null
  expires_at?: string | null
  status: string
  created_at: string

  label?: string | null
  clicks?: number | null
  legal_hold?: {
    reason: string
    set_by?: string | null
    set_by_name?: string | null
    set_at?: string | null
  } | null
}

/**
 * Freezing a link for legal reasons -- and the record of why.
 *
 * A legal hold is not a delete and not an outage: the data stays, collection
 * stops, and retention's automatic purge is suspended. That last part is the one
 * that surprises people, so the screen states it as an intentional exception
 * rather than letting somebody later find undeleted data past its retention date
 * and file it as a bug.
 */
export function LegalHold() {
  const { projectId, linkId } = useParams()
  const navigate = useNavigate()
  const me = useMe()
  const qc = useQueryClient()

  const link = useQuery({
    queryKey: ['link', linkId],
    queryFn: () => api.get<LinkResponse>(`/api/v1/links/${linkId}`),
    enabled: Boolean(linkId),
  })

  const goBack = () => {
    if (projectId) navigate(`/p/${projectId}/links`)
    else navigate(-1)
  }

  const [reason, setReason] = useState('')
  const [ack, setAck] = useState(false)
  const [confirmRelease, setConfirmRelease] = useState(false)

  const setStatus = useMutation({
    mutationFn: async (input: { status: string; reason: string }) =>
      api.patch<LinkResponse>(`/api/v1/links/${linkId}`, {
        status: input.status,
        legal_hold_reason: input.reason,
      }),
    onSuccess: () => {
      setAck(false)
      setConfirmRelease(false)
      void qc.invalidateQueries({ queryKey: ['link', linkId] })
      void qc.invalidateQueries({ queryKey: ['links'] })
    },
  })

  if (link.isPending) return <Loading label="Đang tải link…" />
  if (link.isError)
    return (
      <div className="p-6">
        <ErrorBanner error={link.error} retry={() => void link.refetch()} />
      </div>
    )
  if (!link.data)
    return (
      <div className="p-6">
        <Empty title="Không tìm thấy link" hint="Link đã bị xoá, hoặc không thuộc tổ chức này." />
      </div>
    )

  const l = link.data
  const held = l.status === 'legal_hold'
  const allowed = can(me.data, 'link.write')
  const failure = setStatus.error instanceof RequestFailed ? setStatus.error : null
  const reasonError = failure?.fields.legal_hold_reason ?? failure?.fields.reason

  return (
    <div className="max-w-2xl p-6">
      <PageHeader
        title={held ? 'Link đang bị giữ vì lý do pháp lý' : 'Tạm ngưng vì lý do pháp lý'}
        meta="link.status → legal_hold · cần link.write"
        actions={
          <button
            type="button"
            className="btn"
            onClick={goBack}
          >
            Huỷ
          </button>
        }
      />

      <div className="mb-3 flex items-center justify-between gap-3 rounded border border-line bg-surface px-3 py-2.5">
        <div className="min-w-0">
          <div className="truncate text-body font-semibold">{l.label ?? `/r/${l.code}`}</div>
          <div className="id-chip truncate">
            {l.short_url}
            {typeof l.clicks === 'number' ? ` · ${num(l.clicks)} lượt quét` : ' · lượt quét: —'}
          </div>
        </div>
        <StatusPill tone={held ? 'overdue' : 'neutral'}>{l.status}</StatusPill>
      </div>

      {held ? (
        <div className="flex flex-col gap-3">
          <Card title="Hold hiện tại" aside={shortId(l.id)}>
            <dl className="grid grid-cols-[110px_1fr] gap-x-3 gap-y-1.5 text-body">
              <dt className="font-mono text-meta tracking-caps text-faint">LÝ DO</dt>
              <dd className="whitespace-pre-wrap">
                {l.legal_hold?.reason ?? (
                  <span className="text-duesoon">
                    ⚠ Không đọc được lý do — API chưa trả kèm bản ghi hold. Tra{' '}
                    <span className="id-chip">link.legal_hold</span> trong nhật ký audit trước khi gỡ.
                  </span>
                )}
              </dd>
              <dt className="font-mono text-meta tracking-caps text-faint">NGƯỜI ĐẶT</dt>
              <dd>{l.legal_hold?.set_by_name ?? l.legal_hold?.set_by ?? '—'}</dd>
              <dt className="font-mono text-meta tracking-caps text-faint">THỜI ĐIỂM</dt>
              <dd>{dateTime(l.legal_hold?.set_at)}</dd>
            </dl>
          </Card>

          <RetentionOverrideNote />

          <Card title="Gỡ legal hold">
            {!allowed && (
              <p role="status" className="mb-2 text-body text-duesoon">
                Bạn không có quyền <span className="id-chip">link.write</span> nên chỉ xem được.
              </p>
            )}
            {!confirmRelease ? (
              <>
                <p className="text-body leading-relaxed">
                  Gỡ hold trả link về trạng thái hoạt động và <strong>bỏ lớp bảo vệ</strong> đang giữ
                  dữ liệu lại. Chỉ gỡ khi căn cứ pháp lý đã chấm dứt và có bằng chứng cho việc đó.
                </p>
                <div className="mt-2.5 flex justify-end">
                  <button
                    type="button"
                    className="btn"
                    disabled={!allowed}
                    onClick={() => setConfirmRelease(true)}
                  >
                    Gỡ legal hold
                  </button>
                </div>
              </>
            ) : (
              <div className="rounded border border-overdue bg-overdue/5 p-3">
                <h3 className="text-body font-semibold text-overdue">Xác nhận gỡ hold</h3>
                <p className="mt-1.5 text-body leading-relaxed">
                  Ngay khi gỡ, hàng đợi xoá theo hạn lưu trữ lại chạy trên các bản ghi của link này:
                  bản ghi nào đã quá <span className="id-chip">purge_at</span> sẽ bị xoá ở lượt quét
                  tiếp theo, và các yêu cầu <span className="id-chip">erase</span> đang bị chặn sẽ
                  được thực hiện — bao gồm crypto-shred field nhạy cảm, không khôi phục được kể cả từ
                  bản sao lưu. Dữ liệu bạn đang giữ để phục vụ vụ việc pháp lý có thể biến mất trong
                  vòng vài giờ.
                </p>
                <label className="mt-2.5 flex items-start gap-2 text-body">
                  <input
                    type="checkbox"
                    checked={ack}
                    onChange={(e) => setAck(e.target.checked)}
                    className="mt-0.5"
                  />
                  <span>
                    Căn cứ pháp lý đã chấm dứt và tôi chấp nhận việc dữ liệu quá hạn lưu bị xoá.
                  </span>
                </label>
                <div className="mt-3 flex justify-end gap-2">
                  <button
                    type="button"
                    className="btn"
                    onClick={() => {
                      setConfirmRelease(false)
                      setAck(false)
                    }}
                  >
                    Quay lại
                  </button>
                  <button
                    type="button"
                    disabled={!allowed || !ack || setStatus.isPending}
                    onClick={() => setStatus.mutate({ status: 'active', reason: reason.trim() })}
                    className="rounded border border-overdue bg-overdue px-3 py-1.5 text-body font-semibold text-white disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    {setStatus.isPending ? 'Đang gỡ…' : 'Gỡ hold — dữ liệu quá hạn sẽ bị xoá'}
                  </button>
                </div>
              </div>
            )}
            {failure && (
              <div className="mt-2">
                <ErrorBanner error={failure} />
              </div>
            )}
          </Card>
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          <div>
            <label htmlFor="hold-reason" className="mb-1 block text-meta font-semibold">
              Lý do nội bộ · bắt buộc
            </label>
            <textarea
              id="hold-reason"
              rows={3}
              className="input"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              disabled={!allowed || setStatus.isPending}
              placeholder="Ví dụ: Yêu cầu của Thanh tra Sở TT&TT ngày 05/08/2026, hồ sơ 2026/TT-114."
            />
            <p className="mt-1 text-meta text-muted">
              Lý do đi vào nhật ký bất biến và là thứ tổ chức dùng để giải thích vì sao dữ liệu bị giữ
              quá hạn lưu. Người đọc sau này có thể không phải bạn — viết đủ để họ hiểu.
            </p>
            {reasonError && (
              <p role="alert" className="mt-1 text-meta text-overdue">
                {reasonError}
              </p>
            )}
          </div>

          <div className="rounded border border-accent bg-accent/5 p-3">
            <h2 className="text-body font-semibold text-accent">Khi bật, hệ thống sẽ</h2>
            <ul className="mt-1.5 flex flex-col gap-1.5 text-body leading-relaxed">
              <li>
                · Trả <span className="id-chip">451</span> cho mọi lượt quét, không tiết lộ lý do
              </li>
              <li>· Giữ nguyên toàn bộ bản ghi đã thu — kể cả bản ghi nằm trong yêu cầu xoá đang chờ</li>
              <li>· Chặn xoá link và chặn xoá bản ghi cho tới khi gỡ hold</li>
              <li>· Ghi người bật, thời điểm và lý do vào nhật ký bất biến</li>
            </ul>
          </div>

          <RetentionOverrideNote />

          {!allowed && (
            <p role="status" className="text-body text-duesoon">
              Bạn không có quyền <span className="id-chip">link.write</span> nên không bật được hold.
            </p>
          )}

          <label className="flex items-start gap-2 text-body">
            <input
              type="checkbox"
              checked={ack}
              onChange={(e) => setAck(e.target.checked)}
              className="mt-0.5"
              disabled={!allowed}
            />
            <span>
              Tôi hiểu hold này <strong>ghi đè quyền xoá của chủ thể dữ liệu</strong> và ghi đè việc
              tự động xoá theo hạn lưu trữ, và tôi vẫn phải trả lời chủ thể bằng văn bản trong hạn
              SLA.
            </span>
          </label>

          {failure && !reasonError && (
            <ErrorBanner error={failure} />
          )}

          {setStatus.isSuccess && (
            <p role="status" className="text-body font-semibold text-ok">
              Đã bật legal hold. Link trả 451 và mọi thao tác xoá lên dữ liệu của nó bị chặn.
            </p>
          )}

          <div className="flex gap-2">
            <button
              type="button"
              disabled={!allowed || !ack || reason.trim() === '' || setStatus.isPending}
              onClick={() => setStatus.mutate({ status: 'legal_hold', reason: reason.trim() })}
              className="flex-1 rounded border border-accent bg-accent px-3 py-2 text-body font-semibold text-white disabled:cursor-not-allowed disabled:opacity-50"
            >
              {setStatus.isPending ? 'Đang bật…' : 'Bật legal hold'}
            </button>
            <button
              type="button"
              className="btn"
              onClick={goBack}
            >
              Huỷ
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

/**
 * The sentence that stops a legal hold being mistaken for a bug.
 *
 * Retention deletes data automatically; a hold suspends that. Somebody auditing
 * later will find records kept past their stated retention period, and they need
 * to see on the same screen that this is the intended exception, with a reason
 * attached, and not a sweeper that quietly stopped working.
 */
function RetentionOverrideNote() {
  return (
    <div className="rounded border border-dashed border-line px-3 py-2.5 text-meta leading-relaxed text-muted">
      <p className="font-semibold text-ink">
        Legal hold ghi đè việc tự động xoá theo hạn lưu — đây là ngoại lệ có chủ đích, không phải lỗi.
      </p>
      <p className="mt-1">
        Trong lúc hold còn hiệu lực, sweeper hạn lưu bỏ qua các bản ghi này, nên chúng được giữ lại
        vượt quá chính sách lưu trữ đã công bố. Đó là lý do lý do nội bộ ở trên là bắt buộc: nó là
        căn cứ để giải thích vì sao dữ liệu vẫn còn.
      </p>
      <p className="mt-1">
        Legal hold cũng thắng quyền xoá của chủ thể dữ liệu. Yêu cầu <span className="id-chip">erase</span>{' '}
        liên quan sẽ dừng ở <span className="id-chip">in_progress</span> kèm ghi chú, đồng hồ SLA vẫn
        chạy — người phụ trách phải trả lời chủ thể bằng văn bản, không được im lặng.
      </p>
    </div>
  )
}
