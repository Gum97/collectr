import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from 'react-router'
import { api, RequestFailed } from '../../lib/api'
import { can, useMe } from '../../lib/session'
import {
  Card,
  dateTime,
  deadline,
  Empty,
  ErrorBanner,
  Loading,
  num,
  PageHeader,
  SensitiveTag,
  StatusPill,
} from '../../components/ui'
import { useDSRRequests, type DSRRow, type DSRScope } from './DSRQueue'
import {
  isClosed,
  maskIdentifier,
  REJECT_GROUNDS,
  rightOf,
  shortId,
  STATUS_LABEL,
  type Right,
} from './rights'

type Stage = 'idle' | 'confirm' | 'reject'

/**
 * Handling one exercise of a data subject right.
 *
 * The screen is built around a single rule: nothing consequential happens
 * without the consequence being stated first, in the words of what is lost. An
 * erasure here shreds a per-subject encryption key, which no backup restores --
 * so the preview comes before the button, and the button comes after a separate
 * acknowledgement, not next to it.
 */
export function DSRRequest() {
  const { requestId } = useParams()
  const me = useMe()
  const qc = useQueryClient()
  // No single-request endpoint exists, so the row comes from the same list the
  // queue reads -- including closed requests, which is why the scope is 'all'.
  const list = useDSRRequests('all')
  const row = list.data?.data.find((r) => r.id === requestId)

  const [stage, setStage] = useState<Stage>('idle')
  const [note, setNote] = useState('')
  const [ground, setGround] = useState<string>(REJECT_GROUNDS[0].id)
  const [ack, setAck] = useState(false)

  const resolve = useMutation({
    mutationFn: async (input: { outcome: 'fulfill' | 'reject'; note: string }) =>
      api.post<{ id: string; status: string; restricted_submissions?: number }>(
        `/api/v1/dsr/requests/${requestId}/${input.outcome}`,
        { note: input.note },
      ),
    onSuccess: () => {
      setStage('idle')
      setAck(false)
      void qc.invalidateQueries({ queryKey: ['dsr', 'requests'] })
    },
  })

  if (list.isPending) return <Loading label="Đang tải yêu cầu…" />
  if (list.isError)
    return (
      <div className="p-6">
        <ErrorBanner error={list.error} retry={() => void list.refetch()} />
      </div>
    )

  if (!row) {
    return (
      <div className="p-6">
        <Empty
          title="Không tìm thấy yêu cầu này"
          hint={
            <>
              Yêu cầu không thuộc tổ chức của bạn, hoặc nằm ngoài 200 yêu cầu gần hạn nhất mà API
              danh sách trả về (chưa có endpoint đọc một yêu cầu theo mã).{' '}
              <Link to="/compliance/dsr" className="underline">
                Về hàng đợi
              </Link>
              .
            </>
          }
        />
      </div>
    )
  }

  const right = rightOf(row.type)
  const closed = isClosed(row.status)
  const d = deadline(row.due_at)
  const overdue = row.overdue || d.overdue
  const allowed = can(me.data, 'dsr.handle')
  const failure = resolve.error instanceof RequestFailed ? resolve.error : null
  const noteError = failure?.fields.note

  return (
    <div className="max-w-3xl p-6">
      <PageHeader
        title={right.title}
        meta={
          <>
            TUÂN THỦ / YÊU CẦU / {shortId(row.id)} · {maskIdentifier(row.subject_identifier)} ·{' '}
            {row.verification_method ?? 'magic_link'} lúc {dateTime(row.verified_at ?? row.received_at)}
          </>
        }
        actions={
          <div
            className={`rounded border px-2.5 py-1.5 text-right ${
              overdue ? 'border-overdue text-overdue' : 'border-line'
            }`}
          >
            <div className="font-mono text-meta tracking-caps">HẠN SLA</div>
            <div className="text-lede font-semibold">
              {closed ? closedVerdict(row) : overdue && !d.overdue ? 'đã quá hạn' : d.text}
            </div>
            <div className="id-chip">hạn {dateTime(row.due_at)}</div>
          </div>
        }
      />

      <p className="mb-3 text-body text-muted">{right.summary}</p>

      <div className="mb-3 flex flex-wrap items-center gap-2">
        <StatusPill>{STATUS_LABEL[row.status] ?? row.status}</StatusPill>
        <span className="id-chip">nhận lúc {dateTime(row.received_at)}</span>
        {row.needs_human && !closed && <StatusPill tone="accent">cần người quyết định</StatusPill>}
        {right.irreversible && <StatusPill tone="overdue">không đảo ngược được</StatusPill>}
      </div>

      <div className="flex flex-col gap-3">
        <ScopeCard scope={row.scope} row={row} />

        <Card title="Sẽ xảy ra khi bạn thực hiện">
          {right.steps.length === 0 ? (
            <p className="text-body text-duesoon">
              Giao diện chưa mô tả được hệ quả của loại quyền <span className="id-chip">{row.type}</span>.
              Đừng thực hiện cho tới khi xác định được hệ quả bằng cách khác.
            </p>
          ) : (
            <ol className="flex flex-col gap-1.5 text-body leading-relaxed">
              {right.steps.map((s, i) => {
                const permanent = /crypto-shred|xóa cứng|xoá cứng/i.test(s)
                return (
                  <li key={s} className={permanent ? 'font-semibold text-overdue' : ''}>
                    {i + 1}. {s}
                    {permanent && <span className="ml-1 text-meta">— không đảo ngược</span>}
                  </li>
                )
              })}
            </ol>
          )}
        </Card>

        {closed ? (
          <Card title="Yêu cầu đã đóng">
            <p className="text-body">
              Đóng ở trạng thái <span className="id-chip">{row.status}</span> ·{' '}
              {STATUS_LABEL[row.status] ?? row.status}
              {row.fulfilled_at && ` lúc ${dateTime(row.fulfilled_at)}`}.
            </p>
            {row.note && (
              <p className="mt-1.5 whitespace-pre-wrap text-body text-muted">
                Ghi chú xử lý: {row.note}
              </p>
            )}
            <p className="mt-1.5 text-meta text-muted">
              Không thể mở lại từ màn này — API chỉ nhận quyết định trên yêu cầu còn mở. Chủ thể muốn
              khiếu nại thì mở một yêu cầu mới.
            </p>
          </Card>
        ) : (
          <Card title="Quyết định">
            {!allowed && (
              <p role="status" className="mb-2 text-body text-duesoon">
                Bạn không có quyền <span className="id-chip">dsr.handle</span> nên chỉ xem được. Nhờ
                người có quyền xử lý — đồng hồ SLA vẫn đang chạy.
              </p>
            )}

            <label htmlFor="dsr-note" className="mb-1 block text-meta font-semibold">
              Ghi chú xử lý{stage === 'reject' ? ' · bắt buộc khi từ chối' : ''}
            </label>
            <textarea
              id="dsr-note"
              rows={3}
              className="input"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              disabled={!allowed || resolve.isPending}
              placeholder="Ghi lại điều bạn đã kiểm tra và căn cứ của quyết định. Nội dung này vào nhật ký audit bất biến."
            />
            {noteError && (
              <p role="alert" className="mt-1 text-meta text-overdue">
                {noteError}
              </p>
            )}

            {stage === 'reject' && (
              <fieldset className="mt-3 rounded border border-line p-3">
                <legend className="px-1 text-meta font-semibold">
                  Không thực hiện được toàn bộ? Nêu căn cứ từ chối
                </legend>
                <div className="flex flex-col gap-1.5">
                  {REJECT_GROUNDS.map((g) => (
                    <label key={g.id} className="flex items-center gap-2 text-body">
                      <input
                        type="radio"
                        name="reject-ground"
                        value={g.id}
                        checked={ground === g.id}
                        onChange={() => setGround(g.id)}
                      />
                      {g.label}
                    </label>
                  ))}
                </div>
                <p className="mt-2 text-meta text-muted">
                  Từ chối là một kết quả hợp lệ, nhưng chủ thể có quyền khiếu nại — căn cứ và ghi chú
                  ở trên là thứ tổ chức dùng để giải thích quyết định.
                </p>
              </fieldset>
            )}

            {stage === 'confirm' && (
              <ConfirmPanel
                right={right}
                scope={row.scope}
                ack={ack}
                setAck={setAck}
                onCancel={() => {
                  setStage('idle')
                  setAck(false)
                }}
                onConfirm={() => resolve.mutate({ outcome: 'fulfill', note })}
                pending={resolve.isPending}
              />
            )}

            {failure && !noteError && <ConflictOrError failure={failure} onReload={() => void list.refetch()} />}

            {stage !== 'confirm' && (
              <div className="mt-3 flex flex-wrap justify-end gap-2">
                {stage === 'reject' ? (
                  <>
                    <button type="button" className="btn" onClick={() => setStage('idle')}>
                      Quay lại
                    </button>
                    <button
                      type="button"
                      className="btn-primary"
                      disabled={!allowed || resolve.isPending || note.trim() === ''}
                      onClick={() =>
                        resolve.mutate({
                          outcome: 'reject',
                          note: `${groundLabel(ground)} — ${note.trim()}`,
                        })
                      }
                    >
                      {resolve.isPending ? 'Đang ghi…' : 'Từ chối kèm căn cứ'}
                    </button>
                  </>
                ) : (
                  <>
                    <button
                      type="button"
                      className="btn"
                      disabled={!allowed}
                      onClick={() => setStage('reject')}
                    >
                      Từ chối kèm căn cứ
                    </button>
                    <button
                      type="button"
                      disabled={!allowed}
                      onClick={() => setStage('confirm')}
                      className={
                        right.irreversible
                          ? 'rounded border border-overdue bg-overdue px-3 py-1.5 text-body font-semibold text-white hover:bg-overdue/90 disabled:cursor-not-allowed disabled:opacity-50'
                          : 'btn-primary'
                      }
                    >
                      {right.actionLabel}
                    </button>
                  </>
                )}
              </div>
            )}

            {resolve.isSuccess && (
              <p role="status" className="mt-2 text-body font-semibold text-ok">
                Đã ghi quyết định.
                {typeof resolve.data.restricted_submissions === 'number' &&
                  ` ${num(resolve.data.restricted_submissions)} bản ghi đã chuyển sang trạng thái hạn chế xử lý.`}
              </p>
            )}
          </Card>
        )}
      </div>
    </div>
  )
}

