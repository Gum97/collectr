/**
 * 4b -- hitting a rate limit, said three different ways.
 *
 * The same 429 means three different things depending on who is reading it, and
 * one shared "Too many requests" component would be wrong in all three places:
 *
 *  1. The person filling in a public form is a customer of whoever runs the
 *     form. They did nothing wrong, their answers are still in the page, and the
 *     only thing they need is how long to wait.
 *  2. The operator reading a report needs to know that blocked traffic exists
 *     and that it is *not* in the funnel numbers underneath -- otherwise a drop
 *     in submissions looks like a drop in interest.
 *  3. The data subject at the portal is the one case where the limiter fails
 *     closed, and where the wording must reveal nothing. Telling this person
 *     "too many attempts for this address" would confirm the address is known,
 *     which is exactly the disclosure the endpoint exists to prevent.
 *
 * Components here take their numbers as props rather than fetching: two of the
 * three render inside surfaces this module does not own (the public form runtime
 * and the DSR portal), and the third has no endpoint behind it yet.
 */
import { useEffect, useState, type ReactNode } from 'react'
import { RequestFailed } from '../../lib/api'
import { Card, PageHeader, num } from '../../components/ui'

/* ------------------------------------------------------------- detection */

export type Throttle =
  /** 429: the limiter answered and said no. */
  | { kind: 'limited'; retryAfterSeconds: number | null }
  /** 503: the limiter itself could not answer and the endpoint fails closed. */
  | { kind: 'unavailable'; retryAfterSeconds: number | null }

/**
 * Reads a throttle out of a failed request.
 *
 * `Retry-After` is a header and `lib/api` deliberately surfaces only the problem
 * body, so the number is usually unknown here. That is handled by saying so
 * rather than by inventing a countdown: a progress bar that finishes before the
 * server is ready teaches people to hammer the button.
 */
export function throttleOf(error: unknown): Throttle | null {
  if (!(error instanceof RequestFailed)) return null
  const body = error.body as { retry_after?: number }
  const retry = typeof body.retry_after === 'number' ? body.retry_after : null
  if (error.status === 429) return { kind: 'limited', retryAfterSeconds: retry }
  if (error.status === 503 && error.body.code === 'unavailable') {
    return { kind: 'unavailable', retryAfterSeconds: retry }
  }
  return null
}

/** Counts down to zero, or stays null when nobody told us how long. */
function useCountdown(seconds: number | null): number | null {
  const [left, setLeft] = useState(seconds)

  useEffect(() => {
    setLeft(seconds)
    if (seconds === null) return
    const id = window.setInterval(() => {
      setLeft((v) => (v === null || v <= 0 ? v : v - 1))
    }, 1000)
    return () => window.clearInterval(id)
  }, [seconds])

  return left
}

function waitLabel(seconds: number): string {
  if (seconds < 60) return `${seconds} giây`
  const mins = Math.ceil(seconds / 60)
  return `${mins} phút`
}

/* ------------------------------------------------- 1. the person filling in */

/**
 * Shown to whoever is filling in a public form when their submission is refused.
 *
 * No blame and no jargon: this reader has never heard of a rate limit, is in the
 * middle of buying something or signing up for something, and the single most
 * important sentence is that their typing has not been lost.
 */
export function RateLimitNotice({
  retryAfterSeconds = null,
  onRetry,
}: {
  retryAfterSeconds?: number | null
  onRetry?: () => void
}) {
  const left = useCountdown(retryAfterSeconds)
  const waiting = left !== null && left > 0
  const progress =
    retryAfterSeconds && left !== null ? ((retryAfterSeconds - left) / retryAfterSeconds) * 100 : 100

  return (
    <div role="alert" className="rounded border border-line bg-surface p-3">
      <p className="text-lede font-semibold">Bạn gửi hơi nhanh</p>
      <p className="mt-1 text-body leading-relaxed">
        {waiting ? (
          <>
            Chờ <span className="font-mono text-meta font-semibold">{waitLabel(left)}</span> rồi gửi
            lại.
          </>
        ) : retryAfterSeconds === null ? (
          <>Vui lòng thử gửi lại sau ít phút.</>
        ) : (
          <>Bây giờ gửi lại được rồi.</>
        )}{' '}
        Nội dung đã điền vẫn giữ nguyên trên máy bạn.
      </p>

      {retryAfterSeconds !== null && (
        <div className="mt-2 h-1.5 rounded bg-chrome" role="presentation">
          <div
            className="h-1.5 rounded bg-ink transition-[width] duration-1000 ease-linear"
            style={{ width: `${Math.min(progress, 100)}%` }}
          />
        </div>
      )}

      {onRetry && (
        <button type="button" className="btn mt-2" disabled={waiting} onClick={onRetry}>
          {waiting ? `Gửi lại sau ${left}s` : 'Gửi lại'}
        </button>
      )}
    </div>
  )
}

