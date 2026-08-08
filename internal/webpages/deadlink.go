package webpages

import "net/http"

// State is why a link no longer works.
//
// Four states, because a person standing in front of a QR code on a poster needs
// four different things from the answer. "Not found" tells them to check the
// code; "ended" tells them the code was real and the campaign is over, so they
// stop retyping it; "legal hold" tells them the silence is deliberate rather
// than a fault. They are separate pages, not one page with a status number.
type State int

const (
	// StateMissing: nothing was ever issued at this address.
	StateMissing State = iota

	// StateEnded: it existed and has been closed -- expired, or taken down.
	//
	// This is the default for anything that once worked and no longer does,
	// including a link withdrawn at a data subject's request. Collapsing those
	// two is the safe direction: a page that said "withdrawn on request" would
	// let anyone holding a list of links learn which people had exercised a
	// right, which is a disclosure about those people made to a stranger.
	StateEnded

	// StateWithdrawn: taken down on request.
	//
	// Read the note on StateEnded first. Use this only where the caller can
	// prove the visitor is entitled to the distinction -- it is here so that the
	// wording exists and is careful, not because the resolver should reach for
	// it. The copy names no person, no data and no reason.
	StateWithdrawn

	// StateLegalHold: suspended under a legal hold.
	StateLegalHold

	// StateUnavailable: the server could not answer. Not a dead link -- a fault,
	// and it says so, because telling someone their link is dead when the
	// database is merely down makes them throw away a working code.
	StateUnavailable
)

type deadData struct {
	chrome
	Code       string
	Title      string
	TitleClass string
	MarkClass  string
	Body       string
	Retry      bool
}

// deadPages holds the copy for each state.
//
// Written out rather than assembled from fragments: this is the text most
// visitors of a shortened link will ever read, and it is easier to keep four
// short paragraphs honest when they sit side by side.
var deadPages = map[State]struct {
	status int
	data   deadData
}{
	StateMissing: {
		status: http.StatusNotFound,
		data: deadData{
			Code:      "404",
			Title:     "Không tìm thấy",
			MarkClass: "",
			Body: "Địa chỉ này chưa từng tồn tại. Có thể đã gõ sai hoặc thiếu một ký tự — " +
				"bạn thử quét lại mã, hoặc kiểm tra lại đường dẫn giúp nhé.",
			Retry: true,
		},
	},
	StateEnded: {
		status: http.StatusGone,
		data: deadData{
			Code:      "410",
			Title:     "Liên kết này đã đóng",
			MarkClass: "dead-solid",
			Body: "Liên kết từng hoạt động và nay đã ngừng. Việc này là bình thường: " +
				"một chiến dịch kết thúc, hoặc liên kết được gỡ đi. Không còn gì để xem ở đây, " +
				"và bạn không cần làm gì thêm.",
		},
	},
	StateWithdrawn: {
		status: http.StatusGone,
		data: deadData{
			Code:      "410",
			Title:     "Liên kết này đã được gỡ",
			MarkClass: "dead-solid",
			// Deliberately says nothing about data, about anyone's data, or about
			// why. "Đã được gỡ theo yêu cầu" would be enough to infer that
			// somebody asked -- and who, if the reader knows who the link was
			// given to.
			Body: "Liên kết đã được gỡ và không còn hoạt động. Không còn gì để xem ở đây, " +
				"và bạn không cần làm gì thêm.",
		},
	},
	StateLegalHold: {
		status: http.StatusUnavailableForLegalReasons,
		data: deadData{
			Code:       "451",
			Title:      "Tạm ngưng vì lý do pháp lý",
			TitleClass: "dead-legal-title",
			MarkClass:  "dead-legal",
			// No reason and no requester, but no ambiguity either: this is a
			// decision somebody took, not a malfunction.
			Body: "Liên kết đang tạm ngưng theo một yêu cầu pháp lý. Đây là quyết định có chủ ý, " +
				"không phải lỗi kỹ thuật. Chúng tôi không nêu lý do và không nêu bên yêu cầu.",
		},
	},
	StateUnavailable: {
		status: http.StatusServiceUnavailable,
		data: deadData{
			Code:      "503",
			Title:     "Tạm thời không mở được",
			MarkClass: "",
			Body: "Hệ thống đang gặp sự cố nên chưa mở được trang này. Liên kết của bạn vẫn còn " +
				"giá trị — vui lòng thử lại sau ít phút.",
			Retry: true,
		},
	},
}

// DeadLink renders the page for one state.
//
// Every one of them is sent with Cache-Control: no-store. When an erasure
// request is approved the link has to be dead on the very next scan; a cached
// 410 would be harmless, but a cached 200 from before the takedown would not,
// and the only way to be sure neither is held is to store nothing at all.
func (h *Handler) DeadLink(w http.ResponseWriter, r *http.Request, state State) {
	p, ok := deadPages[state]
	if !ok {
		p = deadPages[StateMissing]
	}
	data := p.data
	data.chrome = h.chrome(data.Title, "")
	h.render(w, r, h.dead, p.status, appCSP, noStore, data)
}

// LinkNotFound, LinkEnded, LinkWithdrawn and LinkLegalHold are the four pages as
// plain handlers, so the link module can swap its http.Error calls for them
// without importing the State vocabulary.
func (h *Handler) LinkNotFound(w http.ResponseWriter, r *http.Request) {
	h.DeadLink(w, r, StateMissing)
}

func (h *Handler) LinkEnded(w http.ResponseWriter, r *http.Request) {
	h.DeadLink(w, r, StateEnded)
}

func (h *Handler) LinkWithdrawn(w http.ResponseWriter, r *http.Request) {
	h.DeadLink(w, r, StateWithdrawn)
}

func (h *Handler) LinkLegalHold(w http.ResponseWriter, r *http.Request) {
	h.DeadLink(w, r, StateLegalHold)
}
