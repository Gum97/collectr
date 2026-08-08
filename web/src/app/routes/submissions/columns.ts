/**
 * Column registry and cell semantics for the submission grid.
 *
 * A form is edited over time and every published version is immutable, so one
 * table has to show answers collected under v1 next to answers collected under
 * v4. The registry is the union of every version's fields; the hard part is not
 * the union but the emptiness. There are four different reasons a cell has no
 * value and they must not look alike:
 *
 *   not_asked  the version this record used did not contain the field at all
 *   hidden     the field existed but a branch hid it from this respondent
 *   blank      the respondent saw the question and left it empty
 *   masked     there is a value, the reader is not allowed to see it
 *
 * Collapse them and every completion statistic drawn off this screen is wrong:
 * a question added in v3 would read as though two thousand people refused to
 * answer it. `visible_fields`, stored per submission, is what makes the middle
 * two distinguishable at all.
 *
 * Everything here is pure so it can be tested without a browser or an API.
 */

/** Cell states the API reports. Mirrors domain.Cell* in the Go module. */
export type CellState = 'not_asked' | 'hidden' | 'blank' | 'answered'

/** One column as `GET /api/v1/forms/{id}/submissions` returns it. */
export interface ApiColumn {
  field_id: string
  label: string
  type: string
  sensitive: boolean
  /** Version numbers that asked this question. */
  in_versions: number[]
  /** Last version containing the field; absent while the field is still live. */
  retired_after?: number
  /** Set when a type change split one field into several columns. */
  type_variant?: string
}

export interface ApiCell {
  state: string
  value?: unknown
}

export interface ApiRow {
  id: string
  form_version: number
  submitted_at: string
  status: string
  /** Absent when the form asked for no identifier. Correcting on the subject's
   *  behalf is keyed by it, so a row without one gets no button rather than a
   *  button that fails when pressed. */
  subject_id?: string
  cells: Record<string, ApiCell | undefined>
}

export interface GridPage {
  columns: ApiColumn[]
  rows: ApiRow[]
  next_cursor?: string
}

/** A column prepared for display. */
export interface GridColumn {
  /** Unique per column, which `field_id` alone is not once a type change has
   *  split a field into two columns. */
  key: string
  fieldId: string
  label: string
  type: string
  sensitive: boolean
  inVersions: number[]
  /** Human span of `inVersions`, e.g. `v1–v3` or `v1, v3–v4`. */
  versions: string
  /** Last version that had the field, or null while it is still live. */
  retiredAfter: number | null
  typeVariant: string | null
  /** Retired fields start hidden: they are history, not the live questionnaire.
   *  Hidden, never dropped — the answers still exist and are still personal
   *  data somebody may have to act on. */
  hiddenByDefault: boolean
}

/**
 * buildRegistry normalises the API's column list for display.
 *
 * The union itself is computed server-side (one registry, one ordering, shared
 * with the exporter). Recomputing it here from the version schemas would give
 * the screen and the exported file two different column sets for the same form,
 * which is exactly the discrepancy an analyst would never think to check for.
 */
export function buildRegistry(columns: readonly ApiColumn[]): GridColumn[] {
  const mapped = columns.map((c): GridColumn => {
    const retiredAfter = typeof c.retired_after === 'number' && c.retired_after > 0 ? c.retired_after : null
    const typeVariant = c.type_variant ? c.type_variant : null
    const inVersions = [...new Set(c.in_versions ?? [])].sort((a, b) => a - b)
    return {
      key: typeVariant ? `${c.field_id}@${typeVariant}` : c.field_id,
      fieldId: c.field_id,
      label: c.label || c.field_id,
      type: c.type,
      sensitive: Boolean(c.sensitive),
      inVersions,
      versions: versionSpan(inVersions),
      retiredAfter,
      typeVariant,
      hiddenByDefault: retiredAfter !== null,
    }
  })

  // Retired columns sink to the right so the live questionnaire stays together
  // on the left; within each group the API's first-appearance order is kept,
  // because that order is the only thing tying the grid to the form's layout.
  return mapped
    .map((column, index) => ({ column, index }))
    .sort((a, b) => rank(a.column) - rank(b.column) || a.index - b.index)
    .map((entry) => entry.column)
}

function rank(c: GridColumn): number {
  return c.hiddenByDefault ? 1 : 0
}

/** versionSpan collapses [1,2,3,5] into "v1–v3, v5". */
export function versionSpan(versions: readonly number[]): string {
  const unique = [...new Set(versions)].sort((a, b) => a - b)
  if (unique.length === 0) return '—'

  const parts: string[] = []
  let start = unique[0]!
  let prev = start
  for (let i = 1; i < unique.length; i++) {
    const v = unique[i]!
    if (v === prev + 1) {
      prev = v
      continue
    }
    parts.push(start === prev ? `v${start}` : `v${start}–v${prev}`)
    start = v
    prev = v
  }
  parts.push(start === prev ? `v${start}` : `v${start}–v${prev}`)
  return parts.join(', ')
}

