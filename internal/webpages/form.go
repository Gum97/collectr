package webpages

import (
	"errors"
	"net/http"

	"github.com/collectr/collectr/internal/platform/httpx"
)

type formData struct {
	chrome
	Form FormPage
}

// FormPage serves /f/{public_id}.
//
// The server draws the frame -- title, description, who is collecting, the
// consent block -- and web/src/public/form.ts takes over the questions. The
// division is not arbitrary: everything the server renders is something a person
// must be able to read in order to decide, and everything the module renders is
// something they interact with. A browser that runs no script therefore still
// shows a truthful page; it just cannot be submitted, and says so.
//
// The static consent block is rendered *inside* #collectr-form on purpose.
// form.ts clears that container before it renders, so the fallback and the live
// version can never both be on screen -- which they would be if the fallback sat
// beside it, leaving the respondent with two consent blocks and no way to tell
// which one counts.
func (h *Handler) FormPage(w http.ResponseWriter, r *http.Request) {
	publicID := r.PathValue("public_id")

	data := formData{Form: FormPage{PublicID: publicID}}

	if h.cfg.Forms != nil {
		form, err := h.cfg.Forms.PublicFormPage(r.Context(), publicID)
		switch {
		case errors.Is(err, ErrNotFound):
			h.DeadLink(w, r, StateMissing)
			return
		case errors.Is(err, ErrGone):
			h.DeadLink(w, r, StateEnded)
			return
		case errors.Is(err, ErrWithdrawn):
			h.DeadLink(w, r, StateWithdrawn)
			return
		case errors.Is(err, ErrLegalHold):
			h.DeadLink(w, r, StateLegalHold)
			return
		case err != nil:
			httpx.Logger(r.Context()).Error("resolving public form page", "error", err, "public_id", publicID)
			h.DeadLink(w, r, StateUnavailable)
			return
		}
		form.PublicID = publicID
		data.Form = form
	}

	if data.Form.Title == "" {
		// No source wired, or a form with no title. "Biểu mẫu" is honest and
		// keeps the tab from reading as an error while the module loads.
		data.Form.Title = "Biểu mẫu"
	}

	data.chrome = h.chrome(data.Form.Title, h.formScript)
	h.render(w, r, h.form, http.StatusOK, appCSP, noStore, data)
}