/* -------------------------------------------------- 2. the operator reading */

export interface ThrottleRule {
  /** The rule name as it appears in the server log and in metrics. */
  name: string
  blocked: number
  /** What the limit counts, in words. */
  keyedBy: string
}

/**
 * Shown above a report when traffic was refused during the reported period.
 *
 * The point is not the number: it is that the requests below do not include
 * these, so a dip in submissions has an explanation that is not "fewer people
 * were interested". It also refuses to call blocked requests blocked *people* --
 * one person pressing send five times is five refusals.
 */
export function ThrottledTrafficNote({
  blocked,
  rules,
  periodLabel,
  failOpen = true,
}: {
  blocked: number
  rules: ThrottleRule[]
  periodLabel: string
  failOpen?: boolean
}) {
  if (blocked <= 0) return null

  return (
    <section
      role="status"
      className="rounded border border-duesoon/50 bg-duesoon/5 px-3 py-2 text-body"
    >
      <p className="font-semibold text-duesoon">
        {num(blocked)} yêu cầu bị giới hạn tần suất chặn trong {periodLabel}
      </p>
      <p className="mt-0.5 text-muted">
        Những yêu cầu này bị từ chối trước khi tới biểu mẫu, nên chúng{' '}
        <span className="font-semibold">không</span> nằm trong số lượt gửi và không nằm trong tỉ lệ
        chuyển đổi bên dưới. Đây là số <span className="font-semibold">yêu cầu</span>, không phải số
        người: một người bấm gửi năm lần tạo ra năm lần bị chặn.
      </p>

      <ul className="mt-1.5 space-y-0.5">
        {rules.map((r) => (
          <li key={r.name} className="flex items-baseline gap-2">
            <span className="id-chip">{r.name}</span>
            <span className="font-mono text-meta font-semibold">{num(r.blocked)}</span>
            <span className="text-meta text-muted">đếm theo {r.keyedBy}</span>
          </li>
        ))}
      </ul>

      {failOpen && (
        // Stated because it changes how the zero should be read on a quiet week.
        <p className="mt-1.5 text-meta text-muted">
          Hai luật này fail open: khi bộ đếm không trả lời, yêu cầu vẫn được nhận. Con số 0 ở đây
          nghĩa là “không ghi nhận lần chặn nào”, không phải “chắc chắn không ai bị chặn”.
        </p>
      )}
    </section>
  )
}

/* ------------------------------------------------ 3. the data subject portal */

/**
 * Shown at the data-subject portal when the lookup endpoint refuses.
 *
 * This wording is load-bearing. The portal's identify endpoint answers the same
 * way for an address it knows and an address it has never seen, and a message
 * saying "too many attempts for this address" would break that in one sentence.
 * So: nothing about the identifier, nothing about attempts, and an explicit
 * statement that the message is the same for everyone -- otherwise a person who
 * gets it will reasonably assume it means they were recognised.
 *
 * It also fails closed, which is the opposite of the public form: here the wrong
 * answer is being an oracle, so refusing is correct and the person is given
 * another route rather than a retry button.
 */
export function PortalRateLimitNotice({
  retryAfterSeconds = null,
  contact,
}: {
  retryAfterSeconds?: number | null
  contact?: ReactNode
}) {
  const left = useCountdown(retryAfterSeconds)

  return (
    <div role="alert" className="rounded border border-line bg-surface p-3">
      <p className="text-lede font-semibold">Chưa xử lý được yêu cầu tra cứu</p>
      <p className="mt-1 text-body leading-relaxed">
        Cổng dữ liệu cá nhân tạm thời không nhận thêm yêu cầu tra cứu.{' '}
        {left !== null && left > 0
          ? `Vui lòng thử lại sau ${waitLabel(left)}.`
          : 'Vui lòng thử lại sau ít phút.'}
      </p>
      <p className="mt-1.5 text-body leading-relaxed text-muted">
        Thông báo này <span className="font-semibold">giống hệt nhau cho mọi thông tin nhập vào</span>{' '}
        — nó không cho biết thông tin bạn vừa nhập có trong hệ thống hay không.
      </p>
      <p className="mt-1.5 text-body leading-relaxed text-muted">
        Việc chờ ở đây không làm dừng thời hạn xử lý yêu cầu của bạn theo luật.
        {contact ? <> Nếu vẫn không vào được, bạn có thể gửi yêu cầu trực tiếp: {contact}</> : null}
      </p>
    </div>
  )
}

/* ------------------------------------------------------------ demo screen */

const DEMO_RULES: ThrottleRule[] = [
  { name: 'public_write_ip', blocked: 412, keyedBy: 'dải mạng /24 của người gửi' },
  { name: 'public_write_form', blocked: 38, keyedBy: 'từng biểu mẫu' },
]

/**
 * The reference screen for 4b: the three states next to each other, with the
 * reasoning that decided each one written underneath it.
 *
 * Kept as a route so the wording can be reviewed as a set. Whoever changes one of
 * these strings has to look at the other two, which is the whole point -- they
 * are only correct relative to each other.
 */
