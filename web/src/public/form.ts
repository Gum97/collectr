/**
 * The public form page. No framework, on purpose.
 *
 * This is the page a customer on a phone actually waits for, and it is the
 * denominator of the completion rate this product reports on. Shipping React
 * here would make every respondent download the form builder's runtime to fill
 * in six inputs. The whole module is the rule engine plus enough DOM to render
 * what the engine says is visible.
 *
 * The server re-evaluates every submission. Nothing here is a security control:
 * hiding a field stops it being asked, not being accepted.
 */
import { evaluate, type Answers, type Field, type Schema } from '../shared/engine'
import { checkFormat, inputModeFor } from '../shared/format'

/**
 * Exactly what GET /api/pub/forms/{public_id} returns.
 *
 * Written against the handler rather than guessed. An earlier version of this
 * file assumed a flatter shape with a `consent` block beside the schema; the
 * server nests the schema's own consent declaration inside it, so the form
 * rendered its title and then stopped, and nothing was ever submitted.
 */
interface PublicForm {
  form: { public_id: string; title: string; description?: string }
  version: { id: string; no: number }
  schema: Schema
  /** Present when the form declares purposes. See buildConsent. */
  consent?: {
    document_id: string
    version: number
    body_html: string
    content_hash: string
    permalink: string
  }
}

const root = document.getElementById('collectr-form')
if (root) void start(root)

async function start(el: HTMLElement) {
  const publicId = el.dataset.publicId
  if (!publicId) return

  el.textContent = 'Đang tải…'
  let form: PublicForm
  try {
    const res = await fetch(`/api/pub/forms/${encodeURIComponent(publicId)}`, {
      credentials: 'same-origin',
    })
    if (!res.ok) throw new Error(String(res.status))
    form = (await res.json()) as PublicForm
  } catch {
    el.textContent = 'Không tải được biểu mẫu. Vui lòng thử lại.'
    return
  }

  const answers: Answers = {}
  const consent = new Set<string>()
  el.textContent = ''
  render(el, form, answers, consent, publicId)
}

function render(
  el: HTMLElement,
  form: PublicForm,
  answers: Answers,
  consent: Set<string>,
  publicId: string,
) {
  const formEl = document.createElement('form')
  formEl.noValidate = true
  formEl.className = 'cf'

  const fields = document.createElement('div')
  const consentBlock = buildConsent(form, consent)
  const status = document.createElement('p')
  status.className = 'cf-status'
  status.setAttribute('role', 'status')

  const submit = document.createElement('button')
  submit.type = 'submit'
  submit.className = 'cf-submit'
  submit.textContent = 'Gửi'

  // What the last render put on screen. Compared before rebuilding so an answer
  // that changes nothing about which questions are shown does not disturb them.
  let shape = ''

  const draw = () => {
    const result = evaluate(form.schema, answers)
    const next = result.visible.join(',') + '|' + result.required.join(',')
    if (next === shape) return
    shape = next

    // Rebuilding replaces the field elements, which drops focus and anything
    // half-typed in them. That is only acceptable when the set of questions
    // actually changed -- which is why the comparison above exists. Without it,
    // moving from one text box to the next fired blur, rebuilt everything, and
    // destroyed what was being typed into the box the person had just moved to.
    const active = document.activeElement
    const focusedID = active instanceof HTMLElement ? active.id : ''
    const caret =
      active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement
        ? active.selectionStart
        : null

    fields.replaceChildren()
    for (const id of result.visible) {
      const field = form.schema.fields[id]
      if (!field) continue
      fields.append(renderField(id, field, result.required.includes(id), answers, draw))
    }

    if (!focusedID) return
    const restored = document.getElementById(focusedID)
    if (!(restored instanceof HTMLElement)) return
    restored.focus({ preventScroll: true })
    if (
      caret !== null &&
      (restored instanceof HTMLInputElement || restored instanceof HTMLTextAreaElement)
    ) {
      try {
        restored.setSelectionRange(caret, caret)
      } catch {
        // Some input types refuse a selection range; the focus alone is enough.
      }
    }
  }
  draw()

  formEl.append(fields, consentBlock.el, status, submit)
  formEl.addEventListener('submit', (e) => {
    e.preventDefault()
    void send(form, answers, consent, publicId, status, submit)
  })
  el.append(formEl)
}