/** The whole span a grid covers, for the header line. */
export function registrySpan(columns: readonly GridColumn[]): string {
  return versionSpan(columns.flatMap((c) => c.inVersions))
}

// ---------------------------------------------------------------------------
// Cells
// ---------------------------------------------------------------------------

export type CellKind = 'answered' | 'masked' | 'blank' | 'hidden' | 'not_asked' | 'erased' | 'unknown'

export interface CellView {
  kind: CellKind
  /** What the cell shows. */
  text: string
  /** Why it shows that, for a title attribute and for the legend. */
  meaning: string
}

/** The legend printed under the grid. Four marks, four meanings, no colour-only
 *  signals — the distinction matters most to whoever reads the numbers later. */
export const CELL_LEGEND: ReadonlyArray<{ sample: string; meaning: string }> = [
  { sample: 'n/a ở v2', meaning: 'Không hỏi ở version của bản ghi này — cột được thêm hoặc gỡ ở version khác.' },
  { sample: '∅', meaning: 'Có trong version nhưng bị ẩn theo nhánh rẽ — người trả lời chưa từng thấy câu này.' },
  { sample: '—', meaning: 'Có hỏi, người trả lời để trống.' },
  { sample: '••••', meaning: 'Field nhạy cảm, đang che. Cần quyền submission.read_sensitive để mở.' },
]

/**
 * cellView decides what one cell shows.
 *
 * Order matters: an erased record overrides everything, because after a
 * crypto-shred there is nothing left to classify — the key is gone and no
 * statement about whether the respondent answered can still be made.
 */
export function cellView(
  column: GridColumn,
  row: ApiRow,
  opts: { revealSensitive: boolean },
): CellView {
  if (row.status === 'erased') {
    return column.sensitive
      ? {
          kind: 'erased',
          text: 'crypto-shred',
          meaning: 'Khoá giải mã của chủ thể đã bị huỷ; giá trị không còn khôi phục được.',
        }
      : {
          kind: 'erased',
          text: 'đã xoá theo DSR',
          meaning: 'Bản ghi đã được xoá theo yêu cầu của chủ thể dữ liệu.',
        }
  }

  // Cells are keyed by field_id. For a field split by a type change the two
  // columns share that key, so both halves currently read the same cell -- see
  // the note in Submissions.tsx about the API change needed.
  const cell = row.cells[column.fieldId]
  if (!cell) {
    return {
      kind: 'unknown',
      text: '?',
      meaning: `API không trả ô cho cột ${column.fieldId}. Đây là lệch dữ liệu, không phải ô trống.`,
    }
  }

  switch (cell.state as CellState) {
    case 'not_asked':
      return {
        kind: 'not_asked',
        text: `n/a ở v${row.form_version}`,
        meaning: `Version v${row.form_version} không có field ${column.fieldId}. Câu hỏi này chỉ tồn tại ở ${column.versions}.`,
      }
    case 'hidden':
      return {
        kind: 'hidden',
        text: '∅',
        meaning: 'Field có trong version này nhưng bị ẩn theo nhánh rẽ — người trả lời không nhìn thấy câu hỏi.',
      }
    case 'blank':
      return {
        kind: 'blank',
        text: '—',
        meaning: 'Người trả lời đã thấy câu hỏi và để trống.',
      }
    case 'answered':
      if (column.sensitive && !opts.revealSensitive) {
        return {
          kind: 'masked',
          text: '••••',
          meaning: 'Dữ liệu nhạy cảm, đang che. Có giá trị, chỉ là chưa mở.',
        }
      }
      return { kind: 'answered', text: formatValue(cell.value), meaning: '' }
    default:
      return {
        kind: 'unknown',
        text: '?',
        meaning: `Trạng thái ô "${cell.state}" không nhận ra. Giao diện có thể cũ hơn API.`,
      }
  }
}

/**
 * formatValue renders one stored answer.
 *
 * Answers hold option ids, not labels, because labels change and ids do not.
 * Resolving an id back to its label needs the schema of the record's own
 * version; until the API sends those labels, showing the id is the honest
 * choice — a wrong label is worse than a raw id.
 */
