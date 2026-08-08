/**
 * The data subject portal. No framework, same reasoning as form.ts.
 *
 * This is the page a person opens from an email to exercise a right the law
 * gives them. Until now it had no module at all: the Go page rendered a
 * "please enable JavaScript" fallback and nothing ever replaced it, so the
 * portal told every visitor the fault was theirs while the actual cause was a
 * missing bundle. The API behind it was complete the whole time.
 *
 * Three states, decided by the URL:
 *   ?t=<tenant>                 ask who you are, we send a link
 *   ?t=<tenant>&token=<token>   exchange it for a session, then show the data
 *   neither                     say plainly that the link is what opens this
 *
 * The session cookie is issued with Path=/api/dsr, so this page cannot read it
 * and cannot tell whether one exists. It asks the API instead, which is also
 * what makes a forwarded copy of this URL show the reader nothing.
 */

const root = document.getElementById('collectr-portal')
if (root) void start(root)

interface Question {
  label: string
  type: string
  sensitive?: boolean
}

interface Submission {
  id: string
  form: string
  form_version: number
  answers: Record<string, unknown>
  questions?: Record<string, Question>
  submitted_at: string
  status: string
  /** True when the record holds sealed answers that could not be opened. Not the
   *  same as holding none, and the page must not blur the two. */
  sensitive_unreadable?: boolean
}

interface RequestRow {
  id: string
  type: string
  status: string
  created_at?: string
  due_at?: string
}

/** The rights a subject may exercise from here, and what each one costs them.
 *
 *  Erasure is last and carries its own wording. The others are recoverable
 *  positions; that one destroys the key and is the only button on this page
 *  that cannot be taken back. */
const REQUEST_TYPES: { type: string; label: string; blurb: string; danger?: boolean }[] = [
  {
    type: 'export',
    label: 'Tải toàn bộ dữ liệu về',
    blurb: 'Chúng tôi gửi bạn một tệp máy đọc được, chậm nhất trong thời hạn ghi ở dưới.',
  },
  {
    type: 'rectify',
    label: 'Yêu cầu chỉnh sửa',
    blurb:
      'Dùng cho những ô bạn không sửa trực tiếp được ở trên, ví dụ dữ liệu nhạy cảm đang mã hoá.',
  },
  {
    type: 'restrict',
    label: 'Hạn chế xử lý',
    blurb: 'Dữ liệu được giữ nhưng ngừng dùng cho tới khi có kết luận. Cần người xét.',
  },
  {
    type: 'withdraw',
    label: 'Rút đồng ý',
    blurb:
      'Dừng xử lý theo mục đích đã đồng ý, kể từ lúc rút. Không tự động xoá dữ liệu đã thu.',
  },
  {
    type: 'erase',
    label: 'Yêu cầu xoá toàn bộ',
    blurb:
      'Khoá mã hoá riêng của bạn bị huỷ. Sau đó không ai đọc được dữ liệu của bạn nữa, kể cả chúng tôi. Không có hoàn tác.',
    danger: true,
  },
]

async function start(el: HTMLElement) {
  const params = new URLSearchParams(location.search)
  const tenant = params.get('t') ?? ''
  const token = params.get('token') ?? ''

  if (token && tenant) {
    el.replaceChildren(text('p', 'meta', 'Đang mở liên kết…'))
    try {
      const res = await fetch('/api/dsr/session', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tenant, token }),
        credentials: 'same-origin',
      })
      if (!res.ok) throw new Error(String(res.status))
    } catch {
      showExpired(el, tenant)
      return
    }
    // The token is single-use and now spent, but it stays in the address bar,
    // in history, and in whatever the browser syncs. Removed before anything is
    // rendered, so the copy the person might share is the bare page.
    history.replaceState(null, '', location.pathname + (tenant ? `?t=${encodeURIComponent(tenant)}` : ''))
    await showData(el, tenant)
    return
  }

  // No token: either they arrived cold, or the session is still alive from a
  // link opened minutes ago. Ask the API, because this page cannot see the
  // cookie that would answer it.
  const alive = await fetch('/api/dsr/me/submissions', { credentials: 'same-origin' })
  if (alive.ok) {
    await showData(el, tenant, await alive.json())
    return
  }
  if (tenant) showIdentify(el, tenant)
  else showNoLink(el)
}

/* ------------------------------------------------------------------ states */

