/**
 * The seven data subject rights, as the Vietnamese PDPL grants them.
 *
 * The codes are the ones the Go domain uses (`internal/modules/dsr/domain`), not
 * new ones: `export` is what the API calls portability, and renaming it here
 * would mean the filter chips send a type the server has never heard of.
 *
 * Each right carries the consequence of granting it, spelled out. The queue is
 * worked by people under a deadline, and "fulfil" means something different for
 * each of these -- one of them destroys data that no backup can bring back.
 */

export const RIGHT_CODES = [
  'access',
  'rectify',
  'erase',
  'restrict',
  'withdraw',
  'export',
  'object',
] as const

export type RightCode = (typeof RIGHT_CODES)[number]

export interface Right {
  code: RightCode
  /** Column and chip label. Short, because it sits in a dense table. */
  label: string
  /** Screen title on the detail page. */
  title: string
  /** What the right is, in one line, for whoever has to decide. */
  summary: string
  /**
   * What the system does when this is granted, step by step.
   *
   * Shown before the button is pressed rather than after, because for erasure
   * there is no after in which to explain it.
   */
  steps: string[]
  /**
   * True when granting it destroys something no backup can restore.
   *
   * Only erasure qualifies: sensitive fields are sealed with a per-subject key
   * and granting erasure shreds that key, so the ciphertext left in every
   * backup becomes permanently unreadable. Restriction, objection and
   * withdrawal all stop processing without destroying anything, and can be
   * undone by a later decision.
   */
  irreversible: boolean
  /** Label for the confirm button. Names the act, never "OK". */
  actionLabel: string
  /**
   * The sentence shown at the confirmation step.
   *
   * It must name what disappears. "Bạn có chắc không?" tells a tired operator
   * nothing they did not already know when they clicked.
   */
  confirmSentence: string
  /**
   * True when a person must decide, mirroring `needsHuman` in
   * `internal/modules/dsr/api/admin.go`. The worker closes the rest on its own,
   * which is how deadlines get met on a Sunday.
   */
  needsHuman: boolean
}

