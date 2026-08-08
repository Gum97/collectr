/**
 * Hash-chain verification for the audit log.
 *
 * Every entry stores the hash of the entry before it, so an entry's hash depends
 * on the entire history preceding it. Editing or deleting one row leaves every
 * later hash wrong, and this endpoint recomputes the whole chain to find the
 * first place that happens.
 *
 * What it proves and what it does not: this is tamper-*evident*, not
 * tamper-proof. Whoever holds the database owner's credentials could rebuild the
 * chain from the altered row forward. What it defeats is a quiet edit -- and
 * since the application's database role holds only INSERT and SELECT on
 * audit.entries, the process handling personal data cannot rewrite its own
 * record of what it did.
 *
 * A break is not a warning. It means the log can no longer speak for itself from
 * that sequence number on, which is the evidentiary basis for every access to
 * personal data recorded after it. It is drawn accordingly.
 */
import { useMutation } from '@tanstack/react-query'
import { api } from '../../lib/api'
import { ErrorBanner, StatusPill, dateTime, num } from '../../components/ui'

/** Mirrors audit.VerifyResult in the Go service, field for field. */
export interface VerifyResult {
  tenant_id: string
  entries: number
  valid: boolean
  /** Sequence number where verification first failed. Absent when valid. */
  broken_at?: number
  reason?: string
}

export interface ChainVerify {
  run: () => void
  running: boolean
  result: VerifyResult | undefined
  error: unknown
  /** When the result on screen was produced. */
  at: number | undefined
}

export function useChainVerify(): ChainVerify {
  const m = useMutation({
    mutationFn: () => api.post<VerifyResult>('/api/v1/audit/verify'),
  })
  return {
    run: () => m.mutate(),
    running: m.isPending,
    result: m.data,
    error: m.error,
    at: m.isSuccess ? m.submittedAt : undefined,
  }
}

export function VerifyButton({ verify, disabled }: { verify: ChainVerify; disabled?: boolean }) {
  return (
    <button
      type="button"
      className="btn"
      onClick={verify.run}
      disabled={verify.running || disabled}
      aria-describedby="chain-panel"
    >
      {verify.running ? 'Đang kiểm…' : 'Kiểm toàn vẹn'}
    </button>
  )
}

export function VerifyPanel({ verify }: { verify: ChainVerify }) {
  if (verify.error) {
    return (
      <div id="chain-panel">
        <ErrorBanner error={verify.error} retry={verify.run} />
      </div>
    )
  }

  if (verify.running) {
    return (
      <p
        id="chain-panel"
        role="status"
        className="rounded border border-line bg-surface px-3 py-2 text-body"
      >
        Đang tính lại toàn bộ chuỗi hash… Với nhật ký lớn việc này mất vài giây vì mỗi bản ghi phải
        được băm lại theo đúng thứ tự.
      </p>
    )
  }

  const res = verify.result
  if (!res) return <ChainExplainer />
  if (res.valid) return <ChainIntact result={res} at={verify.at} />
  return <ChainBroken result={res} at={verify.at} />
}

/** Before anyone has pressed the button. Explains the mechanism rather than
 *  showing a green tick nobody has earned yet. */
function ChainExplainer() {
  return (
    <section
      id="chain-panel"
      className="rounded border border-line bg-surface px-3 py-2 text-body"
    >
      <h2 className="font-semibold">Chuỗi hash bảo vệ nhật ký này như thế nào</h2>
      <p className="mt-1 text-muted">
        Mỗi bản ghi mang theo <span className="font-mono">sha256</span> của bản ghi ngay trước nó,
        nên hash của một dòng phụ thuộc vào toàn bộ lịch sử đứng trước dòng đó. Sửa nội dung hay xoá
        bớt một dòng sẽ làm sai hash của chính nó <em>và của mọi dòng phía sau</em> — không thể sửa
        lén một dòng rồi để nguyên phần còn lại.
      </p>
      <p className="mt-1 text-muted">
        Bấm <strong>Kiểm toàn vẹn</strong> để tính lại chuỗi và xem nó còn khớp không. Đây là bằng
        chứng <em>phát hiện</em> can thiệp, không phải chống can thiệp: người nắm quyền owner của
        database vẫn có thể dựng lại cả chuỗi. Thứ nó chặn là chỉnh sửa âm thầm — và vì role ứng
        dụng chỉ có quyền INSERT/SELECT trên <span className="font-mono">audit.entries</span>, chính
        hệ thống đang xử lý dữ liệu cá nhân không thể viết lại hồ sơ về việc mình đã làm.
      </p>
    </section>
  )
}