function showNoLink(el: HTMLElement) {
  el.replaceChildren(
    card('Trang này mở bằng liên kết riêng của bạn', [
      text(
        'p',
        '',
        'Liên kết được gửi tới email hoặc số điện thoại bạn đã dùng khi gửi biểu mẫu. ' +
          'Không có liên kết thì trang này không biết bạn là ai, và cố tình như vậy: ' +
          'nếu chỉ cần biết địa chỉ của một người là xem được dữ liệu của họ thì đó không phải bảo vệ.',
      ),
      text(
        'p',
        'meta',
        'Hãy mở lại liên kết trong email, hoặc yêu cầu một liên kết mới từ trang của đơn vị đã thu thập dữ liệu của bạn.',
      ),
    ]),
  )
}

function showExpired(el: HTMLElement, tenant: string) {
  const wrap = card('Liên kết đã hết hạn hoặc đã dùng rồi', [
    text(
      'p',
      '',
      'Mỗi liên kết chỉ dùng được một lần và có hiệu lực trong 15 phút. Đây là lý do liên kết bị chuyển tiếp cho người khác cũng không mở được gì.',
    ),
  ])
  el.replaceChildren(wrap)
  if (tenant) wrap.append(identifyForm(el, tenant, 'Gửi cho tôi liên kết mới'))
}

function showIdentify(el: HTMLElement, tenant: string) {
  const wrap = card('Xem dữ liệu của bạn', [
    text(
      'p',
      '',
      'Nhập email hoặc số điện thoại bạn đã dùng khi gửi biểu mẫu. Chúng tôi gửi tới đó một liên kết mở trang này.',
    ),
  ])
  el.replaceChildren(wrap)
  wrap.append(identifyForm(el, tenant, 'Gửi liên kết cho tôi'))
}

function identifyForm(el: HTMLElement, tenant: string, submitLabel: string): HTMLElement {
  const form = document.createElement('form')
  form.className = 'cf'
  form.noValidate = true

  const field = document.createElement('div')
  field.className = 'cf-field'
  const label = document.createElement('label')
  label.className = 'cf-label'
  label.htmlFor = 'dsr-id'
  label.textContent = 'Email hoặc số điện thoại'
  const input = document.createElement('input')
  input.id = 'dsr-id'
  input.className = 'cf-input'
  input.autocomplete = 'email'
  const err = document.createElement('p')
  err.className = 'cf-error'
  err.hidden = true
  field.append(label, input, err)

  const btn = document.createElement('button')
  btn.type = 'submit'
  btn.className = 'cf-submit'
  btn.textContent = submitLabel

  form.append(field, btn)
  form.addEventListener('submit', (e) => {
    e.preventDefault()
    const value = input.value.trim()
    // Guessed from the shape rather than asked as a second question. Nobody
    // filling this in thinks of themselves as choosing an identifier kind.
    const kind = value.includes('@') ? 'email' : 'phone'
    if (!value) {
      err.textContent = 'Nhập email hoặc số điện thoại bạn đã dùng.'
      err.hidden = false
      return
    }
    err.hidden = true
    btn.disabled = true
    btn.textContent = 'Đang gửi…'
    void fetch('/api/dsr/identify', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ tenant, identifier: value, kind }),
      credentials: 'same-origin',
    })
      .then(() => {
        // The API answers the same way whether or not the identifier is known,
        // so this page must too. Saying "we sent it" only when the person
        // exists turns the form into a way to test who is in the database.
        el.replaceChildren(
          card('Nếu thông tin này có trong hệ thống, liên kết đã được gửi', [
            text('p', '', 'Liên kết có hiệu lực trong 15 phút và chỉ dùng được một lần.'),
            text(
              'p',
              'meta',
              'Chúng tôi trả lời như nhau dù địa chỉ đó có trong hệ thống hay không — nếu không, ' +
                'bất kỳ ai cũng có thể dùng ô này để dò xem một người có dữ liệu ở đây hay không.',
            ),
          ]),
        )
      })
      .catch(() => {
        err.textContent = 'Không gửi được. Kiểm tra kết nối rồi thử lại.'
        err.hidden = false
        btn.disabled = false
        btn.textContent = submitLabel
      })
  })
  return form
}

/* -------------------------------------------------------------------- data */