const RIGHTS: Record<RightCode, Right> = {
  access: {
    code: 'access',
    label: 'Truy cập',
    title: 'Yêu cầu truy cập dữ liệu',
    summary:
      'Chủ thể muốn biết tổ chức đang giữ dữ liệu gì về họ, thu từ đâu và dùng cho mục đích nào.',
    steps: [
      'Tập hợp toàn bộ bản ghi của chủ thể trong workspace này, kèm mục đích và căn cứ pháp lý',
      'Gửi bản kết xuất qua chính kênh đã xác minh danh tính, không gửi kênh khác',
      'Ghi audit việc truy cập hàng loạt dữ liệu cá nhân',
    ],
    irreversible: false,
    actionLabel: 'Xác nhận đã cung cấp dữ liệu',
    confirmSentence:
      'Đóng yêu cầu này ở trạng thái đã đáp ứng. Đồng hồ SLA dừng lại và bản ghi sẽ mang dấu đã trả lời — muộn hay đúng hạn tính theo thời điểm bấm nút này.',
    needsHuman: false,
  },
  rectify: {
    code: 'rectify',
    label: 'Chỉnh sửa',
    title: 'Yêu cầu chỉnh sửa dữ liệu',
    summary: 'Chủ thể cho rằng dữ liệu đang sai và yêu cầu sửa lại cho đúng.',
    steps: [
      'Ghi giá trị cũ vào submission_revisions trước khi ghi đè — bản cũ được giữ, không bị xoá',
      'Cập nhật câu trả lời trên bản ghi gốc',
      'Ghi audit và thông báo cho chủ thể phần nào đã được sửa',
    ],
    irreversible: false,
    actionLabel: 'Xác nhận đã chỉnh sửa',
    confirmSentence:
      'Đóng yêu cầu này ở trạng thái đã đáp ứng. Giá trị trước khi sửa vẫn nằm trong lịch sử bản ghi, không mất đi.',
    needsHuman: true,
  },
  erase: {
    code: 'erase',
    label: 'Xóa',
    title: 'Yêu cầu xóa dữ liệu',
    summary:
      'Chủ thể yêu cầu xoá dữ liệu của họ. Đây là quyền duy nhất mà việc đáp ứng phá huỷ dữ liệu vĩnh viễn.',
    steps: [
      'Xóa cứng bản ghi và tệp đính kèm của chủ thể khỏi PostgreSQL và ổ lưu trữ',
      'Crypto-shred khóa của field nhạy cảm — không thể khôi phục kể cả từ bản sao lưu',
      'Giữ lại bản ghi đồng ý ở dạng ẩn danh làm bằng chứng pháp lý (identifier hash bị xoá cùng khóa)',
      'Ghi audit + gửi xác nhận cho chủ thể',
    ],
    irreversible: true,
    actionLabel: 'Xóa vĩnh viễn — không hoàn tác',
    confirmSentence:
      'Toàn bộ bản ghi và tệp đính kèm của chủ thể này bị xoá cứng, và khóa mã hóa của các field nhạy cảm bị huỷ. Sau khi huỷ khóa, dữ liệu nhạy cảm còn nằm trong mọi bản sao lưu trở thành rác không giải mã được — không có thao tác nào, không có bản backup nào lấy lại được. Chỉ bản ghi đồng ý ở dạng ẩn danh được giữ lại.',
    needsHuman: false,
  },
  restrict: {
    code: 'restrict',
    label: 'Hạn chế xử lý',
    title: 'Yêu cầu hạn chế xử lý',
    summary:
      'Chủ thể yêu cầu ngừng sử dụng dữ liệu nhưng không xoá — thường khi đang có tranh chấp về tính chính xác.',
    steps: [
      'Chuyển bản ghi của chủ thể sang trạng thái restricted — dữ liệu vẫn còn nguyên',
      'Mọi đường đọc (grid, export, webhook, đồng bộ) đã lọc theo trạng thái này nên ngừng trả về bản ghi',
      'Ghi audit kèm số bản ghi bị hạn chế',
    ],
    irreversible: false,
    actionLabel: 'Áp hạn chế xử lý',
    confirmSentence:
      'Bản ghi của chủ thể chuyển sang trạng thái restricted và biến mất khỏi mọi export, webhook và màn dữ liệu. Dữ liệu không bị xoá và có thể bỏ hạn chế sau nếu căn cứ thay đổi.',
    needsHuman: true,
  },
  withdraw: {
    code: 'withdraw',
    label: 'Rút đồng ý',
    title: 'Yêu cầu rút đồng ý',
    summary:
      'Chủ thể rút đồng ý cho một hoặc nhiều mục đích xử lý. Việc xử lý trước đó vẫn hợp pháp.',
    steps: [
      'Ghi thêm dòng withdrawn vào lịch sử đồng ý — dòng đồng ý cũ không bị sửa hay xoá, vì việc đã từng đồng ý là một sự thật lịch sử',
      'Cập nhật đồng ý hiện hành: mọi export và đồng bộ cho mục đích đó dừng lại',
      'Nếu mục đích bị rút là căn cứ duy nhất để giữ dữ liệu, hệ thống tự sinh một yêu cầu xoá riêng',
    ],
    irreversible: false,
    actionLabel: 'Xác nhận đã rút đồng ý',
    confirmSentence:
      'Việc xử lý cho mục đích bị rút dừng lại kể từ bây giờ. Dữ liệu đã thu không tự động bị xoá — nếu chủ thể muốn xoá, đó là một yêu cầu riêng.',
    needsHuman: false,
  },
  export: {
    code: 'export',
    label: 'Chuyển dữ liệu',
    title: 'Yêu cầu chuyển dữ liệu (portability)',
    summary:
      'Chủ thể yêu cầu nhận dữ liệu của mình ở định dạng máy đọc được để mang sang nơi khác.',
    steps: [
      'Kết xuất dữ liệu chủ thể đã cung cấp ở định dạng máy đọc được',
      'Gửi qua chính kênh đã xác minh danh tính',
      'Ghi audit việc truy cập hàng loạt dữ liệu cá nhân',
    ],
    irreversible: false,
    actionLabel: 'Xác nhận đã chuyển dữ liệu',
    confirmSentence:
      'Đóng yêu cầu này ở trạng thái đã đáp ứng. Dữ liệu vẫn nằm nguyên trong hệ thống — chuyển dữ liệu không phải là xoá dữ liệu.',
    needsHuman: false,
  },
  object: {
    code: 'object',
    label: 'Phản đối',
    title: 'Yêu cầu phản đối việc xử lý',
    summary:
      'Chủ thể phản đối việc xử lý dữ liệu của họ. Không phải phản đối nào cũng phải được chấp nhận — nhưng từ chối thì phải có căn cứ ghi lại được.',
    steps: [
      'Cân nhắc căn cứ xử lý của tổ chức so với lý do phản đối của chủ thể',
      'Nếu chấp nhận: ngừng xử lý cho mục đích bị phản đối',
      'Ghi audit quyết định kèm lập luận, vì chủ thể có quyền khiếu nại quyết định này',
    ],
    irreversible: false,
    actionLabel: 'Chấp nhận phản đối',
    confirmSentence:
      'Tổ chức ngừng xử lý dữ liệu cho mục đích bị phản đối. Dữ liệu không bị xoá và quyết định này có thể xem lại sau.',
    needsHuman: true,
  },
}