function renderField(
  id: string,
  field: Field,
  required: boolean,
  answers: Answers,
  draw: () => void,
): HTMLElement {
  const wrap = document.createElement('div')
  wrap.className = 'cf-field'

  const label = document.createElement('label')
  label.className = 'cf-label'
  label.htmlFor = id
  label.textContent = field.label
  if (required) {
    const star = document.createElement('span')
    star.className = 'cf-req'
    star.textContent = ' *'
    star.setAttribute('aria-label', 'bắt buộc')
    label.append(star)
  }
  wrap.append(label)

  // Re-evaluating on every keystroke would move a question out from under
  // somebody's cursor mid-sentence. Text commits on blur; choices commit
  // immediately, because that is the input branching actually hangs off.
  const commit = (value: unknown, immediate: boolean) => {
    answers[id] = value
    if (immediate) draw()
  }

  switch (field.type) {
    case 'choice':
    case 'dropdown': {
      if (field.type === 'dropdown') {
        const select = document.createElement('select')
        select.id = id
        select.className = 'cf-input'
        select.append(new Option('— chọn —', ''))
        for (const o of field.options ?? []) select.append(new Option(o.label, o.id))
        select.addEventListener('change', () => commit(select.value, true))
        wrap.append(select)
        break
      }
      const group = document.createElement('div')
      group.className = 'cf-options'
      group.setAttribute('role', 'radiogroup')
      group.setAttribute('aria-labelledby', id)
      for (const o of field.options ?? []) {
        group.append(
          choiceRow('radio', id, o.id, o.label, answers[id] === o.id, () => commit(o.id, true)),
        )
      }
      wrap.append(group)
      break
    }
    case 'multi_choice': {
      const group = document.createElement('div')
      group.className = 'cf-options'
      const current = new Set(Array.isArray(answers[id]) ? (answers[id] as string[]) : [])
      for (const o of field.options ?? []) {
        group.append(
          choiceRow('checkbox', id, o.id, o.label, current.has(o.id), (on) => {
            if (on) current.add(o.id)
            else current.delete(o.id)
            commit([...current], true)
          }),
        )
      }
      wrap.append(group)
      break
    }
    case 'rating': {
      const group = document.createElement('div')
      group.className = 'cf-rating'
      for (let n = 1; n <= (field.scale ?? 5); n++) {
        const b = document.createElement('button')
        b.type = 'button'
        b.className = 'cf-star'
        b.textContent = String(n)
        b.setAttribute('aria-pressed', String(answers[id] === n))
        b.addEventListener('click', () => commit(n, true))
        group.append(b)
      }
      wrap.append(group)
      break
    }
    case 'date': {
      const input = document.createElement('input')
      input.type = 'date'
      input.id = id
      input.className = 'cf-input'
      // The native picker greys out what the server would refuse anyway, so the
      // bound is visible before the mistake instead of after it.
      if (field.min) input.min = field.min
      if (field.max) input.max = field.max
      const note = hintLine(wrap)
      input.addEventListener('change', () => {
        note(checkFormat(field, input.value))
        commit(input.value, true)
      })
      wrap.append(input)
      break
    }
    case 'file': {
      const input = document.createElement('input')
      input.type = 'file'
      input.id = id
      input.className = 'cf-input'
      if (field.accept?.length) input.accept = field.accept.join(',')
      input.addEventListener('change', () => commit(input.files?.[0]?.name ?? '', true))
      wrap.append(input)
      break
    }
    default: {
      const input = field.multiline
        ? document.createElement('textarea')
        : document.createElement('input')
      if (input instanceof HTMLInputElement) input.type = 'text'
      input.id = id
      input.className = 'cf-input'
      // The keyboard, not the validation. A phone asked for twelve digits and
      // given a QWERTY layout is a form people abandon.
      const mode = inputModeFor(field)
      if (mode) input.inputMode = mode
      input.value = typeof answers[id] === 'string' ? (answers[id] as string) : ''

      const note = hintLine(wrap)
      input.addEventListener('input', () => {
        // Cleared while typing, never raised. Telling somebody their email is
        // malformed at the third character is telling them off for not having
        // finished.
        note('')
        commit(input.value, false)
      })
      input.addEventListener('blur', () => {
        note(checkFormat(field, input.value))
        commit(input.value, true)
      })
      wrap.append(input)
    }
  }

  return wrap
}

/**
 * Appends an error line to a field and returns a setter for it.
 *
 * The element is created once and only its text changes, so setting a message
 * never re-runs draw(). draw() rebuilds the question list, which would destroy
 * the input the respondent is still standing in -- the bug that once ate a text
 * answer on every blur.
 */
function hintLine(wrap: HTMLElement): (msg: string) => void {
  const p = document.createElement('p')
  p.className = 'cf-error'
  p.hidden = true
  wrap.append(p)
  return (msg: string) => {
    p.textContent = msg
    p.hidden = msg === ''
  }
}

function choiceRow(
  type: 'radio' | 'checkbox',
  name: string,
  value: string,
  label: string,
  checked: boolean,
  onChange: (on: boolean) => void,
): HTMLElement {
  const row = document.createElement('label')
  row.className = 'cf-option'
  const input = document.createElement('input')
  input.type = type
  input.name = name
  input.value = value
  input.defaultChecked = checked
  input.addEventListener('change', () => onChange(input.checked))
  const text = document.createElement('span')
  text.textContent = label
  row.append(input, text)
  return row
}

