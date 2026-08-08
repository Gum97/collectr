import { useState } from 'react'
import { Link } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RequestFailed, api } from '../lib/api'
import { ErrorBanner, Field, Loading } from '../components/ui'
import { ROLE_SUMMARY } from './members/roles'

/**
 * Wireframe 1t. The one-time bootstrap of a self-hosted deployment.
 *
 * Public by necessity: a fresh install has no account, so there is nobody to
 * authenticate. That is also why it must close behind itself -- an endpoint that stays
 * open lets anyone who reaches the host append themselves as a second owner. The API
 * enforces that; this screen has to show it rather than presenting a form that will be
 * refused.
 *
 * The legal panel is not decoration. Someone running Collectr on their own hardware is
 * the data controller ("Bên Kiểm soát dữ liệu") under Law 91/2025/QH15, and the moment
 * they type an organisation name is the moment that becomes true. Discovering it later,
 * from a regulator, is the failure this panel exists to prevent.
 */

const PASSWORD_MIN = 12

interface SetupStatus {
  setup_complete: boolean
}

export function Setup() {
  const qc = useQueryClient()
  const [orgName, setOrgName] = useState('')
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [kekBackedUp, setKekBackedUp] = useState(false)
  const [done, setDone] = useState(false)

  const status = useQuery({
    queryKey: ['setup'],
    queryFn: async () => api.get<SetupStatus>('/api/auth/setup'),
    retry: false,
    staleTime: Infinity,
  })

  const create = useMutation({
    mutationFn: async () =>
      api.post<{ user_id: string; message: string }>('/api/auth/setup', {
        org_name: orgName.trim(),
        name: name.trim(),
        email: email.trim(),
        password,
      }),
    onSuccess: () => {
      setDone(true)
      setPassword('')
      setConfirm('')
      qc.invalidateQueries({ queryKey: ['setup'] })
    },
    onError: (err) => {
      // Someone else finished the bootstrap while this tab was open. Re-reading the
      // status flips the screen to the "already set up" state instead of leaving a form
      // that can never succeed.
      if (err instanceof RequestFailed && err.status === 409) status.refetch()
    },
  })

  if (status.isPending) return <Loading label="Đang kiểm tra trạng thái cài đặt…" />

  if (status.isError) {
    return (
      <Frame subtitle="không kết nối được tới máy chủ">
        <ErrorBanner error={status.error} retry={() => status.refetch()} />
      </Frame>
    )
  }

  // Closed for good. Not a disabled form: offering the fields at all invites somebody to
  // fill them in and wonder why nothing happens.
  if (status.data?.setup_complete && !done) {
    return (
      <Frame subtitle="deployment đã được khởi tạo">
        <div className="rounded border border-line bg-surface p-4">
          <h2 className="text-lede font-semibold">Đã cài đặt xong</h2>
          <p className="mt-1.5 text-body leading-relaxed text-muted">
            Deployment này đã có tổ chức và tài khoản chủ sở hữu. Màn khởi tạo chỉ chạy đúng một
            lần và đã đóng lại — không tạo được chủ sở hữu thứ hai từ đây, kể cả khi biết địa chỉ
            này.
          </p>
          <p className="mt-2 text-body leading-relaxed text-muted">
            Cần thêm người? Đăng nhập rồi mời họ ở màn <strong>Thành viên &amp; quyền</strong>.
            Mất quyền truy cập tài khoản chủ sở hữu? Dùng mã khôi phục — không có đường vòng nào
            khác, và đó là chủ đích.
          </p>
          <Link to="/login" className="btn-primary mt-3 inline-block">
            Đăng nhập
          </Link>
        </div>
      </Frame>
    )
  }

  if (done) {
    return (
      <Frame subtitle="bước 2/3 · bắt buộc bật MFA">
        <Steps current={2} />
        <div className="rounded border border-line bg-surface p-4">
          <h2 className="text-lede font-semibold">Đã tạo tổ chức và tài khoản chủ sở hữu</h2>
          <p className="mt-1.5 text-body leading-relaxed">
            Bước tiếp theo <strong>bắt buộc</strong>: bật xác thực 2 lớp. Vai trò{' '}
            <code className="font-mono">owner</code> chạm được dữ liệu cá nhân của mọi dự án, nên
            hệ thống không cho vào tiếp khi tài khoản còn chỉ có mật khẩu.
          </p>
          <p className="mt-2 text-body leading-relaxed text-muted">
            Đăng nhập bằng địa chỉ vừa tạo — màn đăng nhập sẽ đưa thẳng sang phần quét mã TOTP và
            lưu mã khôi phục.
          </p>
          <Link to="/login" className="btn-primary mt-3 inline-block">
            Đăng nhập để bật MFA
          </Link>
        </div>
      </Frame>
    )
  }

  const fieldErrors = create.error instanceof RequestFailed ? create.error.fields : {}
  const mismatch = confirm.length > 0 && confirm !== password
  const tooShort = password.length > 0 && password.length < PASSWORD_MIN
  const ready =
    orgName.trim() !== '' &&
    email.trim() !== '' &&
    password.length >= PASSWORD_MIN &&
    confirm === password &&
    kekBackedUp

  return (
    <Frame subtitle={`${location.host} · chưa có tổ chức nào · bước 1/3`}>
      <Steps current={1} />

      <h2 className="mb-2 text-body font-semibold">Bạn là Bên Kiểm soát dữ liệu</h2>
      <div className="mb-4 rounded border border-accent bg-accent/5 px-3 py-2.5 text-body leading-relaxed">
        Tổ chức bạn tạo dưới đây hiện trên mọi khối đồng ý mà chủ thể dữ liệu đọc trước khi gửi
        thông tin. Theo <strong>Luật 91/2025/QH15</strong>, đây là bên chịu trách nhiệm pháp lý
        với dữ liệu cá nhân thu thập được — <strong>không phải Collectr</strong>. Nghĩa vụ đi kèm
        gồm trả lời yêu cầu của chủ thể dữ liệu đúng hạn, thông báo vi phạm trong 72 giờ, và xoá
        dữ liệu khi hết thời hạn lưu.
      </div>

      <form
        onSubmit={(e) => {
          e.preventDefault()
          create.mutate()
        }}
        className="flex flex-col gap-3"
      >
        <Field
          label="Tên tổ chức"
          error={fieldErrors['org_name']}
          hint="Tên pháp lý, đúng như trên đăng ký kinh doanh — nó xuất hiện trong văn bản đồng ý."
        >
          <input
            required
            autoFocus
            className="input"
            placeholder="Công ty Acme"
            value={orgName}
            onChange={(e) => setOrgName(e.target.value)}
          />
        </Field>

        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Tên chủ sở hữu" error={fieldErrors['name']}>
            <input
              className="input"
              autoComplete="name"
              placeholder="Lê Minh"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
          <Field
            label="Email chủ sở hữu"
            error={fieldErrors['email']}
            hint="Dùng để đăng nhập và nhận cảnh báo bảo mật."
          >
            <input
              type="email"
              required
              autoComplete="username"
              className="input"
              placeholder="ten@congty.vn"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </Field>
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          <Field
            label="Mật khẩu"
            error={fieldErrors['password'] ?? (tooShort ? `Cần ít nhất ${PASSWORD_MIN} ký tự.` : undefined)}
            hint={`Ít nhất ${PASSWORD_MIN} ký tự.`}
          >
            <input
              type="password"
              required
              minLength={PASSWORD_MIN}
              autoComplete="new-password"
              className="input"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>
          <Field
            label="Nhập lại mật khẩu"
            error={mismatch ? 'Hai lần nhập chưa khớp.' : undefined}
          >
            <input
              type="password"
              required
              autoComplete="new-password"
              className="input"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
            />
          </Field>
        </div>

        <label className="flex items-start gap-2 rounded border border-dashed border-accent px-3 py-2.5">
          <input
            type="checkbox"
            className="mt-0.5"
            checked={kekBackedUp}
            onChange={(e) => setKekBackedUp(e.target.checked)}
          />
          <span className="text-body leading-relaxed">
            Tôi đã sao lưu <code className="font-mono">TENANT_KEK</code> ở nơi tách biệt với bản
            sao lưu cơ sở dữ liệu. <strong>Mất khoá là mất vĩnh viễn mọi field nhạy cảm</strong> —
            không có bản sao nào ở phía Collectr để khôi phục.
          </span>
        </label>

        <div className="rounded border border-dashed border-line px-3 py-2 text-meta leading-relaxed text-muted">
          Tài khoản đầu tiên nhận vai trò <code className="font-mono">owner</code>.{' '}
          {ROLE_SUMMARY.owner} Ngay sau bước này bạn sẽ phải bật xác thực 2 lớp.
        </div>

        {create.isError && <ErrorBanner error={create.error} />}

        <button type="submit" className="btn-primary" disabled={!ready || create.isPending}>
          {create.isPending ? 'Đang tạo…' : 'Tạo tổ chức · tiếp: bật MFA'}
        </button>
        {!ready && !create.isPending && (
          <p className="text-meta text-muted">
            Điền tên tổ chức, email, mật khẩu ({PASSWORD_MIN}+ ký tự) và xác nhận đã sao lưu khoá
            để tiếp tục.
          </p>
        )}
      </form>
    </Frame>
  )
}

function Frame({ subtitle, children }: { subtitle: string; children: React.ReactNode }) {
  return (
    <div className="mx-auto flex min-h-screen max-w-xl flex-col justify-center px-6 py-10">
      <h1 className="font-display text-2xl font-semibold">Chào mừng tới Collectr</h1>
      <p className="id-chip mb-4 mt-0.5">{subtitle}</p>
      {children}
    </div>
  )
}

/** Three steps, because the second one is not optional and hiding that until it happens
 *  makes it look like an obstacle rather than part of the setup. */
function Steps({ current }: { current: 1 | 2 | 3 }) {
  const labels = ['Tạo tổ chức', 'Bật MFA', 'Mời thành viên']
  return (
    <div className="mb-4">
      <div className="flex gap-1.5">
        {labels.map((label, i) => (
          <div
            key={label}
            className={`h-1 flex-1 rounded ${i < current ? 'bg-ink' : 'bg-chrome'}`}
          />
        ))}
      </div>
      <div className="mt-1 flex justify-between">
        {labels.map((label, i) => (
          <span
            key={label}
            className={`text-meta ${i < current ? 'font-semibold text-ink' : 'text-faint'}`}
          >
            {i + 1}. {label}
          </span>
        ))}
      </div>
    </div>
  )
}