export function RateLimitedDemo() {
  const [nonce, setNonce] = useState(0)

  return (
    <div className="p-6">
      <PageHeader
        title="Giới hạn tần suất"
        meta="429 · Retry-After · ByIPPrefix / ByPathValue"
        actions={
          <button type="button" className="btn" onClick={() => setNonce((n) => n + 1)}>
            Chạy lại đồng hồ đếm
          </button>
        }
      />

      <p className="mb-3 max-w-3xl text-body text-muted">
        Ba trạng thái dưới đây dùng chung một mã lỗi và không dùng chung một câu chữ nào. Đây là màn
        minh hoạ; các component được nối vào form công khai, báo cáo dự án và cổng chủ thể dữ liệu.
      </p>

      <div className="grid gap-3 lg:grid-cols-3">
        <Card title="Người điền biểu mẫu" aside="429 khi gửi">
          <RateLimitNotice key={`filler-${nonce}`} retryAfterSeconds={30} onRetry={() => {}} />
          <p className="mt-2 text-meta leading-relaxed text-muted">
            Người đọc là khách hàng của tổ chức chạy biểu mẫu, không phải người vận hành hệ thống.
            Không đổ lỗi, không nhắc tới “giới hạn”, “vi phạm” hay tên luật — chỉ nói phải chờ bao lâu
            và rằng dữ liệu đã gõ vẫn còn. Người gõ chậm hiếm khi chạm ngưỡng này; nó chặn bot bơm dữ
            liệu rác vào biểu mẫu đang chạy quảng cáo.
          </p>
          <p className="mt-1 text-meta leading-relaxed text-muted">
            Không có <span className="font-mono">Retry-After</span> thì không vẽ đồng hồ đếm: một
            thanh chạy hết trước khi server sẵn sàng dạy người ta bấm liên tục.
          </p>
        </Card>

        <Card title="Người quản trị đọc báo cáo" aside="ghi chú trong báo cáo">
          <ThrottledTrafficNote blocked={450} rules={DEMO_RULES} periodLabel="30 ngày qua" />
          <p className="mt-2 text-meta leading-relaxed text-muted">
            Ở đây con số không phải lời xin lỗi mà là một chú thích cho mẫu số: lưu lượng bị chặn
            không bao giờ thành lượt gửi, nên nó không nằm trong phễu bên dưới. Nếu thiếu dòng này,
            một tuần bị bot tấn công rồi bị chặn sẽ đọc thành “ít người quan tâm hơn”.
          </p>
          <p className="mt-1 text-meta leading-relaxed text-muted">
            Đếm theo yêu cầu, không theo người — hệ thống không nhận diện người truy cập, và một người
            bấm gửi năm lần là năm lần bị chặn.
          </p>
        </Card>

        <Card title="Cổng chủ thể dữ liệu" aside="fail closed">
          <PortalRateLimitNotice
            key={`portal-${nonce}`}
            retryAfterSeconds={600}
            contact={<span className="font-mono text-meta">dpo@acme.vn</span>}
          />
          <p className="mt-2 text-meta leading-relaxed text-muted">
            Khác hai chỗ trên ở hai điểm. Thứ nhất, endpoint tra cứu fail closed: khi bộ đếm không trả
            lời thì từ chối, vì một endpoint tra cứu không giới hạn là công cụ dò xem tổ chức đang giữ
            dữ liệu của ai — tệ hơn nhiều so với việc tạm thời không dùng được.
          </p>
          <p className="mt-1 text-meta leading-relaxed text-muted">
            Thứ hai, câu chữ không được tiết lộ gì. Không “bạn đã thử quá nhiều lần với email này”:
            một câu như vậy xác nhận email đó có trong hệ thống. Thông báo phải giống hệt nhau cho
            email lạ và email đã biết, và phải nói ra điều đó để người đọc không tự suy diễn ngược.
          </p>
          <p className="mt-1 text-meta leading-relaxed text-muted">
            Kèm một lối đi khác, vì thời hạn xử lý yêu cầu theo luật vẫn đang chạy trong lúc người ta
            bị chặn.
          </p>
        </Card>
      </div>

      <div className="mt-4 grid gap-3 lg:grid-cols-3">
        <Card title="Khi không biết phải chờ bao lâu">
          <RateLimitNotice retryAfterSeconds={null} onRetry={() => {}} />
          <p className="mt-2 text-meta leading-relaxed text-muted">
            <span className="font-mono">Retry-After</span> là header, còn{' '}
            <span className="font-mono">lib/api</span> chỉ trả về phần thân lỗi — nên trong hầu hết
            trường hợp phía trình duyệt không biết con số này. Trạng thái đó phải nói thật thay vì
            đoán bừa một khoảng chờ.
          </p>
        </Card>
      </div>
    </div>
  )
}