/**
 * The consent block.
 *
 * Every purpose gets its own control and nothing is pre-ticked. A single
 * blanket checkbox, or one already on, is not consent that can be evidenced
 * later -- and the record this writes is meant to be exactly that evidence.
 */
function buildConsent(form: PublicForm, chosen: Set<string>) {
  const el = document.createElement('fieldset')
  el.className = 'cf-consent'
  const purposes = form.schema.consent?.purposes ?? []
  if (purposes.length === 0) return { el }

  const legend = document.createElement('legend')
  legend.textContent = 'Đồng ý xử lý dữ liệu'
  el.append(legend)

  // The text itself, on the page, above the boxes.
  //
  // The submission carries a digest of this string, and the server refuses it if
  // it does not match the version it holds. That check is only worth anything if
  // the string was actually shown -- which is why the document is rendered here
  // rather than linked to and hashed unseen.
  if (form.consent) {
    const doc = document.createElement('div')
    doc.className = 'cf-doc-body'
    // Server-authored, validated on write to contain no script, form, meta or
    // base tag; see consent/domain.ValidateBodyHTML.
    doc.innerHTML = form.consent.body_html
    el.append(doc)
  }

  for (const p of purposes) {
    const row = document.createElement('label')
    row.className = 'cf-option'
    const box = document.createElement('input')
    box.type = 'checkbox'
    box.checked = false
    box.addEventListener('change', () => {
      if (box.checked) chosen.add(p.code)
      else chosen.delete(p.code)
    })
    const text = document.createElement('span')
    // The schema carries a code, not a display name, so the code is shown. A
    // friendly label invented here would put words into the controller's mouth
    // on the one screen where the wording is the legal act itself.
    text.textContent = (p.label ?? p.code) + (p.required ? ' *' : ' (tùy chọn)')
    row.append(box, text)
    el.append(row)
  }

  if (form.consent) {
    const link = document.createElement('a')
    link.href = form.consent.permalink
    link.target = '_blank'
    link.rel = 'noopener'
    link.className = 'cf-doc'
    // The exact version is named, not just "our policy": the record stores which
    // version was shown, and the person agreeing should be able to reread that
    // same one afterwards.
    link.textContent = `Mở bản đầy đủ (v${form.consent.version})`
    el.append(link)
  }

  return { el }
}

async function send(
  form: PublicForm,
  answers: Answers,
  consent: Set<string>,
  publicId: string,
  status: HTMLElement,
  submit: HTMLButtonElement,
) {
  const result = evaluate(form.schema, answers)
  const missing = result.required.filter((id) => {
    const v = answers[id]
    return v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)
  })
  if (missing.length > 0) {
    const first = form.schema.fields[missing[0]!]
    status.textContent = `Vui lòng trả lời: ${first?.label ?? missing[0]}`
    document.getElementById(missing[0]!)?.focus()
    return
  }

  submit.disabled = true
  status.textContent = 'Đang gửi…'
  try {
    const res = await fetch(`/api/pub/forms/${encodeURIComponent(publicId)}/submissions`, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        form_version_id: form.version.id,
        answers,
        // Every declared purpose is reported, granted or not. Sending only the
        // ticked ones would leave the record unable to distinguish "declined"
        // from "never asked", and those are different facts about consent.
        consents: (form.schema.consent?.purposes ?? []).map((p) => ({
          purpose: p.code,
          granted: consent.has(p.code),
        })),
        // The digest of the text put on the page, not the one the server sent.
        // Echoing the server's own hash would prove nothing about what was
        // displayed, which is the only thing this field exists to establish.
        consent_proof: form.consent
          ? {
              document_id: form.consent.document_id,
              rendered_hash: await sha256Hex(form.consent.body_html),
            }
          : undefined,
        visit_token: new URLSearchParams(location.search).get('cx') ?? '',
        client_time: new Date().toISOString(),
        locale: navigator.language,
      }),
    })
    if (res.status === 429) {
      status.textContent = 'Quá nhiều lượt gửi. Vui lòng thử lại sau ít phút.'
      submit.disabled = false
      return
    }
    if (!res.ok) {
      const body = (await res.json().catch(() => null)) as { title?: string } | null
      status.textContent = body?.title ?? 'Không gửi được. Vui lòng thử lại.'
      submit.disabled = false
      return
    }
    status.textContent = 'Đã gửi. Cảm ơn bạn.'
    submit.hidden = true
  } catch {
    // The answers stay in the DOM so a retry does not mean typing it all again.
    status.textContent = 'Mất kết nối. Vui lòng thử lại.'
    submit.disabled = false
  }
}

/** SHA-256 of a string, hex encoded, via the platform's own crypto. */
async function sha256Hex(text: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text))
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, '0')).join('')
}