async function showData(el: HTMLElement, tenant: string, preloaded?: { data: Submission[] }) {
  el.replaceChildren(text('p', 'meta', 'Đang tải dữ liệu của bạn…'))

  let subs: Submission[] = []
  let reqs: RequestRow[] = []
  try {
    const list = preloaded ?? ((await (await fetch('/api/dsr/me/submissions', { credentials: 'same-origin' })).json()) as { data: Submission[] })
    subs = list.data ?? []
    const r = await fetch('/api/dsr/me/requests', { credentials: 'same-origin' })
    if (r.ok) reqs = ((await r.json()) as { data: RequestRow[] }).data ?? []
  } catch {
    showExpired(el, tenant)
    return
  }

  const out = document.createDocumentFragment()

  if (subs.length === 0) {
    out.append(
      card('Không có bản ghi nào', [
        text(
          'p',
          '',
          'Chúng tôi không giữ biểu mẫu nào gắn với thông tin này. Bạn vẫn có thể gửi các yêu cầu dưới đây.',
        ),
      ]),
    )
  }
  for (const s of subs) out.append(recordCard(s))

  out.append(requestsCard(reqs))
  el.replaceChildren(out)
}

function recordCard(s: Submission): HTMLElement {
  const body: HTMLElement[] = [
    text(
      'p',
      'meta',
      `Gửi lúc ${when(s.submitted_at)} · bản ${s.form_version} của biểu mẫu`,
    ),
  ]

  const list = document.createElement('div')
  const inputs = new Map<string, HTMLInputElement>()

  for (const [fieldId, value] of Object.entries(s.answers ?? {})) {
    const q = s.questions?.[fieldId]
    const row = document.createElement('div')
    row.className = 'cf-field'

    const label = document.createElement('label')
    label.className = 'cf-label'
    label.htmlFor = `${s.id}-${fieldId}`
    // The question as it was worded in the version this record was collected
    // under. The id is the fallback and should never be what somebody reads.
    label.textContent = q?.label || fieldId
    if (q?.sensitive) {
      const tag = document.createElement('span')
      tag.className = 'cf-req'
      tag.textContent = ' · dữ liệu nhạy cảm'
      label.append(tag)
    }

    const input = document.createElement('input')
    input.id = `${s.id}-${fieldId}`
    input.className = 'cf-input'
    // Attachments are shown, not edited: the value is a reference to bytes the
    // portal cannot replace, and a text box over it would invite a change that
    // silently does nothing.
    const isFile = q?.type === 'file' || (value !== null && typeof value === 'object')
    const shown = isFile ? '(tệp đính kèm)' : String(value ?? '')

    if (q?.sensitive) {
      // Shown, because the law gives the right to see it, and covered, because
      // a portal left open on a desk should not put somebody's identity card on
      // screen. One click, and the click is theirs.
      //
      // Read-only: correcting this value means re-sealing it under the
      // subject's key, and no write path does that. Offering a text box would
      // send the new value to the plaintext column, where erasure could no
      // longer reach it -- the server refuses exactly that, and the page should
      // not be asking.
      input.value = '••••••••'
      input.disabled = true
      input.dataset.hidden = '1'

      const reveal = document.createElement('button')
      reveal.type = 'button'
      reveal.className = 'btn btn-sm'
      reveal.textContent = 'Hiện'
      reveal.addEventListener('click', () => {
        const hidden = input.dataset.hidden === '1'
        input.value = hidden ? shown : '••••••••'
        input.dataset.hidden = hidden ? '0' : '1'
        reveal.textContent = hidden ? 'Ẩn' : 'Hiện'
      })

      row.append(label, input, reveal)
      row.append(
        text(
          'p',
          'meta',
          'Ô này được mã hoá bằng khoá riêng của bạn. Muốn sửa, hãy dùng “Yêu cầu chỉnh sửa” ở dưới — ' +
            'sửa trực tiếp ở đây sẽ đặt giá trị vào chỗ mà lệnh xoá không với tới được.',
        ),
      )
      list.append(row)
      continue
    }

    input.value = shown
    input.disabled = isFile
    if (!isFile) inputs.set(fieldId, input)

    row.append(label, input)
    list.append(row)
  }
  body.push(list)

  if (s.sensitive_unreadable) {
    body.push(
      text(
        'p',
        'cf-error',
        'Bản ghi này có dữ liệu nhạy cảm mà chúng tôi không mở được để hiển thị. ' +
          'Đây không phải là “không có gì” — hãy dùng “Tải toàn bộ dữ liệu về” hoặc liên hệ đơn vị đã thu thập.',
      ),
    )
  }

  const status = document.createElement('p')
  status.className = 'cf-status'
  status.setAttribute('role', 'status')

  const save = document.createElement('button')
  save.type = 'button'
  save.className = 'btn'
  save.textContent = 'Lưu chỉnh sửa'
  save.addEventListener('click', () => {
    const answers: Record<string, unknown> = {}
    for (const [fieldId, input] of inputs) answers[fieldId] = input.value
    save.disabled = true
    status.textContent = 'Đang lưu…'
    void fetch(`/api/dsr/me/submissions/${encodeURIComponent(s.id)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ answers }),
      credentials: 'same-origin',
    })
      .then((res) => {
        // The old values are kept on record rather than overwritten, so the
        // person is told what actually happened: the correction is a fact about
        // the data, not a rewrite of history.
        status.textContent = res.ok
          ? 'Đã lưu. Giá trị cũ vẫn được giữ trong nhật ký để chứng minh đã sửa gì và khi nào.'
          : 'Không lưu được. Liên kết có thể đã hết hạn — mở lại liên kết trong email.'
      })
      .catch(() => {
        status.textContent = 'Không lưu được. Kiểm tra kết nối rồi thử lại.'
      })
      .finally(() => {
        save.disabled = false
      })
  })

  body.push(save, status)
  return card(s.form, body)
}

function requestsCard(existing: RequestRow[]): HTMLElement {
  const body: HTMLElement[] = []

  const status = document.createElement('p')
  status.className = 'cf-status'
  status.setAttribute('role', 'status')

  for (const t of REQUEST_TYPES) {
    const row = document.createElement('div')
    row.className = t.danger ? 'p-right p-right-danger' : 'p-right'
    row.append(text('h3', '', t.label), text('p', '', t.blurb))

    const btn = document.createElement('button')
    btn.type = 'button'
    btn.className = t.danger ? 'btn btn-danger' : 'btn'
    btn.textContent = t.danger ? 'Yêu cầu xoá…' : t.label

    // Erasure asks twice, and the second press is a different button with
    // different words. A single click that destroys a key is a click somebody
    // makes by accident once and cannot undo ever.
    let armed = false
    btn.addEventListener('click', () => {
      if (t.danger && !armed) {
        armed = true
        btn.textContent = 'Bấm lần nữa để xác nhận xoá vĩnh viễn'
        status.textContent =
          'Sau khi thực hiện, khoá mã hoá của bạn bị huỷ và không ai đọc được dữ liệu của bạn nữa. ' +
          'Nếu bạn còn cần dữ liệu, hãy chọn “Tải toàn bộ dữ liệu về” trước.'
        return
      }
      btn.disabled = true
      status.textContent = 'Đang gửi yêu cầu…'
      void fetch('/api/dsr/me/requests', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ type: t.type }),
        credentials: 'same-origin',
      })
        .then(async (res) => {
          if (!res.ok) {
            status.textContent = 'Không gửi được yêu cầu. Mở lại liên kết trong email rồi thử lại.'
            btn.disabled = false
            return
          }
          const out = (await res.json()) as { due_at?: string }
          status.textContent = out.due_at
            ? `Đã ghi nhận. Chúng tôi phải trả lời chậm nhất ${when(out.due_at)}.`
            : 'Đã ghi nhận yêu cầu của bạn.'
        })
        .catch(() => {
          status.textContent = 'Không gửi được. Kiểm tra kết nối rồi thử lại.'
          btn.disabled = false
        })
    })

    row.append(btn)
    body.push(row)
  }

  body.push(status)

  if (existing.length > 0) {
    body.push(text('p', 'label', 'Yêu cầu bạn đã gửi'))
    for (const r of existing) {
      body.push(
        text(
          'p',
          'meta',
          `${r.type} · ${r.status}${r.due_at ? ` · hạn trả lời ${when(r.due_at)}` : ''}`,
        ),
      )
    }
  }

  return card('Yêu cầu của bạn', body)
}

/* ----------------------------------------------------------------- helpers */

function card(title: string, body: (HTMLElement | string)[]): HTMLElement {
  const section = document.createElement('section')
  section.className = 'card'
  section.append(text('p', 'label', title))
  for (const b of body) section.append(b)
  return section
}

function text(tag: string, className: string, content: string): HTMLElement {
  const el = document.createElement(tag)
  if (className) el.className = className
  // textContent, never innerHTML. Everything here came off the wire, and some
  // of it is text a stranger typed into a form.
  el.textContent = content
  return el
}

function when(iso: string | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getDate())}/${pad(d.getMonth() + 1)}/${d.getFullYear()} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}