/** Grounds for refusing, from the law rather than free text.
 *
 * A refusal that says only "không đồng ý" cannot be defended if the subject
 * complains, so the reason is picked from a list and then explained. */
export const REJECT_GROUNDS = [
  { id: 'legal_retention', label: 'Nghĩa vụ lưu trữ theo pháp luật khác' },
  { id: 'dispute', label: 'Đang có tranh chấp / thủ tục tố tụng' },
  { id: 'identity', label: 'Không xác thực được danh tính chủ thể' },
  { id: 'legal_hold', label: 'Dữ liệu đang bị legal hold' },
  { id: 'other', label: 'Căn cứ khác (nêu rõ bên dưới)' },
] as const

export function isRightCode(v: string): v is RightCode {
  return (RIGHT_CODES as readonly string[]).includes(v)
}

/**
 * The right a code names, or a safe placeholder.
 *
 * The API is the authority on which types exist; if it grows an eighth right
 * this screen must still render the row rather than crash and hide a deadline.
 */
export function rightOf(code: string): Right {
  if (isRightCode(code)) return RIGHTS[code]
  return {
    code: 'access',
    label: code,
    title: `Yêu cầu ${code}`,
    summary: 'Giao diện chưa biết loại quyền này. Kiểm tra lại trước khi hành động.',
    steps: [],
    irreversible: false,
    actionLabel: 'Đánh dấu đã đáp ứng',
    confirmSentence:
      'Giao diện không mô tả được hệ quả của loại quyền này, nên không thể nói trước điều gì sẽ mất. Chỉ tiếp tục nếu bạn đã xác định được hệ quả bằng cách khác.',
    needsHuman: true,
  }
}

/** Vietnamese for the lifecycle states in `internal/modules/dsr/domain`. */
export const STATUS_LABEL: Record<string, string> = {
  received: 'Mới nhận',
  verified: 'Đã xác minh',
  in_progress: 'Đang xử lý',
  fulfilled: 'Đã đáp ứng',
  rejected: 'Đã từ chối',
}

/** True once the request is closed and the SLA clock has stopped. */
export function isClosed(status: string): boolean {
  return status === 'fulfilled' || status === 'rejected'
}

/**
 * Partially masks a data subject identifier.
 *
 * Whoever works this queue needs to tell two cases apart, not to read someone's
 * email address. Masking is applied at the point of display and there is no
 * control anywhere that reveals the full value -- an unmask button would make
 * every reader's curiosity a disclosure decision.
 */
export function maskIdentifier(raw: string | null | undefined): string {
  if (!raw) return '—'
  const at = raw.lastIndexOf('@')
  if (at > 0) {
    const local = raw.slice(0, at)
    const domain = raw.slice(at)
    return `${local.slice(0, 2)}***${domain}`
  }
  const digits = raw.replace(/\D/g, '')
  if (digits.length >= 6 && digits.length === raw.replace(/[\s+.-]/g, '').length) {
    return `${digits.slice(0, 3)}***${digits.slice(-2)}`
  }
  return raw.length <= 4 ? '***' : `${raw.slice(0, 2)}***`
}

/**
 * Short form of an internal id, for a table cell.
 *
 * A data subject id is a pseudonym the system assigned, not something the
 * person would recognise, so it is shown truncated as a lookup handle.
 */
export function shortId(id: string | null | undefined): string {
  if (!id) return '—'
  return id.length <= 10 ? id : `${id.slice(0, 8)}…`
}
