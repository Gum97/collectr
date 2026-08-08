import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router'
import { api, RequestFailed } from '../../lib/api'
import { Card, ErrorBanner, Loading, PageHeader, StatusPill } from '../../components/ui'
import { useMe, type Me } from '../../lib/session'
import { checkPassword } from './password'
import { PasswordMeter } from './ResetPassword'
import { RecoveryCodesPanel } from './EnableMFA'
import { requiresMFA, roleLabel } from '../members/roles'

/** Fields the server does not send yet, declared so the screen uses them the
 *  moment it does. Until then `mfa_required` falls back to the mirrored role
 *  table, which only chooses the wording -- the obligation is enforced at
 *  sign-in by the API, not here. */
interface AccountMe extends Me {
  mfa_required?: boolean
  mfa_enrolled_at?: string
}

interface RecoveryStatus {
  remaining: number
  /** Absent today. Without it there is no denominator, and "2" is shown alone
   *  rather than invented as "2 / 10". */
  total?: number
}

/** Screen 3b: password, second factor, and what is left of the recovery codes. */
export function Account() {
  const me = useMe()
  const profile = me.data as AccountMe | null | undefined

  return (
    <div className="max-w-2xl p-6">
      <PageHeader
        title="Tài khoản & bảo mật"
        meta="GET /api/v1/auth/me"
        actions={
          profile && (
            <StatusPill>
              {[profile.org_role, profile.org_name].filter(Boolean).join(' · ') || 'tài khoản'}
            </StatusPill>
          )
        }
      />

      {me.isPending && <Loading />}
      {me.isError && <ErrorBanner error={me.error} retry={() => me.refetch()} />}

      {profile && (
        <div className="flex flex-col gap-4">
          <ChangePassword />
          <SecondFactor profile={profile} />
          {profile.mfa_enabled && <RecoveryCodes />}
        </div>
      )}
    </div>
  )
}

function ChangePassword() {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [reveal, setReveal] = useState(false)
  const [done, setDone] = useState('')

  const save = useMutation({
    mutationFn: () =>
      api.post<{ message?: string }>('/api/v1/auth/password', {
        current_password: current,
        new_password: next,
      }),
    onSuccess: (res) => {
      setCurrent('')
      setNext('')
      setDone(res.message ?? 'Đã đổi mật khẩu. Các phiên đăng nhập khác đã bị thu hồi.')
    },
  })

  // The current password is in hand here, so "not the same as the old one" is a
  // rule this screen can actually decide -- unlike the reset-by-email form.
  const check = checkPassword(next, { currentPassword: current || undefined })
  const errors = save.error instanceof RequestFailed ? save.error.fields : {}

  return (
    <Card title="Đổi mật khẩu">
      <form
        onSubmit={(e) => {
          e.preventDefault()
          setDone('')
          save.mutate()
        }}
        className="flex flex-col gap-3"
      >
        <div className="flex flex-col gap-3 sm:flex-row">
          <label className="flex flex-1 flex-col gap-1">
            <span className="text-meta font-semibold">Mật khẩu hiện tại</span>
            <input
              type="password"
              autoComplete="current-password"
              required
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              className="input font-mono"
            />
            {errors.current_password && (
              <span role="alert" className="text-meta text-overdue">
                {errors.current_password}
              </span>
            )}
          </label>

          <label className="flex flex-1 flex-col gap-1">
            <span className="flex items-baseline justify-between text-meta font-semibold">
              Mật khẩu mới
              <button
                type="button"
                onClick={() => setReveal((v) => !v)}
                className="text-meta font-normal text-accent hover:text-accent-dark"
              >
                {reveal ? 'Ẩn' : 'Hiện'}
              </button>
            </span>
            <input
              type={reveal ? 'text' : 'password'}
              autoComplete="new-password"
              required
              value={next}
              onChange={(e) => setNext(e.target.value)}
              className="input font-mono"
            />
            {errors.new_password && (
              <span role="alert" className="text-meta text-overdue">
                {errors.new_password}
              </span>
            )}
          </label>
        </div>

        {next !== '' && <PasswordMeter check={check} />}

        {save.isError && Object.keys(errors).length === 0 && (
          <ErrorBanner error={save.error} />
        )}

        {done && (
          <p role="status" className="text-meta font-semibold text-ok">
            {done}
          </p>
        )}

        <div>
          <button type="submit" disabled={!check.ok || save.isPending} className="btn-primary">
            {save.isPending ? 'Đang lưu…' : 'Lưu'}
          </button>
        </div>

        <p className="text-meta text-muted">
          Đổi mật khẩu thu hồi mọi phiên đăng nhập khác. Phiên trên trình duyệt này vẫn giữ, nên bạn
          không bị đăng xuất khỏi trang đang mở.
        </p>
      </form>
    </Card>
  )
}