function ChainIntact({ result, at }: { result: VerifyResult; at: number | undefined }) {
  return (
    <section
      id="chain-panel"
      role="status"
      className="flex flex-wrap items-center gap-3 rounded border border-ok bg-ok/5 px-3 py-2"
    >
      <span className="text-body font-semibold text-ok">
        <span aria-hidden>✓</span> Chuỗi nguyên vẹn
      </span>
      <span className="id-chip">
        kiểm {num(result.entries)} bản ghi
        {at ? ` lúc ${dateTime(new Date(at).toISOString())}` : ''} · mọi hash khớp với nội dung và
        với bản ghi liền trước
      </span>
    </section>
  )
}

/**
 * A break, drawn as what it is.
 *
 * Full width, alert role, the sequence number stated in the largest type on the
 * screen. A broken chain has always meant either an edit that bypassed the
 * application or a restore that lost rows, and both need a person to look at the
 * database today -- not a grey line somebody scrolls past.
 */
function ChainBroken({ result, at }: { result: VerifyResult; at: number | undefined }) {
  return (
    <section
      id="chain-panel"
      role="alert"
      className="rounded border border-overdue bg-overdue/10 p-4"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span aria-hidden className="text-[18px] leading-none text-overdue">
          ⚠
        </span>
        <h2 className="text-[15px] font-semibold text-overdue">CHUỖI HASH ĐỨT GÃY</h2>
        <StatusPill tone="overdue">nghiêm trọng</StatusPill>
      </div>

      <p className="mt-2 text-lede">
        Đứt tại bản ghi <span className="font-mono text-[15px] font-semibold">seq {num(result.broken_at ?? 0)}</span>{' '}
        trong {num(result.entries)} bản ghi đã kiểm
        {at ? ` lúc ${dateTime(new Date(at).toISOString())}` : ''}.
      </p>

      {result.reason && (
        <p className="mt-1 rounded border border-overdue/40 bg-surface px-2 py-1 font-mono text-meta text-overdue">
          {result.reason}
        </p>
      )}

      <div className="mt-3 grid grid-cols-1 gap-3 text-body md:grid-cols-2">
        <div>
          <h3 className="font-semibold">Nghĩa là gì</h3>
          <ul className="mt-1 list-disc pl-4 text-muted">
            <li>
              Bản ghi <span className="font-mono">seq {num(result.broken_at ?? 0)}</span> và{' '}
              <strong>mọi bản ghi sau nó</strong> không còn tự chứng minh được nữa: hash của chúng
              phụ thuộc vào phần đã sai.
            </li>
            <li>
              Phần trước điểm đứt vẫn đứng vững — chuỗi đã được tính lại tới đúng chỗ này thì mới
              hỏng.
            </li>
            <li>
              Chỉ có hai nguyên nhân: một thao tác ghi đè đi vòng qua ứng dụng, hoặc một lần khôi
              phục/di trú làm mất dòng.
            </li>
          </ul>
        </div>
        <div>
          <h3 className="font-semibold">Phải làm gì bây giờ</h3>
          <ul className="mt-1 list-disc pl-4 text-muted">
            <li>Báo DPO và người vận hành deployment ngay, kèm số seq ở trên.</li>
            <li>
              Không xoá, không &ldquo;sửa lại cho khớp&rdquo;. Việc dựng lại chuỗi sẽ xoá luôn bằng
              chứng về chính sự cố này.
            </li>
            <li>
              Đối chiếu với bản ký ở <span className="font-mono">audit.checkpoints</span> để khoanh
              vùng thời điểm, và soát log truy cập database quanh mốc đó.
            </li>
          </ul>
        </div>
      </div>
    </section>
  )
}