/**
 * The confirmation step, separated from the button that opens it.
 *
 * Two clicks in the same place is a habit; a click, then reading a sentence
 * about what disappears, then a deliberate acknowledgement, is a decision.
 */
function ConfirmPanel({
  right,
  scope,
  ack,
  setAck,
  onCancel,
  onConfirm,
  pending,
}: {
  right: Right
  scope: DSRScope | null | undefined
  ack: boolean
  setAck: (v: boolean) => void
  onCancel: () => void
  onConfirm: () => void
  pending: boolean
}) {
  const tone = right.irreversible ? 'border-overdue bg-overdue/5' : 'border-line'
  return (
    <div className={`mt-3 rounded border p-3 ${tone}`} role="group" aria-labelledby="confirm-h">
      <h3
        id="confirm-h"
        className={`text-body font-semibold ${right.irreversible ? 'text-overdue' : ''}`}
      >
        {right.irreversible ? 'Xác nhận huỷ dữ liệu vĩnh viễn' : 'Xác nhận quyết định'}
      </h3>

      <p className="mt-1.5 text-body leading-relaxed">{right.confirmSentence}</p>

      {right.irreversible &&
        (scope ? (
          <p className="mt-1.5 text-body font-semibold">
            Cụ thể: {num(scope.submissions)} bản ghi và {num(scope.files)} tệp đính kèm thuộc{' '}
            {num(scope.projects)} dự án sẽ bị xoá.
          </p>
        ) : (
          <p className="mt-1.5 text-body font-semibold text-duesoon">
            ⚠ Chưa đếm được chính xác bao nhiêu bản ghi sẽ mất — API chưa trả phạm vi ảnh hưởng.
            Thao tác này xoá <em>toàn bộ</em> bản ghi và tệp của chủ thể trong tổ chức này, kể cả
            phần bạn chưa nhìn thấy trên màn này.
          </p>
        ))}

      <label className="mt-2.5 flex items-start gap-2 text-body">
        <input
          type="checkbox"
          checked={ack}
          onChange={(e) => setAck(e.target.checked)}
          className="mt-0.5"
        />
        <span>
          {right.irreversible
            ? 'Tôi hiểu dữ liệu này không thể khôi phục, kể cả từ bản sao lưu, và tôi chịu trách nhiệm về quyết định.'
            : 'Tôi đã kiểm tra phạm vi ảnh hưởng và xác nhận quyết định này.'}
        </span>
      </label>

      <div className="mt-3 flex justify-end gap-2">
        <button type="button" className="btn" onClick={onCancel} disabled={pending}>
          Quay lại
        </button>
        <button
          type="button"
          disabled={!ack || pending}
          onClick={onConfirm}
          className={
            right.irreversible
              ? 'rounded border border-overdue bg-overdue px-3 py-1.5 text-body font-semibold text-white disabled:cursor-not-allowed disabled:opacity-50'
              : 'btn-primary'
          }
        >
          {pending ? 'Đang thực hiện…' : right.actionLabel}
        </button>
      </div>
    </div>
  )
}