function SecondFactor({ profile }: { profile: AccountMe }) {
  const on = Boolean(profile.mfa_enabled)
  const forced = profile.mfa_required ?? requiresMFA(profile.org_role ?? '')

  return (
    <Card title="Xác thực 2 lớp">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <StatusPill tone={on ? 'ok' : 'duesoon'}>{on ? 'đang bật' : 'chưa bật'}</StatusPill>
            {forced && <StatusPill tone="accent">bắt buộc</StatusPill>}
          </div>

          <p className="mt-1.5 text-meta text-muted">
            {on
              ? profile.mfa_enrolled_at
                ? `Thiết bị đăng ký ${new Date(profile.mfa_enrolled_at).toLocaleDateString('vi-VN')}`
                : 'Ứng dụng xác thực đã được đăng ký.'
              : 'Tài khoản đang chỉ được bảo vệ bằng mật khẩu.'}
            {forced && ` · vai trò ${roleLabel(profile.org_role ?? '')} buộc phải bật`}
          </p>
        </div>

        {on ? (
          <div className="text-right">
            <button type="button" disabled className="btn" title="Chưa có thao tác tắt">
              Không thể tắt
            </button>
            <p className="mt-1 max-w-[16rem] text-meta text-muted">
              {forced
                ? 'Vai trò của bạn bắt buộc giữ xác thực 2 lớp.'
                : 'Giao diện chưa có thao tác tắt xác thực 2 lớp — liên hệ quản trị viên tổ chức nếu bạn cần đổi thiết bị.'}
            </p>
          </div>
        ) : (
          <Link to="/account/mfa" className="btn-primary">
            Bật xác thực 2 lớp
          </Link>
        )}
      </div>

      {!on && forced && (
        <p
          role="alert"
          className="mt-3 rounded border border-accent bg-accent/5 px-3 py-2 text-meta text-ink"
        >
          Vai trò của bạn nhìn thấy dữ liệu cá nhân trên toàn tổ chức, nên xác thực 2 lớp là bắt
          buộc chứ không phải tuỳ chọn. Bật ngay để không bị chặn ở lần đăng nhập sau.
        </p>
      )}
    </Card>
  )
}

function RecoveryCodes() {
  const qc = useQueryClient()
  const [confirming, setConfirming] = useState(false)
  const [fresh, setFresh] = useState<string[] | null>(null)

  const status = useQuery({
    queryKey: ['recovery-codes'],
    queryFn: () => api.get<RecoveryStatus>('/api/v1/auth/mfa/recovery-codes'),
  })

  const regenerate = useMutation({
    mutationFn: () => api.post<{ codes: string[] }>('/api/v1/auth/mfa/recovery-codes'),
    onSuccess: (res) => {
      setFresh(res.codes ?? [])
      setConfirming(false)
      qc.invalidateQueries({ queryKey: ['recovery-codes'] })
      qc.invalidateQueries({ queryKey: ['me'] })
    },
  })

  const remaining = status.data?.remaining
  // A freshly issued set tells us how many a full set is; the count endpoint does
  // not. Without one, the meter is not drawn at all rather than drawn against a
  // guessed denominator.
  const total = fresh?.length ?? status.data?.total ?? null
  const low = remaining !== undefined && remaining <= 2

  return (
    <Card title="Mã khôi phục">
      {status.isPending && <Loading />}
      {status.isError && <ErrorBanner error={status.error} retry={() => status.refetch()} />}

      {remaining !== undefined && (
        <>
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <p className="text-body">
                Còn dùng được{' '}
                <span className={`font-mono text-body ${low ? 'text-overdue' : 'text-ink'}`}>
                  {total === null ? remaining : `${remaining} / ${total}`}
                </span>
                {total === null && ' mã'}
              </p>
              {total === null && (
                <p className="id-chip mt-0.5">máy chủ không cho biết tổng số mã của một bộ</p>
              )}
            </div>

            <button
              type="button"
              className={`btn ${low ? 'border-accent text-accent' : ''}`}
              onClick={() => setConfirming(true)}
              disabled={confirming || regenerate.isPending}
            >
              Tạo lại bộ mới
            </button>
          </div>

          {total !== null && total > 0 && (
            <div className="mt-2 flex gap-1" aria-hidden>
              {Array.from({ length: total }, (_, i) => (
                <span
                  key={i}
                  className={`h-1.5 flex-1 rounded ${
                    i < remaining ? (low ? 'bg-accent' : 'bg-ink') : 'bg-chrome'
                  }`}
                />
              ))}
            </div>
          )}

          {remaining === 0 ? (
            <p
              role="alert"
              className="mt-3 rounded border border-overdue/40 bg-overdue/5 px-3 py-2 text-meta text-overdue"
            >
              Không còn mã khôi phục nào. Mất điện thoại lúc này là mất quyền vào tài khoản — tạo bộ
              mới ngay.
            </p>
          ) : (
            low && (
              <p
                role="alert"
                className="mt-3 rounded border border-accent bg-accent/5 px-3 py-2 text-meta text-ink"
              >
                Sắp hết mã khôi phục. Tạo bộ mới trước khi dùng nốt số còn lại — khi hết mã, cách duy
                nhất để vào lại là nhờ quản trị viên.
              </p>
            )
          )}
        </>
      )}

      {confirming && (
        <div className="mt-3 rounded border border-dashed border-line bg-panel px-3 py-3">
          <p className="text-meta leading-relaxed text-ink">
            Tạo lại sẽ vô hiệu <b>toàn bộ</b> mã của bộ hiện tại — cả những mã bạn chưa dùng
            {remaining !== undefined && ` (${remaining} mã)`}. Bộ mới chỉ hiện một lần, đúng như lúc
            bật 2FA.
          </p>
          <div className="mt-2 flex gap-2">
            <button
              type="button"
              className="btn-primary"
              disabled={regenerate.isPending}
              onClick={() => regenerate.mutate()}
            >
              {regenerate.isPending ? 'Đang tạo…' : 'Xác nhận tạo bộ mới'}
            </button>
            <button type="button" className="btn" onClick={() => setConfirming(false)}>
              Huỷ
            </button>
          </div>
        </div>
      )}

      {regenerate.isError && <div className="mt-3"><ErrorBanner error={regenerate.error} /></div>}

      {fresh && (
        <div className="mt-3">
          <RecoveryCodesPanel
            codes={fresh}
            note="Bộ mã cũ đã hết hiệu lực ngay khi bộ này được tạo."
            actionLabel="Tôi đã lưu"
            onAcknowledge={() => setFresh(null)}
          />
        </div>
      )}
    </Card>
  )
}