export function formatValue(value: unknown): string {
  if (value === null || value === undefined) return ''
  if (typeof value === 'string') return value
  if (typeof value === 'number') return Number.isFinite(value) ? String(value) : ''
  if (typeof value === 'boolean') return value ? 'có' : 'không'
  if (Array.isArray(value)) return value.map((v) => formatValue(v)).filter(Boolean).join('; ')
  if (typeof value === 'object') {
    const fileId = (value as { file_id?: unknown }).file_id
    if (typeof fileId === 'string') return `tệp ${fileId}`
    return JSON.stringify(value)
  }
  return String(value)
}

// ---------------------------------------------------------------------------
// Record status
// ---------------------------------------------------------------------------

export type StatusTone = 'neutral' | 'accent' | 'duesoon' | 'ok'

export interface StatusMeta {
  code: string
  label: string
  tone: StatusTone
  /** One sentence a reader can act on, not a restatement of the label. */
  meaning: string
  /** Whether the record is left out of exports and reports. */
  excluded: boolean
  /** Whether the row should read as inert. */
  dimmed: boolean
}

/**
 * statusMeta explains one record's legal state.
 *
 * The three the database stores today are active / restricted / erased. The two
 * below them are recognised so that a newer API does not turn a withdrawn or
 * anonymised record into an unlabelled row; an unknown code is shown verbatim
 * rather than assumed harmless, because assuming harmless is how a restricted
 * record ends up in a marketing list.
 */
export function statusMeta(status: string): StatusMeta {
  switch (status) {
    case 'active':
      return {
        code: status,
        label: 'Bình thường',
        tone: 'neutral',
        meaning: 'Được xử lý bình thường theo mục đích đã đồng ý.',
        excluded: false,
        dimmed: false,
      }
    case 'restricted':
      return {
        code: status,
        label: 'Hạn chế xử lý',
        tone: 'accent',
        meaning:
          'Chủ thể yêu cầu hạn chế xử lý: được lưu, không được dùng để phân tích, gửi tin hay xuất file.',
        excluded: true,
        dimmed: false,
      }
    case 'erased':
      return {
        code: status,
        label: 'Đã xoá',
        tone: 'neutral',
        meaning:
          'Đã xoá theo yêu cầu của chủ thể. Dòng còn lại chỉ để đối soát; nội dung không khôi phục được.',
        excluded: true,
        dimmed: true,
      }
    case 'withdrawn':
      return {
        code: status,
        label: 'Đã rút đồng ý',
        tone: 'accent',
        meaning:
          'Chủ thể đã rút đồng ý. Dữ liệu còn đó nhưng không còn căn cứ cho mục đích bị rút.',
        excluded: true,
        dimmed: false,
      }
    case 'anonymized':
      return {
        code: status,
        label: 'Đã ẩn danh',
        tone: 'neutral',
        meaning:
          'Định danh đã bị gỡ theo chính sách lưu trữ. Còn dùng được để thống kê, không truy ngược về một người.',
        excluded: false,
        dimmed: true,
      }
    default:
      return {
        code: status,
        label: status || 'không rõ',
        tone: 'duesoon',
        meaning:
          'Trạng thái này giao diện chưa biết. Đừng xử lý bản ghi cho tới khi xác định được nó nghĩa là gì.',
        excluded: true,
        dimmed: false,
      }
  }
}

// ---------------------------------------------------------------------------
// Date range over a keyset cursor
// ---------------------------------------------------------------------------

/**
 * cursorForTo turns the upper bound of a date range into a keyset cursor.
 *
 * The API pages backwards through `submitted_at` with a `before` cursor, so the
 * end of a range is expressible exactly: start the walk there. There is no
 * server-side `from`, which is why the lower bound is applied by clipToFrom.
 */
export function cursorForTo(to: string): string {
  if (!to) return ''
  const d = new Date(`${to}T23:59:59.999`)
  if (Number.isNaN(d.getTime())) return ''
  return d.toISOString()
}

/**
 * clipToFrom applies the lower bound of a date range to one page.
 *
 * Sound only because the API returns rows strictly newest-first: once a row
 * falls before the bound, every row after it does too, so dropping the tail
 * cannot hide a record that a later page would have shown. `reachedStart` says
 * the walk is over, which is what stops pagination at the right place instead
 * of fetching to the beginning of time.
 */
export function clipToFrom(
  rows: readonly ApiRow[],
  from: string,
): { rows: ApiRow[]; reachedStart: boolean } {
  if (!from) return { rows: [...rows], reachedStart: false }
  const bound = new Date(`${from}T00:00:00`).getTime()
  if (Number.isNaN(bound)) return { rows: [...rows], reachedStart: false }

  const kept: ApiRow[] = []
  for (const row of rows) {
    if (new Date(row.submitted_at).getTime() < bound) {
      return { rows: kept, reachedStart: true }
    }
    kept.push(row)
  }
  return { rows: kept, reachedStart: false }
}