/** Impact, or an honest statement that impact is unknown. */
function ScopeCard({ scope, row }: { scope: DSRScope | null | undefined; row: DSRRow }) {
  if (!scope) {
    return (
      <Card title="Phạm vi ảnh hưởng" aside="chưa đo được">
        <p className="text-body text-duesoon">
          ⚠ Chưa có API trả về số bản ghi, tệp và dự án mà yêu cầu này chạm tới. Màn này cố ý{' '}
          <strong>không</strong> hiện số 0: chưa đếm được khác với không có gì.
        </p>
        <p className="mt-1.5 text-meta text-muted">
          Chủ thể: {maskIdentifier(row.subject_identifier)} · mã nội bộ{' '}
          <span className="id-chip">{shortId(row.data_subject_id)}</span>
        </p>
      </Card>
    )
  }

  return (
    <Card title="Phạm vi ảnh hưởng">
      <div className="grid grid-cols-3 gap-3">
        <Stat label="BẢN GHI" value={scope.submissions} />
        <Stat label="TỆP ĐÍNH KÈM" value={scope.files} />
        <Stat label="DỰ ÁN" value={scope.projects} />
      </div>
      {scope.items && scope.items.length > 0 && (
        <ul className="mt-2.5 flex flex-col gap-1.5 border-t border-dashed border-line pt-2.5">
          {scope.items.map((it) => (
            <li key={it.id} className="flex items-center justify-between gap-3 text-body">
              <span className="truncate">
                {it.form_title}
                {it.project_name && <span className="id-chip"> · {it.project_name}</span>}{' '}
                <span className="id-chip">{shortId(it.id)}</span>
              </span>
              <span className="shrink-0">
                {it.has_sensitive ? <SensitiveTag>có field nhạy cảm</SensitiveTag> : null}
                {it.note && <span className="id-chip ml-1.5">{it.note}</span>}
                {!it.has_sensitive && !it.note && <span className="id-chip">—</span>}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  )
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div>
      <div className="font-mono text-meta tracking-caps text-faint">{label}</div>
      <div className="text-[15px] font-semibold">{num(value)}</div>
    </div>
  )
}

/** A 409 here means somebody else already decided; that is not a failure to
 *  retry, it is news the reader has to act on. */
function ConflictOrError({ failure, onReload }: { failure: RequestFailed; onReload: () => void }) {
  if (failure.status === 409) {
    return (
      <div
        role="alert"
        className="mt-2 rounded border border-duesoon/50 bg-duesoon/5 px-3 py-2 text-body text-duesoon"
      >
        <p className="font-semibold">{failure.body.title}</p>
        <p className="mt-0.5">
          Nhiều khả năng một người khác vừa xử lý yêu cầu này. Tải lại để xem quyết định đã ghi.
        </p>
        <button type="button" className="btn mt-2" onClick={onReload}>
          Tải lại
        </button>
      </div>
    )
  }
  return (
    <div className="mt-2">
      <ErrorBanner error={failure} />
    </div>
  )
}

function groundLabel(id: string): string {
  return REJECT_GROUNDS.find((g) => g.id === id)?.label ?? id
}

/** Whether a closed request was answered in time, said plainly or not at all. */
function closedVerdict(row: DSRRow): string {
  if (row.answered_late === true) return 'trả lời muộn'
  if (row.answered_late === false) return 'đúng hạn'
  if (row.fulfilled_at) {
    return new Date(row.fulfilled_at).getTime() > new Date(row.due_at).getTime()
      ? 'trả lời muộn'
      : 'đúng hạn'
  }
  return 'chưa ghi nhận'
}
